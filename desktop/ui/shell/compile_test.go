package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSourceWidthFollowsPreference(t *testing.T) {
	s, tt := newTestShell(t, writeDoc(t, "# Width"))
	s.setMode(modeSource)
	width := func(pct float64) float32 {
		s.prefs.ContentWidthPercent = pct
		tt.Frame()
		r, ok := tt.Find("Markdown")
		if !ok {
			t.Fatal(tt.Texts())
		}
		return r.W
	}
	narrow, wide := width(45), width(90)
	if narrow < 1 || wide < narrow*1.98 || wide > narrow*2.02 {
		t.Fatalf("source widths: 45%%=%v 90%%=%v", narrow, wide)
	}
}

func TestCompiledPreview(t *testing.T) {
	for _, tc := range []struct{ name, ext, exe, doc, part string }{
		{"Typst", "typ", "typst", "// heading\n= First\n#pagebreak()\n= Later chapter\n#include \"part.typ\"", "Second"},
		{"LaTeX", "tex", "xelatex", `\documentclass{article}
\usepackage{hyperref}
\begin{document}
{\Huge First}

\newpage
\section{Later chapter}
\input{part.tex}\end{document}`, "Second"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, exe := range []string{tc.exe, "pdftoppm", "pdfinfo", "pdftotext"} {
				if _, err := exec.LookPath(exe); err != nil {
					t.Skip(exe, " unavailable")
				}
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "main."+tc.ext)
			for p, v := range map[string]string{path: "saved", filepath.Join(dir, "part."+tc.ext): tc.part} {
				if err := os.WriteFile(p, []byte(v), 0600); err != nil {
					t.Fatal(err)
				}
			}
			s, tt := newTestShell(t, path)
			tab := s.activeTab()
			tab.replaceText(tc.doc)
			posted := make(chan func(), 2)
			s.post = func(fn func()) { posted <- fn }
			defer s.closeCompile()
			click := func(label string) {
				t.Helper()
				if err := tt.Click(label); err != nil {
					t.Fatal(err)
				}
			}
			wait := func() {
				t.Helper()
				select {
				case fn := <-posted:
					fn()
				case <-time.After(90 * time.Second):
					t.Fatal("preview timed out")
				}
				tt.Frame()
			}
			click(s.tr("semanticEditing"))
			wait()
			if p := s.compile; p == nil || p.err != "" || p.image == nil || p.pages != 2 {
				t.Fatalf("preview: %+v", p)
			}
			s.prefs.ContentWidthPercent = 45
			tt.Frame()
			narrow, _ := tt.Find(s.tr("compiledPage"))
			s.prefs.ContentWidthPercent = 90
			tt.Frame()
			wide, _ := tt.Find(s.tr("compiledPage"))
			if narrow.W <= 0 || wide.W < narrow.W*1.98 || wide.W > narrow.W*2.02 {
				t.Fatalf("PDF size at 45%% and 90%%: %v %v", narrow, wide)
			}
			// 对应 3840 × 1938 的 2× 显示器；首张快览也必须按实际纸面重新栅格化。
			tt.SetSize(1920, 969)
			tt.SetScale(2)
			wide, _ = tt.Find(s.tr("compiledPage"))
			for {
				bmp := s.compile.cached[1]
				if bmp != nil {
					w, h := bmp.Size()
					if w >= int(2*wide.W)-1 && max(w, h) > 1400 {
						t.Logf("Retina page: %.0f DIP, %d × %d px", wide.W, w, h)
						break
					}
				}
				wait()
			}
			tt.SetScale(1)
			tt.SetSize(1200, 800)
			tt.Frame()
			snapshot(t, tt, "compile-"+tc.ext)
			// 点击真实栅格页首个墨迹像素，覆盖两侧留白、文字命中和单栏到双栏的切换。
			tt.Frame()
			box, ok := tt.Find(s.tr("compiledPage"))
			if !ok {
				t.Fatal("missing preview")
			}
			screen := tt.Image()
			clicked := false
			for y := int(box.Y) + 4; !clicked && y < int(box.Y+box.H)-4; y++ {
				for x := int(box.X) + 4; x < int(box.X+box.W)-4; x++ {
					r, g, b, _ := screen.At(x, y).RGBA()
					if max(r, g, b) < 0x7777 {
						tt.ClickAt(float32(x)+0.5, float32(y)+0.5)
						clicked = true
						break
					}
				}
			}
			if !clicked {
				t.Fatal("no page text")
			}
			deadline := time.After(5 * time.Second)
			for tab.typesetMode != typesetSplit {
				select {
				case fn := <-posted:
					fn()
				case <-deadline:
					t.Fatal("PDF click did not resolve")
				}
			}
			tt.Frame()
			want := strings.Index(tc.doc, "First")
			snap := tab.Source().Buffer.Snapshot()
			if tab.typesetMode != typesetSplit || snap.LineOf(tab.Source().Cursor()) != snap.LineOf(want) {
				t.Fatalf("jumped to line %d, want %d", snap.LineOf(tab.Source().Cursor()), snap.LineOf(want))
			}

			if len(s.compile.bookmarks) == 0 {
				t.Fatal("missing PDF bookmarks")
			}
			click("Later chapter")
			tt.Frame()
			for s.compile.cached[2] == nil {
				wait()
			}
			if first, last := s.compile.list.Visible(); first > 1 || last < 1 {
				t.Fatal(tt.Texts())
			}
			tab.replaceText(map[string]string{"tex": `\badcommand`, "typ": "#invalid("}[tc.ext])
			tt.Frame()
			wait()
			if s.compile.err == "" || s.compile.busy {
				t.Fatal("expected compile error")
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != "saved" {
				t.Fatalf("source overwritten: %q %v", got, err)
			}
			s.NewDocument()
			tt.Frame()
			if s.compile != nil {
				t.Fatal("preview survived tab change")
			}
		})
	}
}

func TestTypesetSourceOutlineAfterEdit(t *testing.T) {
	for _, tc := range []struct{ ext, headings string }{
		{"tex", "\\section{First}\nbody\n\\section*{Second}"},
		{"typ", "= First\nbody\n= Second <second>"},
	} {
		t.Run(tc.ext, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source."+tc.ext)
			if err := os.WriteFile(path, []byte(tc.headings), 0600); err != nil {
				t.Fatal(err)
			}
			s, tt := newTestShell(t, path)
			tab := s.activeTab()
			if len(tab.Outline()) != 2 {
				t.Fatal(tab.Outline())
			}
			tab.replaceText(strings.Replace(tc.headings, "body", "新增一行\n正文", 1))
			tt.Frame()
			if err := tt.Click("Second"); err != nil {
				t.Fatal(err)
			}
			src := tab.Source()
			if line := src.Buffer.Snapshot().LineOf(src.Cursor()); line != 3 {
				t.Fatalf("source outline jumped to line %d, want 4", line+1)
			}
		})
	}
}

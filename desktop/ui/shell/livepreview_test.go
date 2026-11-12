package shell

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui/editor"
)

func TestTeXEdit(t *testing.T) {
	for _, a := range []string{"", "啊乙ab🙂", "ab甲甲", "a" + strings.Repeat("界", 3000)} {
		for _, b := range []string{"", "啊乙ab🙃", "ab申甲", "a" + strings.Repeat("界", 2999) + "谢"} {
			i := changedOffset(editor.NewBuffer(a).Snapshot(), editor.NewBuffer(b).Snapshot())
			if !utf8.ValidString(a[:i]) || a[:i] != b[:i] || i+4 < min(len(a), len(b)) && a[i:i+4] == b[i:i+4] && a != b {
				t.Fatalf("incorrect changed offset %d", i)
			}
		}
	}
}
func TestLiveTeX(t *testing.T) {
	for _, exe := range []string{"xelatex", "pdfinfo", "typst", "pdftoppm"} {
		if _, e := exec.LookPath(exe); e != nil {
			t.Skip(exe)
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "main.tex")
	const src = "\\documentclass{article}\n\\usepackage{fontspec}\n\\begin{document}\nThis is the first sentence in a live paragraph.\n\nMore words in another paragraph.\n\\end{document}\n"
	if e := os.WriteFile(path, []byte("Saved content"), 0600); e != nil {
		t.Fatal(e)
	}
	s, tt := newTestShell(t, path)
	s.activeTab().replaceText(src)
	posted := make(chan func(), 16)
	s.post = func(f func()) { posted <- f }
	defer s.closeScholar()
	defer s.closeCompile()
	tt.Frame()
	s.setTypesetView(s.scholar, s.activeTab(), typesetSplit)
	tt.Frame()
	p := s.compile
	allowError := false
	wait := func(f func() bool) {
		t.Helper()
		timeout := time.After(45 * time.Second)
		for !f() {
			select {
			case fn := <-posted:
				fn()
				tt.Frame()
				if p.err != "" && !allowError {
					t.Fatal(p.err)
				}
			case <-timeout:
				t.Fatal("timeout", tt.Texts())
			}
		}
	}
	wait(func() bool { return p.image != nil && !p.busy })
	before := p.image
	for i := range 3 {
		then := time.Now()
		for k := range 3 {
			s.activeTab().replaceText(strings.Replace(src, "first", fmt.Sprintf("updated %d%d", i, k), 1))
			tt.Frame()
		}
		wait(func() bool { return p.paragraph != nil || p.inlineText != "" })
		t.Log("edit burst to visible paragraph:", time.Since(then))
		snapshot(t, tt, "live-tex")
		wait(func() bool { return !p.busy && p.paragraph == nil && p.image != before })
		before = p.image
	}
	allowError = true
	s.activeTab().replaceText("\\badcommand")
	tt.Frame()
	wait(func() bool { return p.err != "" && !p.busy })
	if p.image != before {
		t.Fatal("error discarded last good page")
	}
	s.activeTab().replaceText(src)
	tt.Frame()
	wait(func() bool { return p.err == "" && !p.busy && p.image != nil })
	if os.Getenv("NOMO_PREVIEW_COMPARE") != "" {
		for i := range 3 {
			then := time.Now()
			s.activeTab().replaceText(strings.Replace(src, "first", fmt.Sprintf("updated %d2", i), 1))
			s.openTypeset(s.activeTab(), false)
			p = s.compile
			wait(func() bool { return !p.busy && p.image != nil })
			t.Log("edit to manual XeLaTeX page:", time.Since(then))
		}
	}
	if b, e := os.ReadFile(path); e != nil || string(b) != "Saved content" {
		t.Fatal("source overwritten")
	}
}

func TestLiveTypstCoalescesAndClampsPage(t *testing.T) {
	for _, exe := range []string{"typst", "pdftoppm", "pdfinfo"} {
		if _, e := exec.LookPath(exe); e != nil {
			t.Skip(exe)
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "main.typ")
	if e := os.WriteFile(path, []byte("One\n#pagebreak()\nTwo"), 0600); e != nil {
		t.Fatal(e)
	}
	s, tt := newTestShell(t, path)
	posted := make(chan func(), 16)
	s.post = func(f func()) { posted <- f }
	defer s.closeScholar()
	defer s.closeCompile()
	tt.Frame()
	s.setTypesetView(s.scholar, s.activeTab(), typesetSplit)
	tt.Frame()
	p := s.compile
	wait := func(page, total int) {
		t.Helper()
		timer := time.After(15 * time.Second)
		for p.busy || p.cached[page] == nil || p.pages != total {
			select {
			case f := <-posted:
				f()
				tt.Frame()
				if p.err != "" {
					t.Fatal(p.err)
				}
			case <-timer:
				t.Fatal(tt.Texts())
			}
		}
	}
	wait(1, 2)
	paper, ok := tt.Find(s.tr("compiledPage"))
	if !ok {
		t.Fatal("missing paper")
	}
	tt.Scroll(paper.X+paper.W/2, paper.Y+paper.H/2, 0, paper.H)
	tt.Frame()
	wait(2, 2)
	for i := range 20 {
		s.activeTab().replaceText(fmt.Sprintf("Latest %d", i))
		tt.Frame()
	}
	wait(1, 1)
	s.activeTab().replaceText("Pending")
	tt.Frame()
	s.NewDocument()
	tt.Frame()
	if s.compile != nil {
		t.Fatal("background tab retained live preview")
	}
}

func TestLiveTypstSemanticReferencesTrackRevision(t *testing.T) {
	if err := exec.Command("typst", "eval", "--help").Run(); err != nil {
		t.Skip("Typst eval unavailable")
	}
	for _, name := range []string{"pdfinfo", "pdftoppm"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name)
		}
	}
	const source = "#set heading(numbering: \"1\")\n= Results <sec-results>\n"
	path := filepath.Join(t.TempDir(), "main.typ")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	s, tt := newTestShell(t, path)
	posted := make(chan func(), 16)
	s.post = func(fn func()) {
		select {
		case posted <- fn:
		case <-t.Context().Done():
		}
	}
	defer s.closeScholar()
	defer s.closeCompile()
	tt.Frame()
	tab := s.activeTab()
	s.setTypesetView(s.scholar, tab, typesetSemantic)
	tt.Frame()
	wait := func(ready func(*compilePane) bool) *compilePane {
		t.Helper()
		timer := time.NewTimer(20 * time.Second)
		defer timer.Stop()
		for {
			if p := s.compile; p != nil && !p.busy {
				if p.err != "" {
					t.Fatal(p.err)
				}
				if ready(p) {
					return p
				}
			}
			select {
			case fn := <-posted:
				fn()
				tt.Frame()
			case <-timer.C:
				t.Fatal("preview did not finish")
			}
		}
	}
	numbered := func(number string) func(*compilePane) bool {
		return func(p *compilePane) bool {
			return p.referenceIndex != nil && p.referenceIndex.ByLabel["sec-results"].Number == number
		}
	}
	if p := wait(numbered("1")); p.image != nil || p.pages != 0 {
		t.Fatal("metadata query populated a PDF view")
	}
	tab.replaceText(strings.Replace(source, "= Results", "= Earlier\n= Results", 1))
	tt.Frame()
	wait(numbered("2"))
	s.setTypesetView(s.scholar, tab, typesetSplit)
	tt.Frame()
	wait(func(p *compilePane) bool { return p.pages == 1 && p.image != nil })
}

func TestLiveTeXFollowsEditedPage(t *testing.T) {
	for _, exe := range []string{"xelatex", "pdftotext", "pdftoppm", "pdfinfo"} {
		if _, e := exec.LookPath(exe); e != nil {
			t.Skip(exe)
		}
	}
	src := "\\documentclass{article}\n\\begin{document}\nFirst page.\n\n\\newpage\n\nA unique paragraph on the second page.\n\n\\end{document}\n"
	path := filepath.Join(t.TempDir(), "main.tex")
	if e := os.WriteFile(path, []byte(src), 0600); e != nil {
		t.Fatal(e)
	}
	s, tt := newTestShell(t, path)
	posted := make(chan func(), 8)
	s.post = func(fn func()) { posted <- fn }
	defer s.closeScholar()
	defer s.closeCompile()
	tt.Frame()
	s.setTypesetView(s.scholar, s.activeTab(), typesetSplit)
	tt.Frame()
	p := s.compile
	wait := func(ok func() bool) {
		t.Helper()
		timeout := time.After(30 * time.Second)
		for !ok() {
			select {
			case fn := <-posted:
				fn()
				tt.Frame()
				if p.err != "" {
					t.Fatal(p.err)
				}
			case <-timeout:
				t.Fatalf("preview timed out, page=%d anchor=%+v", p.page, p.anchor)
			}
		}
	}
	wait(func() bool { return p.image != nil && !p.busy })
	s.activeTab().replaceText(strings.Replace(src, "unique paragraph", "changed paragraph", 1))
	tt.Frame()
	wait(func() bool { return p.anchor.Page == 2 && p.inlineText != "" })
	if p.page != 2 {
		t.Fatal("patch shown on wrong page")
	}
	wait(func() bool { return !p.busy && p.inlineText == "" })
	if p.page != 2 || p.anchor.Page != 2 {
		t.Fatal("lost edited page after correction")
	}
}

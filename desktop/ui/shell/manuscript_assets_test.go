package shell

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDropManuscriptAssetsPreservesBodyAndImportsFigure(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.typ")
	const body = "// Template notes\n#set text(size: 12pt)\n= Heading\n\nKeep this paragraph.\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "plot.png")
	f, err := os.Create(outside)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	img.Set(0, 0, color.White)
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
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
	v := s.manuscript
	if v == nil {
		t.Fatal("semantic canvas unavailable")
	}
	i := strings.Index(body, "this")
	v.editor.Select(i, i+4)
	tt.Frame()
	if !tt.DropFiles(700, 360, outside) {
		t.Fatal("canvas did not accept the image")
	}
	tt.Frame()
	if f := v.author; !f.open || f.action != "figure" || f.asset.importBase == "" {
		t.Fatalf("missing figure import card: %+v", f)
	}
	if err := tt.Click(s.tr("author-apply")); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	for v.author.open {
		select {
		case fn := <-posted:
			fn()
			tt.Frame()
		case <-deadline.C:
			t.Fatalf("asset insertion did not finish: %s", v.author.issue)
		}
	}
	src := v.buffer.String()
	if !strings.Contains(src, "Keep this paragraph.") || !strings.Contains(src, `#image("assets/plot.png"`) {
		t.Fatalf("invalid result: %s", src)
	}
	if _, err := os.Stat(filepath.Join(root, "assets", "plot.png")); err != nil {
		t.Fatal(err)
	}
	csv := filepath.Join(root, "table.csv")
	if err := os.WriteFile(csv, []byte("Name,Value\n\"a,b\",2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	v.editor.Select(0, 0)
	tt.Frame()
	tt.DropFiles(700, 360, csv)
	tt.Frame()
	if f := v.author; !f.open || f.action != "table" || len(f.asset.rows) != 2 || f.asset.rows[1][0] != "a,b" {
		t.Fatalf("missing CSV preview: %+v", f)
	}
	if err := tt.Click(s.tr("author-apply")); err != nil {
		t.Fatal(err)
	}
	src = v.buffer.String()
	if !strings.Contains(src, "[a,b], [2]") || strings.Index(src, "// nomo-style:object-table") < strings.Index(src, "#set text(") {
		t.Fatalf("CSV cell or leading template changed: %s", src)
	}
}

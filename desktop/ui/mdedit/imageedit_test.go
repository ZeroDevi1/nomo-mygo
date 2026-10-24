package mdedit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestImageEdit：定位 Markdown 图片与 <img>/<p align> 图片，改写宽度与对齐（行内图片忽略对齐），删除独占块的图片。
func TestImageEdit(t *testing.T) {
	src := "text ![a](p.png) tail\n\n![solo](q.png)\n\n<p align=\"center\">\n  <img src=\"r.png\" alt=\"r\" width=\"50%\">\n</p>\n\nend\n"
	ed, tt := newTester(t, src, 800, 600)
	tt.Frame()
	ref, ok := ed.ImageAt(strings.Index(src, "p.png"))
	if !ok || ref.Src != "p.png" || ref.Alt != "a" || ref.Standalone {
		t.Fatalf("行内图片 %+v", ref)
	}
	ed.SetImage(strings.Index(src, "p.png"), "50%", "center")
	if got := ed.Doc.Source(); !strings.HasPrefix(got, "text <img src=\"p.png\" alt=\"a\" width=\"50%\"> tail\n") {
		t.Fatalf("行内图片改宽后 %q", got)
	}
	cur := ed.Doc.Source()
	ref, ok = ed.ImageAt(strings.Index(cur, "r.png"))
	if !ok || ref.Align != "center" || ref.Width != "50%" || !ref.Standalone || !ref.IsTag {
		t.Fatalf("对齐图片 %+v", ref)
	}
	ed.SetImage(strings.Index(cur, "r.png"), "", "")
	if got := ed.Doc.Source(); !strings.Contains(got, "\n\n![r](r.png)\n\nend\n") {
		t.Fatalf("取消对齐与宽度后 %q", got)
	}
	cur = ed.Doc.Source()
	tt.Frame()
	if !ed.DeleteImage(strings.Index(cur, "q.png")) || strings.Contains(ed.Doc.Source(), "solo") {
		t.Fatalf("删除后 %q", ed.Doc.Source())
	}
}

// TestImageResizeDrag：拖动已加载图片的右边缘，松开后写入新宽度。
func TestImageResizeDrag(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pic.png"), pngBytes(t, 200, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	ed, tt := newTester(t, "intro\n\n![a](pic.png)\n", 800, 600)
	tp := &testPost{}
	im := NewImages(tp.post, DefaultImageBudget)
	defer im.Close()
	ed.SetImages(im, dir)
	tt.Frame()
	tp.drain(t, 1)
	tt.Frame()
	var x, y float32
	found := false
	for _, v := range ed.vsegs {
		r := &ed.entries[v.i].rows[v.k]
		if len(r.objRects) == 1 {
			s := ed.segOf(v.i, v.k)
			rc := r.objRects[0]
			x, y = ed.left()+s.x+rc.X+rc.W-3, v.y+s.padTop+rc.Y+rc.H/2
			found = true
		}
	}
	if !found {
		t.Fatal("找不到图片位置")
	}
	tt.Press(x, y)
	tt.Move(x-50, y)
	tt.Frame()
	if ed.resize == nil || ed.resize.w != 150 {
		t.Fatalf("拖动中宽度 %+v", ed.resize)
	}
	tt.Release(x-50, y)
	tt.Frame()
	if got := ed.Doc.Source(); got != "intro\n\n<img src=\"pic.png\" alt=\"a\" width=\"150\">\n" {
		t.Fatalf("拖动后原文 %q", got)
	}
}

// TestMoveImage：行内图片移到同段其他位置；独占一块的图片移到另一处空行成为独立的块；一次撤销恢复。
func TestMoveImage(t *testing.T) {
	src := "前 ![a](p.png) 后\n\n![solo](q.png)\n\n第二段\n\n结尾\n"
	ed, tt := newTester(t, src, 800, 600)
	tt.Frame()
	ref, _ := ed.ImageAt(strings.Index(src, "p.png"))
	if !ed.moveImageTo(ref, strings.Index(src, "后")+len("后")) || !strings.HasPrefix(ed.Doc.Source(), "前  后![a](p.png)\n") {
		t.Fatalf("行内移动后 %q", ed.Doc.Source())
	}
	ed.Run("undo")
	if ed.Doc.Source() != src {
		t.Fatalf("撤销后 %q", ed.Doc.Source())
	}
	tt.Frame()
	ref, _ = ed.ImageAt(strings.Index(src, "q.png"))
	drop := strings.Index(src, "\n\n结尾") + 1 // “第二段”之后的空行
	if !ed.moveImageTo(ref, drop) {
		t.Fatal("未移动")
	}
	if got := ed.Doc.Source(); got != "前 ![a](p.png) 后\n\n第二段\n\n![solo](q.png)\n\n结尾\n" {
		t.Fatalf("块移动后 %q", got)
	}
}

// TestImageDragMove：按住已加载的图片拖到文末松开，图片原文移到那里；不拖动时单击仍进入图片原文。
func TestImageDragMove(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pic.png"), pngBytes(t, 120, 60), 0o644); err != nil {
		t.Fatal(err)
	}
	ed, tt := newTester(t, "![a](pic.png)\n\n第一段文字\n", 800, 600)
	tp := &testPost{}
	im := NewImages(tp.post, DefaultImageBudget)
	defer im.Close()
	ed.SetImages(im, dir)
	tt.Frame()
	tp.drain(t, 1)
	tt.Frame()
	var x, y float32
	for _, v := range ed.vsegs {
		if r := &ed.entries[v.i].rows[v.k]; len(r.objRects) == 1 {
			s := ed.segOf(v.i, v.k)
			rc := r.objRects[0]
			x, y = ed.left()+s.x+rc.X+rc.W/2, v.y+s.padTop+rc.Y+rc.H/2
		}
	}
	end, ok := ed.caretRect(len("![a](pic.png)\n\n第一段文字"))
	if !ok {
		t.Fatal("找不到文末位置")
	}
	tt.Press(x, y)
	tt.Move(x+20, y+20)
	tt.Move(end.X+2, end.Y+end.H/2)
	tt.Release(end.X+2, end.Y+end.H/2)
	tt.Frame()
	if got := ed.Doc.Source(); got != "\n第一段文字![a](pic.png)\n" && got != "第一段文字![a](pic.png)\n" {
		t.Fatalf("拖动后 %q", got)
	}
}

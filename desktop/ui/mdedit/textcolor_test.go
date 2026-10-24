package mdedit

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestTextColor：包裹选区、改色、去色（选中文字或整个标签都可），投影按颜色着色并隐藏标签。
func TestTextColor(t *testing.T) {
	ed := NewEditor(NewDocument("hello world\n"))
	ed.Select(6, 11)
	if !ed.SetTextColor("#E03E3E") || ed.Doc.Source() != "hello <span style=\"color: #e03e3e\">world</span>\n" {
		t.Fatalf("上色后 %q", ed.Doc.Source())
	}
	if !ed.SetTextColor("#0b6e99") || !strings.Contains(ed.Doc.Source(), "color: #0b6e99\">world</span>") {
		t.Fatalf("改色后 %q", ed.Doc.Source())
	}
	if !ed.SetTextColor("") || ed.Doc.Source() != "hello world\n" {
		t.Fatalf("去色后 %q", ed.Doc.Source())
	}
	ed.Run("undo")
	if !strings.Contains(ed.Doc.Source(), "#0b6e99") {
		t.Fatalf("撤销后 %q", ed.Doc.Source())
	}
	if ed.SetTextColor("javascript:x") {
		t.Fatal("非法颜色应拒绝")
	}

	d := NewDocument("a <span style=\"color: rgb(255, 0, 0)\">红</span> <font color=\"blue\">蓝</font>\n")
	p := projectLeaf(d, d.blocks[0].Index, 0, d.blocks[0].End, expansion{}, leafOpts{})
	if p.text != "a 红 蓝" || len(p.colors) != 2 || p.colors[0] != ui.RGB(255, 0, 0) || p.colors[1] != ui.Hex("#0000ff") {
		t.Fatalf("投影 %q colors=%v", p.text, p.colors)
	}
}

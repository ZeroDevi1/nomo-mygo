package mdedit

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestCalloutHeader：自定义标题、扩展类型与折叠标记的解析。
func TestCalloutHeader(t *testing.T) {
	cases := []struct {
		src          string
		tone         int
		fold         byte
		title, label string
	}{
		{"[!NOTE]\nbody", 0, 0, "", "Note"},
		{"[!warning]- 小心台阶\nbody", 3, '-', "小心台阶", "Warning"},
		{"[!success]+", 1, '+', "", "Success"},
		{"[!bug] 已知问题", 4, 0, "已知问题", "Bug"},
		{"[!custom]", 0, 0, "", "Custom"},
	}
	for _, c := range cases {
		h, ok := parseCalloutHeader(c.src)
		if !ok || h.tone != c.tone || h.fold != c.fold || h.title != c.title || h.label != c.label {
			t.Errorf("%q → %+v", c.src, h)
		}
	}
	if _, ok := parseCalloutHeader("[! NOTE]"); ok {
		t.Error("类型含空格不是提示块")
	}
}

// TestCalloutFold：“-”默认收起（正文行高为 0、块变矮），点标题展开；光标进入块内时自动展开；标题行原文被隐藏。
func TestCalloutFold(t *testing.T) {
	src := "intro\n\n> [!TIP]- 折叠标题\n> 第一行\n>\n> 第二段\n\nend\n"
	ed, tt := newTester(t, src, 800, 600)
	tt.Frame()
	en := ed.ensure(1)
	collapsed := en.height
	for k := range en.rows {
		ed.ensureRow(1, k)
		if en.rows[k].height != 0 {
			t.Fatalf("收起时第 %d 行高度 %v", k, en.rows[k].height)
		}
	}
	i, fi, ok := ed.calloutToggleHit(calloutTitlePoint(ed, 1))
	if !ok {
		t.Fatal("标题行不可点击")
	}
	ed.toggleCallout(i, fi)
	tt.Frame()
	en = ed.ensure(1)
	if en.height <= collapsed {
		t.Fatalf("展开后高度 %v 应大于收起时 %v", en.height, collapsed)
	}
	ed.toggleCallout(i, fi)
	tt.Frame()
	ed.Select(len("intro\n\n> [!TIP]- 折叠标题\n> 第"), len("intro\n\n> [!TIP]- 折叠标题\n> 第"))
	tt.Frame()
	if ed.calloutRowHidden(1, ed.ensure(1), 0) {
		t.Fatal("光标在块内时应展开")
	}
}

// calloutTitlePoint 返回块 i 中第一个可折叠提示块标题行中部的位置（编辑区坐标）。
func calloutTitlePoint(ed *Editor, i int) (pt ui.Point) {
	en := ed.ensure(i)
	for _, v := range ed.vblocks {
		if v.i != i {
			continue
		}
		f := en.frames[0]
		pt.X = ed.left() + f.indent + 40
		pt.Y = v.top + en.rows[f.first].y - calloutTitleH - quotePad + 8 + calloutTitleH/2
	}
	return
}

package mdedit

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// at 把光标放到 src 中 needle 之后（after 为 false 时放在之前）。
func at(t *testing.T, ed *Editor, needle string, after bool) {
	t.Helper()
	i := strings.Index(ed.Doc.Source(), needle)
	if i < 0 {
		t.Fatalf("原文中没有 %q", needle)
	}
	if after {
		i += len(needle)
	}
	ed.Doc.SetSelection(Selection{i, i})
	ed.dirty = true
}

func TestListEnterContinuesAndExits(t *testing.T) {
	ed, tt := newTester(t, "- [x] 甲\n- 乙\n\n1. 一\n", 800, 600)
	at(t, ed, "甲", true)
	tt.Key(0, ui.KeyEnter)
	tt.Type("丙")
	tt.Frame()
	if got := ed.Doc.Source(); got != "- [x] 甲\n- [ ] 丙\n- 乙\n\n1. 一\n" {
		t.Fatalf("任务项回车 %q", got)
	}
	at(t, ed, "一", true)
	tt.Key(0, ui.KeyEnter)
	tt.Type("二")
	tt.Frame()
	if got := ed.Doc.Source(); !strings.HasSuffix(got, "1. 一\n2. 二\n") {
		t.Fatalf("有序项回车 %q", got)
	}
	// 空项回车：去掉标记，结束列表。
	tt.Key(0, ui.KeyEnter)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if got := ed.Doc.Source(); !strings.HasSuffix(got, "1. 一\n2. 二\n\n") {
		t.Fatalf("空项回车 %q", got)
	}
	checkIndex(t, ed.Doc)
}

func TestQuoteEnterAndBackspaceLift(t *testing.T) {
	ed, tt := newTester(t, "> 引用\n", 800, 600)
	at(t, ed, "引用", true)
	tt.Key(0, ui.KeyEnter)
	tt.Type("新")
	tt.Frame()
	if got := ed.Doc.Source(); got != "> 引用\n>\n> 新\n" {
		t.Fatalf("引用内回车 %q", got)
	}
	// 行首退格：去掉一层“>”，内容移出引用。
	at(t, ed, "新", false)
	tt.Key(0, ui.KeyBackspace)
	tt.Frame()
	if got := ed.Doc.Source(); got != "> 引用\n>\n新\n" {
		t.Fatalf("退格提出引用 %q", got)
	}
	// 列表项行首退格：去掉标记。
	ed2, tt2 := newTester(t, "- 项\n", 800, 600)
	at(t, ed2, "项", false)
	tt2.Key(0, ui.KeyBackspace)
	tt2.Frame()
	if got := ed2.Doc.Source(); got != "项\n" {
		t.Fatalf("退格去掉列表标记 %q", got)
	}
}

func TestTabIndentsAndOutdentsListItem(t *testing.T) {
	ed, tt := newTester(t, "- 甲\n- 乙\n", 800, 600)
	at(t, ed, "乙", true)
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if got := ed.Doc.Source(); got != "- 甲\n  - 乙\n" {
		t.Fatalf("Tab 缩进 %q", got)
	}
	if got := ed.Doc.Source()[ed.Doc.Sel.Head-len("乙") : ed.Doc.Sel.Head]; got != "乙" {
		t.Fatalf("缩进后光标位置错误：前面是 %q", got)
	}
	tt.Key(ui.Shift, ui.KeyTab)
	tt.Frame()
	if got := ed.Doc.Source(); got != "- 甲\n- 乙\n" {
		t.Fatalf("Shift+Tab 取消缩进 %q", got)
	}
}

func TestTableTabMovesBetweenCellsAndEnterIsIgnored(t *testing.T) {
	src := "| a | b |\n|---|---|\n| 1 | 2 |\n"
	ed, tt := newTester(t, src, 800, 600)
	at(t, ed, "a", true)
	tt.Key(0, ui.KeyTab)
	if a, z := ed.Doc.Sel.Range(); ed.Doc.Source()[a:z] != "b" {
		t.Fatalf("Tab 后选中 %q", ed.Doc.Source()[a:z])
	}
	tt.Key(0, ui.KeyTab)
	if a, z := ed.Doc.Sel.Range(); ed.Doc.Source()[a:z] != "1" {
		t.Fatalf("Tab 换行后选中 %q", ed.Doc.Source()[a:z])
	}
	tt.Key(0, ui.KeyEnter)
	if ed.Doc.Source() != src {
		t.Fatalf("单元格内回车改动了原文 %q", ed.Doc.Source())
	}
}

func TestTaskCheckboxClickToggles(t *testing.T) {
	ed, tt := newTester(t, "- [ ] 待办\n", 800, 600)
	tt.Frame()
	v := ed.vsegs[0]
	r := &ed.entries[0].rows[0]
	x := ed.left() + r.indent - listIndent + 2 + taskBox/2
	tt.ClickAt(x, v.y+r.padTop+ed.lineH/2)
	tt.Frame()
	if got := ed.Doc.Source(); got != "- [x] 待办\n" {
		t.Fatalf("勾选 %q", got)
	}
	tt.Command("undo")
	if got := ed.Doc.Source(); got != "- [ ] 待办\n" {
		t.Fatalf("撤销勾选 %q", got)
	}
}

func TestComponentProjection(t *testing.T) {
	src := "> [!WARNING]\n> 注意 <strong>粗</strong> &#32;x <!-- 注 --> \\[1\\]\n"
	ed, tt := newTester(t, src, 800, 600)
	tt.Frame()
	en := ed.entries[0]
	if len(en.frames) != 1 || en.frames[0].kind != frameCallout || en.frames[0].tone != 3 {
		t.Fatalf("提示块装饰 %+v", en.frames)
	}
	if got := en.rows[0].proj.text; got != "注意 粗  x <!-- 注 --> [1]" {
		t.Fatalf("组件投影 %q", got)
	}
	// 列表与表格的结构：每个叶子一行，列表首行带标记。
	ed2, tt2 := newTester(t, "1. 甲\n   - 乙\n\n| a | b |\n|:-|-:|\n| 1 | 2 |\n", 800, 600)
	tt2.Frame()
	rows := ed2.entries[0].rows
	if len(rows) != 2 || rows[0].marker != "1." || rows[1].marker != "◦" || rows[1].indent <= rows[0].indent {
		t.Fatalf("列表行 %+v", rows)
	}
	cells := ed2.entries[1].rows
	if len(cells) != 4 || cells[0].band != cells[1].band || cells[2].band == cells[0].band || cells[1].align != ui.End {
		t.Fatalf("表格单元格 %+v", cells)
	}
}

func TestFrontMatterIsOneBlock(t *testing.T) {
	d := NewDocument("---\ntitle: x\ntags:\n  - a\n---\n\n# 正文\n")
	b := d.Blocks()
	if len(b) != 2 || b[0].Kind != KindFrontMatter || d.Source()[b[0].Start:b[0].End] != "---\ntitle: x\ntags:\n  - a\n---" {
		t.Fatalf("front matter 块 %+v", b)
	}
	d.Replace(4, 4, "z", editOther)
	checkIndex(t, d)
}

func TestCodeFenceHiddenOutsideBlock(t *testing.T) {
	ed, tt := newTester(t, "段落\n\n```go\nx := 1\n```\n", 800, 600)
	tt.Frame()
	rows := ed.entries[1].rows
	if rows[0].bandH != 0 || rows[len(rows)-1].bandH != 0 || rows[1].bandH == 0 {
		t.Fatalf("光标在外时围栏应隐藏：%v %v %v", rows[0].bandH, rows[1].bandH, rows[len(rows)-1].bandH)
	}
	at(t, ed, "x := 1", false)
	tt.Frame()
	if ed.entries[1].rows[0].bandH == 0 {
		t.Fatal("光标进入代码块后围栏应显示")
	}
}

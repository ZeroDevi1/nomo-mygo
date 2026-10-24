package mdedit

import "testing"

func TestNextFootnoteID(t *testing.T) {
	for src, want := range map[string]string{
		"无脚注":                        "1",
		"a[^1] b[^3]\n\n[^3]: x":     "4",
		"[^note] [^009]":             "10",
		"[^99999999999999999999999]": "100000000000000000000000",
	} {
		if got := nextFootnoteID(src); got != want {
			t.Errorf("%q: 得到 %s，应为 %s", src, got, want)
		}
	}
}

// TestInsertFootnoteUndoesAsOneStep 插入脚注改动两处原文（引用与文末定义），撤销、重做各为一步。
func TestInsertFootnoteUndoesAsOneStep(t *testing.T) {
	src := "第一段[^1]\n\n第二段\n\n[^1]: 已有"
	ed, tt := newTester(t, src, 600, 400)
	p := len("第一段[^1]\n\n第二段")
	ed.Doc.SetSelection(Selection{Anchor: p, Head: p})
	ed.Run("footnote")
	tt.Frame()
	want := "第一段[^1]\n\n第二段[^2]\n\n[^1]: 已有\n\n[^2]: "
	if got := ed.Doc.Source(); got != want {
		t.Fatalf("插入后：%q", got)
	}
	if ed.Doc.Sel.Head != len(want) {
		t.Fatalf("光标 %d，应在定义末尾 %d", ed.Doc.Sel.Head, len(want))
	}
	ed.Run("undo")
	tt.Frame()
	if got := ed.Doc.Source(); got != src || ed.Doc.Sel.Head != p {
		t.Fatalf("撤销一次应回到原文：%q 光标 %d", got, ed.Doc.Sel.Head)
	}
	ed.Run("redo")
	tt.Frame()
	if got := ed.Doc.Source(); got != want {
		t.Fatalf("重做一次应恢复两处：%q", got)
	}
	// 之后的普通编辑不与脚注步骤相连。
	ed.Run("undo")
	ed.Run("redo")
	ed.Doc.SetSelection(Selection{Anchor: len(want), Head: len(want)})
	ed.replaceSel("注", editOther)
	ed.Run("undo")
	if got := ed.Doc.Source(); got != want {
		t.Fatalf("撤销键入不应连带撤销脚注：%q", got)
	}
}

func TestTocList(t *testing.T) {
	hs := []Heading{{Level: 1, Text: "Hello World!"}, {Level: 2, Text: "中文 标题"}, {Level: 2, Text: "Hello World"},
		{Level: 3, Text: "***"}, {Level: 2, Text: "a_[b]"}}
	want := "- [Hello World!](#hello-world)\n  - [中文 标题](#中文-标题)\n  - [Hello World](#hello-world-2)\n" +
		"    - [\\*\\*\\*](#heading-4)\n  - [a\\_\\[b\\]](#ab)"
	if got := tocList(hs); got != want {
		t.Fatalf("目录：\n%s\n应为：\n%s", got, want)
	}
}

func TestToolbarInserts(t *testing.T) {
	ed, tt := newTester(t, "段落", 600, 400)
	ed.Doc.SetSelection(Selection{Anchor: 0, Head: len("段落")})
	ed.Run("superscript")
	if got := ed.Doc.Source(); got != "^段落^" { // 选区无空白时用 ^上标^ 语法（见 toggleScript）
		t.Fatalf("上标：%q", got)
	}
	ed.Run("superscript")
	if got := ed.Doc.Source(); got != "段落" {
		t.Fatalf("再次切换应取消上标：%q", got)
	}
	ed.Doc.SetSelection(Selection{Anchor: len("段落"), Head: len("段落")})
	ed.InsertTable(2, 1)
	tt.Frame()
	want := "段落\n\n|  |  |\n| :--- | :--- |\n|  |  |\n|  |  |\n\n"
	if got := ed.Doc.Source(); got != want {
		t.Fatalf("表格（列数至少 2）：%q", got)
	}
	if p := len("段落\n\n| "); ed.Doc.Sel.Head != p {
		t.Fatalf("光标 %d，应在第一个表头单元格 %d", ed.Doc.Sel.Head, p)
	}

	ed, _ = newTester(t, "公式 a-->b", 600, 400)
	ed.SetLabels(Labels{InlineComment: "行内注释"})
	ed.Doc.SetSelection(Selection{Anchor: len("公式 "), Head: len("公式 a-->b")})
	ed.Run("inlineComment")
	if got := ed.Doc.Source(); got != "公式 <!-- a-- >b -->" {
		t.Fatalf("行内注释：%q", got)
	}
	a, z := ed.Doc.Sel.Range()
	if ed.Doc.Source()[a:z] != "a-- >b" {
		t.Fatalf("应选中注释内容：%q", ed.Doc.Source()[a:z])
	}
	ed.Doc.SetSelection(Selection{Anchor: 0, Head: 0})
	ed.Run("mathInline")
	if got := ed.Doc.Source(); got[:3] != "$x$" {
		t.Fatalf("行内公式：%q", got)
	}
}

// TestEmptyFootnoteDefinition 文末空脚注定义（刚输入 “[^1]: ”）没有子块，仍须有一行供光标定位，不能越界。
func TestEmptyFootnoteDefinition(t *testing.T) {
	src := "a\n\n[^1]: "
	ed, tt := newTester(t, src, 600, 400)
	ed.Doc.SetSelection(Selection{Anchor: len(src), Head: len(src)})
	ed.reveal, ed.dirty = true, true
	tt.Frame()
	if n := len(ed.entries[1].rows); n != 1 {
		t.Fatalf("空定义应显示一行原文，得到 %d 行", n)
	}
}

// TestRefreshToc：标题变化后刷新目录区列表；未变化时不改写，撤销恢复原列表。
func TestRefreshToc(t *testing.T) {
	src := "# A\n\n<!-- toc -->\n- [A](#a)\n<!-- /toc -->\n\n## B\n"
	ed := NewEditor(NewDocument(src))
	if !ed.RefreshToc() {
		t.Fatal("新增标题后应刷新目录")
	}
	want := "# A\n\n<!-- toc -->\n- [A](#a)\n  - [B](#b)\n<!-- /toc -->\n\n## B\n"
	if got := ed.Doc.Source(); got != want {
		t.Fatalf("刷新后 %q", got)
	}
	if ed.RefreshToc() {
		t.Fatal("没有变化时不应改写")
	}
	ed.Run("undo")
	if ed.Doc.Source() != src {
		t.Fatalf("撤销后 %q", ed.Doc.Source())
	}
}

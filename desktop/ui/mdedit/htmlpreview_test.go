package mdedit

import (
	"strings"
	"testing"
)

func TestHTMLPreviewParseAndKinds(t *testing.T) {
	root, ok := parseHTMLPreview(`<table><tr><th colspan="2">头</th></tr><tr><td rowspan=2>甲<td><p>一<p>二</tr><tr><td>乙</table>`)
	if !ok {
		t.Fatal("表格应可预览")
	}
	tbl := root.children[0]
	if tbl.tag != "table" || len(tbl.children) != 3 {
		t.Fatalf("隐式关闭后应有 3 行：%d", len(tbl.children))
	}
	if row := tbl.children[1]; len(row.children) != 2 || len(row.children[1].children) != 2 {
		t.Fatalf("第二行单元格或段落数不对")
	}
	if _, ok := parseHTMLPreview("<details><summary>x</summary></details>"); ok {
		t.Fatal("白名单外的标签应按原文显示（与原版一致）")
	}
	cases := map[string]uint8{
		"<!-- 注释 -->":               previewComment,
		"<!-- toc -->":              previewNone,
		"<div><p>段落</p></div>":      previewHTML,
		"<details>\n<summary>":      previewNone,
		"<script>alert(1)</script>": previewNone,
	}
	for src, want := range cases {
		if got := htmlBlockKind(src); got != want {
			t.Errorf("%q kind=%d，期望 %d", src, got, want)
		}
	}
	if got := collapseSpace("  a \n\t b  "); got != " a b " {
		t.Fatalf("空白折叠 %q", got)
	}
}

func TestHTMLPreviewRowsAndToc(t *testing.T) {
	src := "# 甲\n\n<!-- toc -->\n- [甲](#甲)\n<!-- /toc -->\n\n<table><tr><th>A</th><th>B</th></tr><tr><td colspan=\"2\">合并</td></tr></table>\n\n<!-- 说明 -->\n\n## 乙\n"
	ed, tt := newTester(t, src, 900, 700)
	bs := ed.Doc.Blocks()
	if len(bs) != 5 || bs[1].Kind != KindToc {
		t.Fatalf("目录区应合并为一个块：%d 块，第 2 块 kind=%d", len(bs), bs[1].Kind)
	}
	for i, kind := range map[int]uint8{1: previewToc, 2: previewHTML, 3: previewComment} {
		r := &ed.ensure(i).rows[0]
		ed.ensureRow(i, 0)
		if r.htmlKind != kind || len(r.proj.objs) != 1 || r.proj.objs[0].html == nil {
			t.Fatalf("块 %d 应为预览对象 kind=%d：%+v", i, kind, r.proj.objs)
		}
	}
	toc := ed.entries[1].rows[0].proj.objs[0].html
	if len(toc.items) != 2 || toc.items[1].Text != "乙" {
		t.Fatalf("目录条目 %+v", toc.items)
	}
	// 修改标题后目录卡片随之更新。
	at := strings.Index(ed.Doc.Source(), "乙")
	ed.Doc.SetSelection(Selection{at + len("乙"), at + len("乙")})
	tt.Type("丙")
	tt.Frame()
	ed.ensureRow(1, 0)
	if toc := ed.entries[1].rows[0].proj.objs[0].html; toc.items[1].Text != "乙丙" {
		t.Fatalf("标题修改后目录未更新：%+v", toc.items)
	}
	// 光标进入表格块原文内：显示原文而不是预览。
	tb := ed.Doc.Blocks()[2]
	ed.Doc.SetSelection(Selection{tb.Start + 3, tb.Start + 3})
	ed.dirty = true
	tt.Frame()
	if r := &ed.entries[2].rows[0]; len(r.proj.objs) != 0 || !strings.HasPrefix(r.proj.text, "<table>") {
		t.Fatalf("光标在块内应显示原文：%q", r.proj.text)
	}
}

func TestFootnoteRefLabelAndJump(t *testing.T) {
	src := "正文[^n1]继续。\n\n[^n1]: 定义内容。\n"
	ed, tt := newTester(t, src, 800, 600)
	ed.Doc.SetSelection(Selection{len(src), len(src)})
	ed.dirty = true
	tt.Frame()
	r := &ed.entries[0].rows[0]
	if !strings.Contains(r.proj.text, "正文n1继续") {
		t.Fatalf("引用应显示为标签：%q", r.proj.text)
	}
	// 点击标签：跳到定义正文。
	at := strings.Index(r.proj.text, "n1")
	s := ed.segOf(0, 0)
	cr := s.layout.Caret(u16Len(r.proj.text[:at]) + 1)
	var y float32
	for _, v := range ed.vsegs {
		if v.i == 0 {
			y = v.y
		}
	}
	tt.ClickAt(ed.left()+s.x+cr.X, y+s.padTop+cr.Y+cr.H/2)
	tt.Frame()
	if head := ed.Doc.Sel.Head; head != strings.Index(src, "定义") {
		t.Fatalf("点击后光标 %d，期望在定义处 %d", head, strings.Index(src, "定义"))
	}
}

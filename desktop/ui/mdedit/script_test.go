package mdedit

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui/markdown/parser"
)

// smallText 返回投影中按小字号（上下标）显示的各段文字。
func smallText(p *projection) []string {
	var out []string
	for _, r := range p.runs {
		if r.st&stSmall != 0 && r.st&stMarker == 0 {
			out = append(out, projRunText(p, r))
		}
	}
	return out
}

func projRunText(p *projection, r run) string {
	end := len(p.text)
	for _, o := range p.runs {
		if o.dispByte > r.dispByte && o.dispByte < end {
			end = o.dispByte
		}
	}
	return p.text[r.dispByte:end]
}

func TestScriptSyntax(t *testing.T) {
	cases := []struct {
		src, text string
		small     []string
	}{
		{"H~2~O 与 x^2^", "H2O 与 x2", []string{"2", "2"}},
		{"a ~x~ b", "a x b", []string{"x"}},
		{"~~删除~~ 与 ~a b~", "删除 与 a b", nil}, // 双 ~ 与含空白的单 ~ 仍是删除线
		{"x^a b^ 与 2^10", "x^a b^ 与 2^10", nil},
		{`\^a^ 与 ~~x~ y`, "^a^ 与 ~~x~ y", nil},
		{"`a^b^` 代码", "a^b^ 代码", nil},
	}
	for _, c := range cases {
		d := NewDocument(c.src + "\n")
		p := projectLeaf(d, d.blocks[0].Index, 0, d.blocks[0].End, expansion{}, leafOpts{})
		if p.text != c.text || strings.Join(smallText(p), "|") != strings.Join(c.small, "|") {
			t.Errorf("%q: 显示 %q 上下标 %q，期望 %q %q", c.src, p.text, smallText(p), c.text, c.small)
		}
	}
	// 光标触及时显示定界符。
	d := NewDocument("x^2^\n")
	if p := projectLeaf(d, d.blocks[0].Index, 0, d.blocks[0].End, expansion{inBlock: true, caret: 4}, leafOpts{}); p.text != "x^2^" {
		t.Errorf("展开 %q", p.text)
	}
}

func TestExpandScripts(t *testing.T) {
	src := "H~2~O x^2^ ~~del~~ ~a b~ ~y~ `c^d^` \\^e^\n\n```\nf^g^\n```\n"
	md := parser.New(parser.Default | parser.ExtDollarMath)
	md.Set(src)
	want := "H<sub>2</sub>O x<sup>2</sup> ~~del~~ ~a b~ <sub>y</sub> `c^d^` \\^e^\n\n```\nf^g^\n```\n"
	if got := ExpandScripts(md); got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	md.Set(want)
	if html := md.HTML(); !strings.Contains(html, "H<sub>2</sub>O x<sup>2</sup> <del>del</del> <del>a b</del> <sub>y</sub>") {
		t.Fatalf("HTML %q", html)
	}
}

func TestToggleScript(t *testing.T) {
	ed := NewEditor(NewDocument("x2 and a b\n"))
	ed.Doc.Sel = Selection{Anchor: 1, Head: 2}
	ed.Run("superscript")
	if got := ed.Doc.Source(); got != "x^2^ and a b\n" {
		t.Fatalf("上标 %q", got)
	}
	ed.Run("superscript")
	if got := ed.Doc.Source(); got != "x2 and a b\n" {
		t.Fatalf("取消上标 %q", got)
	}
	ed.Doc.Sel = Selection{Anchor: 7, Head: 10} // “a b” 含空白：用标签
	ed.Run("subscript")
	if got := ed.Doc.Source(); got != "x2 and <sub>a b</sub>\n" {
		t.Fatalf("下标 %q", got)
	}
	ed.Run("subscript")
	if got := ed.Doc.Source(); got != "x2 and a b\n" {
		t.Fatalf("取消下标 %q", got)
	}
}

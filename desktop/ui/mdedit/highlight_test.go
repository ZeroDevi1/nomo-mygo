package mdedit

import (
	"hash/maphash"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"nomo/desktop/providers/typesetting"
)

func TestCodeHighlightCarriesStateAcrossLines(t *testing.T) {
	src := "```go\n/* a\nb */\nx := 1\n```\n"
	ed, tt := newTester(t, src, 800, 600)
	en := &ed.entries[0]
	if en.frames[0].tok == nil {
		t.Fatal("go 代码块应有 Tokenizer")
	}
	if !en.rows[2].hlKnown || en.rows[2].hlIn == 0 {
		t.Fatal("块注释第二行的行首状态应处于注释中")
	}
	if en.rows[3].hlIn != 0 {
		t.Fatal("注释结束后的行首状态应回到初始")
	}
	keep := en.rows[3].layout
	// 拆开“/*”：第二行不再处于注释中，应按新状态重排；第四行状态未变，复用布局。
	p := len("```go\n") + 1
	ed.Doc.SetSelection(Selection{p, p})
	tt.Type(" ")
	tt.Frame()
	en = &ed.entries[0]
	if en.rows[2].hlIn != 0 || en.rows[2].sig != 1<<2 {
		t.Fatalf("第二行行首状态 %d 签名 %d", en.rows[2].hlIn, en.rows[2].sig)
	}
	if en.rows[3].layout != keep {
		t.Fatal("状态未变的代码行应复用布局")
	}
}

func TestCodeSpansMergeAndColor(t *testing.T) {
	ed, _ := newTester(t, "x\n", 400, 300)
	text := `x := "s" // c`
	toks, _ := codeTokenizer("go").Tokenize(text, 0, nil)
	spans := ed.codeSpans(text, toks)
	got := ""
	colored := 0
	for _, sp := range spans {
		got += sp.Text
		if sp.Color.A > 0 {
			colored++
		}
	}
	if got != text {
		t.Fatalf("片段拼接为 %q", got)
	}
	if colored == 0 {
		t.Fatal("字符串与注释应着色")
	}
	if codeTokenizer("no-such-lang") != nil || codeTokenizer("") != nil {
		t.Fatal("未知语言不应高亮")
	}
}

func TestManuscriptHighlightPreservesSourceAndProjection(t *testing.T) {
	const source = `\noindent\textbf{关键词}\quad 正文 \custom{中文}`
	e := NewManuscriptEditor(editor.NewBuffer(source), true)
	defer e.CloseManuscript()
	e.SetManuscriptLanguage(typesetting.LaTeX.Language)
	e.setTheme(&ui.Theme{})
	b := e.Doc.blocks[0]
	r := row{b: b.End - b.Start}
	tokenColor := e.syntax.Tokens[editor.TokenFunction].Color
	raw := e.projectManuscript(b, &r, expansion{inBlock: true})
	if !raw.manuscriptSource || raw.text != source {
		t.Fatalf("expanded row changed source: %q", raw.text)
	}
	highlighted := e.manuscriptCodeSpans(nil, ui.Span{Text: raw.text}, 0)
	if len(highlighted) == 0 || highlighted[0].Color != tokenColor ||
		!strings.Contains(highlighted[0].Text, `\textbf`) {
		t.Fatalf("expanded commands lack syntax color: %+v", highlighted)
	}
	reading := e.projectManuscript(b, &r, expansion{})
	if reading.text != "关键词\u2003正文 "+`\custom{中文}` || reading.manuscriptSource {
		t.Fatalf("unexpected reading projection: %q", reading.text)
	}
	var display strings.Builder
	var command string
	for _, span := range e.spans(reading, nil) {
		display.WriteString(span.Text)
		if span.Color == tokenColor {
			command += span.Text
		}
	}
	if display.String() != reading.text || command != `\custom` {
		t.Fatalf("reading projection colored %q in %q", command, display.String())
	}

	e.SetManuscriptLanguage(typesetting.Typst.Language)
	const code = "/* first line\ncontinuation */\n#math.equation(block: true)"
	var rebuilt strings.Builder
	for _, span := range e.manuscriptCodeSpans(nil, ui.Span{Text: code}, 0) {
		rebuilt.WriteString(span.Text)
		if span.Text == "continuation */" && span.Color != e.syntax.Tokens[editor.TokenComment].Color {
			t.Fatal("Typst comment state did not carry to the next line")
		}
		if span.Text == "#math.equation" && span.Color != tokenColor {
			t.Fatal("qualified Typst command lost its syntax color")
		}
	}
	if rebuilt.String() != code {
		t.Fatalf("highlight spans changed source: %q", rebuilt.String())
	}
}

func TestRawBlockKeepsContentIndent(t *testing.T) {
	cases := []struct{ src, want string }{
		{"```py\ndef f():\n    return 1\n```\n", "    return 1"},
		{"- a\n  ```js\n  if (x) {\n    y()\n  ```\n", "  y()"},
		{"> ```sh\n>   echo hi\n> ```\n", "  echo hi"},
	}
	for _, c := range cases {
		d := NewDocument(c.src)
		var got []string
		for _, b := range d.Blocks() {
			rows, _ := buildRows(d, 0, b, maphash.MakeSeed(), true)
			for _, r := range rows {
				got = append(got, c.src[b.Start+r.a:b.Start+r.b])
			}
		}
		if !slices.Contains(got, c.want) {
			t.Errorf("%q 的行 %q 中缺少 %q", c.src, got, c.want)
		}
	}
}

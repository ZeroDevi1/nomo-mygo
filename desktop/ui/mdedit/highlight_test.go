package mdedit

import (
	"hash/maphash"
	"slices"
	"testing"
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
			rows, _ := buildRows(d, 0, b, maphash.MakeSeed())
			for _, r := range rows {
				got = append(got, c.src[b.Start+r.a:b.Start+r.b])
			}
		}
		if !slices.Contains(got, c.want) {
			t.Errorf("%q 的行 %q 中缺少 %q", c.src, got, c.want)
		}
	}
}

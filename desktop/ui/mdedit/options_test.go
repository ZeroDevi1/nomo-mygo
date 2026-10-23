package mdedit

import (
	"strings"
	"testing"
)

func TestEditorOptions(t *testing.T) {
	ed, tt := newTester(t, "段落 `code` 结束\n\n```go\nfunc a() {\nreturn\n}\n```\n", 800, 600)
	d := ed.Doc
	// 代码行默认编号（围栏行不编号），行号栏让出宽度。
	en := ed.ensure(1)
	var nums []int32
	for _, r := range en.rows {
		if r.lineNo > 0 {
			nums = append(nums, r.lineNo)
		}
	}
	if len(nums) != 3 || nums[2] != 3 {
		t.Fatalf("代码行行号 %v", nums)
	}
	withNumbers := en.rows[1].indent
	ed.SetOptions(Options{HideLineNumbers: true})
	tt.Frame()
	if ed.ensure(1).rows[1].indent >= withNumbers {
		t.Fatal("关闭行号后应收回行号栏")
	}
	// 行内代码按原文显示：反引号可见。
	ed.SetOptions(Options{RawInlineCode: true})
	tt.Frame()
	if p := ed.segOf(0, 0).proj; p == nil || !strings.Contains(p.text, "`code`") {
		t.Fatalf("关闭行内代码渲染后应显示反引号：%+v", p)
	}
	// 代码块内 Tab 插入设置的缩进，Shift+Tab 去掉。
	ed.SetOptions(Options{CodeIndent: "    "})
	at := strings.Index(d.Source(), "return")
	d.SetSelection(Selection{Anchor: at, Head: at})
	tt.Frame()
	if s, ok := ed.segAt(at); !ok || !ed.codeTab(s, false) || !strings.Contains(d.Source(), "\n    return") {
		t.Fatalf("Tab 后 %q", d.Source())
	}
	s, _ := ed.segAt(d.Sel.Head)
	ed.codeTab(s, true)
	if !strings.Contains(d.Source(), "\nreturn") {
		t.Fatalf("Shift+Tab 后 %q", d.Source())
	}
	// 插入代码块与图表使用默认语言、默认模板。
	ed.SetOptions(Options{CodeLanguage: "ts", DiagramCode: "pie\n  \"A\" : 1"})
	d.SetSelection(Selection{Anchor: 0, Head: 0})
	ed.Run("codeBlock")
	if !strings.Contains(d.Source(), "```ts\n") {
		t.Fatalf("代码块应带默认语言：%q", d.Source())
	}
	ed.Run("mermaid")
	if !strings.Contains(d.Source(), "```mermaid\npie\n  \"A\" : 1\n```") {
		t.Fatalf("图表应使用默认模板：%q", d.Source())
	}
}

func TestPlainText(t *testing.T) {
	got := plainText("# 标题\n\n**粗** 与 `代码` [链接](http://x)\n\n- 项一\n- 项二")
	for _, want := range []string{"标题", "粗 与 代码 链接", "项一\n", "项二"} {
		if !strings.Contains(got, want) {
			t.Fatalf("纯文本 %q 缺少 %q", got, want)
		}
	}
	if strings.ContainsAny(got, "#*`[]") {
		t.Fatalf("纯文本不应含 Markdown 语法：%q", got)
	}
}

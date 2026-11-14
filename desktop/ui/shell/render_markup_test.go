package shell

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui/markdown/parser"

	"nomo/desktop/ui/mdedit"
)

// TestExportRenderMarkers：导出时按 MyGo 生成的 HTML 标记识别公式与 Mermaid（renderExportMarkup 的替换依据）。
func TestExportRenderMarkers(t *testing.T) {
	md := parser.New(parser.Default | parser.ExtDollarMath)
	md.Set("行内 $a^2$ 公式\n\n$$\nx=1\n$$\n\n```mermaid\nflowchart TD\n  A-->B\n```\n")
	out := md.HTML()
	if n := len(exportMathRe.FindAllStringSubmatchIndex(out, -1)); n != 2 {
		t.Errorf("公式标记 %d 处，HTML：%s", n, out)
	}
	if n := len(exportMermaidRe.FindAllStringSubmatchIndex(out, -1)); n != 1 {
		t.Errorf("Mermaid 标记 %d 处，HTML：%s", n, out)
	}
}

func TestExportMathDelimiters(t *testing.T) {
	src := "Inline $x^2$ and \\(y_1\\).\n\n" +
		"$$\na\n=\nb\n$$\n\n" +
		"> \\[\n> \\frac{c}{d}\n> =\n> e\n> \\]\n\n" +
		"Inline display \\[m+n\\] stays in its paragraph.\n\n" +
		"Code: `\\(code\\)`, escaped: \\\\(literal\\\\).\n"
	s, _ := newTestShell(t, writeDoc(t, src))
	for _, pdf := range []bool{false, true} {
		t.Run(fmt.Sprintf("pdf=%t", pdf), func(t *testing.T) {
			doc, outline := s.exportDocument(s.activeTab(), false, pdf)
			if len(outline) != 0 {
				t.Fatalf("formula lines unexpectedly became headings: %+v", outline)
			}
			out := replaceExportMarkup(doc, func(reqs []mdedit.RenderRequest) ([]markupItem, string, error) {
				want := []struct{ kind, src string }{
					{mdedit.RenderMathInline, "x^2"},
					{mdedit.RenderMathInline, "y_1"},
					{mdedit.RenderMathDisplay, "a\n=\nb"},
					{mdedit.RenderMathDisplay, "\\frac{c}{d}\n=\ne"},
					{mdedit.RenderMathDisplay, "m+n"},
				}
				if len(reqs) != len(want) {
					t.Fatalf("render requests = %+v", reqs)
				}
				items := make([]markupItem, len(reqs))
				for i, r := range reqs {
					if r.Kind != want[i].kind || r.Src != want[i].src {
						t.Errorf("request %d = %q %q, want %q %q", i, r.Kind, r.Src, want[i].kind, want[i].src)
					}
					items[i].html = fmt.Sprintf(`<span data-test-formula="%d"></span>`, i)
				}
				return items, "/* formula stylesheet */", nil
			})
			for _, want := range []string{
				`class="math-inline"`, `class="math-block"`, `data-test-formula="3"`, "/* formula stylesheet */", `<code>\(code\)</code>`,
				`<p>Inline display <span class="math-block"><span data-test-formula="4"></span></span> stays in its paragraph.</p>`,
			} {
				if !strings.Contains(out, want) {
					t.Errorf("export is missing %q", want)
				}
			}
		})
	}
}

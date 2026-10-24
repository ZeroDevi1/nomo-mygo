package shell

import (
	"testing"

	"github.com/egoist/mygo/ui/markdown/parser"
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

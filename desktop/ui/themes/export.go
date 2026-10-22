package themes

import (
	_ "embed"
	"slices"
	"strings"
)

// ExportCSS 是导出 HTML/PDF 的正文样式（原版 export-document.css 的生成副本），
// 颜色与尺寸取自 CSSDeclarations 给出的变量。
//
//go:embed export_document.css
var ExportCSS string

// CSSDeclarations 返回主题某一变体的 CSS 变量声明（"--name: value;" 依名称排序），
// 供导出文档的 :root 使用；没有对应变量名的令牌跳过。
func (t *Theme) CSSDeclarations(dark bool) string {
	v := t.Variants[0]
	if dark {
		v = t.Variants[1]
	}
	var decls []string
	for _, m := range []map[string]string{v.Tokens, v.Style} {
		for k, val := range m {
			if name := CSSVariables[k]; name != "" && !strings.ContainsAny(val, ";{}<>") {
				decls = append(decls, name+": "+val+";")
			}
		}
	}
	slices.Sort(decls)
	return strings.Join(decls, "")
}

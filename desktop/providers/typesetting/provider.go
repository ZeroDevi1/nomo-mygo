// Package typesetting 提供内置 LaTeX 与 Typst 编辑、预览及导出能力。
package typesetting

import (
	"strings"

	"github.com/egoist/mygo/ui/editor"

	"nomo/desktop/provider"
	"nomo/desktop/typeset"
)

const (
	kindLaTeX = "LaTeX"
	kindTypst = "Typst"
)

func open(path string) (provider.Session, error) {
	s, e := typeset.Open(path)
	if e != nil {
		return nil, e
	}
	return s, nil
}

var LaTeX = provider.Descriptor{
	ID: "latex", Name: kindLaTeX, Extension: ".tex",
	Language:  texLanguage,
	BindHints: func(st *editor.State) func() { return bindTypesetHints(st, kindLaTeX) },
	Open:      open, ExportPDF: typeset.ExportPDF, Fragment: previewFragment,
}
var Typst = provider.Descriptor{
	ID: "typst", Name: kindTypst, Extension: ".typ",
	Language:  typLanguage,
	BindHints: func(st *editor.State) func() { return bindTypesetHints(st, kindTypst) },
	Open:      open, ExportPDF: typeset.ExportPDF,
}

func previewFragment(s editor.Snapshot, offset int) (header, body string, start int, ok bool) {
	const begin = `\begin{document}`
	const end = `\end{document}`
	head := s.Slice(0, min(s.Len(), 32768))
	idx := strings.Index(head, begin)
	if idx < 0 || offset < idx+len(begin) {
		return
	}
	lo := max(idx+len(begin), offset-8192)
	hi := min(s.Len(), offset+8192)
	left, right := s.Slice(lo, offset), s.Slice(offset, hi)
	if k := strings.LastIndex(left, "\n\n"); k >= 0 {
		left = left[k+2:]
	} else if lo != idx+len(begin) {
		return
	}
	if k := strings.Index(right, "\n\n"); k >= 0 {
		right = right[:k]
	} else if hi != s.Len() {
		return
	}
	if k := strings.Index(right, end); k >= 0 {
		right = right[:k]
	}
	raw := left + right
	body = strings.TrimSpace(raw)
	start = offset - len(left) + strings.Index(raw, body)
	return head[:idx], body, start, true
}

package shell

import (
	"github.com/egoist/mygo/ui"
	"os"
	"path/filepath"
	"testing"
)

func TestTypesetCompletionInEditor(t *testing.T) {
	for _, tc := range []struct{ ext, input, accept, want string }{
		{"tex", `\`, "documentclass", `\documentclass`}, {"typ", "#", "set", "#set"},
		{"tex", `\subsec`, "subsection", `\subsection`}, {"tex", `\begin{equa`, "equation", `\begin{equation}`}, {"typ", "#fig", "figure", "#figure"}, {"typ", "#set tex", "text", "#set text"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "paper."+tc.ext)
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
			s, tt := newTestShell(t, path)
			for _, ch := range tc.input {
				tt.Type(string(ch))
				tt.Frame()
			}
			if !tt.HasText(tc.accept) {
				t.Fatalf("missing candidate %s: %v", tc.accept, tt.Texts())
			}
			if doc := map[string]string{`\`: "选择文档类，决定文章、报告或书籍的基本版式。", "#": "为当前作用域设置指定元素的默认参数。"}[tc.input]; doc != "" && !tt.HasText(doc) {
				t.Fatalf("missing documentation: %v", tt.Texts())
			}
			snapshot(t, tt, "syntax-"+tc.ext)
			tt.Key(0, ui.KeyEnter)
			tt.Frame()
			if got := s.activeTab().Source().Buffer.String(); got != tc.want {
				t.Fatalf("accepted %q, want %q", got, tc.want)
			}
		})
	}
}

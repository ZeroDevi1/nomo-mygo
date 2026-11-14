package shell

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestMarkdownCrossDocumentAnchor(t *testing.T) {
	const targetSource = "# 中文 标题\n\nfirst\n\n# 中文 标题\n\nsecond\n"
	for _, mode := range []string{modeSemantic, modeSource, modeSplit} {
		t.Run(mode, func(t *testing.T) {
			path := writeDoc(t, "# 起点\n\n[跳转](章节%20A.md#中文-标题-2)\n")
			target := filepath.Join(filepath.Dir(path), "章节 A.md")
			if err := os.WriteFile(target, []byte(targetSource), 0o644); err != nil {
				t.Fatal(err)
			}
			s, tt := newTestShell(t, path)
			start := s.activeTab()
			s.setMode(mode)
			tt.Frame()
			if mode == modeSplit {
				s.splitActive = modeSource
			}
			href := "章节%20A.md#" + url.PathEscape("中文-标题-2")
			linkPos := strings.Index(start.Doc.Source(), "跳转")
			if raw := start.Ed.LinkDestinationAt(linkPos); raw != "章节%20A.md#中文-标题-2" {
				t.Fatalf("source link resolution = %q", raw)
			}
			if mode == modeSemantic {
				start.Ed.Select(linkPos+len("跳"), linkPos+len("跳"))
				tt.Frame()
				caret, ok := start.Ed.CaretRect()
				if !ok {
					t.Fatal("link is outside the semantic viewport")
				}
				tt.ClickAtWith(ui.Cmd, s.editorOrigin.X+caret.X, s.editorOrigin.Y+caret.Y+caret.H/2)
			} else {
				s.openLink(ui.Services{}, start, href)
			}
			tab := s.activeTab()
			want := strings.LastIndex(targetSource, "中文 标题")
			if tab == start || !samePath(tab.Path, target) || len(s.tabs) != 2 {
				t.Fatalf("did not open target tab: active=%+v tabs=%d", tab, len(s.tabs))
			}
			if got := tab.Doc.Sel.Head; got != want {
				t.Fatalf("heading position = %d, want %d", got, want)
			}
			if mode != modeSemantic && tab.Source().Cursor() != want {
				t.Fatalf("source cursor = %d, want %d", tab.Source().Cursor(), want)
			}
			tt.Frame()
			s.openLink(ui.Services{}, tab, "#"+url.PathEscape("中文 标题"))
			if got := tab.Doc.Sel.Head; got != strings.Index(targetSource, "中文 标题") {
				t.Fatalf("same-document heading position = %d", got)
			}
			s.activate(s.findTab(path))
			s.openLink(ui.Services{}, start, (&url.URL{Scheme: "file", Path: target, Fragment: "中文-标题-2"}).String())
			if s.activeTab() != tab || len(s.tabs) != 2 || tab.Doc.Sel.Head != want {
				t.Fatalf("file URL did not reuse the target tab and focus its second heading")
			}
			s.openLink(ui.Services{}, tab, "#missing")
			if tab.Doc.Sel.Head != want || !strings.Contains(s.status, "missing") {
				t.Fatalf("missing fragment changed the position or was not reported: %q", s.status)
			}
		})
	}
}

package typesetting

import (
	"testing"

	"github.com/egoist/mygo/ui/editor"
)

func TestTypesetCompletionContext(t *testing.T) {
	for _, tc := range []struct{ kind, line string }{{kindLaTeX, "普通正文"}, {kindLaTeX, `% \sec`}, {kindLaTeX, `\\sec`}, {kindTypst, `// #fig`}, {kindTypst, `#text("before #fig`}} {
		if items, _ := hintsAt(tc.kind, tc.line); items != nil {
			t.Fatalf("unexpected candidates at %s", tc.line)
		}
	}
}

func TestTypesetLocalDeclarations(t *testing.T) {
	for _, tc := range []struct{ kind, src, name string }{{kindLaTeX, `\newcommand{\remember}[1]{#1}`, "remember"}, {kindTypst, "#let my-figure(x) = x", "my-figure"}} {
		b := editor.NewBuffer(tc.src)
		d := declaredHints{}
		items := d.get(b.Snapshot(), tc.kind)
		if len(items) != 1 || items[0].name != tc.name {
			t.Fatalf("declarations: %v", items)
		}
		b.Edit(editor.Edit{Start: 0, End: b.Len(), Text: "removed"})
		if len(d.get(b.Snapshot(), tc.kind)) != 0 {
			t.Fatal("stale declaration")
		}
	}
}

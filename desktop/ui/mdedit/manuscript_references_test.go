package mdedit

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui/editor"
	"nomo/desktop/typeset"
)

func TestManuscriptProjectsLiteralReferenceAlias(t *testing.T) {
	src := `#let figref(l) = [图#ref(l, supplement: none)]
#let custom(l) = [#strong[#ref(l, supplement: none)]]

#figref(<fig-err>)与` + "`figures/#figref(<fig-err>)`" + `中的#ref(<fig-err>, supplement: none)，#custom(<fig-err>)。
`
	e := NewManuscriptEditor(editor.NewBuffer(src), false)
	defer e.CloseManuscript()
	e.SetManuscriptProject(ManuscriptProject{Index: &typeset.ManuscriptReferences{
		ByLabel: map[string]typeset.ManuscriptReference{"fig-err": {Number: "4-2"}},
	}})
	i, _ := e.Doc.BlockAt(strings.Index(src, "#figref("))
	b := e.Doc.blocks[i]
	r := row{b: b.End - b.Start}
	want := "图4-2与figures/#figref(<fig-err>)中的4-2，#custom(<fig-err>)。"
	if got := e.projectManuscript(b, &r, expansion{}).text; got != want {
		t.Fatalf("projection = %q, want %q", got, want)
	}
	if got := e.projectManuscript(b, &r, expansion{inBlock: true}).text; got != src[b.Start:b.End] {
		t.Fatalf("expanded block lost source: %q", got)
	}
}

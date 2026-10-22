package shell

import (
	"github.com/egoist/mygo/ui/editor"

	"nomo/desktop/ui/mdedit"
)

func selection(pos int) mdedit.Selection { return mdedit.Selection{Anchor: pos, Head: pos} }

func editorEdit(at int, text string) editor.Edit { return editor.Edit{Start: at, End: at, Text: text} }

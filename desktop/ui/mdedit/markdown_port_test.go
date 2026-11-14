package mdedit

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestNestedTableHistory(t *testing.T) {
	for _, tc := range []struct {
		name, heading, prefix, closing string
	}{
		{"list", "- item\n", "  ", "- next\n"},
		{"ordered-task", "12. [x] item\n", "    ", "13. next\n"},
		{"quote", "> item\n>\n", "> ", ">\n> next\n"},
		{"nested", "> - item\n>\n", ">   ", ">\n> - next\n"},
		{"details", "<details open>\n<summary>Table</summary>\n\nitem\n", "", "</details>\n"},
		{"details-list", "<details open>\n<summary>Table</summary>\n\n- item\n", "  ", "- next\n\n</details>\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.heading + "\n" + tc.closing
			e := NewEditor(NewDocument(before))
			at := strings.Index(before, "item") + 1
			e.Select(at, at)
			e.InsertTable(1, 2)
			inserted, insertedSel := e.Doc.Source(), e.Doc.Sel
			want := tc.prefix + "|  |  |\n" + tc.prefix + "| :--- | :--- |\n" + tc.prefix + "|  |  |"
			if !strings.Contains(inserted, want) || !strings.HasSuffix(inserted, tc.closing) {
				t.Fatalf("inserted table escaped its container: %q", inserted)
			}
			if row, col, rows, cols, ok := e.TableCell(insertedSel.Head); !ok || row != 0 || col != 0 || rows != 2 || cols != 2 {
				t.Fatalf("inserted caret is outside the table: %v %v %v %v %v; source %q", row, col, rows, cols, ok, inserted)
			}
			if !e.TableCommand(insertedSel.Head, "addColumnAfter") {
				t.Fatal("adding a column failed")
			}
			edited, editedSel := e.Doc.Source(), e.Doc.Sel
			if row, col, _, cols, ok := e.TableCell(editedSel.Head); !ok || row != 0 || col != 1 || cols != 3 {
				t.Fatalf("added column did not preserve the table: %q; cell %d,%d of %d, found=%t", edited, row, col, cols, ok)
			}
			if !e.pasteTableCells("a|b\t2\n3\t4\n") {
				t.Fatal("TSV paste failed")
			}
			pasted, pastedSel := e.Doc.Source(), e.Doc.Sel
			if !strings.Contains(pasted, tc.prefix+"|  | a\\|b | 2 |") ||
				!strings.Contains(pasted, tc.prefix+"|  | 3 | 4 |") {
				t.Fatalf("paste changed the table shape or prefix: %q", pasted)
			}
			checkIndex(t, e.Doc)
			check := func(source string, sel Selection) {
				t.Helper()
				if e.Doc.Source() != source || e.Doc.Sel != sel {
					t.Fatalf("source/selection = %q %+v; want %q %+v", e.Doc.Source(), e.Doc.Sel, source, sel)
				}
				checkIndex(t, e.Doc)
			}
			e.Run("undo")
			check(edited, editedSel)
			e.Run("undo")
			check(inserted, insertedSel)
			e.Run("undo")
			check(before, Selection{Anchor: at, Head: at})
			e.Run("redo")
			check(inserted, insertedSel)
			e.Run("redo")
			check(edited, editedSel)
			e.Run("redo")
			check(pasted, pastedSel)
		})
	}
}

func TestNestedTableDeleteOnlyTable(t *testing.T) {
	const source = "> - item\n>\n>   | A | B |\n>   | --- | --- |\n>   | C | D |\n>\n> - next\n"
	e := NewEditor(NewDocument(source))
	if !e.TableCommand(strings.Index(source, "C"), "deleteTable") {
		t.Fatal("deleting the nested table failed")
	}
	deleted := e.Doc.Source()
	if strings.Contains(deleted, "|") || !strings.Contains(deleted, "> - item") || !strings.Contains(deleted, "> - next") {
		t.Fatalf("deletion affected neighboring blocks: %q", deleted)
	}
	e.Run("undo")
	if e.Doc.Source() != source {
		t.Fatalf("undo = %q", e.Doc.Source())
	}
	e.Run("redo")
	if e.Doc.Source() != deleted {
		t.Fatalf("redo = %q", e.Doc.Source())
	}
}

func TestFormulaDraftPreservesDelimiters(t *testing.T) {
	for _, tc := range []struct{ source, from, to string }{
		{"A \\(x^2\\) formula.\n", "x^2", "y^2"},
		{"A $x^2$ formula.\n", "x^2", "y^2"},
		{"\\[\nx^2\n=\n1\n\\]\n", "x^2\n=\n1", "y^2\n=\n2"},
		{"> $$\n> x^2\n> $$\n", "x^2", "y^2"},
		{"```mermaid\nflowchart LR\nA --> B\n```\n", "flowchart LR\nA --> B", "flowchart LR\nA --> C"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			e := NewEditor(NewDocument(tc.source))
			im := NewImages(func(func()) {}, DefaultImageBudget)
			im.SetRenderer(func([]RenderRequest) []RenderResult { return nil })
			e.SetImages(im, "")
			at := strings.Index(tc.source, strings.Split(tc.from, "\n")[0])
			if !e.BeginEmbedEdit(at) || e.embed.text != tc.from {
				t.Fatalf("cannot open draft: %+v", e.embed)
			}
			e.embed.text = tc.to
			want := strings.Replace(tc.source, tc.from, tc.to, 1)
			if !e.applyEmbedEdit() || e.Doc.Source() != want {
				t.Fatalf("apply: got %q, want %q, draft %+v", e.Doc.Source(), want, e.embed)
			}
			e.Run("undo")
			if e.Doc.Source() != tc.source {
				t.Fatalf("undo = %q", e.Doc.Source())
			}
		})
	}
}

func TestInsertTableInEmptyContainer(t *testing.T) {
	for _, tc := range []struct{ before, prefix string }{
		{"- ", "  "},
		{"12. ", "    "},
		{"> ", "> "},
		{"> - ", ">   "},
		{"<details open>\n<summary>Empty</summary>\n\n</details>\n", ""},
	} {
		t.Run(tc.before, func(t *testing.T) {
			e := NewEditor(NewDocument(tc.before))
			at := len(tc.before)
			if tc.prefix == "" {
				at = strings.Index(tc.before, "\n\n")
			}
			e.Select(at, at)
			e.InsertTable(1, 2)
			tbl := e.tableBlockAt(e.Doc.Sel.Head)
			if tbl == 0 {
				t.Fatalf("caret did not land in a table: %q", e.Doc.Source())
			}
			if tc.prefix != "" && e.Doc.md.Block(tbl).Parent == 0 {
				t.Fatalf("table is outside its container: %q", e.Doc.Source())
			}
			if tc.prefix == "" && len(e.Doc.details().frames) != 1 {
				t.Fatalf("details was not preserved: %q", e.Doc.Source())
			}
			e.Run("undo")
			if e.Doc.Source() != tc.before {
				t.Fatalf("undo = %q, want %q", e.Doc.Source(), tc.before)
			}
		})
	}
}

func TestFormulaDraftWidgetSaveAndCancel(t *testing.T) {
	const src = "Inline \\(x^2\\) formula.\n"
	e, tt := newTester(t, src, 800, 600)
	im := NewImages(func(func()) {}, DefaultImageBudget)
	defer im.Close()
	im.SetRenderer(func([]RenderRequest) []RenderResult { return nil })
	e.SetImages(im, "")
	e.SetLabels(Labels{EmbedFormula: "Edit formula", EmbedSave: "Save formula", EmbedCancel: "Cancel formula"})
	tt.Frame()
	at := strings.Index(src, "x^2")
	if !e.BeginEmbedEdit(at) {
		t.Fatal("opening formula draft failed")
	}
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("y^2")
	tt.Frame()
	if e.embed == nil || e.embed.text != "y^2" || e.Doc.Source() != src {
		t.Fatalf("typing changed the document instead of its draft: %q", e.Doc.Source())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if e.embed != nil || e.Doc.Source() != src {
		t.Fatalf("Escape did not discard the draft: %q", e.Doc.Source())
	}
	if !e.BeginEmbedEdit(at) {
		t.Fatal("reopening formula draft failed")
	}
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("z^2")
	tt.Frame()
	if err := tt.Click("Save formula"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if want := strings.Replace(src, "x^2", "z^2", 1); e.embed != nil || e.Doc.Source() != want {
		t.Fatalf("save = %q, want %q", e.Doc.Source(), want)
	}
}

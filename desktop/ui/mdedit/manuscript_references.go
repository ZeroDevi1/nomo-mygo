package mdedit

import (
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui/markdown/parser"
	"nomo/desktop/typeset"
)

// ManuscriptReferenceChoices merges the current buffer's titles with formal
// numbering from the matching project revision. It is built when requested.
func (e *Editor) ManuscriptReferenceChoices() []typeset.ManuscriptReference {
	d, m := e.Doc, e.Doc.manuscript
	items := make(map[string]typeset.ManuscriptReference)
	if m.project.Index != nil {
		maps.Copy(items, m.project.Index.ByLabel)
	}
	heading := ""
	for _, b := range d.blocks {
		if b.End-b.Start > 16<<10 || b.Kind == manuscriptSetup || b.Kind == manuscriptBibliography {
			continue
		}
		src := strings.TrimSpace(d.slice(b.Start, b.End))
		if b.Kind == parser.Heading {
			if a, z, level := manuscriptHeading(src, m.latex); level > 0 {
				heading = src[a:z]
				if !m.latex {
					if label := typeset.ManuscriptLabel(src, false); label != "" {
						heading = strings.TrimSpace(strings.TrimSuffix(heading, "<"+label+">"))
					}
				}
			}
		}
		label := typeset.ManuscriptLabel(src, m.latex)
		if label == "" || len(items) >= 2048 && items[label].Label == "" {
			continue
		}
		item := items[label]
		item.Label = label
		if item.Kind == "" {
			item.Kind = typeset.ReferenceKind(label)
		}
		if b.Kind == parser.MathBlock {
			item.Kind = "equation"
		}
		if b.Kind == manuscriptStyled {
			if object, ok := typeset.ReadNativeAuthorObject(src, "", m.latex); ok {
				item.Kind = object.Kind
				if title := object.Fields["caption"]; title != "" {
					item.Title = title
				}
			}
		}
		if item.Kind == "section" && heading != "" && item.Title == "" {
			item.Title = heading
		}
		if item.Title == "" && b.Kind == parser.MathBlock {
			item.Title = strings.TrimSpace(src)
		}
		if len(item.Title) > 256 {
			cut := 253
			for !utf8.RuneStart(item.Title[cut]) {
				cut--
			}
			item.Title = item.Title[:cut] + "…"
		}
		item.Title = strings.Clone(strings.Join(strings.Fields(item.Title), " "))
		items[label] = item
	}
	out := slices.Collect(maps.Values(items))
	slices.SortFunc(out, func(a, b typeset.ManuscriptReference) int { return strings.Compare(a.Label, b.Label) })
	return out
}

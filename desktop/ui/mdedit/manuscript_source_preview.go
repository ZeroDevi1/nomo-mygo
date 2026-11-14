package mdedit

import (
	"context"
	"strings"

	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown/parser"

	"nomo/desktop/typeset"
)

// ManuscriptSourcePreview 从共享快照取出当前段落或完整的排版块。它不创建写作画布或文档索引。
func ManuscriptSourcePreview(ctx context.Context, snap editor.Snapshot, project ManuscriptProject, line int) (block typeset.BlockPreview, first, last int, incomplete bool) {
	first, last = line, line
	at := snap.LineStart(line)
	if snap.LineEnd(line)-at > maxLeafBytes {
		return
	}
	latex := typeset.IsLaTeXPath(project.Path)
	m := manuscript{snap: snap, latex: latex}
	block.Path, block.Root, block.Main = project.Path, project.Root, project.Main
	if latex {
		block.Preamble = ManuscriptLaTeXPreamble(snap.Slice(0, min(snap.Len(), manuscriptSetupLimit)))
		if block.Preamble == "" {
			block.Preamble = project.MainPreamble
		}
		m.blockCommands = latexBlockCommands(block.Preamble)
	}
	var declarations strings.Builder
	bibliography := func(b Block) {
		if !latex && b.Kind == manuscriptBibliography && block.Bibliography == "" && b.End-b.Start <= 4096 {
			block.Bibliography = snap.Slice(b.Start, b.End)
		}
	}
	paragraph, start, end := -1, -1, 0
	var kind parser.Kind
	for b := range m.blocks(0, 0) {
		if ctx.Err() != nil {
			return typeset.BlockPreview{}, line, line, false
		}
		size := b.End - b.Start
		prose := b.Kind == parser.Paragraph && size <= maxLeafBytes && strings.TrimSpace(snap.Slice(b.Start, b.End)) != ""
		if b.End < at {
			switch {
			case !latex && b.Kind == manuscriptSetup:
				if declarations.Len()+size+1 > manuscriptSetupLimit {
					return typeset.BlockPreview{}, line, line, false
				}
				declarations.WriteString(snap.Slice(b.Start, b.End))
				declarations.WriteByte('\n')
			}
			bibliography(b)
			if !prose {
				paragraph = -1
			} else if paragraph < 0 || b.End-paragraph > maxLeafBytes {
				paragraph = b.Start
			}
			continue
		}
		if start >= 0 {
			if !prose || b.End-start > maxLeafBytes {
				bibliography(b)
				break
			}
			end = b.End
			continue
		}
		if size > maxLeafBytes || b.Kind == manuscriptSetup || b.Kind == parser.Paragraph && !prose {
			return typeset.BlockPreview{}, line, line, false
		}
		if b.Kind == manuscriptRaw {
			group, grouped := m.group(strings.TrimSpace(snap.Slice(b.Start, b.End)))
			incomplete = b.Index == manuscriptPending || b.Index == 0 && grouped && group.kind != manuscriptSetup
			return typeset.BlockPreview{}, line, line, incomplete
		}
		start, end, kind = b.Start, b.End, b.Kind
		if !prose {
			if b.Kind == manuscriptBibliography {
				block.Bibliography = ""
			}
			break
		}
		if paragraph >= 0 && end-paragraph <= maxLeafBytes {
			start = paragraph
		}
	}
	if start >= 0 {
		block.Source = strings.TrimSpace(snap.Slice(start, end))
		if !latex {
			block.Preamble = declarations.String()
			// Typst can resolve citations against a bibliography later in the file.
			if block.Bibliography == "" && kind != manuscriptBibliography && strings.ContainsAny(block.Source, "@#") && end < snap.Len() {
				for b := range m.blocks(end+1, 0) {
					if ctx.Err() != nil {
						return typeset.BlockPreview{}, line, line, false
					}
					bibliography(b)
					if block.Bibliography != "" {
						break
					}
				}
			}
		}
		first, last = snap.LineOf(start), snap.LineOf(end)
	}
	return
}

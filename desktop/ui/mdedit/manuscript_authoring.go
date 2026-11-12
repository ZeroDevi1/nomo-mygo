package mdedit

import (
	"sort"
	"strings"

	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown/parser"
	"nomo/desktop/typeset"
)

// ApplyManuscriptEdits writes markup and any new preamble entries as one undo step.
func (e *Editor) ApplyManuscriptEdits(edits []editor.Edit, selection Selection) {
	e.UnmarkText()
	if len(edits) == 1 {
		change := edits[0]
		e.Replace(change.Start, change.End, change.Text)
	} else if len(edits) > 1 {
		e.Doc.manuscript.buffer.Edit(edits...)
		e.SyncManuscript()
	}
	e.Select(selection.Anchor, selection.Head)
}

func (e *Editor) ManuscriptStyleVersion() uint64 {
	return e.Doc.manuscript.styleVersion
}

// ManuscriptBodyRange keeps visual authoring out of declarations and document delimiters.
func (e *Editor) ManuscriptBodyRange(start, end int) bool {
	d := e.Doc
	last := max(start, end-1)
	first := sort.Search(len(d.blocks), func(i int) bool { return d.blocks[i].End >= start })
	for _, b := range d.blocks[first:] {
		if b.Start > last {
			break
		}
		if b.Kind == manuscriptSetup || b.Kind == manuscriptBibliography || b.Index == manuscriptPreamble {
			return false
		}
		if d.manuscript.latex && b.Kind == manuscriptRaw {
			text := strings.TrimSpace(d.slice(b.Start, b.End))
			if strings.HasPrefix(text, `\begin{document}`) || strings.HasPrefix(text, `\end{document}`) {
				return false
			}
		}
	}
	return true
}

func (e *Editor) ManuscriptObject(action string) (start, end int, fields typeset.AuthorValues, ok bool) {
	d := e.Doc
	a, z := d.Sel.Range()
	i, _ := d.BlockAt(a)
	if i < 0 || z > d.blocks[i].End {
		return
	}
	b := d.blocks[i]
	if b.Kind != manuscriptStyled || b.End-b.Start > 16<<10 {
		return
	}
	fields, ok = typeset.ReadAuthorObject(d.manuscript.latex, action, d.slice(b.Start, b.End))
	return b.Start, b.End, fields, ok
}

func (e *Editor) NativeManuscriptObject() (start, end int, object typeset.AuthorObject, ok bool) {
	d := e.Doc
	a, z := d.Sel.Range()
	i, _ := d.BlockAt(a)
	if i < 0 || z > d.blocks[i].End {
		return
	}
	b := d.blocks[i]
	if b.Kind != manuscriptStyled || b.End-b.Start > 16<<10 {
		return
	}
	setup := ""
	if !d.manuscript.latex {
		decl := d.declarations()
		at := sort.Search(len(decl.ends), func(i int) bool { return decl.ends[i].source > b.Start })
		if at > 0 {
			setup = decl.text[:decl.ends[at-1].text]
		}
	}
	object, ok = typeset.ReadNativeAuthorObject(d.slice(b.Start, b.End), setup, d.manuscript.latex)
	return b.Start, b.End, object, ok
}

// ManuscriptObjectKind is a cheap hint for the contextual toolbar. The form
// performs full, bounded parsing when opened.
func (e *Editor) ManuscriptObjectKind() string {
	d := e.Doc
	a, _ := d.Sel.Range()
	i, _ := d.BlockAt(a)
	if i < 0 || d.blocks[i].Kind != manuscriptStyled {
		return ""
	}
	b := d.blocks[i]
	return manuscriptObjectKind(strings.TrimSpace(d.slice(b.Start, min(b.End, b.Start+256))))
}

func manuscriptObjectKind(s string) string {
	switch {
	case strings.HasPrefix(s, "#table("), strings.HasPrefix(s, `\begin{table`), strings.HasPrefix(s, `\begin{tabular`), strings.Contains(s, "nomo-style:object-table "):
		return "table"
	case strings.HasPrefix(s, "#figure("):
		if strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(s, "#figure(")), "table(") {
			return "table"
		}
		return "figure"
	case strings.HasPrefix(s, "#image("), strings.HasPrefix(s, `\begin{figure`), strings.Contains(s, "nomo-style:object-figure "):
		return "figure"
	}
	return ""
}

// ManuscriptAssetInsertion inserts after the active block, or at the start of
// the body if the cursor is in the template. It never replaces selected text.
func (e *Editor) ManuscriptAssetInsertion() (int, bool) {
	d := e.Doc
	first := d.manuscript.syntaxHeadEnd
	if !d.manuscript.latex {
		first = e.ManuscriptSetupEnd()
		if d.Sel.Head < first {
			return first, true
		}
	}
	i, _ := d.BlockAt(d.Sel.Head)
	if i >= 0 {
		b := d.blocks[i]
		if b.Start >= first && e.ManuscriptBodyRange(b.Start, b.End) {
			return b.End, true
		}
	}
	at := sort.Search(len(d.blocks), func(i int) bool { return d.blocks[i].Start >= first })
	for _, b := range d.blocks[at:] {
		if b.Start != b.End && e.ManuscriptBodyRange(b.Start, b.End) {
			src := strings.TrimSpace(d.slice(b.Start, min(b.End, b.Start+64)))
			if src != "" && !strings.HasPrefix(src, "%") && !strings.HasPrefix(src, "//") {
				return b.Start, true
			}
		}
	}
	return d.len(), !d.manuscript.latex || e.ManuscriptBodyRange(d.len(), d.len())
}

func (e *Editor) ManuscriptListing() string {
	d := e.Doc
	pos, _ := d.Sel.Range()
	i, _ := d.BlockAt(pos)
	if i < 0 {
		return ""
	}
	b := d.blocks[i]
	if b.Kind != manuscriptStyled || b.End-b.Start > 4096 {
		return ""
	}
	return typeset.ManuscriptListingPath(d.slice(b.Start, b.End), d.manuscript.latex)
}

// ManuscriptSetupEnd returns the end of the leading Typst setup, before body markup.
func (e *Editor) ManuscriptSetupEnd() int {
	d := e.Doc
	end := 0
	for _, b := range d.blocks {
		text := strings.TrimSpace(d.slice(b.Start, b.End))
		if b.Kind != manuscriptSetup && text != "" && !strings.HasPrefix(text, "//") {
			break
		}
		end = min(d.len(), b.End+1)
		if end >= manuscriptSetupLimit {
			break
		}
	}
	return end
}

func (e *Editor) SetManuscriptParagraphMetrics(spacing, indent, pointScale float32, alignment string) {
	m := e.Doc.manuscript
	if m.paragraphGap == spacing && m.paragraphIndent == indent && m.pointScale == pointScale && m.paragraphAlignment == alignment {
		return
	}
	m.paragraphGap, m.paragraphIndent = spacing, indent
	m.pointScale, m.paragraphAlignment = pointScale, alignment
	for i := range e.entries {
		e.entries[i].split = false
	}
	e.layoutGen++
	e.dirty = true
}

// ManuscriptParagraph expands a selection to whole source paragraphs or an existing styled block.
func (e *Editor) ManuscriptParagraph() (start, end int, ok bool) {
	d := e.Doc
	a, z := d.Sel.Range()
	i, _ := d.BlockAt(a)
	j, _ := d.BlockAt(max(a, z-1))
	if i < 0 || j < 0 {
		return a, z, false
	}
	if i == j && d.blocks[i].Kind == manuscriptStyled {
		b := d.blocks[i]
		if _, _, known := typeset.ReadAuthorStyle(d.manuscript.latex, typeset.AuthorParagraph, d.slice(b.Start, b.End)); known {
			return b.Start, b.End, true
		}
	}
	isText := func(b Block) bool { return b.Kind == parser.Paragraph }
	for k := i; k <= j; k++ {
		if !isText(d.blocks[k]) {
			return a, z, false
		}
	}
	if strings.TrimSpace(d.slice(d.blocks[i].Start, d.blocks[j].End)) != "" {
		nonempty := func(b Block) bool { return isText(b) && strings.TrimSpace(d.slice(b.Start, b.End)) != "" }
		for i > 0 && nonempty(d.blocks[i-1]) {
			i--
		}
		for j+1 < len(d.blocks) && nonempty(d.blocks[j+1]) {
			j++
		}
	}
	return d.blocks[i].Start, d.blocks[j].End, true
}

// ManuscriptFormula identifies the enclosing editable inline or display formula, including a trailing Typst label.
func (e *Editor) ManuscriptFormula() (start, end, bodyStart, bodyEnd int, display, ok bool) {
	d := e.Doc
	a, z := d.Sel.Range()
	i, _ := d.BlockAt(a)
	if i < 0 {
		return
	}
	b := d.blocks[i]
	if z > b.End || b.Kind != parser.MathBlock && b.Kind != parser.Paragraph && b.Kind != parser.Heading && b.Kind != manuscriptList {
		return
	}
	src := d.slice(b.Start, b.End)
	latex := d.manuscript.latex
	makeRange := func(i, j, x, y int, display bool) (int, int, int, int, bool, bool) {
		return b.Start + i, b.Start + j, b.Start + x, b.Start + y, display, true
	}
	if b.Kind == parser.MathBlock {
		trim := strings.TrimSpace(src)
		offset := strings.Index(src, trim)
		if !latex {
			if j := typstDisplayClose(trim); j > 0 {
				return makeRange(offset, offset+len(trim), offset+1, offset+j, true)
			}
			const numbered = `#math.equation(block: true, numbering: "(1)", $`
			if strings.HasPrefix(trim, numbered) {
				if j := strings.LastIndex(trim, "$)"); j >= len(numbered) {
					return makeRange(offset, offset+len(trim), offset+len(numbered), offset+j, true)
				}
			}
		}
		for _, pair := range [][2]string{{`\[`, `\]`}, {"$$", "$$"}, {`\begin{equation}`, `\end{equation}`}, {`\begin{equation*}`, `\end{equation*}`}} {
			if len(trim) >= len(pair[0])+len(pair[1]) && strings.HasPrefix(trim, pair[0]) && strings.HasSuffix(trim, pair[1]) {
				return makeRange(offset, offset+len(trim), offset+len(pair[0]), offset+len(trim)-len(pair[1]), true)
			}
		}
		return
	}
	for i := 0; i < len(src); i++ {
		open, close := "", ""
		switch {
		case src[i] == '$':
			open, close = "$", "$"
		case latex && strings.HasPrefix(src[i:], `\(`):
			open, close = `\(`, `\)`
		case src[i] == '\\':
			i++
		case latex && src[i] == '%', !latex && strings.HasPrefix(src[i:], "//"):
			return
		}
		if open != "" {
			x := i + len(open)
			if y := manuscriptMathEnd(src, x, close); y >= x {
				j := y + len(close)
				if b.Start+i <= a && z <= b.Start+j {
					body := src[x:y]
					display := !latex && strings.TrimLeft(body, " \t") != body && strings.TrimRight(body, " \t") != body
					return makeRange(i, j, x, y, display)
				}
				i = j - 1
			}
		}
	}
	return
}

func (e *Editor) ManuscriptHeadingLine() (start, end int, title string, level int, ok bool) {
	d := e.Doc
	a, z := d.Sel.Range()
	i, _ := d.BlockAt(a)
	if i < 0 {
		return
	}
	b := d.blocks[i]
	if z > b.End {
		return
	}
	text := strings.TrimSpace(d.slice(b.Start, b.End))
	if b.Kind == parser.Heading {
		first, last, n := manuscriptHeading(text, d.manuscript.latex)
		if n > 0 {
			if d.manuscript.latex && strings.HasPrefix(text, `\section`) {
				n = 1
			} else if d.manuscript.latex && n >= 3 {
				n--
			}
			return b.Start, b.End, text[first:last], n, true
		}
	}
	if b.Kind == parser.Paragraph && a == z {
		return b.Start, b.End, text, 1, true
	}
	return
}

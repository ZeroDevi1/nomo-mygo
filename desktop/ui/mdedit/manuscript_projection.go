package mdedit

import (
	"hash/maphash"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown/parser"
	"nomo/desktop/typeset"
)

func manuscriptHeading(s string, latex bool) (start, end, level int) {
	if latex {
		a := strings.IndexByte(s, '{')
		if a < 0 || !strings.HasSuffix(s, "}") {
			return
		}
		switch strings.TrimSuffix(s[:a], "*") {
		case `\chapter`, `\part`:
			level = 1
		case `\section`:
			level = 2
		case `\subsection`:
			level = 3
		case `\subsubsection`:
			level = 4
		case `\paragraph`:
			level = 5
		case `\subparagraph`:
			level = 6
		}
		if strings.ContainsAny(s[a+1:len(s)-1], "{}%") {
			level = 0
		}
		return a + 1, len(s) - 1, level
	}
	a := len(s) - len(strings.TrimLeft(s, "="))
	if a == 0 || a == len(s) || !strings.ContainsRune(" \t", rune(s[a])) {
		return
	}
	title := strings.TrimLeft(s[a:], " \t")
	return len(s) - len(title), len(s), min(a, 6)
}

func manuscriptRows(d *Document, b Block, seed maphash.Seed) []row {
	text := d.slice(b.Start, b.End)
	r := row{kind: rowLine, role: roleBody, b: len(text), tone: -1}
	switch b.Kind {
	case parser.Heading:
		_, _, r.level = manuscriptHeading(strings.TrimSpace(text), d.manuscript.latex)
		r.role, r.before, r.after = roleHeading, headingBefore(r.level), headingAfter(r.level)
	case parser.MathBlock:
		r.role, r.before, r.after = roleMath, 5, 5
	case manuscriptStyled, manuscriptBibliography:
		r.role, r.before, r.after = roleCode, 6, 6
	case manuscriptRaw, manuscriptSetup:
		r.role = roleCode
	case manuscriptList:
		r.indent = 18
	case parser.Paragraph:
		m := d.manuscript
		switch m.paragraphAlignment {
		case "center":
			r.align = ui.Center
		case "right":
			r.align = ui.End
		}
		if strings.TrimSpace(text) == "" {
			r.after = m.paragraphGap
		} else if m.paragraphIndent != 0 && !(m.latex && strings.HasPrefix(strings.TrimSpace(text), `\noindent`)) {
			before, _ := d.BlockAt(max(0, b.Start-2))
			if b.Start == 0 || before >= 0 && before < len(d.blocks) && strings.TrimSpace(d.slice(d.blocks[before].Start, d.blocks[before].End)) == "" {
				r.indent = m.paragraphIndent
			}
		}
	}
	if len(text) <= maxLeafBytes {
		r.hash = maphash.String(seed, text) ^ uint64(b.Kind)
		return []row{r}
	}
	rows := make([]row, 0, len(text)/maxRowBytes+1)
	for from := 0; from < len(text); {
		to := len(text)
		if to-from > maxRowBytes {
			to = chunkCut(text, from, from+maxRowBytes)
		}
		rows = append(rows, row{kind: rowLine, role: r.role, a: from, b: to, tone: -1,
			band: len(rows), cont: from > 0, hash: maphash.String(seed, text[from:to]) ^ uint64(b.Kind)})
		from = to
	}
	return rows
}

func (e *Editor) projectManuscript(b Block, r *row, ex expansion) *projection {
	src := e.Doc.slice(b.Start+r.a, b.Start+r.b)
	if ex.inBlock || b.End-b.Start > maxLeafBytes {
		return projectManuscriptSource(src)
	}
	latex := e.Doc.manuscript.latex
	trim := strings.TrimSpace(src)
	start := strings.Index(src, trim)
	end := start + len(trim)
	pb := projBuilder{src: src, p: &projection{}}
	switch b.Kind {
	case parser.MathBlock, manuscriptStyled, manuscriptBibliography:
		if path := typeset.ManuscriptListingPath(trim, latex); path != "" {
			pb.replace(0, len(src), e.Doc.manuscript.project.ListingTitle+" · "+path, stMarker)
			return pb.finish()
		}
		if e.canRender() {
			if style := e.manuscriptBlockStyle(b, trim, false); style != nil {
				kind := RenderTypstBlock
				if latex {
					kind = RenderLaTeXBlock
				}
				if b.Kind == parser.MathBlock {
					kind = RenderTypstMathDisplay
					if latex {
						kind = RenderMathDisplay
					}
				}
				obj := imgObj{render: kind, src: strings.Clone(trim), manuscript: style}
				if b.Kind == manuscriptStyled {
					switch manuscriptObjectKind(trim[:min(len(trim), 256)]) {
					case "figure":
						obj.cardTitle = e.Doc.manuscript.project.FigureTitle
					case "table":
						obj.cardTitle = e.Doc.manuscript.project.TableTitle
					}
				}
				if en := e.imageEntry(obj); en != nil && (en.state != imageFailed || obj.cardTitle != "") {
					pb.object(start, end, obj, 0)
					return pb.finish()
				}
				if b.Kind != parser.MathBlock {
					pb.replace(0, len(src), e.Doc.manuscript.project.PreviewUnavailable+" · "+firstLine(trim), stMarker)
					return pb.finish()
				}
				return projectManuscriptSource(src)
			}
		}
		if b.Kind == parser.MathBlock {
			if body := manuscriptDisplayMath(trim, latex); body != "" && e.manuscriptMathObject(&pb, b, start, end, body, true, latex) {
				return pb.finish()
			}
		}
	case manuscriptSetup, manuscriptRaw:
		if name := manuscriptSetupSummary(trim, latex); name != "" {
			pb.replace(0, len(src), e.Doc.manuscript.project.SetupTitle+" · "+name, stMarker)
			return pb.finish()
		}
	case parser.Heading:
		a, z, _ := manuscriptHeading(trim, latex)
		if !latex {
			if label := typeset.ManuscriptLabel(trim, false); label != "" && z >= len(label)+2 {
				z = len(strings.TrimRight(trim[:z-len(label)-2], " \t"))
			}
		}
		pb.emit(0, start+a, stMarker, false)
		e.manuscriptInlines(&pb, b, start+a, start+z, latex, 0, 0)
		pb.emit(start+z, len(src), stMarker, false)
		return pb.finish()
	case manuscriptList:
		pb.emit(0, start, 0, true)
		pb.replace(start, start+2, "• ", 0)
		e.manuscriptInlines(&pb, b, start+2, len(src), latex, 0, 0)
		return pb.finish()
	case parser.Paragraph:
		e.manuscriptInlines(&pb, b, 0, len(src), latex, 0, 0)
		return pb.finish()
	}
	return projectManuscriptSource(src)
}

func projectManuscriptSource(src string) *projection {
	p := projectIdentity(src)
	p.manuscriptSource = true
	return p
}

func manuscriptSetupSummary(s string, latex bool) string {
	if latex {
		for _, name := range []string{`\documentclass`, `\setstretch`, `\setmainfont`} {
			if strings.HasPrefix(s, name) {
				return name
			}
		}
		if strings.HasPrefix(s, `\usepackage`) && strings.HasSuffix(s, "{geometry}") {
			return "geometry"
		}
		return ""
	}
	if strings.HasPrefix(s, "// nomo-style:page ") {
		return "page"
	}
	for _, name := range []string{"page", "text", "par"} {
		if strings.HasPrefix(s, "#set "+name+"(") {
			return name
		}
	}
	for _, keyword := range []string{"#set ", "#show ", "#let ", "#import "} {
		if tail, ok := strings.CutPrefix(s, keyword); ok {
			name, _, _ := strings.Cut(firstLine(tail), ":")
			name, _, _ = strings.Cut(name, "(")
			name, _, _ = strings.Cut(name, " ")
			return name
		}
	}
	return ""
}

func manuscriptDisplayMath(s string, latex bool) string {
	if !latex {
		if end := typstDisplayClose(s); end > 0 {
			return strings.TrimSpace(s[1:end])
		}
		return ""
	}
	for _, pair := range [][2]string{{`\[`, `\]`}, {"$$", "$$"}, {`\begin{equation}`, `\end{equation}`}, {`\begin{equation*}`, `\end{equation*}`}} {
		if len(s) >= len(pair[0])+len(pair[1]) && strings.HasPrefix(s, pair[0]) && strings.HasSuffix(s, pair[1]) {
			return strings.TrimSpace(s[len(pair[0]) : len(s)-len(pair[1])])
		}
	}
	return ""
}

func (e *Editor) manuscriptReference(text string, pos int, latex, start bool) (string, int) {
	index := e.Doc.manuscript.project.Index
	if index == nil {
		return "", 0
	}
	if latex && text[0] == '\\' || !latex && text[0] == '@' && start {
		label, end, parens := typeset.ManuscriptReferenceUse(text, latex)
		if number := index.ByLabel[label].Number; end > 0 && number != "" {
			if parens {
				number = "(" + number + ")"
			}
			return number, end
		}
	}
	if !latex && text[0] == '#' {
		name, label, end, noSupplement := typeset.TypstReferenceCall(text)
		if number := index.ByLabel[label].Number; end > 0 && number != "" {
			if name == "ref" && noSupplement {
				return number, end
			}
			if alias := e.Doc.declarations().refAliases[name]; !noSupplement && alias.before > 0 && alias.before <= pos {
				return alias.prefix + number + alias.suffix, end
			}
		}
	}
	return "", 0
}

func (e *Editor) manuscriptInlines(pb *projBuilder, b Block, start, end int, latex bool, inherited style, depth int) {
	src := pb.src[:end]
	for i := start; i < end; {
		rest := src[i:]
		if rest[0] == '\\' || rest[0] == '@' || rest[0] == '#' {
			if number, n := e.manuscriptReference(rest, b.Start, latex, i == start || !isWordByte(src[i-1])); n > 0 {
				pb.emit(i, i, inherited, true)
				pb.replace(i, i+n, number, inherited|stLink)
				i += n
				continue
			}
		}
		if depth < 12 && (rest[0] == '#' || rest[0] == '\\' || rest[0] == '{') {
			if span, ok := manuscriptStyledSpan(rest, latex); ok && span.end > span.body {
				pb.emit(i, i, inherited, true)
				pb.emit(i, i+span.body, stMarker, false)
				e.manuscriptInlines(pb, b, i+span.body, i+span.end, latex, manuscriptSpanFlags(pb.p, inherited, span), depth+1)
				pb.emit(i+span.end, i+span.next, stMarker, false)
				i += span.next
				continue
			}
		}
		switch {
		case latex && rest[0] == '%', !latex && strings.HasPrefix(rest, "//"):
			pb.emitManuscriptCode(i, end, inherited|stMarker)
			i = end
		case latex && rest[0] == '~':
			pb.replace(i, i+1, "\u00a0", inherited)
			i++
		case latex && strings.HasPrefix(rest, "--"):
			n, dash := 2, "–"
			if strings.HasPrefix(rest, "---") {
				n, dash = 3, "—"
			}
			pb.replace(i, i+n, dash, inherited)
			i += n
		case latex && rest[0] == '\\':
			if n, visible, ok := manuscriptTeXSpacing(rest); ok {
				pb.emit(i, i, inherited, true)
				if visible == "" {
					pb.emit(i, i+n, stMarker, false)
				} else {
					pb.replace(i, i+n, visible, inherited)
				}
				i += n
			} else if strings.HasPrefix(rest, `\(`) {
				if z := manuscriptMathEnd(src, i+2, `\)`); z > i+2 {
					if !e.manuscriptMathObject(pb, b, i, z+2, src[i+2:z], false, true) {
						pb.emitManuscriptCode(i, z+2, inherited)
					}
					i = z + 2
				} else {
					pb.emitManuscriptCode(i, i+2, inherited)
					i += 2
				}
			} else {
				n := 1
				if command := latexLeadingControlWord.FindStringIndex(rest); command != nil {
					n = command[1]
				} else if len(rest) > 1 {
					_, size := utf8.DecodeRuneInString(rest[1:])
					n += size
				}
				pb.emitManuscriptCode(i, i+n, inherited)
				i += n
			}
		case !latex && rest[0] == '`':
			open := len(rest) - len(strings.TrimLeft(rest, "`"))
			close := manuscriptInlineRawEnd(rest, open)
			if close <= open {
				pb.emit(i, end, inherited, true)
				i = end
				continue
			}
			pb.emit(i, i, inherited, true)
			pb.emit(i, i+open, stMarker, false)
			pb.emit(i+open, i+close, inherited|stCode, true)
			pb.emit(i+close, i+close+open, stMarker, false)
			i += close + open
		case strings.HasPrefix(rest, "$$"):
			i += 2
		case strings.HasPrefix(rest, "$"):
			if z := manuscriptMathEnd(src, i+1, "$"); z > i+1 {
				body := src[i+1 : z]
				display := !latex && strings.TrimLeft(body, " \t") != body && strings.TrimRight(body, " \t") != body
				if !e.manuscriptMathObject(pb, b, i, z+1, body, display, latex) {
					pb.emitManuscriptCode(i, z+1, inherited)
				}
				i = z + 1
			} else {
				i++
			}
		case rest[0] == '\\':
			i += min(2, len(rest))
		case !latex && rest[0] == '#':
			// A function call can change the meaning of the remaining source.
			pb.emitManuscriptCode(i, end, inherited)
			i = end
		case !latex && (rest[0] == '*' || rest[0] == '_'):
			marker := rest[:1]
			z := strings.Index(rest[1:], marker) + 1
			if z > 1 && strings.TrimSpace(rest[1:z]) == rest[1:z] &&
				!strings.ContainsAny(rest[1:z], "*_#$\\") &&
				(i == start || !isWordByte(src[i-1])) && (i+z+1 == end || !isWordByte(rest[z+1])) {
				pb.emit(i, i, inherited, true)
				st := stItalic
				if marker == "*" {
					st = stBold
				}
				pb.emit(i, i+1, stMarker, false)
				pb.emit(i+1, i+z, st|inherited, true)
				pb.emit(i+z, i+z+1, stMarker, false)
				i += z + 1
			} else {
				i++
			}
		default:
			i++
		}
	}
	pb.emit(pb.pos, end, inherited, true)
}

func (pb *projBuilder) emitManuscriptCode(a, z int, st style) {
	pb.emit(a, a, st, true)
	pb.emit(a, z, st|stRaw, true)
}

func manuscriptTeXSpacing(src string) (int, string, bool) {
	for _, item := range [][2]string{{`\noindent`, ""}, {`\qquad`, "\u2003\u2003"}, {`\quad`, "\u2003"},
		{`\smallskip`, ""}, {`\medskip`, ""}, {`\bigskip`, ""}} {
		cmd := item[0]
		if !strings.HasPrefix(src, cmd) {
			continue
		}
		if tail := src[len(cmd):]; tail != "" && (tail[0] >= 'a' && tail[0] <= 'z' || tail[0] >= 'A' && tail[0] <= 'Z' || tail[0] == '@') {
			continue
		}
		return len(src) - len(strings.TrimLeft(src[len(cmd):], " \t")), item[1], true
	}
	return 0, "", false
}

func manuscriptInlineRawEnd(text string, open int) int {
	for at := open; at < len(text); {
		next := strings.IndexByte(text[at:], '`')
		if next < 0 {
			break
		}
		at += next
		run := len(text[at:]) - len(strings.TrimLeft(text[at:], "`"))
		if run == open {
			return at
		}
		at += run
	}
	return -1
}

func manuscriptLaTeXStyle(s string) (int, style) {
	for _, item := range []struct {
		prefix string
		style  style
	}{{`\textbf{`, stBold}, {`\emph{`, stItalic}, {`\textit{`, stItalic},
		{`\underline{`, stUnderline}, {`\sout{`, stStrike}, {`\texttt{`, stCode}, {`\textsuperscript{`, stSmall | stRaise}, {`\textsubscript{`, stSmall | stLower}} {
		if strings.HasPrefix(s, item.prefix) {
			return len(item.prefix), item.style
		}
	}
	return 0, 0
}

func manuscriptMathEnd(src string, start int, close string) int {
	for i := start; i < len(src); i++ {
		if strings.HasPrefix(src[i:], close) {
			return i
		}
		if src[i] == '\\' {
			i++
		}
	}
	return -1
}

func isWordByte(b byte) bool {
	return b == '_' || b < 128 && (unicode.IsLetter(rune(b)) || unicode.IsDigit(rune(b)))
}

func (e *Editor) manuscriptMathObject(pb *projBuilder, b Block, start, end int, body string, display, latex bool) bool {
	if !e.canRender() || len(body) > maxManuscriptMath || strings.TrimSpace(body) == "" {
		return false
	}
	kind := RenderMathInline
	if display {
		kind = RenderMathDisplay
	}
	source := pb.src[start:end]
	context := e.manuscriptBlockStyle(b, source, true)
	if !latex {
		// 局部 Typst 公式在独立上下文排版，含代码或分隔符的表达式保留原文。
		if context == nil && (strings.ContainsAny(body, "#$`\\") || strings.Contains(body, "//") || strings.Contains(body, "/*")) {
			return false
		}
		if display {
			kind = RenderTypstMathDisplay
		} else {
			kind = RenderTypstMathInline
		}
	}
	input := body
	if context != nil {
		input = source
	}
	obj := imgObj{render: kind, src: cloneString(input), alt: cloneString(source), manuscript: context}
	if context != nil {
		if en := e.imageEntry(obj); en != nil && en.state == imageFailed {
			return false
		}
	}
	pb.object(start, end, obj, 0)
	return true
}

const maxManuscriptMath = 1024

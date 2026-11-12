package mdedit

import (
	"iter"
	"regexp"
	"strings"

	"github.com/egoist/mygo/ui/markdown/parser"
	"nomo/desktop/typeset"
)

const manuscriptBlockLines = 96

type manuscriptGroup struct {
	kind        parser.Kind
	open, close string
	markerEnd   string
	depth       int
	typstCode   bool
	latexMacro  bool
}

var latexBlockBody = regexp.MustCompile(`\\(?:par|begin|noindent|smallskip|medskip|bigskip|vspace)\b`)
var latexEnvironmentDefinition = regexp.MustCompile(`\\(?:newenvironment|renewenvironment|newtheorem)\*?\s*\{([A-Za-z][A-Za-z0-9@*-]*)\}`)

// Only standalone commands declared as block content join adjacent source lines.
func latexBlockCommands(head string) map[string]bool {
	var commands map[string]bool
	add := func(name string) {
		if commands == nil {
			commands = make(map[string]bool)
		}
		commands[name] = true
	}
	for line := range strings.Lines(head) {
		line = latexBeforeComment(line)
		for _, env := range latexEnvironmentDefinition.FindAllStringSubmatch(line, -1) {
			add(`\begin{` + env[1])
		}
		for _, match := range latexMacroDefinition.FindAllStringSubmatchIndex(line, -1) {
			if !latexBlockBody.MatchString(line[match[1]:]) {
				continue
			}
			i := 2
			if match[i] < 0 {
				i = 4
			}
			add(strings.Clone(line[match[i]:match[i+1]]))
		}
	}
	return commands
}

func (m *manuscript) group(line string) (manuscriptGroup, bool) {
	if strings.HasPrefix(line, typeset.AuthorStylePrefix(m.latex, typeset.AuthorPage)) {
		return manuscriptGroup{kind: manuscriptSetup, markerEnd: typeset.AuthorStyleEnd(m.latex, typeset.AuthorPage)}, true
	}
	for _, kind := range []string{"object-table", "object-figure"} {
		if strings.HasPrefix(line, typeset.AuthorStylePrefix(m.latex, kind)) {
			return manuscriptGroup{kind: manuscriptStyled, markerEnd: typeset.AuthorStyleEnd(m.latex, kind)}, true
		}
	}
	if m.latex {
		if line == typeset.AuthorLaTeXParagraphOpen {
			return manuscriptGroup{kind: manuscriptStyled, open: `\begingroup`, close: `\endgroup`}, true
		}
		line = latexBeforeComment(line)
		if line == `\maketitle` {
			return manuscriptGroup{kind: manuscriptStyled, latexMacro: true, open: `\maketitle`}, true
		}
		if strings.HasPrefix(line, `\[`) {
			return manuscriptGroup{kind: parser.MathBlock, open: `\[`, close: `\]`}, true
		}
		if strings.HasPrefix(line, "$$") {
			if _, tail, ok := strings.Cut(line[2:], "$$"); ok && strings.TrimSpace(tail) != "" {
				return manuscriptGroup{}, false
			}
			return manuscriptGroup{kind: parser.MathBlock, open: "$$", close: "$$"}, true
		}
		name, tail, ok := strings.Cut(strings.TrimPrefix(line, `\begin{`), "}")
		if !ok || !strings.HasPrefix(line, `\begin{`) {
			if strings.HasPrefix(line, "\\") && (len(m.blockCommands) > 0 || strings.HasPrefix(line, `\lstinputlisting`)) {
				cmd := latexControlWord.FindStringIndex(line)
				if cmd != nil && cmd[0] == 0 && (m.blockCommands[line[:cmd[1]]] || line[:cmd[1]] == `\lstinputlisting`) {
					args := strings.TrimSpace(line[cmd[1]:])
					if args == "" || args[0] == '{' || args[0] == '[' {
						return manuscriptGroup{kind: manuscriptStyled, latexMacro: true, open: line[:cmd[1]]}, true
					}
				}
			}
			return manuscriptGroup{}, false
		}
		kind := manuscriptStyled
		switch strings.TrimSuffix(name, "*") {
		case "equation", "align", "alignat", "gather", "multline", "flalign", "displaymath":
			kind = parser.MathBlock
		case "figure", "table", "tabular", "tabularx", "longtable", "theorem", "lemma", "definition",
			"proposition", "corollary", "remark", "example", "proof", "enumerate", "itemize",
			"description", "quote", "quotation", "center", "tikzpicture", "tcolorbox", "algorithm", "algorithmic",
			"abstract", "abstract-zh", "abstract-en", "symbols", "acknowledgements", "lstlisting", "verbatim", "thebibliography":
		default:
			if !m.blockCommands[`\begin{`+name] && !m.blockCommands[`\begin{`+strings.TrimSuffix(name, "*")] {
				return manuscriptGroup{}, false
			}
		}
		open, close := `\begin{`+name+"}", `\end{`+name+"}"
		// A complete group occupies its own source lines. Preserve trailing content if it shares a closing line.
		if at := strings.LastIndex(tail, close); at >= 0 && strings.TrimSpace(tail[at+len(close):]) != "" {
			return manuscriptGroup{}, false
		}
		return manuscriptGroup{kind: kind, open: open, close: close}, true
	}

	if strings.HasPrefix(line, "```") {
		fence := line[:len(line)-len(strings.TrimLeft(line, "`"))]
		return manuscriptGroup{kind: manuscriptStyled, open: fence, close: fence}, true
	}
	if strings.HasPrefix(line, "#text(") {
		if _, ok := manuscriptStyledSpan(line, false); ok {
			return manuscriptGroup{}, false
		}
	}
	if strings.HasPrefix(line, "#math.equation(") {
		return manuscriptGroup{kind: parser.MathBlock, typstCode: true}, true
	}
	if line == "$" || len(line) > 1 && line[0] == '$' && strings.ContainsRune(" \t", rune(line[1])) {
		return manuscriptGroup{kind: parser.MathBlock, open: "$", close: "$"}, true
	}
	name, ok := strings.CutPrefix(line, "#")
	if !ok {
		return manuscriptGroup{}, false
	}
	if i := strings.IndexFunc(name, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r == '-') }); i >= 0 {
		name = name[:i]
	}
	kind := manuscriptStyled
	switch name {
	case "import", "let", "set", "show":
		kind = manuscriptSetup
	case "bibliography":
		kind = manuscriptBibliography
	case "figure", "table", "image", "grid", "stack", "block", "align", "thm", "theorem", "lemma",
		"definition", "proof", "heading", "text", "abstract-zh", "abstract-en", "symbols", "raw", "quote",
		"columns", "outline", "list", "enum", "terms", "rect", "box", "place", "pad", "footnote":
	default:
		tail := line[1+len(name):]
		if name == "" || tail == "" || tail[0] != '(' && tail[0] != '[' {
			return manuscriptGroup{}, false
		}
		if _, _, end, _ := typeset.TypstReferenceCall(line); end > 0 {
			return manuscriptGroup{}, false
		}
		switch name {
		case "ref", "cite", "h", "v", "counter", "state", "context":
			return manuscriptGroup{}, false
		}
	}
	return manuscriptGroup{kind: kind, typstCode: true}, true
}

func latexBeforeComment(s string) string {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '%':
			return strings.TrimSpace(s[:i])
		}
	}
	return s
}

// typstDisplayClose accepts a trailing label in the same source block as the equation.
func typstDisplayClose(s string) int {
	i := strings.LastIndexByte(s, '$')
	if i < 0 || i > 0 && s[i-1] != ' ' && s[i-1] != '\t' {
		return -1
	}
	tail := strings.TrimSpace(s[i+1:])
	if tail == "" || strings.HasPrefix(tail, "<") && strings.HasSuffix(tail, ">") &&
		!strings.ContainsAny(tail[1:len(tail)-1], "<> \t") {
		return i
	}
	return -1
}

func (g *manuscriptGroup) closed(line string, first bool) bool {
	if g.markerEnd != "" {
		return strings.TrimSpace(line) == g.markerEnd
	}
	if g.typstCode {
		g.depth = typstLineState(line, g.depth, true)
		return g.depth == 0
	}
	if g.open == "$" {
		at := typstDisplayClose(strings.TrimSpace(line))
		return at >= 0 && (!first || at > 0)
	}
	if g.open == "$$" || strings.HasPrefix(g.open, "```") {
		line = strings.TrimSpace(line)
		if g.open == "$$" {
			line = latexBeforeComment(line)
		}
		if first {
			line = strings.TrimPrefix(line, g.open)
		}
		return strings.HasSuffix(line, g.close)
	}
	line = latexBeforeComment(strings.TrimSpace(line))
	if g.latexMacro {
		if first {
			line = strings.TrimSpace(strings.TrimPrefix(line, g.open))
		}
		for i := 0; i < len(line); i++ {
			switch line[i] {
			case '\\':
				i++
			case '{':
				g.depth++
			case '}':
				g.depth--
			}
		}
		return g.depth <= 0
	}
	g.depth += strings.Count(line, g.open) - strings.Count(line, g.close)
	return g.depth == 0 && strings.HasSuffix(line, g.close)
}

// Block.Index is the lexical state at the start of a line. Complete groups are atomic blocks;
// incomplete or oversized groups retain their original lines and can be reparsed after an edit.
func (m *manuscript) blocks(start, state int) iter.Seq[Block] {
	return func(yield func(Block) bool) {
		next, stop := iter.Pull2(m.lines(start))
		defer stop()
		for {
			pos, line, ok := next()
			if !ok {
				return
			}
			b := Block{Start: pos, End: pos + len(line), Index: state, Kind: parser.Paragraph}
			if state != 0 {
				b.Kind = manuscriptRaw
				state = m.rawState(line, state)
			} else if group, ok := m.group(strings.TrimSpace(line)); ok {
				if group.closed(line, true) {
					b.Kind = group.kind
				} else {
					pending := []Block{b}
					closed := false
					for len(pending) < manuscriptBlockLines && pending[len(pending)-1].End-b.Start <= maxLeafBytes {
						at, raw, ok := next()
						if !ok {
							break
						}
						pending = append(pending, Block{Start: at, End: at + len(raw), Index: manuscriptPending, Kind: manuscriptRaw})
						if group.closed(raw, false) {
							closed = true
							break
						}
					}
					last := pending[len(pending)-1]
					if closed && last.End-b.Start <= maxLeafBytes {
						b.End, b.Kind = last.End, group.kind
					} else {
						pending[0].Kind = manuscriptRaw
						for _, row := range pending {
							if !yield(row) {
								return
							}
						}
						continue
					}
				}
			} else {
				b.Kind, state = m.lineKind(line)
			}
			if !yield(b) {
				return
			}
		}
	}
}

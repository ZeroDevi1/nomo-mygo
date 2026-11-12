package mdedit

import (
	"context"
	"iter"
	"maps"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown/parser"
	"nomo/desktop/typeset"
)

type manuscript struct {
	buffer                        *editor.Buffer
	ctx                           context.Context
	cancel                        context.CancelFunc
	snap                          editor.Snapshot
	latex                         bool
	project                       ManuscriptProject
	styleVersion, preambleVersion uint64
	preamble                      string
	macros                        map[string]bool
	blockCommands                 map[string]bool
	syntaxHeadEnd                 int
	declarations                  manuscriptDeclarations
	blockContext                  context.Context
	cancelBlocks                  context.CancelFunc
	blockStyleVersion             uint64
	paragraphGap, paragraphIndent float32
	pointScale                    float32
	paragraphAlignment            string
	draft                         *editor.Edit // 输入法预编辑只进入显示，提交时作为一个缓冲区事务写入。
}

func NewManuscriptEditor(buffer *editor.Buffer, latex bool) *Editor {
	ctx, cancel := context.WithCancel(context.Background())
	d := &Document{manuscript: &manuscript{buffer: buffer, snap: buffer.Snapshot(), latex: latex, ctx: ctx, cancel: cancel}}
	d.indexManuscript(0, 0, d.len())
	return NewEditor(d)
}

// CloseManuscript 提交组字并取消画布请求的局部公式渲染。
func (e *Editor) CloseManuscript() {
	e.SyncManuscript()
	e.Doc.Unmark()
	e.Doc.manuscript.cancel()
}

// SyncManuscript 同步从源码编辑器、外部文件或撤销栈送来的新版本。
// 画布内编辑拥有精确的替换区间，走增量索引，不经过这个入口。
func (e *Editor) SyncManuscript() {
	d := e.Doc
	m := d.manuscript
	if m == nil || m.snap.Version() == m.buffer.Version() {
		return
	}
	n := d.len()
	m.snap = m.buffer.Snapshot()
	m.draft, d.hasMarked = nil, false
	d.Sel = Selection{Anchor: min(d.Sel.Anchor, d.len()), Head: min(d.Sel.Head, d.len())}
	e.applyEdit(Edit{Change: d.indexManuscript(0, n, d.len()), Applied: true})
}

func (m *manuscript) len() int {
	n := m.snap.Len()
	if r := m.draft; r != nil {
		n += len(r.Text) - (r.End - r.Start)
	}
	return n
}

func (m *manuscript) chunks(a, z int, yield func(string) bool) {
	r := m.draft
	if r == nil {
		m.snap.Chunks(a, z, yield)
		return
	}
	more := true
	if a < r.Start {
		m.snap.Chunks(a, min(z, r.Start), func(s string) bool {
			more = yield(s)
			return more
		})
	}
	end := r.Start + len(r.Text)
	if i, j := max(a, r.Start), min(z, end); more && i < j {
		more = yield(r.Text[i-r.Start : j-r.Start])
	}
	if more && z > end {
		m.snap.Chunks(max(a, end)-end+r.End, z-end+r.End, yield)
	}
}

func (m *manuscript) slice(a, z int) string {
	if m.draft == nil {
		return m.snap.Slice(a, z)
	}
	var b strings.Builder
	m.chunks(a, z, func(s string) bool {
		b.WriteString(s)
		return true
	})
	return b.String()
}

func (m *manuscript) lines(start int) iter.Seq2[int, string] {
	return func(yield func(int, string) bool) {
		if m.draft == nil {
			for i := m.snap.LineOf(start); i < m.snap.LineCount(); i++ {
				if !yield(m.snap.LineStart(i), m.snap.Line(i)) {
					return
				}
			}
			return
		}
		var tail strings.Builder
		pos := start
		more := true
		m.chunks(start, m.len(), func(s string) bool {
			for s != "" {
				line, rest, newline := strings.Cut(s, "\n")
				if !newline {
					tail.WriteString(line)
					break
				}
				if tail.Len() != 0 {
					tail.WriteString(line)
					line = tail.String()
					tail.Reset()
				}
				if !yield(pos, line) {
					more = false
					return false
				}
				pos += len(line) + 1
				s = rest
			}
			return true
		})
		if more {
			yield(pos, tail.String())
		}
	}
}

// indexManuscript 重扫改动周围的块，到达相同词法状态的未改动行后复用后缀。
func (d *Document) indexManuscript(from, to, inserted int) Change {
	m := d.manuscript
	reindex := false
	if m.latex && from <= m.syntaxHeadEnd {
		head := ManuscriptLaTeXPreamble(d.slice(0, min(d.len(), manuscriptSetupLimit)))
		next := latexBlockCommands(head)
		reindex = !maps.Equal(m.blockCommands, next)
		m.blockCommands, m.syntaxHeadEnd = next, len(head)
	}
	old := d.blocks
	p := max(0, sort.Search(len(old), func(i int) bool { return old[i].End >= from })-1)
	start, state, u := 0, 0, 0
	if reindex {
		p = 0
	} else if len(old) != 0 {
		for rewind := 0; p > 0 && rewind < manuscriptBlockLines && old[p].Index == manuscriptPending; rewind++ {
			p--
		}
		b := old[p]
		start, state, u = b.Start, b.Index, b.U16Start
	}
	q := max(p, sort.Search(len(old), func(i int) bool { return old[i].Start >= to }))
	delta := inserted - (to - from)
	changed := d.spare[:0]
	edge, ud := start, 0
	aligned := false
	for b := range d.manuscript.blocks(start, state) {
		u += b.Start - edge // 块间只隔一个 LF。
		for q < len(old) && old[q].Start+delta < b.Start {
			q++
		}
		if !reindex && q < len(old) && b.Start >= from+inserted {
			prev := old[q]
			if b.Start == prev.Start+delta && b.End == prev.End+delta && b.Index == prev.Index && b.Kind == prev.Kind {
				ud, aligned = u-prev.U16Start, true
				break
			}
		}
		b.U16Start = u
		b.U16Len = u16Len(d.slice(b.Start, b.End))
		edge, u = b.End, u+b.U16Len
		changed = append(changed, b)
	}
	if !aligned {
		q = len(old)
	}
	if len(changed) != 0 && p < q && changed[0] == old[p] && old[p].End < from {
		p++
		changed = changed[1:]
	}
	n, removed := len(changed), q-p
	d.updateManuscriptDeclarations(old[p:q], changed, to, delta)
	d.blocks = slices.Replace(old, p, q, changed...)
	d.spare = changed[:0]
	for i := p + n; i < len(d.blocks); i++ {
		b := &d.blocks[i]
		b.Start, b.End, b.U16Start = b.Start+delta, b.End+delta, b.U16Start+ud
	}
	if len(d.blocks) != 0 {
		last := d.blocks[len(d.blocks)-1]
		d.u16Total = last.U16Start + last.U16Len
	}
	d.version++
	return Change{First: p, Removed: removed, Inserted: n, At: from, OldLen: to - from, NewLen: inserted}
}

func (d *Document) editManuscript(from, to int, text string) Edit {
	m := d.manuscript
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if d.slice(from, to) == text {
		return Edit{}
	}
	m.buffer.Edit(editor.Edit{Start: from, End: to, Text: text})
	m.snap = m.buffer.Snapshot()
	head := from + len(text)
	d.Sel = Selection{Anchor: head, Head: head}
	return Edit{Change: d.indexManuscript(from, to, len(text)), Applied: true}
}

func (d *Document) markManuscript(from, to int, text string, a, z int) Edit {
	m := d.manuscript
	if strings.Contains(text, "\r\n") {
		a, z = len(strings.ReplaceAll(text[:a], "\r\n", "\n")), len(strings.ReplaceAll(text[:z], "\r\n", "\n"))
		text = strings.ReplaceAll(text, "\r\n", "\n")
	}
	unchanged := d.slice(from, to) == text
	if m.draft != nil && (from != d.marked.Anchor || to != d.marked.Head) {
		d.finishManuscriptComposition()
	}
	if m.draft == nil {
		m.draft = &editor.Edit{Start: from, End: to}
	}
	m.draft.Text = text
	d.marked = Selection{Anchor: from, Head: from + len(text)}
	d.hasMarked = text != ""
	d.Sel = Selection{Anchor: from + a, Head: from + z}
	if unchanged {
		return Edit{}
	}
	return Edit{Change: d.indexManuscript(from, to, len(text)), Applied: true}
}

func (d *Document) finishManuscriptComposition() {
	m := d.manuscript
	if m.draft != nil {
		m.buffer.Edit(*m.draft)
		m.snap = m.buffer.Snapshot()
		m.draft = nil
	}
	d.hasMarked = false
}

func (d *Document) len() int {
	if d.manuscript != nil {
		return d.manuscript.len()
	}
	return len(d.md.Source())
}

func (d *Document) slice(a, z int) string {
	if d.manuscript != nil {
		return d.manuscript.slice(a, z)
	}
	return d.md.Source()[a:z]
}

func (d *Document) runeStart(pos int) int {
	if d.manuscript == nil {
		return runeStartBefore(d.md.Source(), pos)
	}
	for pos > 0 && pos < d.len() && !utf8.RuneStart(d.slice(pos, pos+1)[0]) {
		pos--
	}
	return pos
}

const (
	manuscriptRaw parser.Kind = 202 + iota
	manuscriptList
	manuscriptStyled
	manuscriptSetup
	manuscriptBibliography
)

const (
	manuscriptPreamble = -1
	manuscriptVerbatim = -2
	manuscriptRawFence = -3
	manuscriptPending  = -4
)

func (m *manuscript) rawState(line string, state int) int {
	s := strings.TrimSpace(line)
	switch state {
	case manuscriptPreamble:
		if strings.HasPrefix(s, `\begin{document}`) {
			return 0
		}
	case manuscriptVerbatim:
		if strings.HasPrefix(s, `\end{verbatim`) || s == `\end{lstlisting}` || s == `\end{minted}` {
			return 0
		}
	case manuscriptRawFence:
		if strings.HasPrefix(s, "```") {
			return 0
		}
	default:
		return typstLineState(line, state, state >= typstCodeState)
	}
	return state
}

func (m *manuscript) lineKind(line string) (parser.Kind, int) {
	s := strings.TrimSpace(line)
	if m.latex {
		switch {
		case strings.HasPrefix(s, `\documentclass`) && !strings.Contains(s, `\begin{document}`):
			return manuscriptSetup, manuscriptPreamble
		case strings.HasPrefix(s, `\begin{verbatim`), strings.HasPrefix(s, `\begin{lstlisting}`), strings.HasPrefix(s, `\begin{minted}`):
			return manuscriptRaw, manuscriptVerbatim
		case strings.HasPrefix(s, "%"), strings.HasPrefix(s, `\begin{`), strings.HasPrefix(s, `\end{`):
			return manuscriptRaw, 0
		}
	} else {
		switch {
		case strings.HasPrefix(s, "```") && !strings.Contains(s[3:], "```"):
			return manuscriptRaw, manuscriptRawFence
		case strings.HasPrefix(s, "#"):
			if _, ok := manuscriptStyledSpan(s, false); ok {
				return parser.Paragraph, 0
			}
			if _, _, end, _ := typeset.TypstReferenceCall(s); end > 0 {
				return parser.Paragraph, 0
			}
			return manuscriptRaw, typstLineState(s, 0, true)
		case strings.HasPrefix(s, "//"), strings.HasPrefix(s, "```"):
			return manuscriptRaw, 0
		case strings.Contains(s, "/*"):
			return manuscriptRaw, typstLineState(s, 0, false)
		}
	}
	if _, _, level := manuscriptHeading(s, m.latex); level > 0 {
		return parser.Heading, 0
	}
	if !m.latex && strings.HasPrefix(s, "- ") {
		return manuscriptList, 0
	}
	return parser.Paragraph, 0
}

const typstCodeState = 1 << 12

// 只跟踪会跨行的注释和代码分隔符；原文区结束后恢复对正文的识别。
func typstLineState(s string, state int, code bool) int {
	depth, comments := state/typstCodeState, state%typstCodeState
	for i := 0; i < len(s); i++ {
		switch {
		case strings.HasPrefix(s[i:], "/*"):
			comments = min(typstCodeState-1, comments+1)
			i++
		case comments != 0:
			if strings.HasPrefix(s[i:], "*/") {
				comments--
				i++
			}
		case strings.HasPrefix(s[i:], "//"):
			return depth*typstCodeState + comments
		case s[i] == '\\':
			i++
		case code && (s[i] == '"' || s[i] == '`'):
			quote := s[i]
			for i++; i < len(s) && s[i] != quote; i++ {
				if s[i] == '\\' {
					i++
				}
			}
		case code && strings.ContainsRune("([{", rune(s[i])):
			depth = min(typstCodeState-1, depth+1)
		case code && strings.ContainsRune(")]}", rune(s[i])):
			depth = max(0, depth-1)
		}
	}
	return depth*typstCodeState + comments
}

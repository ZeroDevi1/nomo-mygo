package mdedit

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui/markdown/parser"
)

// 块级右键命令按原文改写，每条命令为一次独立的撤销步。表格保留容器前缀和列对齐。

// codeFence 返回顶层围栏代码块 i 的内容范围 [a, z) 与开围栏行末。
func (d *Document) codeFence(i int) (a, z, fenceEnd int, ok bool) {
	b := d.blocks[i]
	src := d.Source()
	nl := strings.IndexByte(src[b.Start:b.End], '\n')
	if nl < 0 {
		return b.End, b.End, b.End, true // 只有开围栏
	}
	fenceEnd = b.Start + nl
	a, z = fenceEnd+1, b.End
	// 已闭合的块尾包含闭围栏行。
	pb := d.md.Block(b.Index)
	if !pb.Open {
		if k := strings.LastIndexByte(src[a:b.End], '\n'); k >= 0 {
			z = a + k
		} else {
			z = a // 内容为空：开围栏后紧跟闭围栏
		}
	}
	return a, z, fenceEnd, a <= z
}

// BlockContent 返回 pos 所在代码块、公式块或图表的内容原文（不含围栏）；不在这些块中时 ok 为 false。
func (e *Editor) BlockContent(pos int) (string, bool) {
	i, _ := e.Doc.BlockAt(pos)
	if i < 0 {
		return "", false
	}
	switch b := e.Doc.blocks[i]; {
	case b.Kind == parser.CodeBlock && !e.Doc.md.Block(b.Index).Fenced:
		// 缩进代码块：去掉每行的 4 个空格缩进。
		lines := strings.Split(e.Doc.Source()[b.Start:b.End], "\n")
		for k, l := range lines {
			lines[k] = strings.TrimPrefix(l, "    ")
		}
		return strings.Join(lines, "\n"), true
	case b.Kind == parser.MathBlock:
		return strings.TrimSuffix(e.Doc.md.Lines(b.Index), "\n"), true
	case b.Kind == parser.CodeBlock:
		a, z, _, ok := e.Doc.codeFence(i)
		if !ok {
			return "", false
		}
		return e.Doc.Source()[a:z], true
	}
	return "", false
}

// FormulaSource 返回完整的公式原文，包括定界符。
func (e *Editor) FormulaSource(pos int) (string, bool) {
	i, _ := e.Doc.BlockAt(pos)
	if i < 0 || e.Doc.blocks[i].Kind != parser.MathBlock {
		return "", false
	}
	b := e.Doc.blocks[i]
	return e.Doc.slice(b.Start, b.End), true
}

// SetCodeLanguage 把 pos 所在围栏代码块的语言改为 lang（空串为无语言），保留围栏字符与其后的其他信息。
func (e *Editor) SetCodeLanguage(pos int, lang string) bool {
	i, _ := e.Doc.BlockAt(pos)
	if i < 0 {
		return false
	}
	b := e.Doc.blocks[i]
	if b.Kind != parser.CodeBlock || !e.Doc.md.Block(b.Index).Fenced {
		return false
	}
	src := e.Doc.Source()
	line := src[b.Start:]
	if nl := strings.IndexByte(line, '\n'); nl >= 0 {
		line = line[:nl]
	}
	fence := len(line) - len(strings.TrimLeft(line, "`~"))
	info := strings.TrimSpace(line[fence:])
	rest := ""
	if k := strings.IndexAny(info, " \t"); k >= 0 {
		rest = info[k:]
	}
	e.Replace(b.Start+fence, b.Start+len(line), lang+rest)
	return true
}

// DeleteBlock 删除 pos 所在的顶层块（连同其后的一个空行），光标置于原处。
func (e *Editor) DeleteBlock(pos int) bool {
	i, _ := e.Doc.BlockAt(pos)
	if i < 0 {
		return false
	}
	b := e.Doc.blocks[i]
	src := e.Doc.Source()
	a, z := b.Start, b.End
	switch {
	case strings.TrimSpace(src[z:]) == "":
		// 文末的块：连同前面的空行删到文末，保留上一块后的一个换行。
		z = len(src)
		for a > 0 && src[a-1] == '\n' {
			a--
		}
		if a > 0 {
			a++
		}
	case strings.HasPrefix(src[z:], "\n\n"):
		z += 2
	case strings.HasPrefix(src[z:], "\n"):
		z++
	}
	e.Replace(a, z, "")
	return true
}

// TableCell 报告 pos 所在的表格单元格：row 为表格行（0 为表头，分隔行不计），col 为列。
func (e *Editor) TableCell(pos int) (row, col, rows, cols int, ok bool) {
	t, ok := e.tableAtPos(pos)
	if !ok {
		return 0, 0, 0, 0, false
	}
	return t.row, t.col, len(t.cells), len(t.align), true
}

// TableCommand 在 pos 所在的表格上执行 op：addRowBefore、addRowAfter、deleteRow、addColumnBefore、addColumnAfter、
// deleteColumn、alignLeft、alignCenter、alignRight、alignNone、deleteTable。不适用（如删除表头行）时返回 false。
func (e *Editor) TableCommand(pos int, op string) bool {
	t, ok := e.tableAtPos(pos)
	if !ok {
		return false
	}
	if op == "deleteTable" || op == "deleteColumn" && len(t.align) == 1 {
		if !t.nested {
			return e.DeleteBlock(pos)
		}
		// 首行之前可能是列表标记，保留该标记及相邻段落的分隔行。
		src := e.Doc.Source()
		prefix := src[lineStart(src, t.start):t.start]
		if strings.HasSuffix(prefix, "[ ] ") || strings.HasSuffix(prefix, "[x] ") || strings.HasSuffix(prefix, "[X] ") {
			// An empty GFM task needs body text for the task marker to survive parsing.
			e.Replace(t.start, t.end, "<!-- -->")
		} else {
			e.Replace(t.start, t.end, "")
		}
		return true
	}
	n := len(t.align)
	blank := func() []string { return make([]string, n) }
	row, col := t.row, t.col
	switch op {
	case "addRowBefore":
		at := max(1, row) // 表头之前不能插入：插到表头之后
		t.cells = slices.Insert(t.cells, at, blank())
		t.prefixes = slices.Insert(t.prefixes, at, t.separator)
		t.extra = slices.Insert(t.extra, at, "")
		row = at
	case "addRowAfter":
		t.cells = slices.Insert(t.cells, row+1, blank())
		t.prefixes = slices.Insert(t.prefixes, row+1, t.separator)
		t.extra = slices.Insert(t.extra, row+1, "")
		row++
	case "deleteRow":
		if row == 0 {
			return false
		}
		t.cells = append(t.cells[:row], t.cells[row+1:]...)
		t.prefixes = slices.Delete(t.prefixes, row, row+1)
		t.extra = slices.Delete(t.extra, row, row+1)
		row = min(row, len(t.cells)-1)
	case "addColumnBefore", "addColumnAfter":
		at := col
		if op == "addColumnAfter" {
			at++
		}
		for k := range t.cells {
			t.cells[k] = slices.Insert(t.cells[k], at, "")
		}
		t.align = slices.Insert(t.align, at, parser.AlignNone)
		col = at
	case "deleteColumn":
		for k := range t.cells {
			t.cells[k] = append(t.cells[k][:col], t.cells[k][col+1:]...)
		}
		t.align = append(t.align[:col], t.align[col+1:]...)
		col = min(col, len(t.align)-1)
	case "alignLeft", "alignCenter", "alignRight", "alignNone":
		t.align[col] = map[string]parser.Align{"alignLeft": parser.AlignLeft, "alignCenter": parser.AlignCenter,
			"alignRight": parser.AlignRight, "alignNone": parser.AlignNone}[op]
	default:
		return false
	}
	e.replaceTable(t, row, col)
	return true
}

// tableEdit 是解析为单元格的一张表格：cells[0] 为表头，每行的单元格数等于列数（不足补空，多出截断，与 GFM 一致）。
type tableEdit struct {
	start, end int
	cells      [][]string
	align      []parser.Align
	row, col   int // pos 所在单元格
	nested     bool
	prefixes   []string
	extra      []string // 超出表头列数的原文，GFM 不显示，结构编辑仍保留
	separator  string
}

func (e *Editor) tableBlockAt(pos int) int {
	i, _ := e.Doc.BlockAt(pos)
	if i < 0 {
		return 0
	}
	top := e.Doc.blocks[i]
	if top.Kind == parser.Table {
		return top.Index
	}
	if top.Kind != parser.List && top.Kind != parser.Quote && top.Kind != parser.FootnoteDefinition {
		return 0
	}
	s, ok := e.segAt(pos)
	if !ok || e.entries[s.i].rows[s.k].ncol == 0 {
		return 0
	}
	cell := e.Doc.md.Block(top.Index + e.entries[s.i].rows[s.k].rel)
	if cell.Kind != parser.TableCell {
		return 0
	}
	table := e.Doc.md.Block(cell.Parent).Parent
	b := e.Doc.md.Block(table)
	if pos < b.Start || pos > trailingEnd(e.Doc.Source(), b.End) {
		return 0
	}
	return table
}

func (e *Editor) tableAtPos(pos int) (tableEdit, bool) {
	i := e.tableBlockAt(pos)
	if i == 0 {
		return tableEdit{}, false
	}
	b := e.Doc.md.Block(i)
	src := e.Doc.Source()
	t := tableEdit{start: b.Start, end: trailingEnd(src, b.End), align: slices.Clone(b.Align), nested: b.Parent != 0}
	n := len(t.align)
	if n == 0 {
		return tableEdit{}, false
	}
	for child := range e.Doc.md.Children(i) {
		r := e.Doc.md.Block(child)
		end := trailingEnd(src, r.Start)
		prefix := ""
		if r.Header {
			if end == len(src) {
				return tableEdit{}, false
			}
			sep := src[end+1 : trailingEnd(src, end+1)]
			at := strings.IndexAny(sep, "|:-")
			if at < 0 {
				return tableEdit{}, false
			}
			t.separator = sep[:at]
		} else {
			prefix = src[lineStart(src, r.Start):r.Start]
		}
		cells, bounds := splitRow(src[r.Start:end])
		if pos >= r.Start && pos <= end {
			t.row, t.col = len(t.cells), 0
			for c, bd := range bounds {
				if pos-r.Start >= bd {
					t.col = c
				}
			}
			t.col = min(t.col, n-1)
		}
		row := make([]string, n)
		copy(row, cells)
		t.cells = append(t.cells, row)
		t.prefixes = append(t.prefixes, prefix)
		suffix := ""
		if len(cells) > n {
			suffix = src[r.Start+bounds[n] : end]
		}
		t.extra = append(t.extra, suffix)
	}
	return t, len(t.cells) > 0
}

func (e *Editor) replaceTable(t tableEdit, row, col int) {
	text, caret := t.render(row, col)
	version := e.Doc.Version()
	e.Replace(t.start, t.end, text)
	at := t.start + caret
	e.Doc.SetSelection(Selection{Anchor: at, Head: at})
	if n := len(e.Doc.hist.undo); version != e.Doc.Version() && n > 0 {
		e.Doc.hist.undo[n-1].after = e.Doc.Sel
	}
	e.cells = nil
	e.reveal, e.dirty = true, true
}

// splitRow 把一行表格拆为单元格（去首尾空白），并返回各单元格在行内的起点。只有“\|”不分隔；与 GFM 一致，
// 行内代码中的“|”同样分隔单元格。
func splitRow(line string) (cells []string, starts []int) {
	s := 0
	trim := strings.TrimLeft(line, " \t")
	if strings.HasPrefix(trim, "|") {
		s = len(line) - len(trim) + 1
	}
	start := s
	for i := s; i <= len(line); i++ {
		if i < len(line) {
			if line[i] == '\\' {
				i++
				continue
			}
			if line[i] != '|' {
				continue
			}
		}
		cell := line[start:i]
		if i == len(line) && strings.TrimSpace(cell) == "" && len(cells) > 0 {
			break // 行尾“|”之后的空白
		}
		cells = append(cells, strings.TrimSpace(cell))
		starts = append(starts, start)
		start = i + 1
	}
	return cells, starts
}

// render 输出表格原文，并返回 (row, col) 单元格内容起点在表格内的偏移。
func (t tableEdit) render(row, col int) (string, int) {
	var b strings.Builder
	caret := 0
	line := func(cells []string, r int) {
		b.WriteString("|")
		for c, v := range cells {
			b.WriteByte(' ')
			if r == row && c == col {
				caret = b.Len()
			}
			b.WriteString(v)
			b.WriteString(" |")
		}
	}
	for r, cells := range t.cells {
		if r > 0 {
			b.WriteByte('\n')
			b.WriteString(t.prefixes[r])
		}
		line(cells, r)
		b.WriteString(t.extra[r])
		if r == 0 {
			b.WriteByte('\n')
			b.WriteString(t.separator)
			b.WriteByte('|')
			for _, a := range t.align {
				b.WriteString(delimiterCell(a))
			}
		}
	}
	return b.String(), caret
}

// delimiterCell 返回分隔行中一列的原文（含其后的“|”）。
func delimiterCell(a parser.Align) string {
	switch a {
	case parser.AlignLeft:
		return " :--- |"
	case parser.AlignCenter:
		return " :---: |"
	case parser.AlignRight:
		return " ---: |"
	}
	return " --- |"
}

// tableInput keeps plain input and paste inside one GFM cell. Tabs are left
// intact here so a structured table paste can be handled before this path.
func (e *Editor) tableInput(a, z int, text string) string {
	if !strings.ContainsAny(text, "|\r\n") || e.Doc.manuscript != nil {
		return text
	}
	s, ok := e.segAt(a)
	if !ok || e.entries[s.i].rows[s.k].ncol == 0 {
		return text
	}
	src := e.Doc.Source()
	lo, hi := s.base, s.end
	for lo > 0 && (src[lo-1] == ' ' || src[lo-1] == '\t') {
		lo--
	}
	for hi < len(src) && (src[hi] == ' ' || src[hi] == '\t') {
		hi++
	}
	if a < lo || z > hi {
		return text
	}
	slashes := 0
	for k := a; k > lo && src[k-1] == '\\'; k-- {
		slashes++
	}
	return escapeTableText(text, slashes)
}

func escapeTableText(text string, slashes int) string {
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\\':
			slashes++
			b.WriteByte('\\')
			continue
		case '|':
			if slashes%2 == 0 {
				b.WriteByte('\\')
			}
			b.WriteByte('|')
		case '\r', '\n':
			if text[i] == '\r' && i+1 < len(text) && text[i+1] == '\n' {
				i++
			}
			b.WriteString("<br>")
		default:
			b.WriteByte(text[i])
		}
		slashes = 0
	}
	return b.String()
}

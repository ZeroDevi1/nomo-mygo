package mdedit

import (
	"strings"

	"github.com/egoist/mygo/ui/markdown/parser"
)

// 块级右键命令（原版 contextMenu.ts 的代码块、表格、公式与图表菜单）：只作用于顶层块，按原文改写，每条命令为
// 一次独立的撤销步。表格命令整表重写为规范的管道表格（单元格去首尾空白、每行首尾带“|”），对齐方式保留。

// codeFence 返回顶层代码、公式块 i 的内容范围 [a, z)（不含开闭围栏行）与开围栏行末；未闭合的块内容到块尾。
func (d *Document) codeFence(i int) (a, z, fenceEnd int, ok bool) {
	b := d.blocks[i]
	src := d.Source()
	nl := strings.IndexByte(src[b.Start:b.End], '\n')
	if nl < 0 {
		return b.End, b.End, b.End, true // 只有开围栏
	}
	fenceEnd = b.Start + nl
	a, z = fenceEnd+1, b.End
	// 闭合的块尾含闭围栏行（公式块的闭合行由 blockSourceEnd 补入），内容止于其前一行；未闭合时内容到块尾。
	pb := d.md.Block(b.Index)
	closed := !pb.Open
	if b.Kind == parser.MathBlock {
		closed = blockSourceEnd(src, pb.Kind, pb.Start, pb.End) != pb.End
	}
	if closed {
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
	case b.Kind == parser.CodeBlock || b.Kind == parser.MathBlock:
		a, z, _, ok := e.Doc.codeFence(i)
		if !ok {
			return "", false
		}
		return e.Doc.Source()[a:z], true
	}
	return "", false
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

// TableCell 报告 pos 所在的表格单元格：row 为表格行（0 为表头，分隔行不计），col 为列；不在顶层表格中时 ok 为 false。
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
	if op == "deleteTable" {
		if _, ok := e.tableAtPos(pos); !ok {
			return false
		}
		return e.DeleteBlock(pos)
	}
	t, ok := e.tableAtPos(pos)
	if !ok {
		return false
	}
	n := len(t.align)
	blank := func() []string { return make([]string, n) }
	row, col := t.row, t.col
	switch op {
	case "addRowBefore":
		at := max(1, row) // 表头之前不能插入：插到表头之后
		t.cells = append(t.cells[:at], append([][]string{blank()}, t.cells[at:]...)...)
		row = at
	case "addRowAfter":
		t.cells = append(t.cells[:row+1], append([][]string{blank()}, t.cells[row+1:]...)...)
		row++
	case "deleteRow":
		if row == 0 {
			return false
		}
		t.cells = append(t.cells[:row], t.cells[row+1:]...)
		row = min(row, len(t.cells)-1)
	case "addColumnBefore", "addColumnAfter":
		at := col
		if op == "addColumnAfter" {
			at++
		}
		for k := range t.cells {
			t.cells[k] = append(t.cells[k][:at], append([]string{""}, t.cells[k][at:]...)...)
		}
		t.align = append(t.align[:at], append([]parser.Align{parser.AlignNone}, t.align[at:]...)...)
		col = at
	case "deleteColumn":
		if n == 1 {
			return e.DeleteBlock(pos)
		}
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
	text, caret := t.render(row, col)
	e.Replace(t.start, t.end, text)
	// 光标在单元格内容起点；空单元格（“|  |”）为两个空格之间，输入后即为“| X |”。
	at := t.start + caret
	e.Doc.Sel = Selection{Anchor: at, Head: at}
	return true
}

// tableEdit 是解析为单元格的一张表格：cells[0] 为表头，每行的单元格数等于列数（不足补空，多出截断，与 GFM 一致）。
type tableEdit struct {
	start, end int
	cells      [][]string
	align      []parser.Align
	row, col   int // pos 所在单元格
}

func (e *Editor) tableAtPos(pos int) (tableEdit, bool) {
	i, _ := e.Doc.BlockAt(pos)
	if i < 0 || e.Doc.blocks[i].Kind != parser.Table {
		return tableEdit{}, false
	}
	b := e.Doc.blocks[i]
	src := e.Doc.Source()[b.Start:b.End]
	lines := strings.Split(src, "\n")
	if len(lines) < 2 {
		return tableEdit{}, false
	}
	t := tableEdit{start: b.Start, end: b.End, align: append([]parser.Align(nil), e.Doc.md.Block(b.Index).Align...)}
	n := len(t.align)
	if n == 0 {
		return tableEdit{}, false
	}
	off, rel := 0, pos-b.Start
	for k, l := range lines {
		cells, bounds := splitRow(l)
		if k == 1 {
			off += len(l) + 1
			continue // 分隔行
		}
		r := len(t.cells)
		if rel >= off && rel <= off+len(l) {
			t.row, t.col = r, 0
			for c, bd := range bounds {
				if rel-off >= bd {
					t.col = c
				}
			}
			t.col = min(t.col, n-1)
		}
		row := make([]string, n)
		copy(row, cells)
		t.cells = append(t.cells, row)
		off += len(l) + 1
	}
	return t, true
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
		}
		line(cells, r)
		if r == 0 {
			b.WriteString("\n|")
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

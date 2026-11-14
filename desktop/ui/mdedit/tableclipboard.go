package mdedit

import (
	"encoding/csv"
	"io"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
)

const (
	tablePasteRows  = 512
	tablePasteCols  = 128
	tablePasteBytes = 1 << 20
)

// 矩形选区只保存端点，复制、粘贴或清空时才读取单元格原文。
type cellSelection struct {
	frameStart   int
	anchor, head int // indices relative to the first cell in the table
}

func (e *Editor) cellAtPoint(pt ui.Point) (seg, *frame, bool) {
	e.sync()
	x := pt.X - e.left()
	for _, v := range e.vsegs {
		en := &e.entries[v.i]
		r := &en.rows[v.k]
		if r.ncol == 0 || pt.Y < v.y || pt.Y >= v.y+r.bandH {
			continue
		}
		f := tableFrame(en, v.k)
		if f == nil || x < f.indent || x >= f.indent+f.viewW {
			continue
		}
		left := r.indent + r.cellX - r.shift
		if x >= left && x < left+r.cellW {
			return e.segOf(v.i, v.k), f, true
		}
	}
	return seg{}, nil, false
}

func (e *Editor) startCellSelection(s seg, f *frame) {
	start := e.Doc.blocks[s.i].Start + e.entries[s.i].rows[f.first].a
	e.cells = &cellSelection{frameStart: start, anchor: s.k - f.first, head: s.k - f.first}
	e.setHead(s.base, false)
	e.drag = dragTableCells
}

func (e *Editor) extendCellSelection(pt ui.Point) {
	s, f, ok := e.cellAtPoint(pt)
	if !ok || e.Doc.blocks[s.i].Start+e.entries[s.i].rows[f.first].a != e.cells.frameStart {
		return
	}
	e.cells.head = s.k - f.first
	e.setHead(s.base, false)
}

func (s *cellSelection) bounds(n int) (r0, r1, c0, c1 int) {
	return min(s.anchor/n, s.head/n), max(s.anchor/n, s.head/n),
		min(s.anchor%n, s.head%n), max(s.anchor%n, s.head%n)
}

func (e *Editor) paintCellSelection(p *ui.Painter, s seg, x, y float32) {
	if e.cells == nil {
		return
	}
	en := &e.entries[s.i]
	f := tableFrame(en, s.k)
	if f == nil || e.Doc.blocks[s.i].Start+en.rows[f.first].a != e.cells.frameStart {
		return
	}
	r := &en.rows[s.k]
	r0, r1, c0, c1 := e.cells.bounds(r.ncol)
	row := (s.k - f.first) / r.ncol
	if row >= r0 && row <= r1 && r.col >= c0 && r.col <= c1 {
		p.Fill(ui.Rect{X: x - cellPadX, Y: y - r.padTop, W: r.cellW, H: r.bandH}, e.pal.selection, 0)
	}
}

func (e *Editor) copyTableCells(cut bool) {
	if !e.hasSvc || e.cells == nil {
		return
	}
	t, ok := e.tableAtPos(e.cells.frameStart)
	if !ok {
		return
	}
	r0, r1, c0, c1 := e.cells.bounds(len(t.align))
	if r1 >= len(t.cells) || r1-r0 >= tablePasteRows || c1-c0 >= tablePasteCols {
		return
	}
	var b strings.Builder
	w := csv.NewWriter(&b)
	w.Comma = '\t'
	for r := r0; r <= r1; r++ {
		size := 0
		for _, cell := range t.cells[r][c0 : c1+1] {
			size += len(cell)
			if size+b.Len() > tablePasteBytes {
				return
			}
		}
		if err := w.Write(t.cells[r][c0 : c1+1]); err != nil || b.Len() > tablePasteBytes {
			return
		}
	}
	w.Flush()
	if w.Error() != nil || b.Len() > tablePasteBytes {
		return
	}
	e.svc.WriteClipboard(strings.TrimSuffix(b.String(), "\n"))
	if cut {
		e.clearTableCells()
	}
}

func (e *Editor) clearTableCells() {
	if e.cells == nil {
		return
	}
	t, ok := e.tableAtPos(e.cells.frameStart)
	if !ok {
		return
	}
	r0, r1, c0, c1 := e.cells.bounds(len(t.align))
	if r1 >= len(t.cells) {
		return
	}
	for r := r0; r <= r1; r++ {
		clear(t.cells[r][c0 : c1+1])
	}
	e.replaceTable(t, r0, c0)
}

// pasteTableCells expands the table if necessary and writes the rectangle in one edit.
func (e *Editor) pasteTableCells(text string) bool {
	if e.Doc.manuscript != nil || !strings.ContainsRune(text, '\t') {
		return false
	}
	pos := e.Doc.Sel.Head
	if e.cells != nil {
		pos = e.cells.frameStart
	}
	if e.tableBlockAt(pos) == 0 {
		return false
	}
	if e.cells == nil {
		a, z := e.Doc.Sel.Range()
		if a != z {
			s, ok := e.segAt(a)
			if !ok || a < s.base || z > s.end {
				return false
			}
		}
	}
	if len(text) > tablePasteBytes {
		return false
	}
	reader := csv.NewReader(strings.NewReader(text))
	reader.Comma, reader.FieldsPerRecord = '\t', -1
	reader.LazyQuotes = true
	var incoming [][]string
	cols := 0
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(incoming) >= tablePasteRows || len(row) > tablePasteCols {
			return false
		}
		incoming = append(incoming, row)
		cols = max(cols, len(row))
	}
	if len(incoming) == 0 || cols < 2 {
		return false
	}
	t, ok := e.tableAtPos(pos)
	if !ok {
		return false
	}
	row, col := t.row, t.col
	if e.cells != nil {
		row, _, col, _ = e.cells.bounds(len(t.align))
	}
	ncol := max(col+cols, len(t.align))
	if ncol > tablePasteCols || row+len(incoming) > tablePasteRows && row+len(incoming) > len(t.cells) ||
		ncol > len(t.align) && len(t.cells) > tablePasteRows {
		return false
	}
	if extra := ncol - len(t.align); extra > 0 {
		t.align = slices.Grow(t.align, extra)[:ncol]
		for r := range t.cells {
			t.cells[r] = slices.Grow(t.cells[r], extra)[:ncol]
		}
	}
	for len(t.cells) < row+len(incoming) {
		t.cells = append(t.cells, make([]string, ncol))
		t.prefixes = append(t.prefixes, t.separator)
		t.extra = append(t.extra, "")
	}
	for r, fields := range incoming {
		for c, field := range fields {
			t.cells[row+r][col+c] = escapeTableText(field, 0)
		}
	}
	e.replaceTable(t, row, col)
	return true
}

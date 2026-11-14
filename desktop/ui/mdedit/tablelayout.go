package mdedit

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-text/typesetting/segmenter"

	"github.com/egoist/mygo/ui"
)

const (
	// 在大表中采样，避免按键逐格排版或测量整张表。
	tableSampleRows   = 500
	tableSampleCells  = 4096
	tableMeasureBytes = 512
	tableScrollbarH   = 6
)

// layoutTables 为 rows/frames 中的每张表格计算列宽，写入各单元格行的 cellX、cellW 与 shift。avail 为
// 内容区宽度（不含表格缩进）。frames 中已有的 scrollX 会被夹到新的可滚动范围内。
func (e *Editor) layoutTables(top Block, rows []row, frames []frame, avail float32) {
	src := e.Doc.Source()
	for fi := range frames {
		f := &frames[fi]
		if f.kind != frameTable {
			continue
		}
		ncol := max(1, rows[f.first].ncol)
		chrome := float32(2*cellPadX + 1)
		widths := make([]tableWidth, ncol)
		bands := 0
		for k := f.first; k <= f.last; k++ {
			r := &rows[k]
			if r.col == 0 {
				bands++
				if bands > tableSampleRows+1 || bands > 2 && k-f.first >= tableSampleCells {
					break
				}
			}
			if r.col >= ncol {
				continue
			}
			a, z := top.Start+r.a, top.Start+r.b
			text := src[a:z]
			if len(text) <= tableMeasureBytes && strings.ContainsAny(text, "*_`~[<&$\\") {
				text = projectLeaf(e.Doc, top.Index+r.rel, a, z, expansion{}, leafOpts{}).text
			}
			size := tableContentWidth(text, e.rowFont(r).Size)
			widths[r.col].add(size)
		}
		f.viewW = max(0, avail-f.indent)
		f.cols, f.tableW = fitTableColumns(f.cols[:0], widths, f.viewW, chrome)
		f.scrollX = clampF(f.scrollX, 0, max(0, f.tableW-f.viewW))
		e.applyTableColumns(rows, f)
	}
}

// applyTableColumns 按 f.cols 与 f.scrollX 写入表格各单元格行的 cellX、cellW 与 shift。
func (e *Editor) applyTableColumns(rows []row, f *frame) {
	x := make([]float32, len(f.cols)+1)
	for c, w := range f.cols {
		x[c+1] = x[c] + w
	}
	for k := f.first; k <= f.last; k++ {
		r := &rows[k]
		c := min(r.col, len(f.cols)-1)
		r.cellX, r.cellW, r.shift = x[c], f.cols[c], f.scrollX
	}
}

type tableWidth struct{ minimum, natural float32 }

func (w *tableWidth) add(other tableWidth) {
	w.minimum = max(w.minimum, other.minimum)
	w.natural = max(w.natural, other.natural)
}

// tableContentWidth uses Unicode break opportunities to estimate the longest
// unbreakable fragment and the natural line width.
func tableContentWidth(s string, em float32) tableWidth {
	var w tableWidth
	if len(s) == 0 {
		return w
	}
	if len(s) > tableMeasureBytes {
		s = s[:chunkCut(s, 0, tableMeasureBytes)]
	}
	if len(s) <= 12 && !strings.ContainsAny(s, " -/\r\n\t") && utf8.RuneCountInString(s) == len(s) {
		estimate := float32(len(s)) * em * .6
		return tableWidth{estimate, estimate}
	}
	var sg segmenter.Segmenter
	sg.Init([]rune(s))
	iter := sg.LineIterator()
	var line float32
	for iter.Next() {
		part := iter.Line()
		var width, trailing float32
		for _, r := range part.Text {
			if r == '\n' || r == '\r' {
				continue
			}
			size := em * .6
			if r >= 0x2e80 {
				size = em
			}
			width += size
			if unicode.IsSpace(r) {
				trailing += size
			} else {
				trailing = 0
			}
		}
		line += width
		w.minimum = max(w.minimum, width-trailing)
		w.natural = max(w.natural, line-trailing)
		if part.IsMandatoryBreak {
			line = 0
		}
	}
	return w
}

func fitTableColumns(dst []float32, widths []tableWidth, avail, chrome float32) ([]float32, float32) {
	var low, high float32
	for i := range widths {
		w := &widths[i]
		w.minimum = max(32, w.minimum+chrome)
		w.natural = max(w.minimum, w.natural+chrome)
		low += w.minimum
		high += w.natural
	}
	target := max(low, min(avail, high))
	ratio := float32(0)
	if high > low {
		ratio = (target - low) / (high - low)
	}
	for _, w := range widths {
		dst = append(dst, w.minimum+ratio*(w.natural-w.minimum))
	}
	return dst, target
}

func clampF(v, lo, hi float32) float32 { return min(max(v, lo), hi) }

// tableFrame 返回块 i 中包含第 k 行的表格装饰，没有时返回 nil。
func tableFrame(en *entry, k int) *frame {
	for fi := range en.frames {
		f := &en.frames[fi]
		if f.kind == frameTable && f.first <= k && k <= f.last {
			return f
		}
	}
	return nil
}

// scrollTable 把块 i 中的表格 f 横向滚动 dx（正值向右看），返回是否滚动了。
func (e *Editor) scrollTable(en *entry, f *frame, dx float32) bool {
	x := clampF(f.scrollX+dx, 0, max(0, f.tableW-f.viewW))
	if x == f.scrollX {
		return false
	}
	f.scrollX = x
	e.applyTableColumns(en.rows, f)
	e.dirty = true
	return true
}

// tableAt 返回窗口内坐标 pt 处的表格（块序号与装饰），用于横向滚轮。
func (e *Editor) tableAt(pt ui.Point) (*entry, *frame) {
	for _, v := range e.vsegs {
		en := &e.entries[v.i]
		r := &en.rows[v.k]
		if r.ncol == 0 || pt.Y < v.y || pt.Y >= v.y+r.bandH {
			continue
		}
		if f := tableFrame(en, v.k); f != nil && f.tableW > f.viewW {
			return en, f
		}
	}
	return nil, nil
}

// revealCell 在光标位于横向滚出视口的单元格时，横向滚动所在表格使单元格可见。
func (e *Editor) revealCell(s seg) {
	en := &e.entries[s.i]
	r := &en.rows[s.k]
	if r.ncol == 0 {
		return
	}
	f := tableFrame(en, s.k)
	if f == nil || f.tableW <= f.viewW {
		return
	}
	switch {
	case r.cellX < f.scrollX:
		e.scrollTable(en, f, r.cellX-f.scrollX)
	case r.cellX+r.cellW > f.scrollX+f.viewW:
		e.scrollTable(en, f, r.cellX+r.cellW-f.scrollX-f.viewW)
	}
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

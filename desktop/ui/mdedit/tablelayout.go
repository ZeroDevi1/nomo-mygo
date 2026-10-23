package mdedit

import (
	"slices"

	"github.com/egoist/mygo/ui"
)

// 表格列宽与横向滚动。列宽策略移植自原版 src/lib/table-layout/widths.ts（TABLE_WIDTH_POLICY）：每列的
// 最小宽度保证表头与短值可读，首选宽度取表头、正文 p90 与最长值（各有 em 上限）；可用宽度介于两者之间时
// 按比例插值，放不下最小宽度时整表横向滚动（原版 .nomo-table-scroll 的 overflow-x: auto）。与原版的差异：
// 整表已需横向滚动时按首选宽度排（原版仍压到最小宽度），避免短值（如编号）在每格里折行。

// 原版 TABLE_WIDTH_POLICY 的 em 倍数。
const (
	tableEmptyEm   = 3
	tableMinimumEm = 6
	tableHeaderEm  = 12
	tableBodyEm    = 28
	tableLongEm    = 12
	// tableSampleRows 是参与列宽计算的正文行数上限：超长表格（万行）只取前若干行估计，避免每次编辑
	// 表格都遍历全部单元格；p90 在样本上已稳定。
	tableSampleRows = 500
	// tableScrollbarH 是表格横向滚动条的高度（溢出时画在表格底边内侧）。
	tableScrollbarH = 6
)

// layoutTables 为 rows/frames 中的每张表格计算列宽，写入各单元格行的 cellX、cellW 与 shift。avail 为
// 内容区宽度（不含表格缩进）。frames 中已有的 scrollX 会被夹到新的可滚动范围内。
func (e *Editor) layoutTables(src string, start int, rows []row, frames []frame, avail float32) {
	for fi := range frames {
		f := &frames[fi]
		if f.kind != frameTable {
			continue
		}
		ncol := max(1, rows[f.first].ncol)
		em := e.rowFont(&rows[f.first]).Size
		chrome := float32(2*cellPadX + 1)
		type colStat struct {
			header float32
			body   []float32
		}
		stats := make([]colStat, ncol)
		bands := 0
		for k := f.first; k <= f.last; k++ {
			r := &rows[k]
			if r.col == 0 {
				bands++
				if bands > tableSampleRows+1 {
					break
				}
			}
			if r.col >= ncol {
				continue
			}
			w := cellTextWidth(src[start+r.a:start+r.b], em)
			if r.role == roleCellHead {
				stats[r.col].header = max(stats[r.col].header, w)
			} else if w > 0 {
				stats[r.col].body = append(stats[r.col].body, w)
			}
		}
		minimum := make([]float32, ncol)
		preferred := make([]float32, ncol)
		var sumMin, sumPref float32
		for c := range stats {
			st := &stats[c]
			slices.Sort(st.body)
			var p90, longest float32
			if n := len(st.body); n > 0 {
				p90 = st.body[(n*9+9)/10-1]
				longest = st.body[n-1]
			}
			natural := max(st.header, longest)
			minimum[c] = chrome + max(tableEmptyEm*em, min(natural, tableMinimumEm*em), min(st.header, tableHeaderEm*em))
			preferred[c] = max(minimum[c], chrome+max(min(st.header, tableHeaderEm*em), min(p90, tableBodyEm*em), min(longest, tableLongEm*em)))
			sumMin += minimum[c]
			sumPref += preferred[c]
		}
		target := max(sumMin, min(avail-f.indent, sumPref))
		if sumMin > avail-f.indent {
			target = sumPref
		}
		ratio := float32(0)
		if sumPref > sumMin {
			ratio = (target - sumMin) / (sumPref - sumMin)
		}
		f.cols = f.cols[:0]
		for c := range ncol {
			f.cols = append(f.cols, minimum[c]+ratio*(preferred[c]-minimum[c]))
		}
		f.tableW, f.viewW = target, max(0, avail-f.indent)
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

// cellTextWidth 估算单元格文字不换行时的宽度：全角字符 1em，其余 0.6em（比 textWidth 的 0.55 略宽，
// 列宽宁可略宽也不让短值折行）。
func cellTextWidth(s string, fs float32) float32 {
	wide, narrow := 0, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 0xE0:
			wide++
		case c < 0x80:
			narrow++
		}
	}
	return float32(wide)*fs + float32(narrow)*fs*0.6
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

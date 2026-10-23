package mdedit

import (
	"strings"

	"github.com/egoist/mygo/ui/markdown/parser"
)

// patchRowEdit 尝试把一次编辑作为单行修补应用到块 i 的视图缓存：编辑落在表格某个单元格或代码块某一行之内、
// 且解析结果显示块结构未变时，只平移其后各行的原文区间、作废被编辑行的投影与布局，不重新展开整块。万行表格
// 或数万行代码块按键时，整块重新展开要为每个单元格或每一行重建行记录与哈希（数十毫秒并产生大量垃圾）。
//
// 结构未变的判断以当前解析结果为准：块的后代数与展开时相同（单元格增删、行列变化都会改变它），表格中被编辑
// 单元格的新区间与平移后的旧区间一致；代码块的行数不变、被编辑行不是围栏行且不会变成围栏行。不满足时返回
// false，由调用方整块重新展开。
func (e *Editor) patchRowEdit(i int, ch Change) bool {
	en := &e.entries[i]
	top := e.Doc.blocks[i]
	if !en.split || len(en.rows) < 64 || (top.Kind != parser.Table && top.Kind != parser.CodeBlock) {
		return false // 小块整块重新展开已足够快，只为大块走修补路径
	}
	if ch.At <= top.Start || e.Doc.md.next(top.Index)-top.Index != en.pspan {
		return false
	}
	rel, d := ch.At-top.Start, ch.NewLen-ch.OldLen
	src := e.Doc.Source()
	ins := src[ch.At : ch.At+ch.NewLen]
	if strings.ContainsAny(ins, "\n|\\`~") {
		return false
	}
	// 被编辑行：旧区间完整包含被替换的原文。行按原文顺序排列，二分定位。
	k := -1
	lo, hi := 0, len(en.rows)
	for lo < hi {
		m := (lo + hi) / 2
		if en.rows[m].b < rel {
			lo = m + 1
		} else {
			hi = m
		}
	}
	if lo < len(en.rows) && en.rows[lo].a <= rel && rel+ch.OldLen <= en.rows[lo].b {
		k = lo
	}
	if k < 0 {
		return false
	}
	r := &en.rows[k]
	if r.fence || r.cont || k+1 < len(en.rows) && en.rows[k+1].cont {
		return false
	}
	switch top.Kind {
	case parser.Table:
		cell := e.Doc.md.Block(top.Index + r.rel)
		if cell.Kind != parser.TableCell || cell.Start != top.Start+r.a || cell.End != top.Start+r.b+d {
			return false
		}
	case parser.CodeBlock:
		if strings.Count(src[top.Start:top.End], "\n") != en.lines {
			return false
		}
		if line := strings.TrimLeft(src[top.Start+r.a:top.Start+r.b+d], " "); strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			return false
		}
	}
	// 平移：被编辑行的末端与其后各行整体移动 d；围栏行记录的块区间末端同样移动。
	r.b += d
	for m := k + 1; m < len(en.rows); m++ {
		en.rows[m].a += d
		en.rows[m].b += d
	}
	for m := range en.rows {
		if en.rows[m].fence {
			en.rows[m].blockB += d
		}
	}
	bb := &blockBuilder{d: e.Doc, top: top, src: src, seed: e.seed}
	r.hash = bb.hash(r)
	e.cacheBytes -= r.bytes
	r.proj, r.layout, r.bytes, r.objRects, r.objSizes = nil, nil, 0, nil, nil
	if fr := codeFrame(en, k); fr != nil {
		r.hlKnown = false
		fr.hlUpTo = min(fr.hlUpTo, k)
	}
	if top.Kind == parser.Table {
		e.layoutTables(src, top.Start, en.rows, en.frames, e.width()-en.rinset)
	}
	return true
}

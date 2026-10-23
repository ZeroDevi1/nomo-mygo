package mdedit

import (
	"sort"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown/parser"

	"nomo/desktop/ui/icons"
)

func (e *Editor) draw(p *ui.Painter, r ui.Rect) {
	if e.images != nil {
		e.images.frame()
		// 图片加载完成后行高可能变化：重新计算可见行（阅读锚点保持不动）。
		if e.images.gen != e.imgSeen {
			e.imgSeen, e.dirty = e.images.gen, true
		}
	}
	e.syncToc()
	busy := e.dirty
	e.prepare(r.W, r.H)
	x0 := r.X + e.left()
	// 容器装饰在文字之下。
	for _, v := range e.vblocks {
		en := &e.entries[v.i]
		for _, f := range en.frames {
			e.drawFrame(p, en, f, x0, r.Y+v.top, r)
		}
	}
	selA, selB := e.Doc.Sel.Range()
	mark, marked := e.Doc.Marked()
	head := e.Doc.Sel.Head
	for _, v := range e.vsegs {
		en := &e.entries[v.i]
		row := &en.rows[v.k]
		s := e.segOf(v.i, v.k)
		tx, ty := x0+s.x, r.Y+v.y+row.padTop
		if row.marker != "" || row.task != parser.NoTask {
			e.drawMarker(p, row, s, x0, ty)
		}
		// 分割线在光标不在其中时只画线，不显示“---”。
		if row.role == roleRule && !(head >= s.base && head <= s.end) || row.bandH == 0 {
			continue
		}
		paint := func() {
			e.paintHighlights(p, s, tx, ty)
			if selA < selB && selA <= s.end && selB >= s.base {
				e.paintLines(s, selA, selB, tx, ty, func(rc ui.Rect) { p.Fill(rc, e.pal.selection, 0) })
			}
			s.layout.paint(p, tx, ty, e.rowColor(row))
			if len(row.objRects) > 0 {
				e.drawImages(p, row, tx, ty)
			}
			if marked && mark.Anchor <= s.end && mark.Head >= s.base {
				e.paintRange(p, s, mark.Anchor, mark.Head, tx, ty, func(rc ui.Rect) {
					p.Fill(ui.Rect{X: rc.X, Y: rc.Y + rc.H - 1.5, W: rc.W, H: 1.5}, e.pal.text, 0)
				})
			}
		}
		if row.ncol > 0 {
			// 单元格裁剪到所在列与表格视口的交集，横向滚出视口的部分不画。
			if f := tableFrame(en, v.k); f != nil {
				left := x0 + row.indent
				cell := ui.Rect{X: max(left, left+row.cellX-row.shift), Y: r.Y + v.y, W: 0, H: row.bandH}
				cell.W = min(left+f.viewW, left+row.cellX-row.shift+row.cellW) - cell.X
				if cell.W > 0 {
					p.Clip(cell, 0, paint)
				}
				continue
			}
		}
		paint()
	}
	if e.focused && e.caretOK && selA == selB {
		c := e.caret
		c.X += r.X
		c.Y += r.Y
		p.Fill(c, e.pal.caret, 0)
	}
	if total, pos := e.scrollMetrics(); total > r.H {
		th := max(24, r.H*r.H/total)
		ty := (r.H - th) * min(1, pos/max(1, total-r.H))
		p.Fill(ui.Rect{X: r.X + r.W - 8, Y: r.Y + ty, W: 5, H: th}, e.pal.scrollbar, 2.5)
	}
	// 预排版只在空闲帧进行：本帧处理了输入或滚动（busy）时推迟到下一帧，不占用响应时间。
	switch {
	case busy && e.prefetchedTick != e.tick:
		p.After(prefetchDelay)
	case !busy && !e.prefetch(prefetchBudget):
		p.After(time.Millisecond) // 未完成：下一个空闲帧继续，只重绘不重建视图
	}
}

// prefetchBudget 是每帧用于预排版的时间上限。经验值：留给 120 Hz 一帧（8.3 ms）一半以上的余量。
var prefetchBudget = 4 * time.Millisecond

// prefetchDelay 是输入或滚动之后开始预排版的等待时间：连续滚动、键入期间不预排。
const prefetchDelay = 120 * time.Millisecond

// prefetch 在空闲帧中为视口之上、之下各一屏范围内的块预先排版，使大跨度滚动后的首帧不必
// 整屏排版。只处理锚点块之前与可见区之后的整块：前者高度变化不移动视口（阅读锚点在其后），
// 后者在视口之外。返回本轮是否已全部完成；窗口不可见时 MyGo 不绘制，预排版随之暂停。
func (e *Editor) prefetch(budget time.Duration) bool {
	if e.prefetchedTick == e.tick || len(e.vblocks) == 0 || e.viewH <= 0 {
		return true
	}
	start := time.Now()
	layoutBlock := func(i int) bool {
		en := e.ensure(i)
		for k := range en.rows {
			r := &en.rows[k]
			if r.layout != nil && r.gen == e.layoutGen {
				continue
			}
			e.ensureRow(i, k)
			if time.Since(start) > budget {
				return false
			}
		}
		if en.ydirty {
			e.layoutRows(en)
		}
		return true
	}
	last := e.vblocks[len(e.vblocks)-1].i
	for i, h := last+1, float32(0); i < len(e.entries) && h < e.viewH; i++ {
		if !layoutBlock(i) {
			return false
		}
		h += e.entries[i].height
	}
	for i, h := e.anchor-1, float32(0); i >= 0 && h < e.viewH; i-- {
		if !layoutBlock(i) {
			return false
		}
		h += e.entries[i].height
	}
	e.prefetchedTick = e.tick
	return true
}

// drawFrame 画跨行装饰；top 为块顶部的窗口坐标。屏幕外的装饰由 Painter 剔除。
func (e *Editor) drawFrame(p *ui.Painter, en *entry, f frame, x0, top float32, view ui.Rect) {
	first, last := &en.rows[f.first], &en.rows[f.last]
	y0, y1 := top+first.y, top+last.y+last.bandH
	if y1 < view.Y || y0 > view.Y+view.H {
		return
	}
	x := x0 + f.indent
	w := e.width() - f.indent
	switch f.kind {
	case frameQuote:
		if e.classic {
			// 经典样式：底色 + 4 px 左边框（原版 [data-block-style='classic'] blockquote）。
			box := ui.Rect{X: x, Y: y0 - quotePad, W: w, H: y1 - y0 + 2*quotePad}
			p.Fill(box, e.pal.quoteBg, 6)
			p.Fill(ui.Rect{X: x, Y: box.Y, W: 4, H: box.H}, e.pal.quoteBar, 0)
			break
		}
		if e.pal.quoteBg.A > 0 {
			p.Fill(ui.Rect{X: x, Y: y0 - quotePad, W: w, H: y1 - y0 + 2*quotePad}, e.pal.quoteBg, 12)
		}
		p.Fill(ui.Rect{X: x, Y: y0 - quotePad + 8, W: 4, H: y1 - y0 + 2*quotePad - 16}, e.pal.quoteBar, 2)
	case frameCallout:
		t := e.pal.tones[clamp(f.tone, 0, 4)]
		titleY := y0 - calloutTitleH - quotePad + 8
		if e.classic || t.bg.A > 0 {
			box := ui.Rect{X: x, Y: titleY - 8, W: w, H: y1 - titleY + quotePad}
			p.Fill(box, t.bg, map[bool]float32{true: 6, false: 12}[e.classic])
			if e.classic {
				p.Fill(ui.Rect{X: x, Y: box.Y, W: 4, H: box.H}, t.border, 0)
			}
		}
		if !e.classic {
			p.Fill(ui.Rect{X: x, Y: titleY, W: 4, H: y1 - titleY + quotePad - 8}, t.border, 2)
		}
		// 标题：类型图标 + 默认标题，对应原版 calloutTypes.ts 的 icon 与 label。
		is := 16 * e.scale
		p.Icon(icons.Callout(f.tone), ui.Rect{X: x + quoteIndent, Y: titleY + 4, W: is, H: is}, t.fg)
		p.RichText(x+quoteIndent+is+6, titleY+2, 0, ui.Span{Text: calloutTypes[clamp(f.tone, 0, 4)].label, Weight: 600, Color: t.fg, Size: bodySize * e.scale})
	case frameCode, frameFront:
		box := ui.Rect{X: x, Y: y0 - codePadY, W: w, H: y1 - y0 + 2*codePadY}
		p.Fill(box, e.pal.codeBg, 8)
		p.Stroke(box, e.pal.codeBorder, 8, 1)
		if f.lang != "" {
			lw, _ := p.MeasureText(0, ui.Span{Text: f.lang, Size: 12})
			p.RichText(box.X+box.W-lw-10, box.Y+6, 0, ui.Span{Text: f.lang, Size: 12, Color: e.pal.muted})
		}
	case frameTable:
		// 超宽表格只显示 viewW 宽的视口，按 scrollX 平移；网格与底色在视口内裁剪。
		tw := f.tableW
		vw := min(tw, f.viewW)
		clip := ui.Rect{X: x, Y: y0, W: vw, H: y1 - y0}
		p.Clip(clip, 6, func() {
			tx := x - f.scrollX
			if first.role == roleCellHead {
				p.Fill(ui.Rect{X: tx, Y: y0, W: tw, H: first.bandH}, e.pal.tableHead, 0)
			}
			// 只画可见范围内的横线：万行表格的装饰不随行数线性增长。
			for k := f.first + 1; k <= f.last; k++ {
				r := &en.rows[k]
				if r.col != 0 {
					continue
				}
				if ry := top + r.y; ry > view.Y+view.H {
					break
				} else if ry >= view.Y-1 {
					p.Fill(ui.Rect{X: tx, Y: ry, W: tw, H: 1}, e.pal.tableBorder, 0)
				}
			}
			cx := tx
			for c := 0; c+1 < len(f.cols); c++ {
				cx += f.cols[c]
				p.Fill(ui.Rect{X: cx, Y: y0, W: 1, H: y1 - y0}, e.pal.tableBorder, 0)
			}
		})
		p.Stroke(clip, e.pal.tableBorder, 6, 1)
		if tw > vw+0.5 {
			// 横向滚动条：画在表格视口底边内侧，长度与位置按可见比例。
			bw := max(24, vw*vw/tw)
			bx := x + (vw-bw)*f.scrollX/max(1, tw-vw)
			p.Fill(ui.Rect{X: bx, Y: y1 - tableScrollbarH - 2, W: bw, H: tableScrollbarH - 1}, e.pal.scrollbar, 2.5)
		}
	case frameRule:
		mid := (y0 + y1) / 2
		p.Fill(ui.Rect{X: x, Y: mid, W: w, H: 1}, e.pal.border, 0)
	}
}

// drawMarker 画列表项目符号、序号、任务复选框或脚注标签，与行的首行文字对齐。
func (e *Editor) drawMarker(p *ui.Painter, r *row, s seg, x0, ty float32) {
	first := s.layout.Caret(0)
	lineH := max(first.H, e.lineH)
	ty += first.Y // 加了行距时首行文字下移
	gx := x0 + r.indent - listIndent
	if r.task != parser.NoTask {
		box := ui.Rect{X: gx + 2, Y: ty + (lineH-taskBox)/2, W: taskBox, H: taskBox}
		if r.task == parser.Checked {
			p.Fill(box, e.pal.accentFill, 4)
			var check ui.Path
			check.MoveTo(box.X+4.5, box.Y+9.5).LineTo(box.X+7.8, box.Y+12.8).LineTo(box.X+13.5, box.Y+5.5)
			p.StrokePath(&check, 2, ui.Hex("#ffffff"))
		} else {
			p.Fill(box, e.pal.surface, 4)
			p.Stroke(box, e.pal.border, 4, 1.5)
		}
		return
	}
	sp := ui.Span{Text: r.marker, Color: e.rowColor(r), Size: bodySize * e.scale}
	w, h := p.MeasureText(0, sp)
	x := gx + (listIndent-w)/2 - 2
	if len(r.marker) > 1 && r.marker[0] != '[' {
		x = gx + listIndent - w - 6 // 有序序号右对齐到内容左侧
	}
	p.RichText(x, ty+(lineH-h)/2, 0, sp)
}

// taskHit 返回点击位置下的任务复选框所在行与“[ ]”中间字符的绝对偏移。
func (e *Editor) taskHit(pt ui.Point) (int, bool) {
	for _, v := range e.vsegs {
		r := &e.entries[v.i].rows[v.k]
		if r.task == parser.NoTask {
			continue
		}
		s := e.segOf(v.i, v.k)
		first := s.layout.Caret(0)
		lineH := max(first.H, e.lineH)
		x := e.left() + r.indent - listIndent + 2
		y := v.y + r.padTop + first.Y + (lineH-taskBox)/2
		if pt.X >= x-4 && pt.X <= x+taskBox+4 && pt.Y >= y-4 && pt.Y <= y+taskBox+4 {
			return e.Doc.blocks[v.i].Start + r.taskAt, true
		}
	}
	return 0, false
}

func (e *Editor) paintRange(p *ui.Painter, s seg, a, z int, tx, ty float32, fill func(ui.Rect)) {
	da, dz := s.toDisp(max(a, s.base)), s.toDisp(min(z, s.end))
	if da >= dz {
		return
	}
	for _, rc := range s.layout.SelectionRects(ui.TextInputRange{Start: da, End: dz}) {
		rc.X += tx
		rc.Y += ty
		fill(rc)
	}
}

// paintLines 与 paintRange 相同，但矩形覆盖整个行框（含行距），多行选区连成一片。
func (e *Editor) paintLines(s seg, a, z int, tx, ty float32, fill func(ui.Rect)) {
	da, dz := s.toDisp(max(a, s.base)), s.toDisp(min(z, s.end))
	if da >= dz {
		return
	}
	for _, rc := range s.layout.LineRects(ui.TextInputRange{Start: da, End: dz}) {
		rc.X += tx
		rc.Y += ty
		fill(rc)
	}
}

// paintHighlights 画落在行 s 内的查找结果；二分定位第一个可能相交的结果，只遍历本行。
func (e *Editor) paintHighlights(p *ui.Painter, s seg, tx, ty float32) {
	hs := e.highlights
	k := sort.Search(len(hs), func(k int) bool { return hs[k][1] >= s.base })
	for ; k < len(hs) && hs[k][0] <= s.end; k++ {
		c := e.pal.found
		if k == e.current {
			c = e.pal.foundCurrent
		}
		e.paintRange(p, s, hs[k][0], hs[k][1], tx, ty, func(rc ui.Rect) { p.Fill(rc, c, 2) })
	}
}

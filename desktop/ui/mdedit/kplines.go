package mdedit

import (
	"sort"

	"github.com/egoist/mygo/ui"
)

// kpLine 是精细排版的一个视觉行：l 为该行单独排版（不换行）的文字，覆盖显示 UTF-16 区间 [u0, u1)（行末显示的
// 连字符不在区间内）；行框位于 [y, y+h)，文字原点为 (x, ty)。相邻行首尾相接：u1 等于下一行的 u0。
type kpLine struct {
	l          *ui.TextLayout
	u0, u1     int
	x, y, h    float32
	ty         float32
	lastInPara bool
}

// kpLineAt 返回显示偏移 idx 所在的行：位于两行交界时，downstream 取后一行（行首），否则取前一行（行尾）。
func (ll *lineLayout) kpLineAt(idx int, downstream bool) int {
	n := len(ll.kp)
	if downstream {
		k := sort.Search(n, func(k int) bool { return ll.kp[k].u1 > idx })
		return min(k, n-1)
	}
	k := sort.Search(n, func(k int) bool { return ll.kp[k].u1 >= idx })
	return min(k, n-1)
}

func (k *kpLine) local(idx int) int { return min(max(idx-k.u0, 0), k.u1-k.u0) }

func (ll *lineLayout) kpCaret(idx int) ui.Rect {
	k := &ll.kp[ll.kpLineAt(idx, true)]
	r := k.l.Caret(k.local(idx))
	r.X += k.x
	r.Y += k.ty
	return r
}

// kpBoundary 返回 idx 之前（dir<0）或之后的字素边界；行内边界取该行排版，跨行时落在交界处。
func (ll *lineLayout) kpBoundary(idx, dir int) int {
	if dir > 0 {
		k := &ll.kp[ll.kpLineAt(idx, true)]
		return k.u0 + min(k.l.NextBoundary(k.local(idx)), k.u1-k.u0)
	}
	k := &ll.kp[ll.kpLineAt(idx, false)]
	return k.u0 + min(k.l.PreviousBoundary(k.local(idx)), k.u1-k.u0)
}

// kpIndexAt 按纵坐标找行（行距归属所在行），在行内命中；行末的连字符不对应原文，映射到行尾。
func (ll *lineLayout) kpIndexAt(pt ui.Point) int {
	j := sort.Search(len(ll.kp), func(j int) bool { return ll.kp[j].y+ll.kp[j].h > pt.Y })
	k := &ll.kp[min(j, len(ll.kp)-1)]
	y := min(max(pt.Y-k.ty, 0), max(0, k.h-1))
	return k.u0 + min(k.l.IndexAt(ui.Point{X: pt.X - k.x, Y: y}), k.u1-k.u0)
}

// kpRects 返回显示区间的矩形；full 为 true 时扩展到整个行框（选区底色），否则为文字行高。
func (ll *lineLayout) kpRects(r ui.TextInputRange, full bool) []ui.Rect {
	a, z := min(r.Start, r.End), max(r.Start, r.End)
	var out []ui.Rect
	for i := range ll.kp {
		k := &ll.kp[i]
		if z <= k.u0 || a >= k.u1 && !(a == z && a == k.u1 && i == len(ll.kp)-1) {
			continue
		}
		for _, rc := range k.l.SelectionRects(ui.TextInputRange{Start: k.local(a), End: k.local(z)}) {
			rc.X += k.x
			if full {
				rc.Y, rc.H = k.y, k.h
			} else {
				rc.Y += k.ty
			}
			out = append(out, rc)
		}
	}
	return out
}

func (ll *lineLayout) kpObjects() []ui.TextObject {
	var out []ui.TextObject
	for i := range ll.kp {
		k := &ll.kp[i]
		for _, o := range k.l.Objects() {
			if o.Index >= k.u1-k.u0 {
				continue
			}
			o.Index += k.u0
			o.Rect.X += k.x
			o.Rect.Y += k.ty
			out = append(out, o)
		}
	}
	return out
}

// kpPaint 绘制与可见区相交的行。
func (ll *lineLayout) kpPaint(p *ui.Painter, x, y float32, color ui.Color) {
	vis := p.Visible()
	for i := range ll.kp {
		k := &ll.kp[i]
		if y+k.y+k.h < vis.Y || y+k.y > vis.Y+vis.H {
			continue
		}
		p.TextLayout(k.l, x+k.x, y+k.ty, color)
	}
}

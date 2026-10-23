package mdedit

import (
	"math"
	"sort"

	"github.com/egoist/mygo/ui"
)

// lineLayout 是加了行距的排版结果。MyGo v0.6.2 的 TextLayout 没有行高参数（ui.Font、ui.TextShape 均无此字段），
// 这里保留原排版，把每个视觉行整体下移，使行框高度至少为 lead（对应 CSS line-height，多出的行距上下各半）；
// 位置换算按视觉行平移，绘制时逐行裁剪后平移。不需要加行距时 bands 为空，行为与原排版完全一致。
// 上游提供行高参数后可删去本类型，直接在 ShapeRichTextWith 中传入。
type lineLayout struct {
	l     *ui.TextLayout
	bands []lineBand // 原排版中的各视觉行，自上而下、首尾相接；为空表示没有加行距
	w, h  float32    // 加行距后的尺寸
}

// lineBand 是原排版中一个视觉行的纵向区间 [y, y+h)；该行文字下移 off，上下各加 half 的行距。
type lineBand struct{ y, h, off, half float32 }

// newLineLayout 按行框高度 lead（DIP，0 表示不加行距）包装 l；高于 lead 的行（如含大图）保持原高。
func newLineLayout(l *ui.TextLayout, lead float32) *lineLayout {
	w, h := l.Size()
	ll := &lineLayout{l: l, w: w, h: h}
	if lead <= 0 {
		return ll
	}
	// 各视觉行的区间取自整段选区矩形（每行给出 line.Y、line.Height，双向文字的多个片段 Y 相同）；
	// 没有矩形的行（如末尾空行）由相邻行之间的空隙补出。代价与字形数成正比，只在排版时计算一次。
	var bands []lineBand
	at := float32(0)
	for _, r := range l.SelectionRects(ui.TextInputRange{Start: 0, End: math.MaxInt32}) {
		if n := len(bands); n > 0 && r.Y < bands[n-1].y+bands[n-1].h-0.5 {
			continue
		}
		if r.Y > at+0.5 {
			bands = append(bands, lineBand{y: at, h: r.Y - at})
		}
		bands = append(bands, lineBand{y: r.Y, h: r.H})
		at = r.Y + r.H
	}
	if h > at+0.5 || len(bands) == 0 {
		bands = append(bands, lineBand{y: at, h: max(0, h-at)})
	}
	var shift float32
	for i := range bands {
		b := &bands[i]
		b.half = max(0, lead-b.h) / 2
		b.off = shift + b.half
		shift += 2 * b.half
	}
	if shift > 0 {
		ll.bands, ll.h = bands, h+shift
	}
	return ll
}

// band 返回原排版纵坐标 y 所在的视觉行。
func (ll *lineLayout) band(y float32) *lineBand {
	i := sort.Search(len(ll.bands), func(i int) bool { return ll.bands[i].y > y })
	return &ll.bands[max(0, i-1)]
}

// viewBand 返回加行距后纵坐标 y 所在的视觉行（行距归属其所在行）。
func (ll *lineLayout) viewBand(y float32) *lineBand {
	i := sort.Search(len(ll.bands), func(i int) bool {
		b := &ll.bands[i]
		return b.y+b.off+b.h+b.half > y
	})
	return &ll.bands[min(i, len(ll.bands)-1)]
}

func (ll *lineLayout) shift(r ui.Rect) ui.Rect {
	if ll.bands != nil {
		r.Y += ll.band(r.Y + r.H/2).off
	}
	return r
}

func (ll *lineLayout) Size() (float32, float32) { return ll.w, ll.h }

// Caret 返回 index 处的光标矩形，高度为文字行高（不含行距），与浏览器一致。
func (ll *lineLayout) Caret(index int) ui.Rect { return ll.shift(ll.l.Caret(index)) }

func (ll *lineLayout) PreviousBoundary(index int) int { return ll.l.PreviousBoundary(index) }
func (ll *lineLayout) NextBoundary(index int) int     { return ll.l.NextBoundary(index) }

// IndexAt 把加行距后的点映射回原排版：落在行距中的点归入该行。
func (ll *lineLayout) IndexAt(pt ui.Point) int {
	if ll.bands != nil {
		b := ll.viewBand(pt.Y)
		pt.Y = min(max(pt.Y-b.off, b.y), b.y+max(b.h-0.01, 0))
	}
	return ll.l.IndexAt(pt)
}

// SelectionRects 返回文字范围的矩形，高度为文字行高（查找高亮、输入法下划线用）。
func (ll *lineLayout) SelectionRects(r ui.TextInputRange) []ui.Rect {
	out := ll.l.SelectionRects(r)
	for i := range out {
		out[i] = ll.shift(out[i])
	}
	return out
}

// LineRects 与 SelectionRects 相同，但矩形扩展到整个行框（含行距），相邻行的选区连成一片。
func (ll *lineLayout) LineRects(r ui.TextInputRange) []ui.Rect {
	out := ll.l.SelectionRects(r)
	if ll.bands == nil {
		return out
	}
	for i := range out {
		b := ll.band(out[i].Y + out[i].H/2)
		out[i].Y += b.off - b.half
		out[i].H += 2 * b.half
	}
	return out
}

// Objects 返回行内对象的位置（已按所在行平移）。
func (ll *lineLayout) Objects() []ui.TextObject {
	out := ll.l.Objects()
	for i := range out {
		out[i].Rect = ll.shift(out[i].Rect)
	}
	return out
}

// paint 在 (x, y) 处绘制。加了行距时逐个可见视觉行裁剪到该行原有的行框后平移绘制；Painter 对裁剪区外的行
// 只做一次纵向比较即跳过，每个视觉行的绘制只提交本行字形。超出行框的字形墨迹（极少数字体的上标符号）会被裁掉。
func (ll *lineLayout) paint(p *ui.Painter, x, y float32, color ui.Color) {
	if ll.bands == nil {
		p.TextLayout(ll.l, x, y, color)
		return
	}
	vis := p.Visible()
	for i := range ll.bands {
		b := ll.bands[i]
		top := y + b.y + b.off
		if top+b.h+b.half < vis.Y || top-b.half > vis.Y+vis.H {
			continue
		}
		p.Clip(ui.Rect{X: vis.X, Y: top, W: vis.W, H: b.h}, 0, func() { p.TextLayout(ll.l, x, y+b.off, color) })
	}
}

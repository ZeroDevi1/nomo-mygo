package mdedit

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestLineLayoutLeading(t *testing.T) {
	text := strings.Repeat("行距测试 leading ", 30)
	font := ui.Font{Size: 16}
	raw := ui.ShapeRichTextWith([]ui.Span{{Text: text}}, font, ui.TextShape{Width: 200})
	if ll := newLineLayout(raw, 0); ll.bands != nil {
		t.Fatal("lead 为 0 时不应加行距")
	}
	lead := float32(16 * 1.75)
	ll := newLineLayout(raw, lead)
	_, rawH := raw.Size()
	_, h := ll.Size()
	n := len(ll.bands)
	if n < 3 {
		t.Fatalf("应排成多行，得到 %d 行", n)
	}
	if want := float32(n) * lead; h < want-0.5 || h > want+0.5 || h <= rawH {
		t.Fatalf("高度 %v，应为 %d×%v（原 %v）", h, n, lead, rawH)
	}
	// 相邻视觉行首的光标间距等于行框高度；点回光标中心得到同一位置。
	var ys []float32
	u16 := len([]rune(text)) // 全为 BMP 字符
	for i := 0; i <= u16; i = raw.NextBoundary(i) {
		c := ll.Caret(i)
		if got := ll.IndexAt(ui.Point{X: c.X + 0.1, Y: c.Y + c.H/2}); got != i && ll.Caret(got) != c {
			t.Fatalf("位置 %d 往返得到 %d", i, got)
		}
		if len(ys) == 0 || c.Y > ys[len(ys)-1]+1 {
			ys = append(ys, c.Y)
		}
		if i == u16 {
			break
		}
	}
	for k := 1; k < len(ys); k++ {
		if d := ys[k] - ys[k-1]; d < lead-0.5 || d > lead+0.5 {
			t.Fatalf("第 %d 行与上一行间距 %v，应为 %v", k, d, lead)
		}
	}
	// 行距中的点归入所在行：第二行行框顶部的点落在第二行。
	b := ll.bands[1]
	top := b.y + b.off - b.half + 0.5
	if got, want := ll.Caret(ll.IndexAt(ui.Point{X: 1, Y: top})).Y, ys[1]; got != want {
		t.Fatalf("行距内点击落在 y=%v，应为第二行 %v", got, want)
	}
	// 整行选区矩形覆盖行框，相邻行首尾相接。
	rs := ll.LineRects(ui.TextInputRange{Start: 0, End: u16})
	for k := 1; k < len(rs); k++ {
		if gap := rs[k].Y - (rs[k-1].Y + rs[k-1].H); gap > 0.5 || gap < -0.5 {
			t.Fatalf("选区第 %d 行与上一行间隙 %v", k, gap)
		}
	}
}

func TestEditorLineHeightSetting(t *testing.T) {
	src := strings.Repeat("这是一段会换行的正文，用来检查行高设置。", 20) + "\n\n```\ncode\n```\n"
	ed, tt := newTester(t, src, 600, 800)
	h175 := ed.entries[0].height
	code175 := ed.entries[1].height
	ed.SetLineHeight(1.4)
	tt.Frame()
	if h := ed.entries[0].height; h >= h175 {
		t.Fatalf("行高 1.4 时段落高度 %v 应小于 1.75 时的 %v", h, h175)
	}
	if h := ed.entries[1].height; h != code175 {
		t.Fatalf("代码块不受正文行高影响：%v → %v", code175, h)
	}
}

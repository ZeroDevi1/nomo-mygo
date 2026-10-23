package mdedit

import (
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown/parser"

	"nomo/desktop/ui/typography"
)

// 精细排版（原版 KP 排版，设置“精细排版”）：对段落、列表项与表格单元格的文字行按原版规则求解 Knuth–Plass 断行
// （desktop/ui/typography）。
//   - 测量：整段不换行排版一次，各字素边界的光标横坐标即连续前缀宽度（对应原版的连续前缀测量）。
//   - 单元：字素为盒；空白与源码软换行为可伸缩的胶（软换行两侧有汉字时为零宽，否则为一个空格）；硬换行为强制断点；
//     行内代码与行内图片为不可拆分的盒。
//   - 绘制：每个视觉行单独排版（不换行），间距调整以字距（LetterSpacing）施加在单元的末字符上，行首压缩为整行左移，
//     自动断词的行末补显示连字符。
// 不适用的内容（汉字与拉丁字母以外的文字、过长段落、居中或右对齐、组字中的行）或求解无解、超出预算时返回 nil，
// 调用方保留原生布局（与原版回退一致）。原文、选区与撤销不受影响。

const (
	// kpMaxU16 与原版单段上限一致（20,000 个 UTF-16 单元）。
	kpMaxU16 = 20_000
	// kpBudgetMs 为同步求解的时间预算。原版在 Worker 中求解，预算 100 ms；原生版在排版时同步求解，取 30 ms 以免卡顿，
	// 超出即回退原生布局。
	kpBudgetMs = 30
)

// typeset 对行 r 做精细排版，返回逐行排版结果；不适用或回退时返回 nil。spans 与 p.runs 一一对应（Editor.spans）。
func (e *Editor) typeset(top Block, r *row, p *projection, spans []ui.Span, font ui.Font, w, lead float32) *lineLayout {
	if !e.opts.Typography || r.kind != rowText || r.role == roleHeading || r.align != ui.Start ||
		p.u16 == 0 || p.u16 > kpMaxU16 || len(spans) != len(p.runs) {
		return nil
	}
	if m, ok := e.Doc.Marked(); ok && m.Head >= top.Start+r.a && m.Anchor <= top.Start+r.b {
		return nil // 组字期间保持原生布局，避免候选窗随断行跳动
	}
	text := p.text
	if !typography.SupportedText(strings.ReplaceAll(text, objChar, "")) {
		return nil
	}
	// 显示 UTF-16 偏移 → 字节偏移；各片段的 UTF-16 起点。
	ub := make([]int, 0, p.u16+1)
	for i, c := range text {
		ub = append(ub, i)
		if utf16.RuneLen(c) == 2 {
			ub = append(ub, i)
		}
	}
	ub = append(ub, len(text))
	starts := make([]int, len(spans)+1)
	for k := range spans {
		starts[k+1] = starts[k] + utf16Len(spans[k].Text)
	}
	spanAt := func(u int) int { return sort.Search(len(spans), func(k int) bool { return starts[k+1] > u }) }
	// 测量：换行替换为零宽空格（等长），整段一行排版。
	mspans := make([]ui.Span, len(spans))
	for k, sp := range spans {
		sp.Text = strings.ReplaceAll(sp.Text, "\n", "\u200b")
		mspans[k] = sp
	}
	base := ui.ShapeRichTextWith(mspans, font, ui.TextShape{})
	x := func(u int) float64 { return float64(base.Caret(u).X) }
	spaceW, hyphenW := e.kpGlyphWidths(font)
	hard := e.hardBreaks(top, r, p)
	forced := map[int]bool{}
	var units []typography.Item
	natural := map[int]float64{} // 单元 From → 测量宽度（规则可能改变 Item.Width，绘制按差值调整）
	for u := 0; u < p.u16; {
		k := spanAt(u)
		sp, st := spans[k], p.runs[k].st
		size := float64(font.Size)
		if sp.Size > 0 {
			size = float64(sp.Size)
		}
		unit := typography.Item{Kind: typography.Box, From: u, FontSize: size, Ascent: size * 0.8, Descent: size * 0.2,
			Weight: 1, HasComfort: true}
		var next int
		switch {
		case p.runs[k].atomic && sp.ObjectWidth > 0:
			next = u + 1
			unit.Text, unit.Width = "￼", float64(sp.ObjectWidth)
			unit.Ascent = max(unit.Ascent, float64(sp.ObjectHeight))
		case st&stCode != 0:
			// 行内代码整段为一个盒（原版把 code 视为原子）；占段落自然宽度 35% 以上时规则改用左对齐。
			next = starts[k+1]
			unit.Text, unit.Width, unit.Code = text[ub[u]:ub[next]], x(next)-x(u), true
		default:
			next = min(base.NextBoundary(u), starts[k+1])
			if next <= u {
				next = u + 1
			}
			g := text[ub[u]:ub[next]]
			wd := max(0, x(next)-x(u))
			unit.Text, unit.Width = g, wd
			switch {
			case g == "\n":
				unit.Kind, unit.Discardable = typography.Glue, true
				if hard[u] {
					unit.Text, unit.Width = "\n", 0
					forced[next] = true
				} else {
					unit.Text = typography.SoftBreakText(text[:ub[u]], text[ub[next]:])
					unit.Width = 0
					if unit.Text != "" {
						unit.Width = spaceW
						unit.Stretch = spaceW * 0.5
						unit.ComfortStretch = spaceW * 0.25
					}
					unit.Shrink, unit.ComfortShrink = unit.Width/3, unit.Width*0.25
				}
			case strings.Trim(g, " \t") == "":
				unit.Kind, unit.Discardable, unit.SourceWhitespace = typography.Glue, true, true
				unit.Stretch, unit.Shrink = wd*0.5, wd/3
				unit.ComfortStretch, unit.ComfortShrink = wd*0.25, wd*0.25
			case g == "­":
				unit.Kind, unit.Width, unit.Discardable, unit.BreakWidth = typography.Penalty, 0, true, hyphenW
			default:
				// 墨迹空白：原版用 canvas 测字形边界；这里按汉字标点的字面惯例近似（开标点墨迹在右半、闭标点在左半）。
				unit.HasLeading, unit.HasTrailing = true, true
				if strings.ContainsAny(g, "（［｛《〈「『【〔〖〘〚“‘") {
					unit.LeadingSpace = size * 0.5
				}
				if strings.ContainsAny(g, "）］｝》〉」』】〕〗〙〛”’，。、；：！？") {
					unit.TrailingSpace = size * 0.5
				}
				if len(g) == 1 && (g[0] >= 'A' && g[0] <= 'Z' || g[0] >= 'a' && g[0] <= 'z') {
					unit.BreakWidth = hyphenW
				}
				unit.Hyphenatable = st&(stLink|stCode) == 0
			}
		}
		unit.To = next
		natural[u] = unit.Width
		if unit.Kind == typography.Penalty {
			natural[u] = 0
		}
		units = append(units, unit)
		u = next
	}
	minLH := float64(lead)
	if minLH <= 0 {
		m := font.Metrics()
		minLH = float64(m.Ascent + m.Descent + m.LineGap)
	}
	in := typography.CreateInput(units, float64(w), minLH, typography.DefaultOptions, forced, float64(font.Size))
	in = typography.PreserveSourceWhitespace(in)
	in.TimeBudgetMs = kpBudgetMs
	res := typography.Layout(in)
	if !res.Ready || len(res.Lines) == 0 {
		return nil
	}
	return e.renderKP(in, res, units, natural, text, ub, spans, starts, font, w, base, p.u16)
}

// kpGlyphWidths 返回字体中空格与连字符的宽度（软换行投影与断词显示用），按字体缓存：超过 64 种时清空重建，换字体
// 或缩放时清空（SetStyle、SetFonts）。
func (e *Editor) kpGlyphWidths(font ui.Font) (space, hyphen float64) {
	if v, ok := e.kpWidths[font]; ok {
		return v[0], v[1]
	}
	measure := func(s string) float64 {
		w, _ := ui.ShapeRichTextWith([]ui.Span{{Text: s}}, font, ui.TextShape{}).Size()
		return float64(w)
	}
	if e.kpWidths == nil || len(e.kpWidths) > 64 {
		e.kpWidths = map[ui.Font][2]float64{}
	}
	v := [2]float64{measure(" "), measure("-")}
	e.kpWidths[font] = v
	return v[0], v[1]
}

// renderKP 按求解结果逐行排版：单元的宽度差（规则压缩）与分配到的调整加到其末字符的字距上，紧随其后的零长度胶并入
// 前一单元；行首压缩为整行左移，行末压缩与悬挂从末单元扣除。
func (e *Editor) renderKP(in typography.Input, res typography.Result, units []typography.Item, natural map[int]float64,
	text string, ub []int, spans []ui.Span, starts []int, font ui.Font, w float32, base *ui.TextLayout, total int) *lineLayout {
	spanAt := func(u int) int { return sort.Search(len(spans), func(k int) bool { return starts[k+1] > u }) }
	ll := &lineLayout{w: w}
	y := float32(0)
	ui0 := 0 // units 中当前行的起点
	for li, line := range res.Lines {
		u0 := in.Items[line.Start].From
		u1 := in.Items[line.End-1].To
		extra := map[int]float32{}
		prev := -1
		for i := line.Start; i < line.End; i++ {
			it := &in.Items[i]
			adj := line.Adjustments[i-line.Start]
			if it.From == it.To {
				if prev >= 0 {
					extra[prev] += float32(it.Width + adj)
				}
				continue
			}
			ex := it.Width - natural[it.From] + adj
			if i == line.VisibleEnd-1 {
				ex -= line.TrimEnd + line.Hang
			}
			extra[it.From] += float32(ex)
			prev = it.From
		}
		var ls []ui.Span
		push := func(sp ui.Span) {
			if n := len(ls); n > 0 && sp.LetterSpacing == 0 && ls[n-1].LetterSpacing == 0 && sp.ObjectWidth == 0 && ls[n-1].ObjectWidth == 0 {
				a, b := ls[n-1], sp
				a.Text, b.Text = "", ""
				if a == b {
					ls[n-1].Text += sp.Text
					return
				}
			}
			ls = append(ls, sp)
		}
		for ui0 < len(units) && units[ui0].From < u0 {
			ui0++
		}
		for k := ui0; k < len(units) && units[k].From < u1; k++ {
			un := &units[k]
			sp := spans[spanAt(un.From)]
			piece := text[ub[un.From]:ub[un.To]]
			if piece == "\n" {
				piece = "\u200b"
				if un.Text == " " {
					piece = " "
				}
			}
			ex := extra[un.From]
			if ex == 0 || sp.ObjectWidth > 0 {
				sp.Text = piece
				push(sp)
				continue
			}
			_, n := utf8.DecodeLastRuneInString(piece)
			if head := piece[:len(piece)-n]; head != "" {
				h := sp
				h.Text = head
				push(h)
			}
			sp.Text, sp.LetterSpacing = piece[len(piece)-n:], sp.LetterSpacing+ex
			push(sp)
		}
		if line.HyphenWidth > 0 && !line.Last && len(ls) > 0 {
			h := ls[len(ls)-1]
			h.Text, h.LetterSpacing, h.ObjectWidth, h.ObjectHeight = "-", 0, 0, 0
			ls = append(ls, h)
		}
		if len(ls) == 0 {
			ls = []ui.Span{{Text: "\u200b"}}
		}
		var l *ui.TextLayout
		if len(res.Lines) == 1 && u0 == 0 && u1 == total && line.TrimStart == 0 && line.HyphenWidth == 0 && noExtra(extra) {
			l = base // 单行且无任何间距调整：与测量排版相同，不再排一次（表格单元格等短文字的常见情形）
		} else {
			l = ui.ShapeRichTextWith(ls, font, ui.TextShape{})
		}
		_, th := l.Size()
		lh := max(float32(line.Height), th)
		ll.kp = append(ll.kp, kpLine{l: l, u0: u0, u1: u1, x: -float32(line.TrimStart), y: y, h: lh, ty: y + (lh-th)/2,
			lastInPara: li == len(res.Lines)-1})
		y += lh
	}
	ll.h = y
	return ll
}

func noExtra(m map[int]float32) bool {
	for _, v := range m {
		if v > 0.01 || v < -0.01 {
			return false
		}
	}
	return true
}

// hardBreaks 返回行 r 中硬换行（反斜杠或行尾两个空格）的换行符显示偏移；软换行不在其中。
func (e *Editor) hardBreaks(top Block, r *row, p *projection) map[int]bool {
	out := map[int]bool{}
	src := e.Doc.Source()
	start := top.Start + r.a
	for _, in := range e.Doc.md.Inlines(top.Index + r.rel) {
		if in.Kind != parser.LineBreak || in.Start < start || in.End > top.Start+r.b {
			continue
		}
		if nl := strings.IndexByte(src[in.Start:in.End], '\n'); nl >= 0 {
			out[p.toDisp(in.Start+nl-start)] = true
		}
	}
	return out
}

func utf16Len(s string) int {
	n := 0
	for _, c := range s {
		n += utf16.RuneLen(c)
	}
	return n
}

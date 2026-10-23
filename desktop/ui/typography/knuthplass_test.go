package typography

import (
	"math"
	"testing"
)

// 移植自原版 knuthPlass.test.ts。

func box(width float64, from int) Item {
	return Item{Kind: Box, From: from, To: from + 1, Text: "字", Width: width, FontSize: 10, Ascent: 8, Descent: 2}
}

func input(widths []float64, width float64) Input {
	in := Input{Width: width, MinLineHeight: 14, TimeBudgetMs: 10_000}
	for i, w := range widths {
		it := box(w, i)
		it.Stretch, it.Shrink = 7, 2
		in.Items = append(in.Items, it)
		in.Breaks = append(in.Breaks, BreakPoint{At: i + 1, Required: i == len(widths)-1})
	}
	return in
}

// exhaustive 独立于动态规划的状态合并，枚举全部划分求最小罚分。
func exhaustive(p Input) float64 {
	best := math.Inf(1)
	var visit func(start int, lines []*Line, cost float64, flagged bool)
	visit = func(start int, lines []*Line, cost float64, flagged bool) {
		for _, point := range p.Breaks {
			if point.At <= start || point.Penalty >= 10_000 && !point.Required {
				continue
			}
			if line := evaluateLine(&p, start, point, len(lines), nil, nil); line != nil {
				next := cost + math.Pow(10+line.Badness, 2)
				if !point.Required {
					next += math.Copysign(point.Penalty*point.Penalty, point.Penalty)
				}
				if len(lines) > 0 && abs(lines[len(lines)-1].Fitness-line.Fitness) > 1 {
					next += 3000
				}
				if flagged && point.Flagged {
					next += 3000
				}
				if line.Last && flagged {
					next += 1500
				}
				if point.At == len(p.Items) && len(lines) > 0 && line.NaturalWidth < line.Width*0.2 {
					next += 1000 * (1 - line.NaturalWidth/(line.Width*0.2))
				}
				if point.At == len(p.Items) {
					best = math.Min(best, next)
				} else {
					visit(point.At, append(append([]*Line{}, lines...), line), next, point.Flagged)
				}
			}
			if point.Required {
				break
			}
		}
	}
	visit(0, nil, 0, false)
	return best
}

func TestLayoutMatchesExhaustive(t *testing.T) {
	for seed := 0; seed < 100; seed++ {
		widths := make([]float64, 7)
		for i := range widths {
			widths[i] = float64(5 + (seed*11+i*3)%15)
		}
		p := input(widths, float64(26+seed%10))
		p.LineWidths = []float64{float64(22 + seed%8), 35}
		p.FirstLineIndent = float64(seed % 3)
		p.Breaks[2].Required = seed%3 == 0
		if seed%5 == 0 {
			p.Breaks[1].Penalty = 10000
		} else {
			p.Breaks[1].Penalty = -10
		}
		p.Breaks[3].Flagged, p.Breaks[3].Width = true, 3
		p.Breaks[4].Flagged = true
		want := exhaustive(p)
		got := Layout(p)
		if math.IsInf(want, 1) {
			if got.Ready {
				t.Fatalf("seed %d：穷举无解，求解器却给出结果", seed)
			}
			continue
		}
		if !got.Ready || math.Abs(got.Demerits-want) > 1e-6 {
			t.Fatalf("seed %d：罚分 %v（ready=%v %s），穷举 %v", seed, got.Demerits, got.Ready, got.Reason, want)
		}
	}
}

func TestMandatoryAndProhibitedBreaks(t *testing.T) {
	p := input([]float64{10, 10, 10, 10}, 40)
	p.Breaks[0].Penalty = 10000
	p.Breaks[1].Required = true
	r := Layout(p)
	if !r.Ready || len(r.Lines) != 2 || r.Lines[0].End != 2 || r.Lines[1].End != 4 {
		t.Fatalf("%+v", r)
	}
}

func TestFallbacks(t *testing.T) {
	p := input([]float64{20}, 10)
	p.Items[0].Shrink = 0
	if r := Layout(p); r.Ready || r.Reason != FallbackNoSolution {
		t.Fatalf("无解：%+v", r)
	}
	p.Width = 25
	p.Items[0].Stretch = 0
	if r := Layout(p); !r.Ready {
		t.Fatalf("应可排：%+v", r)
	}
	p.MaxCandidates = -1 // 原版 maxCandidates: 0（这里 0 表示默认，用 -1 表示零预算）
	if r := Layout(p); r.Ready || r.Reason != FallbackBudget {
		t.Fatalf("预算：%+v", r)
	}
	p.Width = math.NaN()
	if r := Layout(p); r.Ready || r.Reason != FallbackInvalidMetrics {
		t.Fatalf("无效度量：%+v", r)
	}
}

func TestCompressionHangingAndTallContent(t *testing.T) {
	p := input([]float64{10, 10}, 14)
	for i := range p.Items {
		p.Items[i].Shrink = 0
	}
	p.Items[1].TrimEnd, p.Items[1].Hang = 3, 3
	if l := evaluateLine(&p, 0, p.Breaks[1], 0, nil, nil); l != nil {
		t.Fatal("不悬挂时应放不下")
	}
	p.Hanging = true
	p.Items[1].Ascent, p.Items[1].Descent = 30, 8
	l := evaluateLine(&p, 0, p.Breaks[1], 0, nil, nil)
	if l == nil || l.NaturalWidth != 14 || l.Height != 38 || l.Hang != 3 {
		t.Fatalf("%+v", l)
	}
	p.Breaks[1].Width = 2
	if l := evaluateLine(&p, 0, p.Breaks[1], 0, nil, nil); l != nil {
		t.Fatal("加上连字符宽度后应放不下")
	}
}

func TestAllocationAndLastLine(t *testing.T) {
	p := input([]float64{10, 10, 10}, 25)
	l := evaluateLine(&p, 0, p.Breaks[1], 0, nil, nil)
	sum := 0.0
	for _, a := range l.Adjustments {
		sum += a
	}
	if math.Abs(sum-5) > 1e-9 {
		t.Fatalf("调整之和 %v", sum)
	}
	if l := evaluateLine(&p, 2, p.Breaks[2], 1, nil, nil); len(l.Adjustments) != 1 || l.Adjustments[0] != 0 {
		t.Fatalf("末行不应拉伸：%v", l.Adjustments)
	}
}

func project(text string) Input {
	var units []Item
	off := 0
	for _, g := range graphemes(text) {
		u := box(10, off)
		u.Text, u.To = g, off+u16len(g)
		off = u.To
		units = append(units, u)
	}
	return CreateInput(units, 100, 18, DefaultOptions, nil, 0)
}

func TestChineseEnglishProjection(t *testing.T) {
	for _, text := range []string{"hello", "a b", "á", "👩‍👩‍👧‍👦", "——", "……"} {
		if p := project(text); len(p.Breaks) != 1 {
			t.Errorf("%q 断点 %d 个，期望 1", text, len(p.Breaks))
		}
	}
	p := project("甲（乙），丙")
	for _, b := range p.Breaks {
		if end := p.Items[b.At-1].To; end == 2 || end == 3 || end == 4 {
			t.Fatalf("禁则：在 %d 之后断开", end)
		}
	}
	zero := func(p Input) int {
		n := 0
		for _, it := range p.Items {
			if it.From == it.To {
				n++
			}
		}
		return n
	}
	if zero(project("中A")) != 1 || zero(project("中 A")) != 0 {
		t.Fatal("中西文之间只在没有空格时加胶")
	}
	for _, c := range [][3]string{{"o", "w", " "}, {"文", "字", ""}, {" ", "A", ""}, {"𠀀", "word", ""}} {
		if got := SoftBreakText(c[0], c[1]); got != c[2] {
			t.Errorf("SoftBreakText(%q,%q)=%q", c[0], c[1], got)
		}
	}
	hy := false
	for _, b := range project("ab­cd").Breaks {
		hy = hy || b.Flagged && b.Width > 0
	}
	if !hy {
		t.Fatal("软连字符应形成带宽度的断点")
	}
	u1 := box(10, 0)
	u1.Text, u1.BreakWidth = "­", 7.125
	u2 := box(10, 1)
	u2.Text = "a"
	if h := CreateInput([]Item{u1, u2}, 100, 18, DefaultOptions, nil, 0); h.Breaks[0].Width != 7.125 {
		t.Fatalf("连字符宽度 %v", h.Breaks[0].Width)
	}
	if p := project("word­"); p.Breaks[len(p.Breaks)-1].Width != 0 {
		t.Fatal("段末不显示连字符")
	}
}

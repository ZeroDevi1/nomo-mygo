package typography

import (
	"math"
	"sort"
	"time"
)

// 移植自原版 knuthPlass.ts。EPSILON 为几何比较容差，FORBIDDEN 为禁止断行的罚分。
const (
	epsilon   = 0.02
	forbidden = 10_000
)

// rangeMetrics 是从某个起点累计到 end 的单元量（原版 RangeMetrics），按端点递增复用，避免重复扫描。
type rangeMetrics struct {
	end                 int
	width               float64
	stretch, shrink     float64
	ascent, descent     float64
	uniformWeight       bool
	stretchComfortRatio float64
	shrinkComfortRatio  float64
}

// allocate 按类别偏好分配 delta，每个单元的容量是硬上限（原版 allocate）。
func allocate(in *Input, start, end int, delta float64) []float64 {
	result := make([]float64, end-start)
	growing := delta >= 0
	remaining := math.Abs(delta)
	if remaining <= epsilon {
		return result
	}
	type unit struct {
		i                int
		capacity, weight float64
	}
	var active []unit
	for k := start; k < end; k++ {
		it := &in.Items[k]
		c := it.Shrink
		if growing {
			c = it.Stretch
		}
		if c > 0 {
			active = append(active, unit{k - start, c, it.weight()})
		}
	}
	for remaining > epsilon && len(active) > 0 {
		total := 0.0
		for _, u := range active {
			total += u.capacity * u.weight
		}
		consumed := 0.0
		for i := len(active) - 1; i >= 0; i-- {
			u := &active[i]
			amount := math.Min(u.capacity, remaining*u.capacity*u.weight/total)
			if growing {
				result[u.i] += amount
			} else {
				result[u.i] -= amount
			}
			u.capacity -= amount
			consumed += amount
			if u.capacity < epsilon {
				active = append(active[:i], active[i+1:]...)
			}
		}
		if consumed < epsilon {
			break
		}
		remaining -= consumed
	}
	return result
}

// evaluateLine 评估以 start 起、在 point 断开的一行（原版 evaluateLine）：边缘压缩与悬挂是拟合的一部分。不可行时
// 返回 nil。measure 为 nil 时逐单元累计（公开测试用），否则用按起点缓存的累计量。
func evaluateLine(in *Input, start int, point BreakPoint, lineIndex int, measure, measureFlex func(first, last int) *rangeMetrics) *Line {
	items := in.Items
	end := point.At
	first, last := start, end
	for first < last && items[first].Discardable {
		first++
	}
	for last > first && items[last-1].Discardable {
		last--
	}
	flexStart, flexEnd := first, last
	if in.EditableWhitespace {
		// 行首行尾的原文空格占有真实光标位置；行内词间空格保留原有伸缩容量与质量评分。
		for flexStart < flexEnd && items[flexStart].SourceWhitespace {
			flexStart++
		}
		for flexEnd > flexStart && items[flexEnd-1].SourceWhitespace {
			flexEnd--
		}
	}
	width := in.Width
	if lineIndex < len(in.LineWidths) {
		width = in.LineWidths[lineIndex]
	}
	if lineIndex == 0 {
		width -= in.FirstLineIndent
	}
	final := end == len(items) || point.Required
	var trimStart, trimEnd, hang float64
	if first < last {
		trimStart = items[first].TrimStart
		trimEnd = items[last-1].TrimEnd
		if in.Hanging {
			hang = math.Min(items[last-1].Hang, math.Max(0, items[last-1].Width-trimEnd))
		}
	}
	naturalWidth := point.Width - trimStart - trimEnd - hang
	var stretch, shrink float64
	ascent := in.MinLineHeight * 0.75
	descent := in.MinLineHeight * 0.25
	var metrics *rangeMetrics
	if measure != nil {
		if flexStart == first && flexEnd == last {
			metrics = measure(first, last)
		} else {
			metrics = measureFlex(flexStart, flexEnd)
		}
		whole := measure(first, last)
		naturalWidth += whole.width
		stretch, shrink = metrics.stretch, metrics.shrink
		ascent = math.Max(ascent, whole.ascent)
		descent = math.Max(descent, whole.descent)
	} else {
		for i := first; i < last; i++ {
			it := &items[i]
			naturalWidth += it.Width
			if i >= flexStart && i < flexEnd {
				stretch += it.Stretch
				shrink += it.Shrink
			}
			ascent = math.Max(ascent, it.Ascent)
			descent = math.Max(descent, it.Descent)
		}
	}
	delta := width - naturalWidth
	ragged := in.Alignment == AlignRagged
	ratio := 0.0
	if !((final || ragged) && delta >= -epsilon) {
		capacity := shrink
		if delta >= 0 {
			capacity = stretch
		}
		if math.Abs(delta) > epsilon && capacity <= 0 {
			return nil
		}
		if capacity > 0 {
			ratio = delta / capacity
		}
		if ratio < -1-epsilon || ratio > 1+epsilon {
			return nil
		}
	}
	if width <= 0 || naturalWidth < -epsilon {
		return nil
	}
	tolerance := in.Tolerance
	if tolerance == 0 {
		tolerance = 10_000
	}
	if !ragged && tolerance >= 100 && metrics != nil && metrics.uniformWeight {
		// 权重相同时分配与容量成正比：在分配数组之前剔除达不到同一舒适上限的间距。
		comfortRatio := metrics.shrinkComfortRatio
		if ratio >= 0 {
			comfortRatio = metrics.stretchComfortRatio
		}
		if 100*math.Pow(math.Max(math.Abs(ratio), math.Min(1, math.Abs(ratio))*comfortRatio), 3) > tolerance {
			return nil
		}
	}
	adjustmentDelta := delta
	if (final || ragged) && delta >= 0 {
		adjustmentDelta = 0
	}
	var adjustments []float64
	getAdjustments := func() []float64 {
		if adjustments != nil {
			return adjustments
		}
		adjustments = make([]float64, end-start)
		if math.Abs(adjustmentDelta) > epsilon {
			inner := allocate(in, flexStart, flexEnd, adjustmentDelta)
			for i := flexStart; i < flexEnd; i++ {
				adjustments[i-start] = inner[i-flexStart]
			}
		}
		for i := start; i < first; i++ {
			adjustments[i-start] = -items[i].Width
		}
		for i := last; i < end; i++ {
			adjustments[i-start] = -items[i].Width
		}
		return adjustments
	}
	badness := 100 * math.Pow(math.Abs(ratio), 3)
	for i := flexStart; math.Abs(adjustmentDelta) > epsilon && i < flexEnd; i++ {
		adj := getAdjustments()[i-start]
		it := &items[i]
		if !it.HasComfort || math.Abs(adj) <= epsilon {
			continue
		}
		comfort := it.ComfortShrink
		if adj >= 0 {
			comfort = it.ComfortStretch
		}
		// 少数大空隙不能藏在整段的伸缩总量之后。
		badness = math.Max(badness, 100*math.Pow(math.Abs(adj)/math.Max(epsilon, comfort), 3))
	}
	if !ragged && badness > tolerance {
		return nil
	}
	if ragged && !final && delta > 0 {
		badness += 100 * math.Pow(delta/width, 2)
	}
	height := math.Max(in.MinLineHeight, ascent+descent)
	fitness := 3
	switch {
	case ratio < -0.5:
		fitness = 0
	case ratio <= 0.5:
		fitness = 1
	case ratio <= 0.8:
		fitness = 2
	}
	if adjustments == nil && measure == nil {
		getAdjustments()
	}
	return &Line{
		Start: start, End: end, VisibleStart: first, VisibleEnd: last,
		Width: width, NaturalWidth: naturalWidth, Ratio: ratio, Badness: math.Min(10_000, badness), Fitness: fitness,
		// 内部候选的零调整数组在选定最终路径后才生成；公开评估返回完整数组。
		Adjustments: adjustments,
		TrimStart:   trimStart, TrimEnd: trimEnd, Hang: hang, HyphenWidth: point.Width,
		Ascent: ascent, Descent: descent, Height: height, Baseline: ascent, Last: final,
	}
}

type state struct {
	at, count, fitness int
	flagged            bool
	cost               float64
	previous           *state
	line               *Line
}

type stateKey struct {
	count, fitness int
	flagged        bool
}

// Layout 求解段落断行（原版 layoutParagraph）：所有路径都在强制断点处截止。AlignAuto 先求两端对齐，无解时在剩余
// 预算内求左对齐，两者共享原有的候选与时间预算。
func Layout(in Input) Result {
	if in.Alignment == AlignAuto {
		started := time.Now()
		j := in
		j.Alignment = AlignJustify
		justified := Layout(j)
		if justified.Ready || justified.Reason != FallbackNoSolution {
			return justified
		}
		budget := in.TimeBudgetMs
		if budget == 0 {
			budget = 100
		}
		maxCand := in.MaxCandidates
		if maxCand == 0 {
			maxCand = 100_000
		}
		remainingTime := budget - float64(time.Since(started).Microseconds())/1000
		remainingCand := maxCand - justified.Candidates
		if remainingTime <= 0 || remainingCand <= 0 {
			return Result{Reason: FallbackBudget, Candidates: justified.Candidates}
		}
		r := in
		r.Alignment, r.TimeBudgetMs, r.MaxCandidates = AlignRagged, remainingTime, remainingCand
		ragged := Layout(r)
		ragged.Candidates += justified.Candidates
		return ragged
	}
	if !finite(in.Width) || in.Width <= 0 || !finite(in.MinLineHeight) || in.MinLineHeight <= 0 {
		return Result{Reason: FallbackInvalidMetrics}
	}
	for i := range in.Items {
		it := &in.Items[i]
		if !finite(it.Width) || it.Width < 0 || !finite(it.Ascent+it.Descent) || !finite(it.Stretch+it.Shrink) || it.Stretch < 0 || it.Shrink < 0 {
			return Result{Reason: FallbackInvalidMetrics}
		}
	}
	n := len(in.Items)
	if n == 0 {
		return Result{Ready: true, Alignment: AlignJustify}
	}
	points := make([]BreakPoint, 0, len(in.Breaks)+1)
	for _, p := range in.Breaks {
		if p.At > 0 && p.At <= n {
			points = append(points, p)
		}
	}
	sort.SliceStable(points, func(a, b int) bool { return points[a].At < points[b].At })
	if len(points) == 0 || points[len(points)-1].At != n {
		points = append(points, BreakPoint{At: n, Required: true})
	}
	states := []*state{{fitness: 1}}
	budgetMs := in.TimeBudgetMs
	if budgetMs == 0 {
		budgetMs = 100
	}
	deadline := time.Now().Add(time.Duration(budgetMs * float64(time.Millisecond)))
	budget := in.MaxCandidates
	if budget == 0 {
		budget = 100_000
	} else if budget < 0 {
		budget = 0
	}
	// 保守下界：可行路径不会被剪掉。
	minimum := make([]float64, n+1)
	edgeAllowance := 0.0
	for i := range in.Items {
		it := &in.Items[i]
		w := 0.0
		if !it.Discardable {
			w = math.Max(0, it.Width-it.Shrink)
		}
		minimum[i+1] = minimum[i] + w
		edgeAllowance = math.Max(edgeAllowance, it.TrimStart+it.TrimEnd+it.Hang)
	}
	maxWidth := in.Width
	for _, w := range in.LineWidths {
		maxWidth = math.Max(maxWidth, w)
	}
	maxWidth += math.Abs(in.FirstLineIndent) + edgeAllowance*2
	candidates := 0
	// 端点按顺序访问：每个起点只累计一次各单元，不为每个端点与松紧状态重扫同一区间。
	newRange := func(first int) *rangeMetrics { return &rangeMetrics{end: first, uniformWeight: true} }
	ranges := map[int]*rangeMetrics{}
	flexRanges := map[int]*rangeMetrics{}
	measureIn := func(cache map[int]*rangeMetrics) func(first, last int) *rangeMetrics {
		return func(first, last int) *rangeMetrics {
			r := cache[first]
			if r == nil {
				r = newRange(first)
				cache[first] = r
			}
			for r.end < last {
				it := &in.Items[r.end]
				r.end++
				r.width += it.Width
				r.stretch += it.Stretch
				r.shrink += it.Shrink
				r.ascent = math.Max(r.ascent, it.Ascent)
				r.descent = math.Max(r.descent, it.Descent)
				r.uniformWeight = r.uniformWeight && it.weight() == 1
				if it.HasComfort {
					r.stretchComfortRatio = math.Max(r.stretchComfortRatio, it.Stretch/math.Max(epsilon, it.ComfortStretch))
					r.shrinkComfortRatio = math.Max(r.shrinkComfortRatio, it.Shrink/math.Max(epsilon, it.ComfortShrink))
				}
			}
			return r
		}
	}
	measure, measureFlex := measureIn(ranges), measureIn(flexRanges)
	for _, point := range points {
		if point.Penalty >= forbidden && !point.Required {
			continue
		}
		// 下界只增不减：越界的起点在之后的断点也不可能可行，其前驱仍可经由路径到达。
		kept := states[:0]
		for _, s := range states {
			if minimum[point.At]-minimum[s.at] <= maxWidth+epsilon {
				kept = append(kept, s)
			}
		}
		states = kept
		best := map[stateKey]*state{}
		var order []stateKey
		for _, s := range states {
			if s.at >= point.At {
				continue
			}
			candidates++
			if candidates > budget || candidates%128 == 0 && time.Now().After(deadline) {
				return Result{Reason: FallbackBudget, Candidates: candidates}
			}
			line := evaluateLine(&in, s.at, point, s.count, measure, measureFlex)
			if line == nil {
				continue
			}
			cost := s.cost + math.Pow(10+line.Badness, 2)
			if !point.Required {
				if point.Penalty >= 0 {
					cost += point.Penalty * point.Penalty
				} else {
					cost -= point.Penalty * point.Penalty
				}
			}
			if abs(s.fitness-line.Fitness) > 1 && s.count > 0 {
				cost += 3_000
			}
			if s.flagged && point.Flagged {
				cost += 3_000
			}
			if line.Last && s.flagged {
				cost += 1_500
			}
			minLast := line.Width * 0.2
			if in.HasLastLineMin {
				minLast = in.LastLineMinWidth
			}
			if point.At == n && s.count > 0 && line.NaturalWidth < minLast {
				pen := in.LastLinePenalty
				if pen == 0 {
					pen = 1_000
				}
				cost += pen * (1 - line.NaturalWidth/minLast)
			}
			next := &state{at: point.At, count: s.count + 1, fitness: line.Fitness, flagged: point.Flagged, cost: cost, previous: s, line: line}
			// 首行之后，等宽段落的后续选择与行数无关，只有可变行宽才保留行数。
			key := stateKey{1, next.fitness, next.flagged}
			if len(in.LineWidths) > 0 {
				key.count = next.count
			}
			if old, ok := best[key]; !ok || old.cost > cost {
				if !ok {
					order = append(order, key)
				}
				best[key] = next
			}
		}
		if point.Required {
			states = nil
		}
		for _, k := range order {
			states = append(states, best[k])
		}
		if point.Required && len(states) == 0 {
			return Result{Reason: FallbackNoSolution, Candidates: candidates}
		}
	}
	var final *state
	for _, s := range states {
		if s.at == n && (final == nil || s.cost < final.cost) {
			final = s
		}
	}
	if final == nil {
		return Result{Reason: FallbackNoSolution, Candidates: candidates}
	}
	var lines []Line
	for s := final; s.line != nil; s = s.previous {
		l := *s.line
		if l.Adjustments == nil {
			l.Adjustments = make([]float64, l.End-l.Start)
			for i := l.Start; i < l.VisibleStart; i++ {
				l.Adjustments[i-l.Start] = -in.Items[i].Width
			}
			for i := l.VisibleEnd; i < l.End; i++ {
				l.Adjustments[i-l.Start] = -in.Items[i].Width
			}
		}
		lines = append(lines, l)
	}
	for a, b := 0, len(lines)-1; a < b; a, b = a+1, b-1 {
		lines[a], lines[b] = lines[b], lines[a]
	}
	top := 0.0
	for i := range lines {
		lines[i].Baseline += top
		top += lines[i].Height
	}
	align := AlignJustify
	if in.Alignment == AlignRagged {
		align = AlignRagged
	}
	return Result{Ready: true, Lines: lines, Demerits: final.cost, Candidates: candidates, Alignment: align}
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

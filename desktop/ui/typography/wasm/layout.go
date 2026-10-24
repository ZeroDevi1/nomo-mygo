package main

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"nomo/desktop/ui/typography"
)

// piece 是页面测量的一个片段：普通文字为一个字素，行内代码整段一个，<br> 为 "\n" 且 br 为 true。
type piece struct {
	T    string  `json:"t"`
	W    float64 `json:"w"`
	S    float64 `json:"s"` // 字号（CSS px）
	Code bool    `json:"code,omitempty"`
	Link bool    `json:"link,omitempty"`
	BR   bool    `json:"br,omitempty"`
}

type request struct {
	Width         float64 `json:"width"`
	MinLineHeight float64 `json:"minLineHeight"`
	FontSize      float64 `json:"fontSize"`
	SpaceWidth    float64 `json:"spaceWidth"`
	HyphenWidth   float64 `json:"hyphenWidth"`
	Pieces        []piece `json:"pieces"`
}

// op 是对某个 UTF-16 区间的调整：at、n 为区间，px 为字距增量（letter-spacing），或行首压缩（负的左外边距）。
type op struct {
	At int     `json:"at"`
	N  int     `json:"n"`
	Px float64 `json:"px"`
}

type line struct {
	U0     int     `json:"u0"`
	U1     int     `json:"u1"`
	Trim   float64 `json:"trim,omitempty"`
	Hyphen bool    `json:"hyphen,omitempty"`
	Ops    []op    `json:"ops,omitempty"`
}

type response struct {
	OK    bool   `json:"ok"`
	Lines []line `json:"lines,omitempty"`
}

func u16(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// layout 按 mdedit/kp.go 的规则构造单元并排版。
func layout(req request) response {
	var text strings.Builder
	for _, p := range req.Pieces {
		text.WriteString(p.T)
	}
	if req.Width <= 0 || len(req.Pieces) == 0 || !typography.SupportedText(text.String()) {
		return response{}
	}
	var units []typography.Item
	natural := map[int]float64{}
	lastRune := map[int][2]int{} // 单元 From → 末字的 UTF-16 区间
	forced := map[int]bool{}
	u := 0
	for _, p := range req.Pieces {
		n := u16(p.T)
		size := p.S
		if size <= 0 {
			size = req.FontSize
		}
		unit := typography.Item{Kind: typography.Box, From: u, To: u + n, Text: p.T, Width: p.W, FontSize: size,
			Ascent: size * 0.8, Descent: size * 0.2, Weight: 1, HasComfort: true}
		switch {
		case p.BR:
			unit.Kind, unit.Discardable, unit.Width = typography.Glue, true, 0
			forced[u+n] = true
		case p.Code:
			unit.Code = true
		case strings.Trim(p.T, " \t") == "":
			unit.Kind, unit.Discardable, unit.SourceWhitespace = typography.Glue, true, true
			unit.Stretch, unit.Shrink = p.W*0.5, p.W/3
			unit.ComfortStretch, unit.ComfortShrink = p.W*0.25, p.W*0.25
		case p.T == "­":
			unit.Kind, unit.Width, unit.Discardable, unit.BreakWidth = typography.Penalty, 0, true, req.HyphenWidth
		default:
			unit.HasLeading, unit.HasTrailing = true, true
			if strings.ContainsAny(p.T, "（［｛《〈「『【〔〖〘〚“‘") {
				unit.LeadingSpace = size * 0.5
			}
			if strings.ContainsAny(p.T, "）］｝》〉」』】〕〗〙〛”’，。、；：！？") {
				unit.TrailingSpace = size * 0.5
			}
			if len(p.T) == 1 && (p.T[0] >= 'A' && p.T[0] <= 'Z' || p.T[0] >= 'a' && p.T[0] <= 'z') {
				unit.BreakWidth = req.HyphenWidth
			}
			unit.Hyphenatable = !p.Link
		}
		natural[u] = unit.Width
		if unit.Kind == typography.Penalty {
			natural[u] = 0
		}
		if r, size := utf8.DecodeLastRuneInString(p.T); size > 0 {
			k := utf16.RuneLen(r)
			lastRune[u] = [2]int{u + n - k, k}
		}
		units = append(units, unit)
		u += n
	}
	in := typography.CreateInput(units, req.Width, req.MinLineHeight, typography.DefaultOptions, forced, req.FontSize)
	res := typography.Layout(in)
	if !res.Ready || len(res.Lines) == 0 {
		return response{}
	}
	out := response{OK: true}
	for _, ln := range res.Lines {
		l := line{U0: in.Items[ln.Start].From, U1: in.Items[ln.End-1].To, Trim: ln.TrimStart, Hyphen: ln.HyphenWidth > 0 && !ln.Last}
		extra := map[int]float64{}
		prev := -1
		for i := ln.Start; i < ln.End; i++ {
			it := &in.Items[i]
			adj := ln.Adjustments[i-ln.Start]
			if it.From == it.To { // 零长度的胶：间距加到前一个单元
				if prev >= 0 {
					extra[prev] += it.Width + adj
				}
				continue
			}
			ex := it.Width - natural[it.From] + adj
			if i == ln.VisibleEnd-1 {
				ex -= ln.TrimEnd + ln.Hang
			}
			extra[it.From] += ex
			prev = it.From
		}
		for from, px := range extra {
			if r, ok := lastRune[from]; ok && (px > 0.01 || px < -0.01) {
				l.Ops = append(l.Ops, op{At: r[0], N: r[1], Px: px})
			}
		}
		out.Lines = append(out.Lines, l)
	}
	return out
}

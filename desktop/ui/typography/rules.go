package typography

import (
	"math"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/go-text/typesetting/segmenter"
)

// 移植自原版 rules.ts。opening/closing/stopping 为汉字标点类别：开括号与引号、闭括号/引号与句读、句读。
const (
	openingChars  = "（［｛《〈「『【〔〖〘〚“‘"
	closingChars  = "）］｝》〉」』】〕〗〙〛”’，。、；：！？"
	stoppingChars = "，。、；：！？"
)

func single(s string) (rune, bool) {
	r, n := utf8.DecodeRuneInString(s)
	return r, n > 0 && n == len(s) && r != utf8.RuneError
}

func isOpening(s string) bool { r, ok := single(s); return ok && strings.ContainsRune(openingChars, r) }
func isClosing(s string) bool { r, ok := single(s); return ok && strings.ContainsRune(closingChars, r) }
func isStopping(s string) bool {
	r, ok := single(s)
	return ok && strings.ContainsRune(stoppingChars, r)
}

// IsHan 报告文本中是否含汉字（原版 isHan）。
func IsHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func isLatin(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Latin, r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

// SoftBreakText 返回源码软换行投影的文本（原版 softBreakText）：两侧有汉字时为空，否则为一个空格。
func SoftBreakText(before, after string) string {
	before = strings.TrimRightFunc(before, func(r rune) bool {
		return unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Mc, r) || r == 0xFE0E || r == 0xFE0F
	})
	left, _ := utf8.DecodeLastRuneInString(before)
	right, _ := utf8.DecodeRuneInString(after)
	if before == "" || after == "" || unicode.IsSpace(left) || unicode.IsSpace(right) {
		return ""
	}
	if unicode.Is(unicode.Han, left) || unicode.Is(unicode.Han, right) {
		return ""
	}
	return " "
}

// SupportedText 报告文本是否只含汉字与拉丁字母（原版 supportedText）；其他文字的段落保留原生布局。
func SupportedText(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) && !unicode.Is(unicode.Han, r) && !unicode.Is(unicode.Latin, r) {
			return false
		}
	}
	return true
}

// PreserveSourceWhitespace 让可编辑视图中的原文空白在行首行尾也保留光标宽度（原版 preserveSourceWhitespace）。
func PreserveSourceWhitespace(in Input) Input {
	has := false
	for i := range in.Items {
		if in.Items[i].SourceWhitespace {
			has = true
			break
		}
	}
	if !has {
		return in
	}
	breaks := map[int]BreakPoint{}
	for _, p := range in.Breaks {
		breaks[p.At] = p
	}
	items := make([]Item, len(in.Items))
	copy(items, in.Items)
	for i := range items {
		if !items[i].SourceWhitespace {
			continue
		}
		// UAX #14 通常在整段空白之后断开；可编辑空白还需要中间断点，几百个空格才不会溢出。
		following := ""
		if i+1 < len(items) {
			following = items[i+1].Text
		}
		if _, ok := breaks[i+1]; !ok && !isClosing(following) && following != " " {
			breaks[i+1] = BreakPoint{At: i + 1, Penalty: 50}
		}
		items[i].Discardable = false
	}
	out := in
	out.Items, out.EditableWhitespace = items, true
	out.Breaks = out.Breaks[:0:0]
	for _, p := range breaks {
		out.Breaks = append(out.Breaks, p)
	}
	sortBreaks(out.Breaks)
	return out
}

func sortBreaks(b []BreakPoint) {
	for i := 1; i < len(b); i++ {
		for j := i; j > 0 && b[j-1].At > b[j].At; j-- {
			b[j-1], b[j] = b[j], b[j-1]
		}
	}
}

// lineOpportunities 返回 text 的 UAX #14 断行机会（UTF-16 偏移 → 是否强制），对应原版的 linebreak 包。
func lineOpportunities(text string) map[int]bool {
	runes := []rune(text)
	u16 := make([]int, len(runes)+1)
	for i, r := range runes {
		u16[i+1] = u16[i] + utf16.RuneLen(r)
	}
	var sg segmenter.Segmenter
	sg.Init(runes)
	it := sg.LineIterator()
	out := map[int]bool{}
	for it.Next() {
		l := it.Line()
		out[u16[l.Offset+len(l.Text)]] = l.IsMandatoryBreak
	}
	return out
}

// graphemes 把文本切成字素（UAX #29），对应原版的 Intl.Segmenter。
func graphemes(s string) []string {
	runes := []rune(s)
	var sg segmenter.Segmenter
	sg.Init(runes)
	it := sg.GraphemeIterator()
	var out []string
	for it.Next() {
		out = append(out, string(it.Grapheme().Text))
	}
	return out
}

func u16len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// CreateInput 由测量得到的单元生成段落输入（原版 createParagraphInput）：UAX #14 与英文 Liang 候选提供断行机会，
// 汉字标点规则施加禁则；在汉字之间、中西文之间插入可伸缩的零长度胶。forced 为强制断点（单元 To 偏移），
// fontSize 为段落正文字号（0 时取首个单元）。
func CreateInput(units []Item, width, minLineHeight float64, opts Options, forced map[int]bool, fontSize float64) Input {
	var text, eligible strings.Builder
	for _, u := range units {
		text.WriteString(u.Text)
		if u.Hyphenatable {
			eligible.WriteString(u.Text)
		} else {
			eligible.WriteString(strings.Repeat("￼", u16len(u.Text)))
		}
	}
	src := text.String()
	hyphens := EnglishHyphenationPoints(eligible.String(), src)
	opportunities := lineOpportunities(src)
	var items []Item
	var breaks []BreakPoint
	offset := 0
	for i := range units {
		unit := units[i]
		before, after := i-1, i+1
		for before >= 0 && units[before].Text == "" {
			before--
		}
		for after < len(units) && units[after].Text == "" {
			after++
		}
		var previous, following *Item
		if before >= 0 {
			previous = &units[before]
		}
		if after < len(units) {
			following = &units[after]
		}
		cjk := IsHan(unit.Text)
		if isOpening(unit.Text) {
			unit.TrimStart = unit.FontSize * 0.5
			if unit.HasLeading {
				unit.TrimStart = math.Min(unit.TrimStart, unit.LeadingSpace)
			}
		}
		if isClosing(unit.Text) {
			k := 0.5
			if opts.Profile == ProfileZhTW {
				k = 0.25
			}
			unit.TrimEnd = unit.FontSize * k
			if unit.HasTrailing {
				unit.TrimEnd = math.Min(unit.TrimEnd, unit.TrailingSpace)
			}
			if isStopping(unit.Text) {
				unit.Hang = unit.FontSize * 0.5
			}
		}
		// 相邻标点：缩小其间的空白，不缩放字形。
		if previous != nil && isClosing(previous.Text) && (isOpening(unit.Text) || isClosing(unit.Text)) {
			amount := math.Min(unit.FontSize, previous.FontSize) * 0.25
			if previous.HasTrailing {
				lead := 0.0
				if unit.HasLeading {
					lead = unit.LeadingSpace
				}
				amount = math.Min(amount, previous.TrailingSpace+lead)
			}
			for k := len(items) - 1; k >= 0; k-- {
				if items[k].Text != "" {
					items[k].Width = math.Max(0, items[k].Width-amount)
					items[k].TrimEnd = math.Max(0, items[k].TrimEnd-amount)
					break
				}
			}
		}
		items = append(items, unit)
		offset += u16len(unit.Text)
		forcedBreak := forced[unit.To]
		if following != nil && !forcedBreak {
			mixed := cjk && isLatin(following.Text) || isLatin(unit.Text) && IsHan(following.Text)
			betweenHan := cjk && IsHan(following.Text)
			cjkBoundary := (cjk || isClosing(unit.Text)) && (IsHan(following.Text) || isOpening(following.Text))
			if mixed || betweenHan || cjkBoundary {
				em := math.Min(unit.FontSize, following.FontSize)
				g := Item{Kind: Glue, From: unit.To, To: unit.To, Weight: 1, Discardable: true, FontSize: em, HasComfort: true,
					Stretch: em * 0.1, ComfortStretch: em * 0.05}
				if mixed {
					g.Width, g.Stretch, g.Shrink = em*0.125, em*0.0625, em*0.0625
					g.ComfortStretch, g.ComfortShrink = em*0.04, em*0.04
				}
				items = append(items, g)
			}
		}
		pair := unit.Text
		if following != nil {
			pair += following.Text
		}
		prohibited := isOpening(unit.Text) || following != nil && isClosing(following.Text) ||
			pair == "——" || pair == "……" || unit.Text == " " || following != nil && following.Text == " "
		automaticHyphen := hyphens[offset] && unit.BreakWidth > 0
		_, opportunity := opportunities[offset]
		if forcedBreak || !prohibited && (opportunity || automaticHyphen) || i == len(units)-1 {
			hyphenated := !forcedBreak && (unit.Text == "­" || automaticHyphen) && i < len(units)-1
			p := BreakPoint{At: len(items), Required: forcedBreak || i == len(units)-1}
			if hyphenated {
				p.Penalty, p.Flagged = 50, true
				p.Width = unit.BreakWidth
				if p.Width == 0 {
					p.Width = unit.FontSize * 0.35
				}
			}
			breaks = append(breaks, p)
		}
	}
	var total, code float64
	for _, u := range units {
		total += u.Width
		if u.Code {
			code += u.Width
		}
	}
	if fontSize == 0 {
		fontSize = minLineHeight / 1.75
		for _, u := range units {
			if u.FontSize > 0 {
				fontSize = u.FontSize
				break
			}
		}
	}
	align := AlignAuto
	if width/fontSize < 20 || code/math.Max(1, total) >= 0.35 {
		align = AlignRagged
	}
	return Input{
		Items: items, Breaks: breaks, Width: width, MinLineHeight: minLineHeight, Hanging: opts.Hanging,
		Alignment: align, Tolerance: 100,
		// 保护真正的孤行尾巴，不把宽段落平衡成两半。
		LastLineMinWidth: math.Min(width*0.12, fontSize*4), HasLastLineMin: true, LastLinePenalty: 500,
	}
}

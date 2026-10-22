// Package mdlint 是原生外壳的 Markdown 格式检查：实现原版所用 markdownlint 中常见的规则子集，
// 规则编号、默认参数与宽松规则集（markdownlint/style/relaxed：关闭 whitespace、line_length、
// ul-indent、no-inline-html、no-bare-urls、fenced-code-language、first-line-h1）与原版一致。
//
// 逐行扫描，识别 front matter、围栏代码块与缩进代码块后跳过其中内容（与 markdownlint 一致，
// MD010、MD013 也检查代码行）。纯函数，无状态，可在后台 goroutine 调用。
package mdlint

import (
	"cmp"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Issue 是一条检查结果；Line 从 1 起，Column 为 0 表示整行。
type Issue struct {
	Rule    string
	Line    int
	Column  int
	Detail  string
	Context string
}

// relaxedOff 是宽松规则集关闭的规则（按 markdownlint 的规则别名与标签展开）。
var relaxedOff = map[string]bool{
	// whitespace 标签
	"MD009": true, "MD010": true, "MD012": true, "MD027": true, "MD028": true, "MD030": true, "MD037": true, "MD038": true, "MD039": true,
	"MD013": true, // line_length
	"MD007": true, // ul-indent
	"MD033": true, // no-inline-html
	"MD034": true, // no-bare-urls
	"MD040": true, // fenced-code-language
	"MD041": true, // first-line-h1
}

// Limit 与原版 worker 的 RESULT_LIMIT 一致：最多返回的条数（Total 仍为全部数量）。
const Limit = 200

var (
	atxRe        = regexp.MustCompile(`^( {0,3})(#{1,6})([ \t]+|$)(.*)$`)
	atxNoSpace   = regexp.MustCompile(`^#{1,6}[^#\s]`)
	setextRe     = regexp.MustCompile(`^ {0,3}(=+|-+)[ \t]*$`)
	fenceRe      = regexp.MustCompile("^( {0,3})(`{3,}|~{3,})(.*)$")
	listRe       = regexp.MustCompile(`^( *)([*+-]|\d{1,9}[.)])( +|$)`)
	bareURLRe    = regexp.MustCompile(`(^|[\s(])(https?://[^\s<>()]+)`)
	htmlRe       = regexp.MustCompile(`<([A-Za-z][A-Za-z0-9-]*)(\s[^<>]*)?/?>`)
	emptyLinkRe  = regexp.MustCompile(`(^|[^!])\[[^\]]+\]\(\s*(#?)\s*\)`)
	noAltRe      = regexp.MustCompile(`!\[\s*\]\(`)
	codeSpanRe   = regexp.MustCompile("`+[^`]*`+")
	quoteRe      = regexp.MustCompile(`^ {0,3}>( {2,})\S`)
	tailRe       = regexp.MustCompile(`\S*$`)
	linkOnlyRe   = regexp.MustCompile(`^[\s>*+\-\d.]*(?:!?\[[^\]]*\]\([^)]*\)|<https?://[^>]+>|https?://\S+)\s*$`)
	frontTitleRe = regexp.MustCompile(`^\s*"?title"?\s*[:=]`)
	inlineRe     = regexp.MustCompile(`<!--\s*markdownlint-(disable-next-line|disable-line|disable-file|enable-file|disable|enable)((?:\s+[A-Za-z0-9-]+)*)\s*-->`)
	refDefRe     = regexp.MustCompile(`^ {0,3}\[[^\]]+\]:\s*\S+`)
)

const headingPunct = ".,;:!。，；：！"

const (
	fenceOpen = 1 + iota
	fenceClose
)

type line struct {
	text    string
	code    bool   // 代码块内容（含围栏行）
	fence   int    // fenceOpen / fenceClose，0 表示不是围栏行
	info    string // 起始围栏的信息串（语言）
	front   bool   // front matter
	heading int    // ATX 或 setext 标题层级，0 表示不是标题
	blank   bool
	list    bool
}

// Lint 检查 markdown；ruleSet 为 "relaxed" 或 "default"。返回最多 Limit 条结果与总数。
func Lint(markdown, ruleSet string) ([]Issue, int) {
	all := lint(markdown, ruleSet)
	total := len(all)
	if total > Limit {
		all = all[:Limit]
	}
	return all, total
}

func lint(markdown, ruleSet string) []Issue {
	lines := classify(markdown)
	disabled := inlineConfig(lines)
	var all []Issue
	add := func(rule string, ln, col int, detail, ctx string) {
		if ruleSet == "relaxed" && relaxedOff[rule] || disabled(ln, rule) {
			return
		}
		all = append(all, Issue{Rule: rule, Line: ln + 1, Column: col, Detail: detail, Context: ctx})
	}
	// front matter 的 title 视为一级标题（markdownlint 的 front_matter_title 默认行为）。
	h1s := 0
	for _, l := range lines {
		if l.front && frontTitleRe.MatchString(l.text) {
			h1s = 1
		}
	}
	lastLevel, firstContent := 0, -1
	seen := map[string]bool{}
	blankRun := 0
	listMarker := ""
	for i, l := range lines {
		t := l.text
		// 逐行规则（代码块内也检查的：MD010、MD013）。
		if !l.front {
			if col := strings.IndexByte(t, '\t'); col >= 0 {
				add("MD010", i, col+1, "", "")
			}
			if n := wrapLength(t); n > 80 && !linkOnlyRe.MatchString(t) && !refDefRe.MatchString(t) {
				add("MD013", i, 81, "Expected: 80; Actual: "+strconv.Itoa(utf16Len(t)), "")
			}
		}
		if l.blank {
			blankRun++
			if blankRun == 2 && !l.code {
				add("MD012", i, 0, "Expected: 1; Actual: 2", "")
			}
		} else {
			blankRun = 0
		}
		if l.code || l.front {
			// 围栏前后需空行（MD031），起始围栏需指定语言（MD040）。
			switch l.fence {
			case fenceOpen:
				if l.info == "" {
					add("MD040", i, 0, "", strings.TrimSpace(t))
				}
				if i > 0 && !lines[i-1].blank && !lines[i-1].front {
					add("MD031", i, 0, "", strings.TrimSpace(t))
				}
			case fenceClose:
				if i+1 < len(lines) && !lines[i+1].blank {
					add("MD031", i, 0, "", strings.TrimSpace(t))
				}
			}
			continue
		}
		if !l.blank && firstContent < 0 {
			firstContent = i
		}
		// 行尾空格：恰好两个空格是硬换行，允许。
		if trimmed := strings.TrimRight(t, " "); len(t)-len(trimmed) > 0 && len(t)-len(trimmed) != 2 && !l.blank {
			add("MD009", i, len(trimmed)+1, "Expected: 0 or 2; Actual: "+strconv.Itoa(len(t)-len(trimmed)), "")
		}
		if quoteRe.MatchString(t) {
			add("MD027", i, 0, "", "")
		}
		if atxNoSpace.MatchString(strings.TrimLeft(t, " ")) && !strings.HasPrefix(strings.TrimLeft(t, " "), "#!") {
			add("MD018", i, 0, "", t)
		}
		if l.heading > 0 {
			text := headingText(lines, i)
			// setext 标题的位置记在文字行（与 markdownlint 一致）。
			start := i
			if isSetext(lines, i) {
				start = i - 1
			}
			if m := atxRe.FindStringSubmatch(t); m != nil {
				if len(m[1]) > 0 {
					add("MD023", i, 1, "", t)
				}
				if len(m[3]) > 1 {
					add("MD019", i, 0, "", t)
				}
			}
			if lastLevel > 0 && l.heading > lastLevel+1 {
				add("MD001", start, 0, "Expected: h"+strconv.Itoa(lastLevel+1)+"; Actual: h"+strconv.Itoa(l.heading), "")
			}
			lastLevel = l.heading
			if l.heading == 1 {
				if h1s++; h1s > 1 {
					add("MD025", start, 0, "", text)
				}
			}
			if seen[text] {
				add("MD024", start, 0, "", text)
			}
			seen[text] = true
			if r, _ := utf8.DecodeLastRuneInString(text); text != "" && strings.ContainsRune(headingPunct, r) && !strings.HasSuffix(text, "&;") {
				add("MD026", start, 0, "Punctuation: '"+string(r)+"'", "")
			}
			// 标题前后需空行。
			ctx := strings.TrimSpace(lines[start].text)
			if start > 0 && !lines[start-1].blank && !lines[start-1].front {
				add("MD022", start, 0, "Expected: 1; Actual: 0; Above", ctx)
			}
			if i+1 < len(lines) && !lines[i+1].blank {
				add("MD022", start, 0, "Expected: 1; Actual: 0; Below", ctx)
			}
		}
		// 列表：前后需空行（MD032），无序列表标记一致（MD004）。
		if l.list {
			if i > 0 && !lines[i-1].list && !lines[i-1].blank && !lines[i-1].front && !continuation(lines, i-1) &&
				!strings.HasPrefix(strings.TrimSpace(lines[i-1].text), "<") {
				add("MD032", i, 0, "", strings.TrimSpace(t))
			}
			if m := listRe.FindStringSubmatch(t); m != nil && len(m[1]) == 0 && strings.ContainsAny(m[2], "*+-") {
				if listMarker == "" {
					listMarker = m[2]
				} else if m[2] != listMarker {
					add("MD004", i, 1, "Expected: "+markerName(listMarker)+"; Actual: "+markerName(m[2]), "")
				}
			}
		} else if i > 0 && lines[i-1].list && !l.blank && l.heading > 0 {
			add("MD032", i-1, 0, "", strings.TrimSpace(lines[i-1].text))
		}
		plain := codeSpanRe.ReplaceAllString(t, "")
		if m := bareURLRe.FindStringSubmatchIndex(plain); m != nil && !strings.Contains(plain[max(0, m[4]-2):m[4]], "](") {
			add("MD034", i, 0, "", plain[m[4]:m[5]])
		}
		for _, m := range htmlRe.FindAllStringSubmatchIndex(plain, -1) {
			add("MD033", i, utf8.RuneCountInString(plain[:m[0]])+1, "Element: "+plain[m[2]:m[3]], "")
		}
		if m := emptyLinkRe.FindString(plain); m != "" {
			add("MD042", i, 0, "", strings.TrimSpace(m))
		}
		if noAltRe.MatchString(plain) {
			add("MD045", i, 0, "", "")
		}
	}
	if firstContent >= 0 && h1s == 0 && lines[firstContent].heading != 1 && !(firstContent+1 < len(lines) && isSetext(lines, firstContent+1) && lines[firstContent+1].heading == 1) {
		add("MD041", firstContent, 0, "", strings.TrimSpace(lines[firstContent].text))
	}
	if markdown != "" && !strings.HasSuffix(markdown, "\n") {
		add("MD047", len(lines)-1, 0, "", "")
	}
	// 按行、再按规则编号排序，便于在问题列表中顺序浏览。
	slices.SortStableFunc(all, func(a, b Issue) int { return cmp.Or(cmp.Compare(a.Line, b.Line), strings.Compare(a.Rule, b.Rule)) })
	return all
}

// inlineConfig 解析 markdownlint 的行内配置注释（disable / enable / disable-file / enable-file
// 作用到后续各行，disable-line 作用于本行，disable-next-line 作用于下一行；可带规则编号列表，
// 不带时作用于全部规则），返回某行（从 0 起）某规则是否被关闭。没有配置注释时不分配内存。
func inlineConfig(lines []line) func(ln int, rule string) bool {
	// scope 是一组被关闭的规则：all 为全部规则，rules 为逐条覆盖（true 关闭、false 重新开启）。
	type scope struct {
		all   bool
		rules map[string]bool
	}
	apply := func(sc scope, names []string, off bool) scope {
		sc.rules = maps.Clone(sc.rules)
		if sc.rules == nil {
			sc.rules = map[string]bool{}
		}
		if len(names) == 0 {
			sc.all = off
			clear(sc.rules)
			return sc
		}
		for _, n := range names {
			sc.rules[n] = off
		}
		return sc
	}
	var per []scope // per[i] 为第 i 行生效的关闭范围；nil 表示文档中没有配置注释
	var cur scope
	var next []scope // 本行 disable-next-line 留给下一行的额外范围
	for i, l := range lines {
		ms := inlineRe.FindAllStringSubmatch(l.text, -1)
		if ms == nil && per == nil {
			continue
		}
		if per == nil {
			per = make([]scope, len(lines))
		}
		line := cur
		pending := next
		next = nil
		for _, m := range ms {
			names := strings.Fields(strings.ToUpper(m[2]))
			switch m[1] {
			case "disable", "disable-file":
				cur, line = apply(cur, names, true), apply(line, names, true)
			case "enable", "enable-file":
				cur, line = apply(cur, names, false), apply(line, names, false)
			case "disable-line":
				line = apply(line, names, true)
			case "disable-next-line":
				next = append(next, apply(scope{}, names, true))
			}
		}
		for _, p := range pending {
			line.all = line.all || p.all
			for n := range p.rules {
				line = apply(line, []string{n}, true)
			}
		}
		per[i] = line
	}
	if per == nil {
		return func(int, string) bool { return false }
	}
	return func(ln int, rule string) bool {
		if ln < 0 || ln >= len(per) {
			return false
		}
		if v, ok := per[ln].rules[rule]; ok {
			return v
		}
		return per[ln].all
	}
}

// classify 标出每行的代码块、front matter、标题与列表属性。
func classify(markdown string) []line {
	texts := strings.Split(strings.TrimSuffix(markdown, "\n"), "\n")
	out := make([]line, len(texts))
	fence := ""
	front := len(texts) > 0 && texts[0] == "---"
	for i, t := range texts {
		t = strings.TrimSuffix(t, "\r")
		l := line{text: t, blank: strings.TrimSpace(t) == ""}
		switch {
		case front:
			l.front = true
			if i > 0 && (t == "---" || t == "...") {
				front = false
			}
		case fence != "":
			l.code = true
			if s := strings.TrimLeft(t, " "); strings.HasPrefix(s, fence) && strings.TrimSpace(strings.TrimLeft(s, fence[:1])) == "" {
				l.fence, fence = fenceClose, ""
			}
		default:
			if m := fenceRe.FindStringSubmatch(t); m != nil && !(m[2][0] == '`' && strings.Contains(m[3], "`")) {
				l.code, l.fence, l.info, fence = true, fenceOpen, strings.TrimSpace(m[3]), m[2]
			} else if strings.HasPrefix(t, "    ") && !l.blank && i > 0 && out[i-1].blank && !out[i-1].list && !prevList(out, i) {
				l.code = true
			} else if m := atxRe.FindStringSubmatch(t); m != nil {
				l.heading = len(m[2])
			} else if setextRe.MatchString(t) && i > 0 && !out[i-1].blank && !out[i-1].code && out[i-1].heading == 0 && !out[i-1].list && !out[i-1].front {
				if strings.Contains(t, "=") {
					l.heading = 1
				} else {
					l.heading = 2
				}
			} else if listRe.MatchString(t) && !(strings.HasPrefix(strings.TrimLeft(t, " "), "---") || strings.HasPrefix(strings.TrimLeft(t, " "), "***")) {
				l.list = true
			}
		}
		out[i] = l
	}
	return out
}

// prevList 报告第 i 行之前（跨空行）最近的内容行是否属于列表（列表项内的缩进内容不是代码块）。
func prevList(lines []line, i int) bool {
	for j := i - 1; j >= 0; j-- {
		if !lines[j].blank {
			return lines[j].list || (strings.HasPrefix(lines[j].text, "  ") && prevList(lines, j))
		}
	}
	return false
}

// continuation 报告第 i 行是否为列表项的延续行（懒惰续行或缩进内容）。
func continuation(lines []line, i int) bool {
	for j := i; j >= 0 && !lines[j].blank; j-- {
		if lines[j].list {
			return true
		}
	}
	return false
}

func isSetext(lines []line, i int) bool {
	return i > 0 && setextRe.MatchString(lines[i].text) && lines[i].heading > 0
}

// headingText 返回标题文字：ATX 去掉井号与闭合井号，setext 取上一行。
func headingText(lines []line, i int) string {
	if isSetext(lines, i) {
		return strings.TrimSpace(lines[i-1].text)
	}
	m := atxRe.FindStringSubmatch(lines[i].text)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimRight(strings.TrimSpace(m[4]), "#"))
}

func markerName(m string) string {
	switch m {
	case "*":
		return "asterisk"
	case "+":
		return "plus"
	}
	return "dash"
}

// wrapLength 是 MD013 比较的长度：行尾不含空白的部分算作一个字符（无法折行的长 URL 等不报），
// 按 UTF-16 码元计数，与 markdownlint 的 JS 字符串长度一致。
func wrapLength(t string) int { return utf16Len(tailRe.ReplaceAllString(t, "#")) }

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

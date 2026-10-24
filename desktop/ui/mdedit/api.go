package mdedit

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown/parser"
)

// 本文件是外壳（desktop/ui/shell）使用的公开接口：格式命令、查找高亮、大纲、统计与
// 源码模式同步。所有修改都经 Document.Replace，作为一步撤销。

// Heading 是大纲中的一个标题。
type Heading struct {
	Level int
	Text  string
	Pos   int // 标题内容起点的字节偏移
}

// Headings 返回顶层标题（含 Setext 标题），按文档顺序。每次调用重新收集，
// 只解析标题块的行内节点；调用方按 Version 缓存。
func (d *Document) Headings() []Heading {
	if d.headings != nil && d.headingsVer == d.version {
		return d.headings
	}
	out := []Heading{}
	for _, b := range d.blocks {
		if b.Kind != parser.Heading {
			continue
		}
		pb := d.md.Block(b.Index)
		ins := d.md.Inlines(b.Index)
		pos := b.Start
		if len(ins) > 0 {
			pos = ins[0].Start
		}
		out = append(out, Heading{Level: pb.Level, Text: strings.TrimSpace(parser.PlainText(ins)), Pos: pos})
	}
	d.headings, d.headingsVer = out, d.version
	return out
}

// Stats 是写作统计：Chars 为非空白字符数，Words 为英文单词与汉字数之和，Lines 为行数。
type Stats struct {
	Chars, Words, Lines int
}

// Stats 返回原文的写作统计：首次调用时整篇统计（O(n)），之后由每次编辑增量维护（见 statsEdit）。
func (d *Document) Stats() Stats {
	if !d.statsOK {
		d.stats, d.statsOK = TextStats(d.Source()), true
	}
	return d.stats
}

// statsEdit 在原文 [from, to) 被替换为 text 之后增量更新统计。统计按字符顺序扫描，只有字母与数字会把“在词中”
// 状态延续到下一个字符，其余字符（空白、换行、标点、汉字与假名）都会把它重置；因此只需重算编辑点向前、向后各到
// 最近一个重置字符为止的区间，区间外的贡献不变。成本与该区间长度成正比（通常是一个词）。
func (d *Document) statsEdit(oldSrc string, from, to int, text string) {
	if !d.statsOK {
		return
	}
	a := from
	for a > 0 {
		r, n := utf8.DecodeLastRuneInString(oldSrc[:a])
		if resetsWord(r) {
			break
		}
		a -= n
	}
	z := to
	for z < len(oldSrc) {
		r, n := utf8.DecodeRuneInString(oldSrc[z:])
		z += n
		if resetsWord(r) {
			break
		}
	}
	src := d.Source()
	old, cur := TextStats(oldSrc[a:z]), TextStats(src[a:z+len(text)-(to-from)])
	d.stats.Chars += cur.Chars - old.Chars
	d.stats.Words += cur.Words - old.Words
	d.stats.Lines += cur.Lines - old.Lines
}

// resetsWord 报告字符 r 之后是否不在词中（与 TextStats 的规则一致）。
func resetsWord(r rune) bool {
	return !(unicode.IsLetter(r) || unicode.IsDigit(r)) ||
		unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r)
}

// TextStats 按与 Document.Stats 相同的规则统计任意文本（TXT/JSON 标签用）。
func TextStats(src string) Stats {
	s := Stats{Lines: 1}
	inWord := false
	for i := 0; i < len(src); {
		// ASCII 快速路径：与下方通用分支的规则相同，免去逐字符的 Unicode 表查询。
		if c := src[i]; c < utf8.RuneSelf {
			i++
			switch {
			case c == '\n':
				s.Lines++
				inWord = false
			case c == ' ' || c >= '\t' && c <= '\r':
				inWord = false
			case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9':
				s.Chars++
				if !inWord {
					s.Words++
				}
				inWord = true
			default:
				s.Chars++
				inWord = false
			}
			continue
		}
		r, n := utf8.DecodeRuneInString(src[i:])
		i += n
		switch {
		case r == '\n':
			s.Lines++
			inWord = false
		case unicode.IsSpace(r):
			inWord = false
		case unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r):
			s.Chars++
			s.Words++
			inWord = false
		default:
			s.Chars++
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				if !inWord {
					s.Words++
				}
				inWord = true
			} else {
				inWord = false
			}
		}
	}
	return s
}

// LineCol 返回光标所在的行与列（从 1 起，列按 rune 计）。
func (d *Document) LineCol() (int, int) {
	src := d.Source()
	h := clamp(d.Sel.Head, 0, len(src))
	line := strings.Count(src[:h], "\n") + 1
	return line, utf8.RuneCountInString(src[lineStart(src, h):h]) + 1
}

// SetSource 把原文整体换成 text，只替换与当前原文不同的最小区间，作为一步撤销。
// 用于源码模式编辑后同步回语义视图；返回是否有变化。
func (e *Editor) SetSource(text string) bool {
	src := e.Doc.Source()
	if src == text {
		return false
	}
	p := 0
	for p < len(src) && p < len(text) && src[p] == text[p] {
		p++
	}
	p = runeStartBefore(src, p)
	s := 0
	for s < len(src)-p && s < len(text)-p && src[len(src)-1-s] == text[len(text)-1-s] {
		s++
	}
	for s > 0 && !utf8.RuneStart(src[len(src)-s]) {
		s--
	}
	sel := e.Doc.Sel
	e.Doc.hist.seal()
	e.applyEdit(e.Doc.Replace(p, len(src)-s, text[p:len(text)-s], editOther))
	e.Doc.hist.seal()
	n := len(e.Doc.Source())
	e.Doc.Sel = Selection{Anchor: clamp(sel.Anchor, 0, n), Head: clamp(sel.Head, 0, n)}
	return true
}

// Select 选中 [a, z) 并滚动到可见处（查找跳转、大纲跳转）。
func (e *Editor) Select(a, z int) {
	e.Doc.SetSelection(Selection{Anchor: a, Head: z})
	e.reveal, e.dirty = true, true
}

// JumpTo 把光标放到 pos，并把所在块滚到视口上部（大纲跳转，与原版一致），
// 而不是 Select 那样只滚到刚好可见。
func (e *Editor) JumpTo(pos int) {
	e.Doc.SetSelection(Selection{Anchor: pos, Head: pos})
	i, after := e.Doc.BlockAt(pos)
	if i < 0 {
		i = max(0, after)
	}
	if i < len(e.entries) {
		e.anchor, e.anchorOff = i, -24
	}
	e.reveal, e.dirty = false, true
}

// SetHighlights 设置查找结果高亮（字节区间），current 为当前项下标，-1 表示无。
func (e *Editor) SetHighlights(ranges [][2]int, current int) {
	e.highlights, e.current = ranges, current
	e.dirty = true
}

// Find 返回 query 在原文中的全部匹配（字节区间）。matchCase 为 false 时按 Unicode
// 简单大小写折叠比较；wholeWord 要求两侧不是字母数字。
func (d *Document) Find(query string, matchCase, wholeWord bool) [][2]int {
	if query == "" {
		return nil
	}
	src := d.Source()
	hay, needle := src, query
	if !matchCase {
		hay, needle = foldASCII(src), foldASCII(query)
	}
	var out [][2]int
	for from := 0; from <= len(hay)-len(needle); {
		k := strings.Index(hay[from:], needle)
		if k < 0 {
			break
		}
		a := from + k
		z := a + len(needle)
		if !wholeWord || (wordBoundary(src, a, -1) && wordBoundary(src, z, 1)) {
			out = append(out, [2]int{a, z})
		}
		from = z
	}
	return out
}

// foldASCII 只折叠 ASCII 大小写，保持字节长度不变，偏移可直接用于原文。
func foldASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func wordBoundary(src string, pos, dir int) bool {
	var r rune
	if dir < 0 {
		if pos == 0 {
			return true
		}
		r, _ = utf8.DecodeLastRuneInString(src[:pos])
	} else {
		if pos >= len(src) {
			return true
		}
		r, _ = utf8.DecodeRuneInString(src[pos:])
	}
	return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
}

// ReplaceRanges 把 ranges（升序、不重叠）都替换为 text，作为一步撤销。
func (e *Editor) ReplaceRanges(ranges [][2]int, text string) {
	if len(ranges) == 0 {
		return
	}
	src := e.Doc.Source()
	a, z := ranges[0][0], ranges[len(ranges)-1][1]
	var b strings.Builder
	pos := a
	for _, r := range ranges {
		b.WriteString(src[pos:r[0]])
		b.WriteString(text)
		pos = r[1]
	}
	b.WriteString(src[pos:z])
	e.Doc.hist.seal()
	e.applyEdit(e.Doc.Replace(a, z, b.String(), editOther))
	e.Doc.hist.seal()
}

// Run 执行一个编辑命令，返回是否认得该命令。命令名与原版工具栏、菜单一致。
func (e *Editor) Run(name string) bool {
	switch name {
	case "bold", "italic", "strikethrough", "copy", "cut", "paste", "selectAll", "undo", "redo", "delete":
		return e.command(name)
	case "inlineCode":
		e.toggleWrap("`")
	case "highlight":
		e.toggleWrapPair("<mark>", "</mark>")
	case "underline":
		e.toggleWrapPair("<u>", "</u>")
	case "superscript":
		e.toggleWrapPair("<sup>", "</sup>")
	case "subscript":
		e.toggleWrapPair("<sub>", "</sub>")
	case "inlineComment":
		e.insertInlineComment()
	case "mathInline":
		e.insertInlineMath()
	case "footnote":
		e.insertFootnote()
	case "toc":
		e.insertToc()
	case "link":
		e.insertLink()
	case "paragraph":
		e.setLinePrefix(func(int) string { return "" }, false)
	case "heading1", "heading2", "heading3", "heading4", "heading5", "heading6":
		mark := strings.Repeat("#", int(name[len(name)-1]-'0')) + " "
		e.setLinePrefix(func(int) string { return mark }, true)
	case "unorderedList":
		e.setLinePrefix(func(int) string { return "- " }, true)
	case "orderedList":
		e.setLinePrefix(func(i int) string { return itoa(i+1) + ". " }, true)
	case "taskList":
		e.setLinePrefix(func(int) string { return "- [ ] " }, true)
	case "quote":
		e.toggleQuote()
	case "codeBlock":
		e.insertCodeBlock()
	case "mermaid":
		e.insertDiagram()
	case "comment":
		e.insertCommentBlock()
	case "mathBlock":
		e.wrapLines("$$\n", "\n$$")
	case "pageBreak":
		// 手动分页符：PDF 导出时换成分页（shell/export.go 的 pdfLayoutHTML），编辑区显示为注释卡片。
		e.insertBlock("<!-- pagebreak -->")
	case "callout":
		e.insertBlock("> [!NOTE]\n> ")
	case "clearFormat":
		e.clearFormat()
	case "horizontalRule":
		e.insertBlock("---")
	case "table":
		e.InsertTable(3, 3)
	case "details":
		e.InsertDetails()
	default:
		return false
	}
	e.dirty = true
	return true
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// toggleWrapPair 用不对称的开闭标记（如 <mark></mark>）包裹或解除包裹选区。
func (e *Editor) toggleWrapPair(open, close string) {
	src := e.Doc.Source()
	a, z := e.Doc.Sel.Range()
	if a >= len(open) && z+len(close) <= len(src) && src[a-len(open):a] == open && src[z:z+len(close)] == close {
		inner := cloneString(src[a:z])
		e.applyEdit(e.Doc.Replace(a-len(open), z+len(close), inner, editOther))
		e.Doc.Sel = Selection{Anchor: a - len(open), Head: z - len(open)}
	} else {
		e.applyEdit(e.Doc.Replace(a, z, open+src[a:z]+close, editOther))
		e.Doc.Sel = Selection{Anchor: a + len(open), Head: z + len(open)}
	}
	e.Doc.hist.seal()
}

// insertLink 把选区变成 [选区]()，光标放进括号等待输入地址。
func (e *Editor) insertLink() {
	src := e.Doc.Source()
	a, z := e.Doc.Sel.Range()
	text := "[" + src[a:z] + "]()"
	e.applyEdit(e.Doc.Replace(a, z, text, editOther))
	p := a + len(text) - 1
	e.Doc.Sel = Selection{Anchor: p, Head: p}
	e.Doc.hist.seal()
}

// selectedLines 返回选区覆盖的整行区间 [行首, 行尾)。
func (e *Editor) selectedLines() (int, int) {
	src := e.Doc.Source()
	a, z := e.Doc.Sel.Range()
	start := lineStart(src, a)
	end := strings.IndexByte(src[z:], '\n')
	if end < 0 {
		end = len(src)
	} else {
		end += z
	}
	return start, end
}

// blockPrefixLen 返回行首块级标记（标题 #、列表、任务框、引用 >）的长度。
func blockPrefixLen(line string) int {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n > 0 && n <= 6 && n < len(line) && line[n] == ' ' {
		return n + 1
	}
	if m := parsePrefix(listMarkerOf(line)); m.marker != "" || strings.HasPrefix(line, "> ") {
		return len(listMarkerOf(line))
	}
	return 0
}

// listMarkerOf 截取行首可能的列表/引用标记部分（不含正文）。
func listMarkerOf(line string) string {
	k := 0
	for k < len(line) && (line[k] == ' ' || line[k] == '>') {
		k++
	}
	j := k
	for j < len(line) && (line[j] >= '0' && line[j] <= '9') {
		j++
	}
	switch {
	case j < len(line) && j > k && (line[j] == '.' || line[j] == ')') && j+1 < len(line) && line[j+1] == ' ':
		j += 2
	case k < len(line) && (line[k] == '-' || line[k] == '*' || line[k] == '+') && k+1 < len(line) && line[k+1] == ' ':
		j = k + 2
	default:
		return line[:k]
	}
	for _, t := range []string{"[ ] ", "[x] ", "[X] "} {
		if strings.HasPrefix(line[j:], t) {
			j += len(t)
		}
	}
	return line[:j]
}

// setLinePrefix 把选区各行的块级标记换成 prefix(行序号)；toggle 为 true 且各行已是该标记时
// 去掉标记（再次点击同一按钮取消）。作为一步撤销，选区随内容平移。
func (e *Editor) setLinePrefix(prefix func(i int) string, toggle bool) {
	src := e.Doc.Source()
	start, end := e.selectedLines()
	lines := strings.Split(src[start:end], "\n")
	same := toggle
	for i, ln := range lines {
		if ln[:blockPrefixLen(ln)] != prefix(i) {
			same = false
		}
	}
	var b strings.Builder
	for i, ln := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		body := ln[blockPrefixLen(ln):]
		if !same {
			b.WriteString(prefix(i))
		}
		b.WriteString(body)
	}
	text := b.String()
	e.applyEdit(e.Doc.Replace(start, end, text, editOther))
	e.Doc.Sel = Selection{Anchor: start + len(text), Head: start + len(text)}
	e.Doc.hist.seal()
}

// toggleQuote 给选区各行加上或去掉“> ”。
func (e *Editor) toggleQuote() {
	src := e.Doc.Source()
	start, end := e.selectedLines()
	lines := strings.Split(src[start:end], "\n")
	all := true
	for _, ln := range lines {
		if !strings.HasPrefix(ln, ">") {
			all = false
		}
	}
	for i, ln := range lines {
		if all {
			lines[i] = strings.TrimPrefix(strings.TrimPrefix(ln, ">"), " ")
		} else {
			lines[i] = "> " + ln
		}
	}
	text := strings.Join(lines, "\n")
	e.applyEdit(e.Doc.Replace(start, end, text, editOther))
	e.Doc.Sel = Selection{Anchor: start + len(text), Head: start + len(text)}
	e.Doc.hist.seal()
}

// wrapLines 用 open/close 包裹选区所在的整行（代码块）；光标放在内容末尾。
func (e *Editor) wrapLines(open, close string) {
	src := e.Doc.Source()
	start, end := e.selectedLines()
	body := src[start:end]
	text := open + body + close
	e.applyEdit(e.Doc.Replace(start, end, text, editOther))
	p := start + len(open) + len(body)
	e.Doc.Sel = Selection{Anchor: p, Head: p}
	e.Doc.hist.seal()
}

// insertBlock 在光标所在块之后插入一个独立块（分割线、表格），前后留空行。
func (e *Editor) insertBlock(block string) {
	src := e.Doc.Source()
	pos := e.Doc.Sel.Head
	at := pos
	if i, _ := e.Doc.BlockAt(pos); i >= 0 {
		at = e.Doc.blocks[i].End
	}
	text := "\n\n" + block + "\n"
	if at >= len(src) || src[at] != '\n' {
		text += "\n"
	}
	e.applyEdit(e.Doc.Replace(at, at, text, editOther))
	p := at + 2 + len(block)
	e.Doc.Sel = Selection{Anchor: p, Head: p}
	e.Doc.hist.seal()
}

// inlineMarkers 是“清除样式”去掉的行内标记（与原版只移除行内表现、不改块级结构一致）。
var inlineMarkers = []string{"**", "__", "~~", "==", "`", "<u>", "</u>", "<ins>", "</ins>", "<mark>", "</mark>", "<sup>", "</sup>", "<sub>", "</sub>",
	"<strong>", "</strong>", "<b>", "</b>", "<em>", "</em>", "<i>", "</i>", "<s>", "</s>", "<del>", "</del>", "*", "_"}

// clearFormat 去掉选区内的行内样式标记，保留文字与块级结构；无选区时不做任何事。
func (e *Editor) clearFormat() {
	a, z := e.Doc.Sel.Range()
	if a == z {
		return
	}
	src := e.Doc.Source()
	// 选区两侧紧贴的标记一并去掉，否则清除 **粗** 中的“粗”会留下孤立的 **。
	for _, m := range inlineMarkers {
		for a >= len(m) && z+len(m) <= len(src) && src[a-len(m):a] == m && src[z:z+len(m)] == m {
			a, z = a-len(m), z+len(m)
		}
	}
	text := src[a:z]
	for _, m := range inlineMarkers {
		text = strings.ReplaceAll(text, m, "")
	}
	if text == src[a:z] {
		return
	}
	e.applyEdit(e.Doc.Replace(a, z, text, editOther))
	e.Doc.Sel = Selection{Anchor: a, Head: a + len(text)}
	e.Doc.hist.seal()
}

// CaretRect 返回光标矩形（编辑区元素坐标，上一次准备布局的结果），不在可见区时返回 false。
// 外壳据此放置快捷插入菜单等浮层。
func (e *Editor) CaretRect() (ui.Rect, bool) {
	e.sync()
	return e.caret, e.caretOK
}

// InsertText 以一次可撤销的编辑替换当前选区为 text。
func (e *Editor) InsertText(text string) { e.replaceSel(text, editOther); e.Doc.hist.seal() }

// Delete 删除原文 [a, z)，作为一步撤销（快捷插入删除触发符等）。
func (e *Editor) Delete(a, z int) {
	e.applyEdit(e.Doc.Replace(a, z, "", editOther))
	e.Doc.hist.seal()
}

// Replace 以一次独立的撤销步把原文 [a, z) 替换为 text，光标置于其后（插入表情等）；前后都结束可合并的
// 撤销步，不与之前或之后的键入合并。
func (e *Editor) Replace(a, z int, text string) {
	e.Doc.hist.seal()
	e.applyEdit(e.Doc.Replace(a, z, text, editOther))
	e.Doc.hist.seal()
}

// CaretContext 返回光标所在行在光标之前的原文（至多 limit 字节，不跨出该行），以及光标是否位于不应自动
// 补全的字面内容中：代码块、公式块、HTML 块等原样行，或行内代码、行内公式、链接、图片、行内 HTML 之内。
// 有选区、正在组字或光标不在任何行内时 ok 为 false。
func (e *Editor) CaretContext(limit int) (before string, literal, ok bool) {
	head := e.Doc.Sel.Head
	if !e.Doc.Sel.Empty() {
		return "", false, false
	}
	if _, marked := e.Doc.Marked(); marked {
		return "", false, false
	}
	s, found := e.segAt(head)
	if !found {
		return "", false, false
	}
	r := &e.entries[s.i].rows[s.k]
	// 空表格单元格的段起点可能在光标之后（“|  |”中光标在两个空格之间），此时光标前没有本段文字。
	before = e.Doc.Source()[min(max(s.base, head-limit), head):head]
	if r.kind != rowText {
		return before, true, true
	}
	top := e.Doc.blocks[s.i]
	for _, in := range e.Doc.md.Inlines(top.Index + r.rel) {
		switch in.Kind {
		case parser.Code, parser.Math, parser.Link, parser.Image, parser.HTML:
			if in.Start < head && head < in.End {
				return before, true, true
			}
		}
	}
	return before, false, true
}

// LiteralAt 报告原文偏移 pos 是否位于不应自动补全的字面内容中：代码块、公式块、HTML 块、front matter、
// 目录区，或行内代码、公式、链接、图片、行内 HTML 之内（源码模式的表情建议用，对应原版 SourceEmojiContext）。
// 只读取块索引与解析结果，不需要视图。
func (d *Document) LiteralAt(pos int) bool {
	i, _ := d.BlockAt(pos)
	if i < 0 {
		return false
	}
	top := d.blocks[i]
	switch top.Kind {
	case KindFrontMatter, KindToc, parser.CodeBlock, parser.HTMLBlock, parser.MathBlock:
		return true
	}
	// 自顶层块向下找包含 pos 的最深的块，按其类型与行内节点判断。
	b := top.Index
	for {
		next := -1
		for c := range d.md.Children(b) {
			cb := d.md.Block(c)
			if cb.Start <= pos && pos <= cb.End {
				next = c
				break
			}
		}
		if next < 0 {
			break
		}
		b = next
	}
	switch d.md.Block(b).Kind {
	case parser.CodeBlock, parser.HTMLBlock, parser.MathBlock:
		return true
	}
	for _, in := range d.md.Inlines(b) {
		switch in.Kind {
		case parser.Code, parser.Math, parser.Link, parser.Image, parser.HTML:
			if in.Start < pos && pos < in.End {
				return true
			}
		}
	}
	return false
}

// TopSourceOffset 返回视口顶部那一行的原文起点（双栏模式同步滚动用）；文档为空时返回 0。
func (e *Editor) TopSourceOffset() int {
	e.sync()
	for _, v := range e.vsegs {
		r := &e.entries[v.i].rows[v.k]
		if v.y+r.bandH > 0 {
			return e.Doc.blocks[v.i].Start + r.a
		}
	}
	return 0
}

// ScrollToSource 滚动视图，使包含原文偏移 off 的行位于视口顶部；不移动光标（双栏模式同步滚动用）。
func (e *Editor) ScrollToSource(off int) {
	if len(e.entries) == 0 {
		return
	}
	i, after := e.Doc.BlockAt(off)
	if i < 0 {
		i = clamp(after+1, 0, len(e.entries)-1)
	}
	en := e.ensure(i)
	y := float32(0)
	rel := off - e.Doc.blocks[i].Start
	for k := range en.rows {
		if en.rows[k].a <= rel {
			y = en.rows[k].y
		}
	}
	e.anchor, e.anchorOff = i, y
	e.normalize()
	e.dirty = true
}

// Focused 报告编辑区是否持有键盘焦点（上一帧的状态）。
func (e *Editor) Focused() bool { return e.focused }

// ContextTarget 描述最近一次右键位置的目标，外壳据此组织编辑区右键菜单（对应原版 contextMenu.ts 的
// ContextMenuTarget）。Kind 为 "selection"（右键落在非空选区内）、"details"（折叠块标题行）、"table"、"code"、
// "math"（公式块）、"mermaid"、"image"、"link"、"heading" 或 "text"；Href 为链接或图片地址，Level 为标题级别，Lang 为代码块语言，
// DetailsKey、DetailsOpen 为折叠块的键与默认展开，Pos 为右键位置的原文偏移（块命令据此定位）。
type ContextTarget struct {
	Kind        string
	Href        string
	Lang        string
	Pos         int
	Level       int
	DetailsKey  int
	DetailsOpen bool
}

// ContextTarget 返回最近一次右键位置的目标；应在右键菜单打开时调用。
func (e *Editor) ContextTarget() ContextTarget {
	d := e.Doc
	pos := clamp(e.ctxPos, 0, len(d.Source()))
	if a, z := d.Sel.Range(); a < z && pos >= a && pos <= z {
		return ContextTarget{Kind: "selection"}
	}
	if e.ctxDetails >= 0 {
		if f, ok := e.detailsFrameByKey(e.ctxDetails); ok {
			return ContextTarget{Kind: "details", DetailsKey: f.key, DetailsOpen: f.defaultOpen}
		}
	}
	i, _ := d.BlockAt(pos)
	if i < 0 {
		return ContextTarget{Kind: "text"}
	}
	b := d.blocks[i]
	switch b.Kind {
	case parser.Table:
		return ContextTarget{Kind: "table", Pos: pos}
	case parser.MathBlock:
		return ContextTarget{Kind: "math", Pos: pos}
	case parser.CodeBlock:
		blk := d.md.Block(b.Index)
		lang := blk.Language()
		if strings.EqualFold(lang, "mermaid") {
			return ContextTarget{Kind: "mermaid", Pos: pos}
		}
		return ContextTarget{Kind: "code", Lang: lang, Pos: pos}
	}
	if b.Kind == parser.Heading {
		return ContextTarget{Kind: "heading", Level: d.md.Block(b.Index).Level}
	}
	if ref, ok := e.ImageAt(pos); ok {
		return ContextTarget{Kind: "image", Href: ref.Src, Pos: pos}
	}
	if ins, k, ok := e.linkInline(pos); ok {
		return ContextTarget{Kind: "link", Href: ins[k].Dest, Pos: pos}
	}
	return ContextTarget{Kind: "text"}
}

// Link 是原文中的一个行内链接：[Start, End) 为整段原文，Label 为方括号内的原文（含行内标记），Dest、Title 为
// 地址与标题。
type Link struct {
	Start, End  int
	Label       string
	Dest, Title string
}

// LinkAt 返回包含原文位置 pos 的链接（含两端）；自动链接（<https://…>、裸网址）没有可编辑的文字，不算。
// pos 须在可见区附近已布局的行内（编辑区按光标布局，光标与右键位置总满足）。
func (e *Editor) LinkAt(pos int) (Link, bool) {
	ins, k, ok := e.linkInline(pos)
	if !ok || ins[k].Auto {
		return Link{}, false
	}
	in, src := ins[k], e.Doc.Source()
	// 方括号内的文字：子节点（k+1 到 Next）覆盖的范围；没有子节点时为空，位于“[”之后。
	la, lz := in.Start+1, in.Start+1
	for j := k + 1; j < in.Next && j < len(ins); j++ {
		lz = max(lz, ins[j].End)
	}
	if lz > in.End || lz > len(src) {
		return Link{}, false
	}
	return Link{Start: in.Start, End: in.End, Label: src[la:lz], Dest: in.Dest, Title: in.Title}, true
}

// linkInline 返回 pos 所在行的行内节点与包含 pos 的链接节点下标。
func (e *Editor) linkInline(pos int) ([]parser.Inline, int, bool) {
	d := e.Doc
	i, _ := d.BlockAt(pos)
	if i < 0 {
		return nil, 0, false
	}
	s, ok := e.segAt(pos)
	if !ok || s.kind != rowText {
		return nil, 0, false
	}
	r := &e.entries[s.i].rows[s.k]
	ins := d.md.Inlines(d.blocks[i].Index + r.rel)
	for k, in := range ins {
		if in.Kind == parser.Link && pos >= in.Start && pos <= in.End {
			return ins, k, true
		}
	}
	return nil, 0, false
}

// CodeAt 报告原文偏移 pos 是否位于代码或公式中（代码块、公式块、front matter、行内代码与公式），源码模式的
// HTML / CSS 补全据此不在代码里弹出。
func (d *Document) CodeAt(pos int) bool {
	i, _ := d.BlockAt(pos)
	if i < 0 {
		return false
	}
	top := d.blocks[i]
	switch top.Kind {
	case KindFrontMatter, parser.CodeBlock, parser.MathBlock:
		return true
	}
	b := top.Index
	for {
		next := -1
		for c := range d.md.Children(b) {
			if cb := d.md.Block(c); cb.Start <= pos && pos <= cb.End {
				next = c
				break
			}
		}
		if next < 0 {
			break
		}
		b = next
	}
	switch d.md.Block(b).Kind {
	case parser.CodeBlock, parser.MathBlock:
		return true
	}
	for _, in := range d.md.Inlines(b) {
		if (in.Kind == parser.Code || in.Kind == parser.Math) && in.Start < pos && pos < in.End {
			return true
		}
	}
	return false
}

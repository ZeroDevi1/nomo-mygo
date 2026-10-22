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
	var out []Heading
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
	return out
}

// Stats 是写作统计：Chars 为非空白字符数，Words 为英文单词与汉字数之和，Lines 为行数。
type Stats struct {
	Chars, Words, Lines int
}

// Stats 统计原文（O(n)）；调用方按 Version 缓存，不要每帧调用。
func (d *Document) Stats() Stats {
	src := d.Source()
	s := Stats{Lines: 1}
	inWord := false
	for i := 0; i < len(src); {
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
		e.wrapLines("```\n", "\n```")
	case "mathBlock":
		e.wrapLines("$$\n", "\n$$")
	case "callout":
		e.insertBlock("> [!NOTE]\n> ")
	case "clearFormat":
		e.clearFormat()
	case "horizontalRule":
		e.insertBlock("---")
	case "table":
		e.insertBlock("| 列 1 | 列 2 |\n| --- | --- |\n|  |  |")
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
var inlineMarkers = []string{"**", "__", "~~", "==", "`", "<u>", "</u>", "<ins>", "</ins>", "<mark>", "</mark>",
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

package mdedit

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui/markdown/parser"
)

// 工具栏插入命令（原版 editorCommands 的 insertTable、insertFootnote、insertToc、insertCommentInline、
// insertMathInline）按 Markdown 原文实现：写出与原版序列化结果相同的原文。

// InsertTable 在当前容器中插入空表格，光标置于第一个表头单元格。
func (e *Editor) InsertTable(rows, columns int) {
	columns, rows = clamp(columns, 2, 8), clamp(rows, 1, 12)
	d := e.Doc
	src := d.Source()
	a, z := d.Sel.Range()
	i, _ := d.BlockAt(z)
	at, prefix := z, ""
	emptyContainer := false
	if i >= 0 {
		top := d.blocks[i]
		at = top.End
		if a < top.Start {
			return
		}
		switch top.Kind {
		case parser.List, parser.Quote:
			s, ok := e.segAt(z)
			if !ok {
				return
			}
			r := &e.entries[i].rows[s.k]
			leaf := d.md.Block(top.Index + r.rel)
			if r.empty && a == z && z == s.end {
				at = z
				prefix = d.tableContinuation(parser.Block{Start: z, Parent: top.Index + r.rel})
				if leaf.Kind == parser.Item && z > 0 && src[z-1] != ' ' && src[z-1] != '\t' {
					prefix += " "
				}
				emptyContainer = true
			} else if r.ncol == 0 && (leaf.Kind == parser.Paragraph || leaf.Kind == parser.Heading) && a >= leaf.Start && z <= trailingEnd(src, leaf.End) {
				at = trailingEnd(src, leaf.End)
				prefix = d.tableContinuation(leaf)
			} else {
				return
			}
		case parser.HTMLBlock:
			if a != z || !e.tableAfterSummary(i, z) {
				return
			}
		case parser.Table, parser.CodeBlock, parser.MathBlock, KindFrontMatter, KindToc:
			return
		}
	} else if a != z {
		return
	}
	row := "|" + strings.Repeat("  |", columns)
	sep := "|" + strings.Repeat(" :--- |", columns)
	lines := []string{row, sep}
	for range rows {
		lines = append(lines, row)
	}
	blank := strings.TrimRight(prefix, " \t")
	lead := "\n" + blank + "\n" + prefix
	if emptyContainer {
		lead = ""
		if at > 0 && src[at-1] != ' ' && src[at-1] != '\t' {
			lead = " "
		}
	} else if at == 0 && prefix == "" {
		lead = ""
	}
	text := lead + strings.Join(lines, "\n"+prefix) + "\n" + blank
	if at == len(src) || src[at] != '\n' {
		text += "\n"
	}
	e.Replace(at, at, text)
	p := at + len(lead) + 2
	d.SetSelection(Selection{Anchor: p, Head: p})
	if n := len(d.hist.undo); n > 0 {
		d.hist.undo[n-1].after = d.Sel
	}
}

func (e *Editor) tableAfterSummary(i, pos int) bool {
	d := e.Doc
	idx := d.details()
	m := idx.marker(i)
	if m == nil || len(m.opens) != 1 {
		return false
	}
	f := &idx.frames[m.opens[0]]
	block := d.blocks[i]
	parsed := d.detailsParsed(d.slice(block.Start, block.End))
	return f.open == i && f.close > i && f.openTok < len(parsed.opens) &&
		pos >= block.Start+parsed.opens[f.openTok].end
}

// tableContinuation 把叶子块首行的列表标记换成等宽缩进，引用标记原样保留。
func (d *Document) tableContinuation(leaf parser.Block) string {
	src := d.Source()
	start := lineStart(src, leaf.Start)
	end := leaf.Start
	prefix := src[start:end]
	for i := leaf.Parent; i > 0; {
		b := d.md.Block(i)
		i = b.Parent
		if b.Kind != parser.Item || b.Start < start || b.Start >= end {
			continue
		}
		marker := strings.IndexAny(src[b.Start:end], " \t")
		if marker < 0 {
			marker = end - b.Start
		}
		a, z := b.Start-start, b.Start-start+marker
		prefix = prefix[:a] + strings.Repeat(" ", marker) + prefix[z:]
		if b.Task != parser.NoTask {
			box := z
			for box < len(prefix) && (prefix[box] == ' ' || prefix[box] == '\t') {
				box++
			}
			if box+4 <= len(prefix) && prefix[box] == '[' && prefix[box+2] == ']' {
				prefix = prefix[:box] + prefix[box+4:]
			}
		}
	}
	return prefix
}

var footnoteIDRe = regexp.MustCompile(`\[\^(\d+)\]`)

// nextFootnoteID 返回比原文中所有数字脚注编号（引用与定义）都大 1 的编号；按十进制字符串比较与递增，
// 超出整数范围也不会复用已有编号（同原版 createNextFootnoteId）。
func nextFootnoteID(src string) string {
	maxID := "0"
	for _, m := range footnoteIDRe.FindAllStringSubmatch(src, -1) {
		id := strings.TrimLeft(m[1], "0")
		if id == "" {
			id = "0"
		}
		if len(id) > len(maxID) || len(id) == len(maxID) && id > maxID {
			maxID = id
		}
	}
	// 十进制字符串加一。
	b := []byte(maxID)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < '9' {
			b[i]++
			return string(b)
		}
		b[i] = '0'
	}
	return "1" + string(b)
}

// insertFootnote 在选区处插入脚注引用 [^n]，在文末追加定义 [^n]: 并把光标放到定义处（原版 insertFootnote）。
// 两处修改记为一步撤销。
func (e *Editor) insertFootnote() {
	src := e.Doc.Source()
	id := nextFootnoteID(src)
	ref := "[^" + id + "]"
	a, z := e.Doc.Sel.Range()
	e.Doc.hist.seal()
	e.applyEdit(e.Doc.Replace(a, z, ref, editOther))
	src = e.Doc.Source()
	end := len(src)
	sep := "\n\n"
	switch {
	case strings.HasSuffix(src, "\n\n"):
		sep = ""
	case strings.HasSuffix(src, "\n"):
		sep = "\n"
	}
	def := sep + ref + ": "
	e.Doc.Sel = Selection{Anchor: end, Head: end}
	e.applyEdit(e.Doc.Replace(end, end, def, editOther))
	e.Doc.hist.linkLast()
	p := end + len(def)
	e.Doc.Sel = Selection{Anchor: p, Head: p}
	e.Doc.hist.seal()
}

// insertToc 在光标所在块之后插入正文目录区（原版 createTocBlock：<!-- toc --> 与 <!-- /toc --> 之间为按当前标题
// 生成的嵌套链接列表）。
func (e *Editor) insertToc() {
	list := tocList(e.Doc.Headings())
	block := tocStartMarker + "\n" + list
	if list != "" {
		block += "\n"
	}
	e.insertBlock(block + tocEndMarker)
}

// tocList 生成目录列表：每级缩进两个空格，锚点规则同原版 slugifyHeading 与 createHeadingIdAllocator（重名追加 -2、-3…，
// 无可用字符时为 heading-序号）。
func tocList(hs []Heading) string {
	titles := make([]string, len(hs))
	for i, h := range hs {
		titles[i] = h.Text
	}
	ids := HeadingIDs(titles)
	lines := make([]string, 0, len(hs))
	for i, h := range hs {
		lines = append(lines, strings.Repeat("  ", max(0, h.Level-1))+"- ["+escapeLinkText(h.Text)+"](#"+ids[i]+")")
	}
	return strings.Join(lines, "\n")
}

// HeadingIDs 按文档顺序为各标题分配锚点（目录链接的 #id，Word 导出的书签据此对应）。
func HeadingIDs(titles []string) []string {
	used := map[string]bool{}
	next := map[string]int{}
	ids := make([]string, len(titles))
	for i, title := range titles {
		base := slugifyHeading(title)
		if base == "" {
			base = "heading-" + strconv.Itoa(i+1)
		}
		id, n := base, next[base]
		if n == 0 {
			n = 2
		}
		for used[id] {
			id = base + "-" + strconv.Itoa(n)
			n++
		}
		next[base], used[id] = n, true
		ids[i] = id
	}
	return ids
}

// slugifyHeading 同原版：小写，去掉字母、数字、空白与连字符以外的字符，首尾去空白后空白串换为一个连字符。
func slugifyHeading(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsSpace(r) || r == '-' {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), "-")
}

// escapeLinkText 转义链接文字中会改变 Markdown 结构的字符。
func escapeLinkText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`\[]*_`+"`", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// insertInlineComment 把选区（无选区时为占位文字）写成行内注释 <!-- … -->，并选中注释内容（原版 insertCommentInline
// 插入后立即进入编辑）。内容中的 --> 改写为 -- >，同原版 serializeMarkdownComment。
func (e *Editor) insertInlineComment() {
	src := e.Doc.Source()
	a, z := e.Doc.Sel.Range()
	content := strings.ReplaceAll(src[a:z], "-->", "-- >")
	if content == "" {
		content = e.labels.InlineComment
	}
	if content == "" {
		content = "comment"
	}
	e.applyEdit(e.Doc.Replace(a, z, "<!-- "+content+" -->", editOther))
	e.Doc.Sel = Selection{Anchor: a + len("<!-- "), Head: a + len("<!-- ") + len(content)}
	e.Doc.hist.seal()
}

// insertInlineMath 把选区写成行内公式 $…$；无选区时插入 $x$ 并选中 x 以便直接输入（原版开始一个空的行内公式草稿，
// 原生版没有公式草稿状态，用可直接覆盖的占位内容代替）。
func (e *Editor) insertInlineMath() {
	src := e.Doc.Source()
	a, z := e.Doc.Sel.Range()
	tex := strings.TrimSpace(src[a:z])
	if tex == "" || strings.Contains(tex, "\n") {
		tex = "x"
	}
	e.applyEdit(e.Doc.Replace(a, z, "$"+tex+"$", editOther))
	e.Doc.Sel = Selection{Anchor: a + 1, Head: a + 1 + len(tex)}
	e.Doc.hist.seal()
}

// RefreshToc 把各目录区（<!-- toc --> … <!-- /toc -->）的列表原文更新为当前标题（原版目录随标题自动更新）；
// 有变化时作为一次独立的撤销步改写并返回 true。外壳在保存前调用，编辑过程中不改写原文（目录卡片已实时显示）。
func (e *Editor) RefreshToc() bool {
	d := e.Doc
	if !d.hasToc {
		return false
	}
	list := tocList(d.Headings())
	want := tocStartMarker + "\n" + list
	if list != "" {
		want += "\n"
	}
	want += tocEndMarker
	changed := false
	// 从后往前改写，前面目录区的偏移不受影响。
	for k := len(d.blocks) - 1; k >= 0; k-- {
		b := d.blocks[k]
		if b.Kind != KindToc {
			continue
		}
		src := d.Source()
		end := strings.Index(src[b.Start:], tocEndMarker)
		if end < 0 {
			continue
		}
		z := b.Start + end + len(tocEndMarker)
		if src[b.Start:z] == want {
			continue
		}
		if !changed {
			d.hist.seal()
		}
		e.applyEdit(d.Replace(b.Start, z, want, editOther))
		changed = true
	}
	if changed {
		d.hist.seal()
	}
	return changed
}

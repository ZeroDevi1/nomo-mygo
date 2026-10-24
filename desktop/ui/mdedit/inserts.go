package mdedit

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// 工具栏插入命令（原版 editorCommands 的 insertTable、insertFootnote、insertToc、insertCommentInline、
// insertMathInline）按 Markdown 原文实现：写出与原版序列化结果相同的原文。

// InsertTable 在光标所在块之后插入 rows 行正文、columns 列的空表格（原版 createTableNode：列 2–8、正文行 1–12，
// 首行为表头，对齐行写 :---），光标置于第一个表头单元格。
func (e *Editor) InsertTable(rows, columns int) {
	columns, rows = clamp(columns, 2, 8), clamp(rows, 1, 12)
	row := "|" + strings.Repeat("  |", columns)
	sep := "|" + strings.Repeat(" :--- |", columns)
	lines := []string{row, sep}
	for range rows {
		lines = append(lines, row)
	}
	e.insertBlock(strings.Join(lines, "\n"))
	// insertBlock 把光标放在块尾；表格起点 = 块尾 - 块长，“| ” 之后即第一个单元格。
	p := e.Doc.Sel.Head - len(strings.Join(lines, "\n")) + 2
	e.Doc.Sel = Selection{Anchor: p, Head: p}
	e.dirty = true
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
	used := map[string]bool{}
	next := map[string]int{}
	lines := make([]string, 0, len(hs))
	for i, h := range hs {
		base := slugifyHeading(h.Text)
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
		lines = append(lines, strings.Repeat("  ", max(0, h.Level-1))+"- ["+escapeLinkText(h.Text)+"](#"+id+")")
	}
	return strings.Join(lines, "\n")
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

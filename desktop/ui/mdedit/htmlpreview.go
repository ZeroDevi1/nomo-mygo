package mdedit

import (
	"hash/maphash"
	"html"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

// HTML 块的静态预览，对应原版 tableHtml 插件与 utils/html.ts 的 sanitizeHtml / renderHtmlPreview：白名单内的
// 标签（段落、标题、列表、引用、pre、表格含 colspan/rowspan、图片与行内格式）按排版结果显示，光标进入块内
// 时显示原文；含白名单外标签（如 details、script）的块与原版一样按原文显示。HTML 注释块显示为“注释”卡片
// （原版 comment_block）。预览只读，不响应链接点击。
//
// 预览以一个行内对象（占位字符）放入行的投影，复用图片对象的光标映射、点击进入原文与绘制路径（imageview.go）。

// htmlAllowed 是可预览的标签，取自原版 FALLBACK_TAGS。
var htmlAllowed = map[string]bool{}

func init() {
	for _, t := range strings.Fields(`section div span strong b em i br img mark sup sub a code s del strike u
		p hr small kbd samp var abbr blockquote pre ul ol li dl dt dd h1 h2 h3 h4 h5 h6
		article header footer main nav aside figure figcaption table caption colgroup col thead tbody tfoot tr th td`) {
		htmlAllowed[t] = true
	}
}

var (
	htmlVoid   = map[string]bool{"br": true, "hr": true, "img": true, "col": true, "wbr": true}
	htmlInline = map[string]bool{"span": true, "strong": true, "b": true, "em": true, "i": true, "br": true, "img": true,
		"mark": true, "sup": true, "sub": true, "a": true, "code": true, "s": true, "del": true, "strike": true, "u": true,
		"small": true, "kbd": true, "samp": true, "var": true, "abbr": true}
	// htmlImplied：打开键标签时，栈顶若是值中的标签则先隐式关闭（HTML 的可省略结束标签）。
	htmlImplied = map[string][]string{
		"p": {"p"}, "li": {"li", "p"}, "dt": {"dt", "dd", "p"}, "dd": {"dt", "dd", "p"},
		"td": {"td", "th", "p"}, "th": {"td", "th", "p"}, "tr": {"tr", "td", "th", "p"},
		"thead": {"thead", "tbody", "tfoot", "tr", "td", "th"}, "tbody": {"thead", "tbody", "tfoot", "tr", "td", "th"},
		"tfoot": {"thead", "tbody", "tfoot", "tr", "td", "th"},
	}
)

// hnode 是 HTML 预览的节点：tag 为空表示文本节点。open 为原样开始标签，按需用 htmlAttr 读取属性。
type hnode struct {
	tag, text, open string
	children        []*hnode
}

func (n *hnode) attr(name string) string {
	if n.open == "" {
		return ""
	}
	return htmlAttr(n.open, name)
}

// parseHTMLPreview 容错解析 HTML 片段；遇到白名单外的标签返回 false（整块按原文显示）。注释被丢弃。
func parseHTMLPreview(src string) (*hnode, bool) {
	root := &hnode{tag: "#root"}
	stack := []*hnode{root}
	top := func() *hnode { return stack[len(stack)-1] }
	for i := 0; i < len(src); {
		lt := strings.IndexByte(src[i:], '<')
		if lt < 0 {
			lt = len(src) - i
		}
		if lt > 0 {
			top().children = append(top().children, &hnode{text: html.UnescapeString(src[i : i+lt])})
			i += lt
			continue
		}
		if strings.HasPrefix(src[i:], "<!--") {
			end := strings.Index(src[i+4:], "-->")
			if end < 0 {
				return nil, false
			}
			i += 4 + end + 3
			continue
		}
		z, q := i+1, byte(0)
		for ; z < len(src) && (q != 0 || src[z] != '>'); z++ {
			switch {
			case q != 0 && src[z] == q:
				q = 0
			case q == 0 && (src[z] == '"' || src[z] == '\''):
				q = src[z]
			}
		}
		if z >= len(src) {
			return nil, false
		}
		tag := src[i : z+1]
		i = z + 1
		name, closing, _, ok := htmlTag(tag)
		if !ok || !htmlAllowed[name] {
			return nil, false
		}
		if closing {
			for k := len(stack) - 1; k > 0; k-- {
				if stack[k].tag == name {
					stack = stack[:k]
					break
				}
			}
			continue
		}
		for _, imp := range htmlImplied[name] {
			if len(stack) > 1 && top().tag == imp {
				stack = stack[:len(stack)-1]
			}
		}
		// 块级元素不能留在 <p> 里：隐式关闭段落。
		if !htmlInline[name] && len(stack) > 1 && top().tag == "p" {
			stack = stack[:len(stack)-1]
		}
		n := &hnode{tag: name, open: tag}
		top().children = append(top().children, n)
		if !htmlVoid[name] && !strings.HasSuffix(tag, "/>") {
			stack = append(stack, n)
		}
	}
	return root, true
}

// htmlBlockPreview 是一个 HTML 块预览：src 为块原文；layout 为最近一次排版结果（按宽度与图片代数缓存）。
type htmlBlockPreview struct {
	src     string
	comment bool
	// toc 为 true 表示目录卡片：条目来自当前文档标题（items），标题文字在绘制时只为可见条目排版并缓存在
	// titles（换宽度或条目变化时清空，至多 tocTitleCache 项），2000 个标题的目录不必一次排完。
	toc    bool
	items  []Heading
	titles map[int]*ui.TextLayout
	root   *hnode
	lay    *hpLayout
	layW   float32
	layGen uint64
	hasImg bool
}

// hpLayout 是预览的排版结果：一组绘制操作，坐标相对预览左上角。
type hpLayout struct {
	ops   []hpOp
	w, h  float32
	bytes int
}

type hpOpKind uint8

const (
	hpText hpOpKind = iota
	hpFill
	hpStroke
	hpImage
)

type hpOp struct {
	kind   hpOpKind
	rect   ui.Rect
	color  ui.Color
	radius float32
	text   *ui.TextLayout
	img    imgObj
}

// hpStyle 是行内样式的累积。
type hpStyle struct {
	bold, italic, under, strike, mark, code, link, small, muted bool
	size                                                        float32
	align                                                       ui.Align
}

type hpCtx struct {
	e      *Editor
	lay    *hpLayout
	hasImg bool
}

// htmlLayout 返回预览在宽度 w 下的排版结果（必要时重新排版）。
func (e *Editor) htmlLayout(b *htmlBlockPreview, w float32) *hpLayout {
	gen := uint64(0)
	if b.hasImg {
		gen = e.imageGen()
	}
	if b.lay != nil && b.layW == w && b.layGen == gen {
		return b.lay
	}
	ctx := &hpCtx{e: e, lay: &hpLayout{}}
	base := hpStyle{size: bodySize * e.scale}
	var h float32
	switch {
	case b.toc:
		h = ctx.tocCard(b, w)
	case b.comment:
		h = ctx.comment(b.src, w)
	default:
		if b.root == nil {
			b.root, _ = parseHTMLPreview(b.src)
		}
		if b.root != nil {
			h = ctx.blocks(b.root.children, 0, 0, w, base)
		}
	}
	ctx.lay.w, ctx.lay.h = w, max(h, e.lineH)
	b.lay, b.layW, b.layGen, b.hasImg = ctx.lay, w, gen, ctx.hasImg
	return b.lay
}

// 目录卡片版式，取自原版 editor-toc.css：内边距 14/18，标题 19 px 粗体并有底边线，条目按级缩进 28 px。
const (
	tocPadX, tocPadY = 18, 14
	tocHeaderH       = 42
	tocIndent        = 28
	tocTitleCache    = 400
)

func (e *Editor) tocItemH() float32 { return e.lineH + 8 }

// tocCard 排版目录卡片的外框与标题，条目在 drawHTML 中按可见范围绘制；返回卡片高度。
func (c *hpCtx) tocCard(b *htmlBlockPreview, w float32) float32 {
	e := c.e
	b.items = e.Doc.Headings()
	b.titles = map[int]*ui.TextLayout{}
	title := ui.ShapeRichTextWith([]ui.Span{{Text: e.tocLabel(), Weight: 800}}, ui.Font{Family: e.fontBody, Size: 19 * e.scale}, ui.TextShape{})
	_, th := title.Size()
	n := len(b.items)
	body := float32(n) * e.tocItemH()
	if n == 0 {
		body = e.lineH + 8
	}
	h := tocPadY + tocHeaderH + body + tocPadY
	c.lay.ops = append(c.lay.ops,
		hpOp{kind: hpFill, rect: ui.Rect{W: w, H: h}, color: e.pal.surface, radius: 8},
		hpOp{kind: hpStroke, rect: ui.Rect{W: w, H: h}, color: e.pal.border, radius: 8},
		hpOp{kind: hpText, rect: ui.Rect{X: tocPadX, Y: tocPadY + (tocHeaderH-8-th)/2}, text: title, color: e.pal.heading},
		hpOp{kind: hpFill, rect: ui.Rect{X: tocPadX, Y: tocPadY + tocHeaderH - 8, W: w - 2*tocPadX, H: 1}, color: e.pal.border})
	if n == 0 {
		empty := ui.ShapeRichTextWith([]ui.Span{{Text: e.noHeadingsLabel()}}, ui.Font{Family: e.fontBody, Size: 14 * e.scale}, ui.TextShape{})
		c.lay.ops = append(c.lay.ops, hpOp{kind: hpText, rect: ui.Rect{X: tocPadX, Y: tocPadY + tocHeaderH}, text: empty, color: e.pal.muted})
	}
	c.lay.bytes += 1024 + n*48
	return h
}

// drawToc 绘制目录卡片中与可见区相交的条目：序号右对齐，标题按级缩进、单行。
func (e *Editor) drawToc(p *ui.Painter, b *htmlBlockPreview, rc ui.Rect) {
	vis := p.Visible()
	ih := e.tocItemH()
	top := rc.Y + tocPadY + tocHeaderH
	first := max(0, int((vis.Y-top)/ih))
	last := min(len(b.items)-1, int((vis.Y+vis.H-top)/ih))
	if len(b.titles) > tocTitleCache {
		clear(b.titles)
	}
	font := ui.Font{Family: e.fontBody, Size: 14 * e.scale}
	for k := first; k <= last; k++ {
		it := b.items[k]
		y := top + float32(k)*ih
		num := ui.ShapeRichTextWith([]ui.Span{{Text: strconv.Itoa(k + 1)}}, font, ui.TextShape{})
		nw, nh := num.Size()
		x := rc.X + tocPadX + float32(clamp(it.Level, 1, 6)-1)*tocIndent
		l := b.titles[k]
		if l == nil {
			// 不设宽度：标题保持单行，超出部分在下面裁剪掉（原版 .toc-text 单行省略）。
			l = ui.ShapeRichTextWith([]ui.Span{{Text: it.Text}}, font, ui.TextShape{})
			b.titles[k] = l
		}
		ty := y + (ih-nh)/2
		p.Fill(ui.Rect{X: x, Y: y + ih/2 - 2, W: 4, H: 4}, e.pal.muted, 2)
		avail := rc.X + rc.W - tocPadX - nw - 16 - (x + 14)
		p.Clip(ui.Rect{X: x + 14, Y: y, W: max(0, avail), H: ih}, 0, func() { p.TextLayout(l, x+14, ty, e.pal.link) })
		p.TextLayout(num, rc.X+rc.W-tocPadX-nw, ty, e.pal.muted)
	}
}

// tocItemAt 返回目录卡片内 (x, y)（相对卡片左上角）处的标题，没有时 ok 为 false。
func (e *Editor) tocItemAt(b *htmlBlockPreview, x, y float32) (Heading, bool) {
	k := int((y - tocPadY - tocHeaderH) / e.tocItemH())
	if y < tocPadY+tocHeaderH || k < 0 || k >= len(b.items) || x < tocPadX {
		return Heading{}, false
	}
	// 位置取当前标题：其他段落的编辑会平移标题偏移而不使卡片重排（摘要只含级别与文字）。
	if hs := e.Doc.Headings(); k < len(hs) {
		return hs[k], true
	}
	return Heading{}, false
}

func (e *Editor) tocLabel() string {
	if e.labels.Toc != "" {
		return e.labels.Toc
	}
	return "Contents"
}

func (e *Editor) noHeadingsLabel() string {
	if e.labels.NoHeadings != "" {
		return e.labels.NoHeadings
	}
	return "No headings"
}

// comment 排版注释卡片：左侧“注释”标签，右侧注释内容（多行原样）。
func (c *hpCtx) comment(src string, w float32) float32 {
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(src), "<!--"), "-->"))
	e := c.e
	label := ui.ShapeRichTextWith([]ui.Span{{Text: e.commentLabel(), Weight: 600}}, ui.Font{Family: e.fontBody, Size: 12}, ui.TextShape{})
	lw, lh := label.Size()
	text := ui.ShapeRichTextWith([]ui.Span{{Text: body}}, ui.Font{Family: e.fontBody, Size: 13 * e.scale}, ui.TextShape{Width: max(40, w-lw-40)})
	_, th := text.Size()
	h := max(lh, th) + 16
	c.lay.ops = append(c.lay.ops,
		hpOp{kind: hpFill, rect: ui.Rect{W: w, H: h}, color: e.pal.codeBg, radius: 6},
		hpOp{kind: hpStroke, rect: ui.Rect{W: w, H: h}, color: e.pal.border, radius: 6},
		hpOp{kind: hpText, rect: ui.Rect{X: 12, Y: 8}, text: label, color: e.pal.muted},
		hpOp{kind: hpText, rect: ui.Rect{X: 12 + lw + 14, Y: 8}, text: text, color: e.pal.text})
	c.lay.bytes += 256 + len(body)*4
	return h
}

// blocks 自上而下排版 nodes（块级元素与连续的行内内容），返回占用高度。
func (c *hpCtx) blocks(nodes []*hnode, x, y, w float32, st hpStyle) float32 {
	y0 := y
	var inline []*hnode
	flush := func() {
		if len(inline) > 0 {
			y += c.paragraph(inline, x, y, w, st)
			inline = inline[:0]
		}
	}
	gap := float32(8)
	for _, n := range nodes {
		if n.tag == "" || htmlInline[n.tag] {
			inline = append(inline, n)
			continue
		}
		flush()
		if y > y0 {
			y += gap
		}
		y += c.block(n, x, y, w, st)
	}
	flush()
	return y - y0
}

func alignOf(n *hnode, def ui.Align) ui.Align {
	switch strings.ToLower(n.attr("align")) {
	case "center":
		return ui.Center
	case "right":
		return ui.End
	case "left":
		return ui.Start
	}
	return def
}

// block 排版一个块级元素，返回高度。
func (c *hpCtx) block(n *hnode, x, y, w float32, st hpStyle) float32 {
	e := c.e
	st.align = alignOf(n, st.align)
	switch n.tag {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		st.bold, st.size = true, headingSize(int(n.tag[1]-'0'))*e.scale
		return c.paragraph(n.children, x, y, w, st)
	case "hr":
		c.lay.ops = append(c.lay.ops, hpOp{kind: hpFill, rect: ui.Rect{X: x, Y: y + 6, W: w, H: 1}, color: e.pal.border})
		return 13
	case "blockquote":
		st.muted = true
		h := c.blocks(n.children, x+quoteIndent, y, w-quoteIndent, st)
		c.lay.ops = append(c.lay.ops, hpOp{kind: hpFill, rect: ui.Rect{X: x, Y: y, W: 3, H: h}, color: e.pal.quoteBar, radius: 1.5})
		return h
	case "pre":
		text := strings.TrimSuffix(textOf(n), "\n")
		l := ui.ShapeRichTextWith([]ui.Span{{Text: text}}, ui.Font{Family: e.monoFont(), Size: 13 * e.scale}, ui.TextShape{Width: max(40, w-2*codePadX)})
		_, th := l.Size()
		h := th + 2*codePadY
		c.lay.ops = append(c.lay.ops, hpOp{kind: hpFill, rect: ui.Rect{X: x, Y: y, W: w, H: h}, color: e.pal.codeBg, radius: 6},
			hpOp{kind: hpText, rect: ui.Rect{X: x + codePadX, Y: y + codePadY}, text: l, color: e.pal.codeFg})
		c.lay.bytes += 512 + len(text)*8
		return h
	case "ul", "ol":
		return c.list(n, x, y, w, st)
	case "dl":
		y0 := y
		for _, ch := range n.children {
			switch ch.tag {
			case "dt":
				s := st
				s.bold = true
				y += c.blocks(ch.children, x, y, w, s) + 2
			case "dd":
				y += c.blocks(ch.children, x+listIndent, y, w-listIndent, st) + 4
			}
		}
		return y - y0
	case "table":
		return c.table(n, x, y, w, st)
	case "figcaption":
		st.muted, st.small = true, true
	}
	return c.blocks(n.children, x, y, w, st)
}

// list 排版 ul/ol：每项左侧画项目符号或序号，内容缩进 listIndent。
func (c *hpCtx) list(n *hnode, x, y, w float32, st hpStyle) float32 {
	e := c.e
	y0 := y
	num := 1
	if s, err := strconv.Atoi(n.attr("start")); err == nil {
		num = s
	}
	first := true
	for _, li := range n.children {
		if li.tag != "li" {
			continue
		}
		if !first {
			y += 4
		}
		first = false
		marker := "•"
		if n.tag == "ol" {
			marker = strconv.Itoa(num) + "."
			num++
		}
		ml := ui.ShapeRichTextWith([]ui.Span{{Text: marker}}, ui.Font{Family: e.fontBody, Size: st.size}, ui.TextShape{})
		mw, _ := ml.Size()
		c.lay.ops = append(c.lay.ops, hpOp{kind: hpText, rect: ui.Rect{X: x + listIndent - mw - 6, Y: y}, text: ml, color: e.pal.muted})
		y += c.blocks(li.children, x+listIndent, y, w-listIndent, st)
	}
	return y - y0
}

// paragraph 把行内节点排成一段文字（含行内图片对象），返回高度；全是空白时高度为 0。
func (c *hpCtx) paragraph(nodes []*hnode, x, y, w float32, st hpStyle) float32 {
	e := c.e
	var spans []ui.Span
	var imgs []imgObj
	var walk func(nodes []*hnode, st hpStyle)
	walk = func(nodes []*hnode, parent hpStyle) {
		for _, n := range nodes {
			st := parent // 每个子节点从父样式开始，样式不在兄弟节点间泄漏
			switch n.tag {
			case "":
				if t := collapseSpace(n.text); t != "" {
					spans = append(spans, c.span(t, st))
				}
				continue
			case "br":
				spans = append(spans, ui.Span{Text: "\n"})
				continue
			case "img":
				o := imgObj{src: n.attr("src"), width: n.attr("width"), alt: n.attr("alt")}
				sz := e.objSize(o, e.imageEntry(o), w)
				c.hasImg = true
				imgs = append(imgs, o)
				spans = append(spans, ui.Span{Text: objChar, ObjectWidth: sz[0], ObjectHeight: sz[1]})
				continue
			case "strong", "b":
				st.bold = true
			case "em", "i", "var":
				st.italic = true
			case "u":
				st.under = true
			case "s", "del", "strike":
				st.strike = true
			case "mark":
				st.mark = true
			case "code", "kbd", "samp":
				st.code = true
			case "a":
				st.link = true
			case "small", "sub", "sup":
				st.small = true
			}
			walk(n.children, st)
		}
	}
	walk(nodes, st)
	// 段首、段尾的折叠空白去掉（浏览器对行首行尾空白的处理）。
	for len(spans) > 0 && strings.TrimSpace(spans[0].Text) == "" && spans[0].ObjectWidth == 0 {
		spans = spans[1:]
	}
	for len(spans) > 0 && strings.TrimSpace(spans[len(spans)-1].Text) == "" && spans[len(spans)-1].ObjectWidth == 0 {
		spans = spans[:len(spans)-1]
	}
	if len(spans) == 0 {
		return 0
	}
	if len(spans) > 0 {
		spans[0].Text = strings.TrimLeft(spans[0].Text, " ")
		spans[len(spans)-1].Text = strings.TrimRight(spans[len(spans)-1].Text, " ")
	}
	font := ui.Font{Family: e.fontBody, Size: st.size}
	if st.bold {
		font.Weight = 700
	}
	l := ui.ShapeRichTextWith(spans, font, ui.TextShape{Width: max(20, w), Align: st.align})
	_, h := l.Size()
	color := e.pal.text
	if st.muted {
		color = e.pal.quoteFg
	}
	if st.size > bodySize*e.scale {
		color = e.pal.heading
	}
	c.lay.ops = append(c.lay.ops, hpOp{kind: hpText, rect: ui.Rect{X: x, Y: y}, text: l, color: color})
	for k, ob := range l.Objects() {
		if k < len(imgs) {
			r := ob.Rect
			r.X += x
			r.Y += y
			c.lay.ops = append(c.lay.ops, hpOp{kind: hpImage, rect: r, img: imgs[k]})
		}
	}
	for _, sp := range spans {
		c.lay.bytes += 64 + len(sp.Text)*8
	}
	return h
}

func (c *hpCtx) span(t string, st hpStyle) ui.Span {
	e := c.e
	sp := ui.Span{Text: t, Italic: st.italic, Underline: st.under || st.link, Strikethrough: st.strike}
	if st.bold {
		sp.Weight = 700
	}
	if st.small {
		sp.Size = st.size * 0.85
	}
	if st.code {
		sp.Font, sp.Background, sp.Radius = e.monoFont(), e.pal.codeBg, 3
	}
	if st.mark {
		sp.Background, sp.Radius = e.pal.mark, 2
	}
	if st.link {
		sp.Color = e.pal.link
	}
	return sp
}

// collapseSpace 把连续空白折叠为一个空格（非 pre 文本的浏览器规则）。
func collapseSpace(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		} else if space {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	if space {
		b.WriteByte(' ')
	}
	return b.String()
}

func textOf(n *hnode) string {
	if n.tag == "" {
		return n.text
	}
	if n.tag == "br" {
		return "\n"
	}
	var b strings.Builder
	for _, ch := range n.children {
		b.WriteString(textOf(ch))
	}
	return b.String()
}

// hpCell 是表格中的一个单元格及其在网格中的位置。
type hpCell struct {
	n                *hnode
	row, col, rs, cs int
	header           bool
}

// table 排版 HTML 表格：按 colspan/rowspan 放入网格；列宽取各列单格内容的自然宽度（上限 28em，下限 3em），
// 超出可用宽度时按比例收缩；行高取该行单格内容高度的最大值，跨行单元格不足的高度补到所跨的最后一行。
func (c *hpCtx) table(t *hnode, x, y, w float32, st hpStyle) float32 {
	e := c.e
	var rows []*hnode
	var collect func(n *hnode)
	collect = func(n *hnode) {
		for _, ch := range n.children {
			switch ch.tag {
			case "tr":
				rows = append(rows, ch)
			case "thead", "tbody", "tfoot":
				collect(ch)
			}
		}
	}
	collect(t)
	var cells []hpCell
	occupied := map[[2]int]bool{}
	ncol := 0
	for r, tr := range rows {
		col := 0
		for _, td := range tr.children {
			if td.tag != "td" && td.tag != "th" {
				continue
			}
			for occupied[[2]int{r, col}] {
				col++
			}
			cs, _ := strconv.Atoi(td.attr("colspan"))
			rs, _ := strconv.Atoi(td.attr("rowspan"))
			cs, rs = clamp(cs, 1, 64), clamp(rs, 1, len(rows)-r)
			for dr := range rs {
				for dc := range cs {
					occupied[[2]int{r + dr, col + dc}] = true
				}
			}
			cells = append(cells, hpCell{n: td, row: r, col: col, rs: rs, cs: cs, header: td.tag == "th"})
			col += cs
			ncol = max(ncol, col)
		}
	}
	if ncol == 0 {
		return 0
	}
	em := st.size
	pad := float32(cellPadX)
	cols := make([]float32, ncol)
	for _, cl := range cells {
		if cl.cs == 1 {
			cols[cl.col] = max(cols[cl.col], min(cellTextWidth(collapseSpace(textOf(cl.n)), em), tableBodyEm*em)+2*pad)
		}
	}
	for k := range cols {
		cols[k] = max(cols[k], tableEmptyEm*em)
	}
	for _, cl := range cells {
		if cl.cs > 1 {
			need := min(cellTextWidth(collapseSpace(textOf(cl.n)), em), tableBodyEm*em) + 2*pad
			var have float32
			for k := cl.col; k < cl.col+cl.cs; k++ {
				have += cols[k]
			}
			if have < need {
				cols[cl.col+cl.cs-1] += need - have
			}
		}
	}
	var total float32
	for _, cw := range cols {
		total += cw
	}
	if total > w {
		f := w / total
		for k := range cols {
			cols[k] *= f
		}
		total = w
	}
	colX := make([]float32, ncol+1)
	for k, cw := range cols {
		colX[k+1] = colX[k] + cw
	}
	// 先排每个单元格的内容（记录操作区间以便之后整体平移到行位置），再定行高。
	type placed struct {
		cell     hpCell
		from, to int
		h        float32
	}
	ps := make([]placed, 0, len(cells))
	for _, cl := range cells {
		cw := colX[cl.col+cl.cs] - colX[cl.col]
		s := st
		s.align = alignOf(cl.n, ui.Start)
		if cl.header {
			s.bold = true
		}
		from := len(c.lay.ops)
		h := c.blocks(cl.n.children, 0, 0, cw-2*pad, s)
		ps = append(ps, placed{cl, from, len(c.lay.ops), h + 2*cellPadY})
	}
	rowH := make([]float32, len(rows))
	for _, p := range ps {
		if p.cell.rs == 1 {
			rowH[p.cell.row] = max(rowH[p.cell.row], p.h)
		}
	}
	for k := range rowH {
		rowH[k] = max(rowH[k], e.lineH+2*cellPadY)
	}
	for _, p := range ps {
		if p.cell.rs > 1 {
			var have float32
			for r := p.cell.row; r < p.cell.row+p.cell.rs; r++ {
				have += rowH[r]
			}
			if have < p.h {
				rowH[p.cell.row+p.cell.rs-1] += p.h - have
			}
		}
	}
	rowY := make([]float32, len(rows)+1)
	for k, rh := range rowH {
		rowY[k+1] = rowY[k] + rh
	}
	// 背景、内容平移、边框。背景须在内容之下：先把单元格内容操作取出，按“底色 → 内容 → 边框”重新追加。
	var content []hpOp
	for _, p := range ps {
		for k := p.from; k < p.to; k++ {
			op := c.lay.ops[k]
			op.rect.X += x + colX[p.cell.col] + pad
			op.rect.Y += y + rowY[p.cell.row] + cellPadY
			content = append(content, op)
		}
	}
	c.lay.ops = c.lay.ops[:ps[0].from]
	for _, p := range ps {
		r := ui.Rect{X: x + colX[p.cell.col], Y: y + rowY[p.cell.row], W: colX[p.cell.col+p.cell.cs] - colX[p.cell.col], H: rowY[p.cell.row+p.cell.rs] - rowY[p.cell.row]}
		if p.cell.header {
			c.lay.ops = append(c.lay.ops, hpOp{kind: hpFill, rect: r, color: e.pal.tableHead})
		}
	}
	c.lay.ops = append(c.lay.ops, content...)
	for _, p := range ps {
		r := ui.Rect{X: x + colX[p.cell.col], Y: y + rowY[p.cell.row], W: colX[p.cell.col+p.cell.cs] - colX[p.cell.col], H: rowY[p.cell.row+p.cell.rs] - rowY[p.cell.row]}
		c.lay.ops = append(c.lay.ops, hpOp{kind: hpStroke, rect: r, color: e.pal.tableBorder})
	}
	c.lay.bytes += 96 * len(cells)
	return rowY[len(rows)]
}

// drawHTML 在 rc 处绘制预览。
func (e *Editor) drawHTML(p *ui.Painter, b *htmlBlockPreview, rc ui.Rect) {
	lay := b.lay
	for _, op := range lay.ops {
		r := op.rect
		r.X += rc.X
		r.Y += rc.Y
		switch op.kind {
		case hpFill:
			p.Fill(r, op.color, op.radius)
		case hpStroke:
			p.Stroke(r, op.color, op.radius, 1)
		case hpText:
			p.TextLayout(op.text, r.X, r.Y, op.color)
		case hpImage:
			e.drawImageAt(p, op.img, r)
		}
	}
	if b.toc {
		e.drawToc(p, b, rc)
	}
}

// commentLabel 返回注释卡片的标签文字（外壳经 SetLabels 设置界面语言的“注释”）。
func (e *Editor) commentLabel() string {
	if e.labels.Comment != "" {
		return e.labels.Comment
	}
	return "Comment"
}

// fencedContent 返回围栏代码块原文去掉首尾围栏行后的内容。
func fencedContent(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) > 0 {
		lines = lines[1:]
	}
	if n := len(lines); n > 0 {
		if l := strings.TrimSpace(lines[n-1]); strings.HasPrefix(l, "```") || strings.HasPrefix(l, "~~~") {
			lines = lines[:n-1]
		}
	}
	return strings.Join(lines, "\n")
}

// htmlBlockKind 判断 HTML 块的显示方式：注释块、可预览的 HTML，或按原文显示（0）。
func htmlBlockKind(src string) uint8 {
	s := strings.TrimSpace(src)
	if strings.HasPrefix(s, "<!--") && strings.HasSuffix(s, "-->") && strings.Count(s, "<!--") == 1 {
		// 目录标记（<!-- toc --> / <!-- /toc -->）由目录块处理，保持原样。
		if inner := strings.TrimSpace(s[4 : len(s)-3]); inner == "toc" || inner == "/toc" {
			return 0
		}
		return previewComment
	}
	if _, ok := parseHTMLPreview(s); ok && strings.HasPrefix(s, "<") {
		return previewHTML
	}
	return 0
}

const (
	previewNone uint8 = iota
	previewHTML
	previewComment
	previewToc
	previewMath    // 公式块（$$…$$）
	previewMermaid // ```mermaid 代码块
)

// projectHTMLPreview 生成 HTML 块、公式块与 Mermaid 块收起时的投影：整块为一个对象。
func projectHTMLPreview(text string, kind uint8) *projection {
	pb := projBuilder{src: text, p: &projection{}}
	switch kind {
	case previewMath:
		src := strings.TrimSpace(text)
		src = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(src, "$$"), "$$"))
		pb.object(0, len(text), imgObj{render: RenderMathDisplay, src: src, alt: src}, 0)
		return pb.finish()
	case previewMermaid:
		code := fencedContent(text)
		pb.object(0, len(text), imgObj{render: RenderMermaid, src: code, alt: code}, 0)
		return pb.finish()
	}
	pb.object(0, len(text), imgObj{html: &htmlBlockPreview{src: text, comment: kind == previewComment, toc: kind == previewToc}}, 0)
	return pb.finish()
}

// syncToc 在文档含目录区且标题有变化时作废目录卡片的布局（卡片内容来自当前标题，不随卡片所在块的编辑而变）。
// 只在原文版本变化时计算标题摘要，标题未变时不做其他事。
func (e *Editor) syncToc() {
	d := e.Doc
	if !d.hasToc || e.tocVer == d.Version() {
		return
	}
	e.tocVer = d.Version()
	var h maphash.Hash
	h.SetSeed(e.seed)
	for _, it := range d.Headings() {
		h.WriteByte(byte(it.Level))
		h.WriteString(it.Text)
		h.WriteByte(0)
	}
	sum := h.Sum64()
	if sum == e.tocHash {
		return
	}
	e.tocHash = sum
	for i, b := range d.blocks {
		if b.Kind != KindToc || !e.entries[i].split {
			continue
		}
		for k := range e.entries[i].rows {
			r := &e.entries[i].rows[k]
			e.cacheBytes -= r.bytes
			r.proj, r.layout, r.bytes, r.objRects, r.objSizes = nil, nil, 0, nil, nil
		}
	}
	e.dirty = true
}

package mdedit

import (
	"html"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown/parser"
)

// style 是显示片段的样式位；高 8 位（stColorShift 起）为文字颜色在 projection.colors 中的下标 + 1，0 表示默认颜色。
type style uint32

const (
	stColorShift       = 16
	stColorMask  style = 0xff << stColorShift
)

const (
	stBold style = 1 << iota
	stItalic
	stStrike
	stCode
	stLink
	stImage
	stMarker    // 就地展开的 Markdown 标记，弱化显示
	stRaw       // 原样显示的原文（公式、脚注引用等 P3 内容）
	stUnderline // <u>、<ins>
	stMark      // <mark>
	stComment   // HTML 注释
	stFootRef   // 脚注引用：显示为标签（原版 sup.footnote-ref 显示 id）
	stSmall     // <sup>、<sub>：按正文 75% 字号显示（同脚注标签）；MyGo 的排版片段没有基线偏移，不做上下错位
	stKbd       // <kbd>：按键样式（等宽、较深底色与圆角）
)

// run 是一段可见原文。src 偏移相对所在行（片段）的起点，块整体平移时投影仍然有效。
// atomic 为 true 时显示文本是 [srcA, srcB) 的替换（如实体解码），映射时作为整体。
type run struct {
	srcA, srcB int
	disp       int // 在显示文本中的 UTF-16 起点
	dispByte   int // 在显示文本中的字节起点
	st         style
	atomic     bool
}

// projection 是一个叶子块（段落、标题、表格单元格）的显示投影：原文去掉隐藏标记后的
// 文本及其映射。显示文本是独立副本，不引用整篇原文，缓存它不会让旧版本原文常驻。
type projection struct {
	text   string
	runs   []run
	u16    int // 显示文本的 UTF-16 长度
	objs   []imgObj
	colors []ui.Color // 行内 HTML 指定的文字颜色（<span style="color:…">、<font color>），由样式高位引用
}

// objChar 是行内图片在显示文本中的占位字符（U+FFFC），布局时按图片尺寸成为行内对象。
const objChar = "\uFFFC"

// imgObj 是投影中的一张行内图片：u16 为占位字符的显示 UTF-16 偏移；src、width、alt 取自
// Markdown 图片或 <img> 标签（width 为原样属性值，如 "600"、"60%"，空表示原始尺寸）。
type imgObj struct {
	u16             int
	src, width, alt string
	// html 非空时对象是 HTML 块预览或注释卡片（见 htmlpreview.go），src 等字段不用。
	html *htmlBlockPreview
	// render 非空时对象是公式或 Mermaid 图表（RenderMathInline 等），src 为 TeX 或 Mermaid 源码。
	render string
}

// expansion 描述投影时哪些标记就地展开：caret 为相对片段起点的光标字节偏移，
// inBlock 为 false 表示光标不在本片段，所有可隐藏的标记都隐藏。
type expansion struct {
	inBlock bool
	caret   int
}

// leafOpts 是叶子投影的附加规则。
type leafOpts struct {
	// prefix 为 true 时，片段开头到第一个行内节点之前的原文（标题的 # 前缀）
	// 以及末个行内节点之后的原文（Setext 下划线）按标记处理。
	prefix bool
	// callout 为 true 时，开头的 [!TYPE] 及其后的换行按标记处理。
	callout bool
	// rawCode 为 true 时行内代码按原文显示（反引号可见、无代码样式），对应 inlineCodeRenderingEnabled 关闭。
	rawCode bool
	// noRender 为 true 时行内公式按原文显示（代码样式），不作为渲染对象（没有渲染器时）。
	noRender bool
}

// projectLeaf 为解析器块 idx（原文区间 [start, end)）生成投影。
func projectLeaf(d *Document, idx, start, end int, ex expansion, o leafOpts) *projection {
	src := d.Source()[start:end]
	pb := projBuilder{src: src, ex: ex, p: &projection{}, rawCode: o.rawCode, noRender: o.noRender}
	ins := d.md.Inlines(idx)
	if len(ins) == 0 {
		pb.emit(0, len(src), 0, true)
		return pb.finish()
	}
	from := 0
	if o.callout {
		if n := calloutMarkerLen(src); n > 0 {
			// 标记行与其后的换行只在光标位于本片段时显示。
			z := n
			for z < len(src) && (src[z] == '\n' || src[z] == '\r') {
				z++
			}
			pb.emit(0, n, stMarker, ex.inBlock)
			pb.emit(n, z, 0, false)
			from = z
			for from < len(src) && (src[from] == ' ' || src[from] == '>' || src[from] == '\t') {
				from++
			}
			pb.emit(z, from, 0, false)
		}
	}
	first := 0
	for first < len(ins) && ins[first].End-start <= from {
		first = ins[first].Next
	}
	if o.prefix {
		pb.emit(from, ins[0].Start-start, stMarker, ex.inBlock)
	}
	pb.inlines(ins, first, len(ins), start, 0)
	pb.emit(pb.pos, len(src), stMarker, ex.inBlock || !o.prefix)
	return pb.finish()
}

// projectIdentity 为原样行生成一一对应的投影（布局与映射统一走 projection）。
func projectIdentity(text string) *projection {
	pb := projBuilder{src: text, p: &projection{}}
	pb.emit(0, len(text), 0, true)
	return pb.finish()
}

type projBuilder struct {
	src      string
	ex       expansion
	rawCode  bool // 见 leafOpts.rawCode
	noRender bool // 见 leafOpts.noRender
	p        *projection
	pos      int // 已处理到的原文偏移（相对片段起点）
	b        strings.Builder
	u16      int
}

// emit 处理 [a, z)：可见时追加为显示片段，否则跳过。a 之前未被任何节点认领的
// 原文按可见处理，保证不会有原文意外消失；但换行之后的容器前缀（空格、Tab、>）
// 属于外层结构，始终隐藏。
func (pb *projBuilder) emit(a, z int, st style, visible bool) {
	a, z = clamp(a, pb.pos, len(pb.src)), clamp(z, pb.pos, len(pb.src))
	if a > pb.pos {
		g := pb.pos
		if g > 0 && pb.src[g-1] == '\n' {
			for g < a && (pb.src[g] == ' ' || pb.src[g] == '\t' || pb.src[g] == '>') {
				g++
			}
		}
		if a > g {
			pb.add(g, a, st&^stMarker, "")
		}
	}
	if z > a && visible {
		pb.add(a, z, st, "")
	}
	if z > pb.pos {
		pb.pos = z
	}
}

// replace 把 [a, z) 显示为 text（整体映射）。
func (pb *projBuilder) replace(a, z int, text string, st style) {
	pb.emit(a, a, st, true)
	if z <= pb.pos {
		return
	}
	pb.add(pb.pos, z, st, text)
	pb.pos = z
}

func (pb *projBuilder) add(a, z int, st style, repl string) {
	atomic := repl != ""
	if n := len(pb.p.runs); n > 0 && !atomic {
		last := &pb.p.runs[n-1]
		if last.srcB == a && last.st == st && !last.atomic {
			last.srcB = z
			pb.write(pb.src[a:z])
			return
		}
	}
	pb.p.runs = append(pb.p.runs, run{srcA: a, srcB: z, disp: pb.u16, dispByte: pb.b.Len(), st: st, atomic: atomic})
	if atomic {
		pb.write(repl)
	} else {
		pb.write(pb.src[a:z])
	}
}

func (pb *projBuilder) write(s string) {
	pb.b.WriteString(s)
	pb.u16 += u16Len(s)
}

func (pb *projBuilder) finish() *projection {
	pb.p.text = pb.b.String()
	pb.p.u16 = pb.u16
	return pb.p
}

// object 把图片节点 [a, z) 显示为一个行内对象占位字符。
func (pb *projBuilder) object(a, z int, o imgObj, st style) {
	pb.emit(a, a, st, true)
	o.u16 = pb.u16
	pb.replace(a, z, objChar, st|stImage)
	pb.p.objs = append(pb.p.objs, o)
}

// htmlAttr 返回 HTML 开始标签中属性 name 的值（实体已解码），没有时返回空串。属性名不区分大小写，
// 值可带单引号、双引号或不带引号。
func htmlAttr(tag, name string) string {
	s := tag[1 : len(tag)-1]
	i := strings.IndexAny(s, " \t\n/")
	if i < 0 {
		return ""
	}
	s = s[i:]
	for len(s) > 0 {
		s = strings.TrimLeft(s, " \t\r\n/")
		n := 0
		for n < len(s) && !strings.ContainsRune(" \t\r\n=/>", rune(s[n])) {
			n++
		}
		if n == 0 {
			return ""
		}
		key := s[:n]
		s = strings.TrimLeft(s[n:], " \t\r\n")
		val := ""
		if strings.HasPrefix(s, "=") {
			s = strings.TrimLeft(s[1:], " \t\r\n")
			if len(s) > 0 && (s[0] == '"' || s[0] == '\'') {
				end := strings.IndexByte(s[1:], s[0])
				if end < 0 {
					return ""
				}
				val, s = s[1:1+end], s[2+end:]
			} else {
				end := strings.IndexAny(s, " \t\r\n>")
				if end < 0 {
					end = len(s)
				}
				val, s = s[:end], s[end:]
			}
		}
		if strings.EqualFold(key, name) {
			return html.UnescapeString(val)
		}
	}
	return ""
}

// expanded 报告光标是否触及 [a, z]（含两端，光标贴着标记也展开）。
func (pb *projBuilder) expanded(a, z int) bool {
	return pb.ex.inBlock && pb.ex.caret >= a && pb.ex.caret <= z
}

// inside 报告光标是否严格位于 (a, z) 内。图片只在此时展开原文：光标贴在图片前后（方向键经过、
// 点击图片旁边）时仍显示图片，避免大图反复变成原文造成版面跳动；点击图片本身进入原文（imageHit）。
func (pb *projBuilder) inside(a, z int) bool {
	return pb.ex.inBlock && pb.ex.caret > a && pb.ex.caret < z
}

// htmlStyles 是原版可编辑 HTML 中按样式渲染的行内标签。
var htmlStyles = map[string]style{
	"strong": stBold, "b": stBold, "em": stItalic, "i": stItalic,
	"u": stUnderline, "ins": stUnderline, "s": stStrike, "del": stStrike, "strike": stStrike,
	"mark": stMark, "code": stCode, "span": 0, "font": 0, "sup": stSmall, "sub": stSmall, "kbd": stKbd, "a": stLink,
}

// htmlTag 解析单个 HTML 标签，返回小写标签名与是否为结束、自闭合标签。
func htmlTag(s string) (name string, closing, selfClosing, ok bool) {
	if len(s) < 3 || s[0] != '<' || s[len(s)-1] != '>' {
		return "", false, false, false
	}
	body := s[1 : len(s)-1]
	if strings.HasPrefix(body, "/") {
		closing, body = true, body[1:]
	}
	if strings.HasSuffix(body, "/") {
		selfClosing, body = true, strings.TrimSuffix(body, "/")
	}
	n := 0
	for n < len(body) && (body[n] >= 'a' && body[n] <= 'z' || body[n] >= 'A' && body[n] <= 'Z' || body[n] >= '0' && body[n] <= '9') {
		n++
	}
	if n == 0 {
		return "", false, false, false
	}
	return strings.ToLower(body[:n]), closing, selfClosing, true
}

// inlines 投影 ins[i:end]，base 为片段起点的绝对偏移。同级 HTML 开始标签为其后的
// 兄弟节点叠加样式，直到对应的结束标签。
func (pb *projBuilder) inlines(ins []parser.Inline, i, end, base int, st style) {
	var stack []struct {
		name string
		st   style
	}
	cur := st
	for i < end {
		in := ins[i]
		a, z := in.Start-base, in.End-base
		switch in.Kind {
		case parser.Text:
			pb.text(a, z, len(in.Text) != z-a, cur)
		case parser.SoftBreak, parser.LineBreak:
			// 反斜杠硬换行的“\”按标记处理；换行之后的容器前缀属于外层结构，始终隐藏。
			nl := strings.IndexByte(pb.src[a:z], '\n')
			if nl < 0 {
				pb.emit(a, z, cur, true)
				break
			}
			if pb.src[a] == '\\' {
				pb.emit(a, a+1, cur|stMarker, pb.ex.inBlock)
			}
			pb.emit(a+nl, a+nl+1, cur, true)
			pb.emit(a+nl+1, z, cur, false)
		case parser.Code:
			if pb.rawCode {
				pb.emit(a, z, cur, true)
				break
			}
			n := 0
			for a+n < z && pb.src[a+n] == '`' {
				n++
			}
			show := pb.expanded(a, z)
			pb.emit(a, a+n, cur|stMarker, show)
			pb.emit(a+n, z-n, cur|stCode, true)
			pb.emit(z-n, z, cur|stMarker, show)
		case parser.Image:
			if in.Dest != "" && !pb.inside(a, z) {
				var alt strings.Builder
				for j := i + 1; j < in.Next; j++ {
					if ins[j].Kind == parser.Text {
						alt.WriteString(ins[j].Text)
					}
				}
				pb.object(a, z, imgObj{src: in.Dest, alt: alt.String()}, cur)
				break
			}
			fallthrough
		case parser.Emphasis, parser.Strong, parser.Strikethrough, parser.Link:
			if i+1 >= in.Next {
				pb.emit(a, z, cur|stRaw, true)
				break
			}
			ca, cz := ins[i+1].Start-base, 0
			for j := i + 1; j < in.Next; j = ins[j].Next {
				cz = ins[j].End - base
			}
			show := pb.expanded(a, z)
			inner := cur | kindStyle(in.Kind)
			pb.emit(a, ca, inner|stMarker, show)
			pb.inlines(ins, i+1, in.Next, base, inner)
			pb.emit(cz, z, inner|stMarker, show)
		case parser.HTML:
			src := pb.src[a:z]
			if strings.HasPrefix(src, "<!--") {
				pb.emit(a, z, cur|stComment, true)
				break
			}
			name, closing, selfClosing, ok := htmlTag(src)
			show := pb.expanded(a, z)
			switch {
			case ok && name == "img" && !closing && !pb.inside(a, z) && htmlAttr(src, "src") != "":
				pb.object(a, z, imgObj{src: htmlAttr(src, "src"), width: htmlAttr(src, "width"), alt: htmlAttr(src, "alt")}, cur)
			case ok && name == "br":
				if show {
					pb.emit(a, z, cur|stMarker, true)
				} else {
					pb.replace(a, z, "\n", cur)
				}
			case ok && closing:
				for k := len(stack) - 1; k >= 0; k-- {
					if stack[k].name == name {
						cur = stack[k].st
						stack = stack[:k]
						break
					}
				}
				pb.emit(a, z, cur|stMarker, show)
			case ok && !selfClosing:
				if add, known := htmlStyles[name]; known {
					stack = append(stack, struct {
						name string
						st   style
					}{name, cur})
					pb.emit(a, z, cur|stMarker, show)
					cur |= add
					if c, ok := tagColor(name, src); ok {
						cur = cur&^stColorMask | pb.colorIndex(c)<<stColorShift
					}
					break
				}
				pb.emit(a, z, cur|stRaw, true)
			default:
				pb.emit(a, z, cur|stRaw, true)
			}
		case parser.Math:
			// 原版只认 $…$ 与 $$…$$；\[…\]、\(…\) 在原版是转义的括号，按转义显示。
			if src := pb.src[a:z]; len(src) >= 4 && src[0] == '\\' && (src[1] == '[' || src[1] == '(') {
				pb.emit(a, a+1, cur|stMarker, pb.ex.inBlock)
				pb.emit(a+1, z-2, cur, true)
				pb.emit(z-2, z-1, cur|stMarker, pb.ex.inBlock)
				pb.emit(z-1, z, cur, true)
				break
			}
			// $…$ 与 $$…$$：光标严格位于原文内时显示原文，否则显示渲染结果（原版 math_inline）。
			if pb.noRender {
				pb.emit(a, z, cur|stCode, true)
				break
			}
			if tex, kind := inlineMath(pb.src[a:z]); tex != "" && !pb.inside(a, z) {
				pb.object(a, z, imgObj{render: kind, src: tex, alt: tex}, cur)
				break
			}
			pb.emit(a, z, cur|stRaw, true)
		case parser.FootnoteRef:
			// 光标触及时显示原文 [^label]，否则显示标签（整体映射，光标不能停在标签中间）。
			if pb.expanded(a, z) || in.Text == "" {
				pb.emit(a, z, cur|stMarker, true)
			} else {
				pb.replace(a, z, in.Text, cur|stFootRef)
			}
		default:
			pb.emit(a, z, cur|stRaw, true)
		}
		i = in.Next
	}
}

// text 处理文本节点；escaped 表示其中有反斜杠转义或实体。转义用的“\”只在光标
// 位于本片段时显示；实体在光标触及时显示原文，否则显示解码结果。
func (pb *projBuilder) text(a, z int, escaped bool, st style) {
	if !escaped {
		pb.emit(a, z, st, true)
		return
	}
	for k := a; k < z; k++ {
		switch c := pb.src[k]; {
		case c == '\\' && k+1 < z && isASCIIPunct(pb.src[k+1]):
			pb.emit(k, k+1, st|stMarker, pb.ex.inBlock)
			k++
		case c == '&':
			n, decoded := parser.Entity(pb.src[k:z])
			if n == 0 {
				continue
			}
			if pb.expanded(k, k+n) {
				pb.emit(k, k+n, st|stMarker, true)
			} else {
				pb.replace(k, k+n, decoded, st)
			}
			k += n - 1
		}
	}
	pb.emit(z, z, st, true)
}

func kindStyle(k parser.InlineKind) style {
	switch k {
	case parser.Emphasis:
		return stItalic
	case parser.Strong:
		return stBold
	case parser.Strikethrough:
		return stStrike
	case parser.Link:
		return stLink
	case parser.Image:
		return stImage
	}
	return 0
}

func isASCIIPunct(c byte) bool {
	return c >= '!' && c <= '/' || c >= ':' && c <= '@' || c >= '[' && c <= '`' || c >= '{' && c <= '~'
}

// calloutTypes 与原版 calloutTypes.ts 一致：类型、默认标题；下标即配色与图标（tones / icons.Callout）。
var calloutTypes = []struct{ name, label string }{
	{"note", "Note"}, {"tip", "Tip"}, {"important", "Important"}, {"warning", "Warning"}, {"caution", "Caution"},
}

// calloutAliases 是 Obsidian 等常用的扩展类型，按语义归入上面的五种配色；默认标题为类型名首字母大写。
var calloutAliases = map[string]int{
	"abstract": 0, "summary": 0, "tldr": 0, "info": 0, "todo": 0, "quote": 0, "cite": 0,
	"hint": 1, "success": 1, "check": 1, "done": 1,
	"question": 2, "help": 2, "faq": 2, "example": 2,
	"attention": 3,
	"failure":   4, "fail": 4, "missing": 4, "danger": 4, "error": 4, "bug": 4,
}

// calloutHeader 是提示块首行 [!TYPE]±  标题 的解析结果：tone 为配色序号，fold 为 '+'（可折叠，默认展开）、'-'
// （默认收起）或 0（不可折叠），title 为自定义标题（空表示用默认标题），label 为默认标题。
type calloutHeader struct {
	tone         int
	fold         byte
	title, label string
}

// parseCalloutHeader 解析段落开头的提示块标记行；不是提示块时 ok 为 false。未知类型按 note 处理（同 Obsidian）。
func parseCalloutHeader(src string) (h calloutHeader, ok bool) {
	n := calloutMarkerLen(src)
	if n == 0 {
		return h, false
	}
	end := strings.IndexByte(src, ']')
	name := strings.ToLower(src[2:end])
	h.tone, h.label = 0, strings.ToUpper(name[:1])+name[1:]
	for i, t := range calloutTypes {
		if t.name == name {
			h.tone, h.label = i, t.label
		}
	}
	if i, ok := calloutAliases[name]; ok {
		h.tone = i
	}
	rest := src[end+1 : n]
	if rest != "" && (rest[0] == '+' || rest[0] == '-') {
		h.fold, rest = rest[0], rest[1:]
	}
	h.title = strings.TrimSpace(rest)
	return h, true
}

// ParseCallout 解析提示块标记行（[!TYPE]±  标题），供导出使用：tone 为配色序号（0 note … 4 caution），typ 为
// 配色对应的类型名，title 为显示标题（自定义标题，没有时为默认标题）。不是提示块时 ok 为 false。
func ParseCallout(src string) (tone int, typ, title string, ok bool) {
	h, ok := parseCalloutHeader(src)
	if !ok {
		return 0, "", "", false
	}
	title = h.title
	if title == "" {
		title = h.label
	}
	return h.tone, calloutTypes[h.tone].name, title, true
}

// calloutType 返回段落开头 [!TYPE] 的配色序号，不是提示块时返回 -1。
func calloutType(src string) int {
	if h, ok := parseCalloutHeader(src); ok {
		return h.tone
	}
	return -1
}

// calloutMarkerLen 返回开头标记行（[!TYPE]、可选的 +/- 与同一行的自定义标题，不含换行）的字节长度，没有时返回 0。
func calloutMarkerLen(src string) int {
	if !strings.HasPrefix(src, "[!") {
		return 0
	}
	end := strings.IndexByte(src, ']')
	if end < 3 || strings.ContainsAny(src[2:end], " \n") {
		return 0
	}
	if nl := strings.IndexByte(src, '\n'); nl >= 0 {
		return nl
	}
	return len(src)
}

// toDisp 把片段内原文偏移映射为显示 UTF-16 偏移；落在隐藏标记内时取其后的
// 第一个可见位置，落在整体替换内部时取替换的起点。
func (p *projection) toDisp(srcOff int) int {
	k := sort.Search(len(p.runs), func(k int) bool { return p.runs[k].srcB >= srcOff })
	if k == len(p.runs) {
		return p.u16
	}
	r := p.runs[k]
	if srcOff <= r.srcA {
		return r.disp
	}
	if r.atomic {
		if srcOff < r.srcB {
			return r.disp
		}
		return r.disp + u16Len(p.text[r.dispByte:p.runEnd(k)])
	}
	off := srcOff - r.srcA
	return r.disp + u16Len(p.text[r.dispByte:r.dispByte+off])
}

func (p *projection) runEnd(k int) int {
	if k+1 < len(p.runs) {
		return p.runs[k+1].dispByte
	}
	return len(p.text)
}

// toSrc 把显示 UTF-16 偏移映射回片段内原文偏移。位于两个片段交界时，
// preferEnd 为 true 取前一片段末尾，否则取后一片段开头。
func (p *projection) toSrc(u int, preferEnd bool) int {
	if len(p.runs) == 0 {
		return 0
	}
	k := sort.Search(len(p.runs), func(k int) bool { return p.runs[k].disp > u }) - 1
	if k < 0 {
		return p.runs[0].srcA
	}
	if preferEnd && k > 0 && p.runs[k].disp == u {
		return p.runs[k-1].srcB
	}
	r := p.runs[k]
	if r.atomic {
		if u == r.disp {
			return r.srcA
		}
		return r.srcB
	}
	off := u16ToByte(p.text[r.dispByte:p.runEnd(k)], u-r.disp)
	return r.srcA + off
}

// inlineMath 返回行内公式原文的 TeX 内容与渲染种类（$$…$$ 为行间样式）；不是 $ 定界的公式时返回空串。
func inlineMath(src string) (string, string) {
	switch {
	case len(src) >= 4 && strings.HasPrefix(src, "$$") && strings.HasSuffix(src, "$$"):
		return strings.TrimSpace(src[2 : len(src)-2]), RenderMathDisplay
	case len(src) >= 2 && src[0] == '$' && src[len(src)-1] == '$':
		return strings.TrimSpace(src[1 : len(src)-1]), RenderMathInline
	}
	return "", ""
}

// footnoteHit 报告 pt 是否点在显示为标签的脚注引用上，是则返回对应脚注定义的内容起点（原版点击引用跳到定义）。
// 引用正以原文显示（光标触及）或找不到定义时返回 false，点击按普通文字处理。
func (e *Editor) footnoteHit(pt ui.Point) (int, bool) {
	pos := e.hit(pt)
	s, ok := e.segAt(pos)
	if !ok || s.kind != rowText {
		return 0, false
	}
	r := &e.entries[s.i].rows[s.k]
	label := ""
	for _, run := range s.proj.runs {
		if run.st&stFootRef != 0 && s.base+run.srcA <= pos && pos <= s.base+run.srcB {
			for _, in := range e.Doc.md.Inlines(e.Doc.blocks[s.i].Index + r.rel) {
				if in.Kind == parser.FootnoteRef && in.Start == s.base+run.srcA {
					label = in.Text
				}
			}
		}
	}
	if label == "" {
		return 0, false
	}
	for _, b := range e.Doc.blocks {
		if pb := e.Doc.md.Block(b.Index); pb.Kind == parser.FootnoteDefinition && strings.EqualFold(pb.Label(), label) {
			if first, ok := e.Doc.firstChildStart(b.Index); ok {
				return first, true
			}
			return b.Start, true
		}
	}
	return 0, false
}

// colorIndex 返回颜色在投影颜色表中的下标 + 1（相同颜色复用），表满（255 种）时返回 0（默认颜色）。
func (pb *projBuilder) colorIndex(c ui.Color) style {
	for i, x := range pb.p.colors {
		if x == c {
			return style(i + 1)
		}
	}
	if len(pb.p.colors) >= 255 {
		return 0
	}
	pb.p.colors = append(pb.p.colors, c)
	return style(len(pb.p.colors))
}

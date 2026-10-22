package mdedit

import (
	"sort"
	"strings"

	"github.com/egoist/mygo/ui/markdown/parser"
)

// style 是显示片段的样式位。
type style uint16

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
	text string
	runs []run
	u16  int // 显示文本的 UTF-16 长度
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
}

// projectLeaf 为解析器块 idx（原文区间 [start, end)）生成投影。
func projectLeaf(d *Document, idx, start, end int, ex expansion, o leafOpts) *projection {
	src := d.Source()[start:end]
	pb := projBuilder{src: src, ex: ex, p: &projection{}}
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
	src string
	ex  expansion
	p   *projection
	pos int // 已处理到的原文偏移（相对片段起点）
	b   strings.Builder
	u16 int
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

// expanded 报告光标是否触及 [a, z]（含两端，光标贴着标记也展开）。
func (pb *projBuilder) expanded(a, z int) bool {
	return pb.ex.inBlock && pb.ex.caret >= a && pb.ex.caret <= z
}

// htmlStyles 是原版可编辑 HTML 中按样式渲染的行内标签。
var htmlStyles = map[string]style{
	"strong": stBold, "b": stBold, "em": stItalic, "i": stItalic,
	"u": stUnderline, "ins": stUnderline, "s": stStrike, "del": stStrike, "strike": stStrike,
	"mark": stMark, "code": stCode, "span": 0, "sup": 0, "sub": 0, "kbd": stCode, "a": stLink,
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
			n := 0
			for a+n < z && pb.src[a+n] == '`' {
				n++
			}
			show := pb.expanded(a, z)
			pb.emit(a, a+n, cur|stMarker, show)
			pb.emit(a+n, z-n, cur|stCode, true)
			pb.emit(z-n, z, cur|stMarker, show)
		case parser.Emphasis, parser.Strong, parser.Strikethrough, parser.Link, parser.Image:
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
			pb.emit(a, z, cur|stRaw, true)
		default: // FootnoteRef：P3 渲染，当前原样显示
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

// calloutTypes 与原版 calloutTypes.ts 一致：类型、默认标题。
var calloutTypes = []struct{ name, label string }{
	{"note", "Note"}, {"tip", "Tip"}, {"important", "Important"}, {"warning", "Warning"}, {"caution", "Caution"},
}

// calloutType 返回段落开头 [!TYPE] 的类型序号，不是提示块时返回 -1。
func calloutType(src string) int {
	n := calloutMarkerLen(src)
	if n == 0 {
		return -1
	}
	name := strings.ToLower(src[2 : n-1])
	for i, t := range calloutTypes {
		if t.name == name {
			return i
		}
	}
	return -1
}

// calloutMarkerLen 返回开头 [!TYPE] 标记的字节长度，没有时返回 0。
func calloutMarkerLen(src string) int {
	if !strings.HasPrefix(src, "[!") {
		return 0
	}
	end := strings.IndexByte(src, ']')
	if end < 3 || strings.ContainsAny(src[2:end], " \n") {
		return 0
	}
	return end + 1
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

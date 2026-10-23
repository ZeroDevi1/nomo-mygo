package mdedit

import (
	"encoding/binary"
	"fmt"
	"hash/maphash"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown/parser"
)

// 块组件版式，取自原版 document-content.css / editor-document.css 的默认值。
const (
	listIndent    = 26 // 列表项内容相对列表的缩进
	quoteIndent   = 20 // 引用条 4 + 内边距 16
	quotePad      = 8  // 引用上下内边距
	blockMargin   = 18 // 引用、提示块、代码块、表格的上下外边距
	codePadX      = 14
	codePadY      = 10
	cellPadX      = 10
	cellPadY      = 6
	calloutTitleH = 26
	taskBox       = 18 // 任务复选框边长，约 1.15em
)

// 超长行分段：排版耗时随文字长度超线性增长（实测等宽 13 px、宽 700：4 KB 约 1.5 ms，16 KB 约 15 ms，
// 64 KB 约 200 ms），超过 maxRowBytes 的原文行拆成多个行片段分别排版，视觉上在片段边界处换行。
// 超过 maxLeafBytes（约 5000 个汉字的单段，正常文档罕见）的段落/标题不做行内投影，按原样行（分段）显示，
// 避免单段落整段重排；投影排版的单段上限因此约为 15 ms。
const (
	maxRowBytes  = 4096
	maxLeafBytes = 16 << 10
)

type rowKind uint8

const (
	rowText rowKind = iota // 叶子块的投影（段落、标题、表格单元格）
	rowLine                // 原文的一行（代码块、HTML 块、公式块、front matter、分割线）
)

// rowRole 决定行的字体与默认颜色。
type rowRole uint8

const (
	roleBody rowRole = iota
	roleHeading
	roleCode
	roleHTML
	roleMath
	roleFront
	roleRule
	roleCellHead
)

// row 是块组件中的一个可布局片段。a、b 为相对所属顶层块起点的原文区间；
// rel 为叶子块在解析器中相对顶层块的序号（顶层块内部结构不变时保持有效）。
type row struct {
	kind  rowKind
	role  rowRole
	level int // 标题级别
	rel   int
	a, b  int
	// 几何：indent 为文字相对内容区左侧的缩进；表格单元格另有 col/ncol，
	// 实际 x 与宽度在布局时按内容宽度计算。band 相同的行共享一条水平带（表格行）。
	indent    float32
	col, ncol int
	align     ui.Align
	band      int
	before    float32 // 所在水平带之上的间距（取带内首行）
	after     float32 // 所在水平带之下的间距（取带内末行）
	padTop    float32 // 文字在行框内的上下留白（单元格、代码块首末行）
	padBottom float32
	callout   bool // 段落以 [!TYPE] 开头
	quoteFg   bool // 位于引用内：使用引用文字色
	tone      int  // 位于提示块内：提示类型序号，否则 -1
	// 列表项标记，只在项的第一行设置：marker 为“•”或“3.”，task 为 0/1/2
	// （无、未选、已选），taskAt 为“[ ]”中间字符相对顶层块的偏移。
	marker string
	task   parser.Task
	taskAt int
	// fence 为 true 表示代码块的围栏行（```lang / ```），只在光标位于代码块
	// [blockA, blockB]（相对顶层块）内时显示，否则高度为 0，与原版只显示语言标签一致。
	fence          bool
	blockA, blockB int
	// htmlImg 为 true 表示只含图片的 HTML 块：整块一行，光标严格位于块内时显示原文，否则显示图片。
	htmlImg bool
	// htmlKind 非 previewNone 表示可预览的 HTML 块或注释块（见 htmlpreview.go），显示规则同 htmlImg。
	htmlKind uint8
	// lineNo 为代码块中代码行的行号（从 1 起，0 表示不编号），由 split 按显示行号设置写入（applyLineNumbers）。
	lineNo int32
	// rinset 为折叠块正文的右内缩（DIP），由 split 按所在层级设置，rowBox 从可用宽度中扣除。
	rinset float32
	// empty 为 true 表示空列表项或空引用的占位行（没有叶子块），编辑时按文本行处理。
	empty bool
	// 表格单元格：cellX、cellW 为所在列相对表格左边的位置与宽度，shift 为表格横向滚动偏移（见 layoutTables）。
	cellX, cellW, shift float32
	// shapeW 为当前布局排版时的文字宽度；行宽变化（列宽重算、窗口缩放）时布局作废。
	shapeW float32
	// cont 为 true 表示超长原文行的后续片段（见 maxRowBytes），行首不是原文的行首。
	cont bool

	hash uint64 // 原文与几何的哈希，编辑后据此复用布局

	// 代码行的 Tokenizer 状态：hlIn 为行首状态，hlOut 为按 hlIn 切分本行后的状态；
	// hlKnown 为 false 表示尚未求出。见 highlight.go。
	hlIn, hlOut uint64
	hlKnown     bool
	// 行内图片：objSizes 为排版时各图片的显示尺寸，objRects 为其在布局中的位置（相对文字原点），
	// 均与 proj.objs 一一对应；imgGen 为排版时的图片缓存代数，变化后重新计算尺寸。
	objSizes [][2]float32
	objRects []ui.Rect
	imgGen   uint64

	proj     *projection
	layout   *lineLayout
	gen      uint32
	sig      uint64
	height   float32 // 行框高度（含 padTop/padBottom）；measured 为 false 时是估算值
	measured bool
	bytes    int
	used     uint64
	y        float32 // 行框顶部相对块顶部的偏移，由 layoutRows 计算
	bandH    float32
}

// frameKind 是跨多行的装饰。
type frameKind uint8

const (
	frameQuote frameKind = iota
	frameCallout
	frameCode
	frameTable
	frameFront
	frameRule
)

// frame 覆盖 rows[first..last]，indent 为其左边缘相对内容区的缩进。
type frame struct {
	kind        frameKind
	first, last int
	indent      float32
	rinset      float32 // 折叠块正文的右内缩，绘制宽度与表格可用宽度从中扣除
	tone        int     // 提示块类型
	lang        string
	// 表格（frameTable）：cols 为各列宽度，tableW 为总宽，viewW 为可见宽度（内容区去掉缩进），scrollX 为
	// 横向滚动偏移（0 ≤ scrollX ≤ tableW−viewW），由 layoutTables 计算、scrollTable 修改。
	cols                   []float32
	tableW, viewW, scrollX float32
	// tok 为代码块语言的 Tokenizer（nil 不高亮）；hlUpTo 为已求出 Tokenizer 状态的行号上界
	// （rows[first, hlUpTo) 有效），块重新展开时从 first 开始。
	tok    editor.Tokenizer
	hlUpTo int
}

// blockBuilder 把一个顶层块展开为行与装饰。
type blockBuilder struct {
	d      *Document
	top    Block
	src    string // 整篇原文，只在构建期间使用
	seed   maphash.Seed
	rows   []row
	frames []frame
	band   int
	// pending 是下一行的额外上间距（容器开头的外边距与内边距）。
	pending float32
}

// container 描述行所处的容器上下文。
type container struct {
	indent  float32
	quote   bool
	tone    int
	tight   bool
	inList  bool
	nesting int // 列表嵌套层数，决定项目符号样式
}

func buildRows(d *Document, ti int, top Block, seed maphash.Seed) ([]row, []frame) {
	bb := &blockBuilder{d: d, top: top, src: d.Source(), seed: seed}
	if d.details().marker(ti) != nil {
		// 折叠块标记：整块一行，收起时为标题行或卡片底边（detailsview.go），光标严格位于块内时显示原文。
		bb.add(row{kind: rowLine, role: roleHTML, htmlKind: previewDetails, a: 0, b: top.End - top.Start, tone: -1, after: paraAfter / 2})
	} else if top.Kind == KindToc {
		// 整个目录区一行：收起时为目录卡片（见 htmlpreview.go），光标严格位于区内时显示原文。
		z := top.End
		bb.pending += 8
		bb.add(row{kind: rowLine, role: roleHTML, htmlKind: previewToc, a: 0, b: z - top.Start, tone: -1})
		bb.closePad(blockMargin)
	} else if top.Kind == KindFrontMatter {
		f := len(bb.rows)
		bb.pending += codePadY
		bb.lines(top.Start, top.End, roleFront, container{tone: -1}, codePadX)
		bb.frames = append(bb.frames, frame{kind: frameFront, first: f, last: len(bb.rows) - 1})
		bb.closePad(codePadY + blockMargin)
	} else {
		bb.block(top.Index, container{tone: -1})
	}
	for k := range bb.rows {
		bb.rows[k].hash = bb.hash(&bb.rows[k])
	}
	return bb.rows, bb.frames
}

// hash 概括行的原文与几何；逐字段写入字节，避免每次按键为数千行做格式化分配。
func (bb *blockBuilder) hash(r *row) uint64 {
	var h maphash.Hash
	h.SetSeed(bb.seed)
	h.WriteString(bb.src[bb.top.Start+r.a : bb.top.Start+r.b])
	var flags uint32
	for i, on := range []bool{r.callout, r.quoteFg, r.fence, r.empty} {
		if on {
			flags |= 1 << i
		}
	}
	b := [...]uint32{uint32(r.kind), uint32(r.role), uint32(r.level), math.Float32bits(r.indent), uint32(r.col), uint32(r.ncol),
		uint32(r.align), uint32(int32(r.tone)), math.Float32bits(r.padTop), math.Float32bits(r.padBottom), flags, uint32(r.htmlKind)}
	var buf [4 * len(b)]byte
	for i, v := range b {
		binary.LittleEndian.PutUint32(buf[4*i:], v)
	}
	h.Write(buf[:])
	return h.Sum64()
}

func (bb *blockBuilder) add(r row) int {
	r.before += bb.pending
	bb.pending = 0
	r.band = bb.band
	bb.band++
	bb.rows = append(bb.rows, r)
	return len(bb.rows) - 1
}

// closePad 把容器的下内边距加到最后一行之下。
func (bb *blockBuilder) closePad(pad float32) {
	if n := len(bb.rows); n > 0 {
		bb.rows[n-1].after += pad
	}
}

func (bb *blockBuilder) rel(abs int) int { return abs - bb.top.Start }

// paraSpacing 是段落之间的间距：紧凑列表内 4，其余 12。
func paraSpacing(c container) float32 {
	if c.inList && c.tight {
		return 4
	}
	return paraAfter
}

func (bb *blockBuilder) block(i int, c container) {
	blk := bb.d.md.Block(i)
	b := &blk
	base := row{rel: i - bb.top.Index, a: bb.rel(b.Start), b: bb.rel(b.End), indent: c.indent, quoteFg: c.quote, tone: c.tone}
	switch b.Kind {
	case parser.Paragraph, parser.Heading:
		if b.End-b.Start > maxLeafBytes {
			bb.lines(b.Start, b.End, roleBody, c, 0)
			bb.closePad(paraSpacing(c))
			return
		}
		if b.Kind == parser.Paragraph {
			base.callout = c.tone >= 0 && bb.isFirstInCallout(i)
			base.after = paraSpacing(c)
			bb.add(base)
			return
		}
		base.role, base.level = roleHeading, b.Level
		base.before = headingBefore(b.Level)
		base.after = headingAfter(b.Level)
		bb.add(base)
	case parser.ThematicBreak:
		f := len(bb.rows)
		bb.pending += 8
		bb.lines(b.Start, b.End, roleRule, c, 0)
		bb.frames = append(bb.frames, frame{kind: frameRule, first: f, last: len(bb.rows) - 1, indent: c.indent})
		bb.closePad(8 + paraAfter)
	case parser.CodeBlock, parser.HTMLBlock, parser.MathBlock:
		role := map[parser.Kind]rowRole{parser.CodeBlock: roleCode, parser.HTMLBlock: roleHTML, parser.MathBlock: roleMath}[b.Kind]
		f := len(bb.rows)
		// 公式块与 Mermaid 代码块整块一行：收起时显示渲染结果（居中），光标严格位于块内时显示原文。
		if kind := renderBlockKind(b); kind != previewNone {
			z := b.Start + len(strings.TrimRight(bb.src[b.Start:b.End], "\r\n"))
			bb.pending += blockMargin - paraAfter
			bb.add(row{kind: rowLine, role: role, htmlKind: kind, align: ui.Center, a: bb.rel(b.Start), b: bb.rel(z), indent: c.indent, quoteFg: c.quote, tone: c.tone})
			bb.closePad(blockMargin)
			return
		}
		if b.Kind == parser.CodeBlock {
			bb.pending += blockMargin - paraAfter + codePadY
			bb.lines(b.Start, b.End, role, c, codePadX)
			if b.Fenced {
				for _, k := range []int{f, len(bb.rows) - 1} {
					bb.rows[k].fence, bb.rows[k].blockA, bb.rows[k].blockB = true, bb.rel(b.Start), bb.rel(b.End)
				}
			}
			bb.frames = append(bb.frames, frame{kind: frameCode, first: f, last: len(bb.rows) - 1, indent: c.indent, lang: b.Language(), tok: codeTokenizer(b.Language())})
			bb.closePad(codePadY + blockMargin)
		} else if align, ok := htmlImagesOnly(bb.src[b.Start:b.End]); ok && b.Kind == parser.HTMLBlock {
			// 只含图片的 HTML 块（原版设置宽度后的 <img>、README 的 <p align="center"><img></p>）
			// 作为一行显示图片，光标进入时展开为原文。
			z := b.Start + len(strings.TrimRight(bb.src[b.Start:b.End], "\r\n"))
			bb.add(row{kind: rowLine, role: roleHTML, htmlImg: true, align: align, a: bb.rel(b.Start), b: bb.rel(z), indent: c.indent, quoteFg: c.quote, tone: c.tone})
			bb.closePad(paraSpacing(c))
		} else if kind := htmlBlockKind(bb.src[b.Start:b.End]); kind != previewNone && b.Kind == parser.HTMLBlock {
			z := b.Start + len(strings.TrimRight(bb.src[b.Start:b.End], "\r\n"))
			bb.add(row{kind: rowLine, role: roleHTML, htmlKind: kind, a: bb.rel(b.Start), b: bb.rel(z), indent: c.indent, quoteFg: c.quote, tone: c.tone})
			bb.closePad(paraSpacing(c))
		} else {
			bb.lines(b.Start, b.End, role, c, 0)
			bb.closePad(paraSpacing(c))
		}
	case parser.Quote:
		tone := -1
		if first, ok := bb.firstChild(i); ok && bb.d.md.Block(first).Kind == parser.Paragraph {
			tone = calloutType(bb.src[bb.d.md.Block(first).Start:])
		}
		inner := c
		inner.indent += quoteIndent
		inner.quote = tone < 0
		inner.tone = tone
		inner.inList, inner.tight = false, false
		f := len(bb.rows)
		bb.pending += blockMargin - paraAfter + quotePad
		if tone >= 0 {
			bb.pending += calloutTitleH
		}
		for ch := range bb.d.md.Children(i) {
			bb.block(ch, inner)
		}
		if len(bb.rows) == f {
			bb.emptyRow(b.End, inner)
		}
		if n := len(bb.rows); n > 0 {
			bb.rows[n-1].after = 0
		}
		kind := frameQuote
		if tone >= 0 {
			kind = frameCallout
		}
		bb.frames = append(bb.frames, frame{kind: kind, first: f, last: len(bb.rows) - 1, indent: c.indent, tone: tone})
		bb.closePad(quotePad + blockMargin)
	case parser.List:
		inner := c
		inner.indent += listIndent
		inner.inList, inner.tight = true, b.Tight
		inner.nesting = c.nesting + 1
		n := 0
		for item := range bb.d.md.Children(i) {
			bb.item(item, b, n, inner)
			n++
		}
		if !c.inList {
			bb.closePad(paraAfter - paraSpacing(inner))
		}
	case parser.Table:
		bb.table(i, b, c)
	case parser.FootnoteDefinition:
		inner := c
		inner.indent += listIndent
		f := len(bb.rows)
		for ch := range bb.d.md.Children(i) {
			bb.block(ch, inner)
		}
		if len(bb.rows) > f {
			bb.rows[f].marker = "[^" + b.Label() + "]"
		}
	default:
		bb.lines(b.Start, b.End, roleBody, c, 0)
	}
}

func (bb *blockBuilder) firstChild(i int) (int, bool) {
	for ch := range bb.d.md.Children(i) {
		return ch, true
	}
	return 0, false
}

// isFirstInCallout 报告段落 i 是否为所在引用的第一个子块（承载 [!TYPE]）。
func (bb *blockBuilder) isFirstInCallout(i int) bool {
	p := bb.d.md.Block(i).Parent
	first, ok := bb.firstChild(p)
	return ok && first == i && bb.d.md.Block(p).Kind == parser.Quote
}

func (bb *blockBuilder) item(i int, list *parser.Block, n int, c container) {
	b := bb.d.md.Block(i)
	f := len(bb.rows)
	for ch := range bb.d.md.Children(i) {
		bb.block(ch, c)
	}
	if len(bb.rows) == f {
		// 空列表项（只有标记）：给一个空行承载光标。
		bb.emptyRow(b.End, c)
	}
	r := &bb.rows[f]
	if list.Ordered {
		r.marker = fmt.Sprintf("%d%c", list.Number+n, list.Marker)
	} else {
		r.marker = [...]string{"•", "◦", "▪"}[(c.nesting-1)%3]
	}
	if b.Task != parser.NoTask {
		r.task = b.Task
		if k := strings.IndexByte(bb.src[b.Start:b.End], '['); k >= 0 {
			r.taskAt = bb.rel(b.Start + k + 1)
		}
	}
	if !c.tight {
		bb.rows[len(bb.rows)-1].after = paraAfter
	}
}

// emptyRow 为没有叶子块的列表项或引用生成占位行，承载光标；区间覆盖标记后的
// 尾随空白，光标落在其中任一位置都属于这一行。
func (bb *blockBuilder) emptyRow(at int, c container) {
	z := trailingEnd(bb.src, at)
	bb.add(row{kind: rowLine, empty: true, a: bb.rel(z), b: bb.rel(z), indent: c.indent, quoteFg: c.quote, tone: c.tone, after: paraSpacing(c)})
}

// lines 为 [start, end) 的每一行生成原样行，去掉行首属于外层容器的前缀。
func (bb *blockBuilder) lines(start, end int, role rowRole, c container, padX float32) {
	s := bb.src[start:end]
	// 原样块（代码、HTML、公式、front matter）的缩进属于内容，续行只去掉容器前缀：引用标记加上
	// 与首行同列的空格（列表内容列与围栏缩进）。其余块沿用按列表层级估算的缩进上限。
	spaces := int(c.indent/listIndent)*4 + 4
	if role == roleCode || role == roleHTML || role == roleMath || role == roleFront {
		ls := strings.LastIndexByte(bb.src[:start], '\n') + 1
		head := bb.src[ls:start]
		q := quotePrefixLen(head, c)
		spaces = len(head) - q - len(strings.TrimLeft(head[q:], " "))
	}
	for a := 0; a <= len(s); {
		z := strings.IndexByte(s[a:], '\n')
		if z < 0 {
			z = len(s)
		} else {
			z += a
		}
		skip := a
		if a > 0 {
			skip = a + prefixLen(s[a:z], c, spaces)
		}
		for cont, from := false, start+skip; ; cont = true {
			to := start + z
			if to-from > maxRowBytes {
				to = chunkCut(bb.src, from, from+maxRowBytes)
			}
			bb.add(row{kind: rowLine, role: role, cont: cont, a: bb.rel(from), b: bb.rel(to), indent: c.indent + padX, quoteFg: c.quote, tone: c.tone})
			if to >= start+z {
				break
			}
			from = to
		}
		a = z + 1
	}
}

// renderBlockKind 返回需要渲染显示的块种类：公式块、Mermaid 围栏代码块（已闭合），其余返回 previewNone。
func renderBlockKind(b *parser.Block) uint8 {
	switch {
	case b.Kind == parser.MathBlock:
		return previewMath
	case b.Kind == parser.CodeBlock && b.Fenced && strings.EqualFold(b.Language(), "mermaid"):
		return previewMermaid
	}
	return previewNone
}

// chunkCut 返回超长行片段 [from, limit) 的切分点：优先取窗口末 256 字节内最后一个空白之后，否则退到
// limit 之前的字符边界。保证 from < 返回值 <= limit。
func chunkCut(src string, from, limit int) int {
	for k := limit; k > max(from+1, limit-256); k-- {
		if c := src[k-1]; c == ' ' || c == '\t' {
			return k
		}
	}
	k := limit
	for k > from+1 && !utf8.RuneStart(src[k]) {
		k--
	}
	return k
}

// prefixLen 返回续行开头属于容器的前缀长度：引用的“>”（含其后一个空格）与至多 spaces 个缩进空格。
func prefixLen(line string, c container, spaces int) int {
	n := quotePrefixLen(line, c)
	for k := 0; k < spaces && n < len(line) && line[n] == ' '; k++ {
		n++
	}
	return n
}

// quotePrefixLen 返回行首引用标记（“>”及其后一个空格，可夹杂空格、可多层）的长度；不在引用或提示块内时为 0。
func quotePrefixLen(line string, c container) int {
	if !c.quote && c.tone < 0 {
		return 0
	}
	n, end := 0, 0
	for n < len(line) {
		switch line[n] {
		case '>':
			n++
			if n < len(line) && line[n] == ' ' {
				n++
			}
			end = n
		case ' ':
			n++
		default:
			return end
		}
	}
	return end
}

func (bb *blockBuilder) table(i int, t *parser.Block, c container) {
	ncol := max(1, len(t.Align))
	f := len(bb.rows)
	bb.pending += blockMargin - paraAfter
	for tr := range bb.d.md.Children(i) {
		row0 := len(bb.rows)
		header := bb.d.md.Block(tr).Header
		col := 0
		for cell := range bb.d.md.Children(tr) {
			cb := bb.d.md.Block(cell)
			r := row{rel: cell - bb.top.Index, a: bb.rel(cb.Start), b: bb.rel(cb.End), indent: c.indent, col: col, ncol: ncol, tone: -1, padTop: cellPadY, padBottom: cellPadY}
			if header {
				r.role = roleCellHead
			}
			if col < len(t.Align) {
				r.align = map[parser.Align]ui.Align{parser.AlignCenter: ui.Center, parser.AlignRight: ui.End}[t.Align[col]]
			}
			r.before = bb.pending
			r.band = bb.band
			bb.rows = append(bb.rows, r)
			col++
		}
		bb.pending = 0
		if len(bb.rows) > row0 {
			bb.band++
		}
	}
	if len(bb.rows) > f {
		bb.frames = append(bb.frames, frame{kind: frameTable, first: f, last: len(bb.rows) - 1, indent: c.indent})
		bb.closePad(blockMargin)
	}
}

func headingBefore(level int) float32 {
	switch level {
	case 1:
		return 4
	case 2:
		return 30 - paraAfter
	}
	return 20 - paraAfter
}

func headingAfter(level int) float32 {
	if level == 1 {
		return 22
	}
	return paraAfter
}

// rowFont 返回行的字体，按编辑器字号缩放（scale = 正文字号 / 16 × 缩放比例），
// 字体族取自 SetFonts（空串为系统字体与系统等宽字体）。
func (e *Editor) rowFont(r *row) ui.Font {
	var f ui.Font
	switch r.role {
	case roleHeading:
		f = ui.Font{Family: e.fontBody, Size: headingSize(r.level), Weight: 700}
	case roleCode, roleMath, roleFront, roleHTML:
		f = ui.Font{Family: e.monoFont(), Size: 13}
	case roleCellHead:
		f = ui.Font{Family: e.fontBody, Size: bodySize, Weight: 600}
	default:
		f = ui.Font{Family: e.fontBody, Size: bodySize}
	}
	f.Size *= e.scale
	return f
}

// rowLead 返回行的行框高度（DIP，对应 CSS line-height），0 表示沿用字体原生行高。与原版样式一致：正文、列表、
// 引用与表格单元格用设置的行高倍数（--md-editor-line-height），标题固定 1.2；代码、公式、HTML 等原文行保持原生行高。
func (e *Editor) rowLead(r *row, f ui.Font) float32 {
	switch {
	case r.role == roleHeading:
		return f.Size * 1.2
	case (r.role == roleBody || r.role == roleCellHead) && (r.kind == rowText || r.empty):
		return f.Size * e.lineMul
	}
	return 0
}

func (e *Editor) monoFont() string {
	if e.fontMono != "" {
		return e.fontMono
	}
	return monoFamily
}

// headingSize 取自原版：h1 32、h2 26，其余按比例递减。
func headingSize(level int) float32 {
	return [...]float32{bodySize, 32, 26, 21, 18, 16, 15}[clamp(level, 0, 6)]
}

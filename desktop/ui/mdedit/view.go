package mdedit

import (
	"hash/maphash"
	"slices"
	"sort"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown/parser"
)

// 版式常量。正文字号与内容宽度沿用现版本默认值（16 px、约 760 px），
// P1 起改由主题令牌提供。
const (
	bodySize     = 16
	maxContentW  = 760
	minPadX      = 24
	padTop       = 32
	paraAfter    = 12
	monoFamily   = "monospace"
	scrollbarHit = 12
	// defaultCacheBudget 是单个编辑器布局缓存的估算字节上限。经验值：
	// 一屏约 30 块，布局按 96 B/UTF-16 码元估算，8 MB 约可容纳 8 万字的布局，
	// 足够覆盖可见区上下数屏，超出时按最近使用淘汰。
	defaultCacheBudget = 8 << 20
)

// entry 是一个顶层块的视图缓存，与 Document.blocks 按下标一一对应。
// 顶层块首次进入布局时由 buildRows 展开为行（split）；之前只有估算高度。
type entry struct {
	rows   []row
	frames []frame
	split  bool
	// height 为块总高度；含未布局行的估算值，用于滚动条与阅读锚点。
	height float32
	ydirty bool // 行高变化后需要重新计算 rows[].y 与 height
	// tablesW 为计算表格列宽时的内容区宽度，变化后重算。
	tablesW float32
	used    uint64
}

// vseg 是一个可见行：y 为行框顶部相对元素顶部的位置。
type vseg struct {
	i, k int
	y    float32
}

type vblock struct {
	i   int
	top float32
}

type tone struct{ border, fg, bg ui.Color }

// Colors 是外壳按颜色主题（desktop/ui/themes 的令牌）提供的配色；零值字段沿用默认配色。
// Tones 依次为 note、tip、important、warning、caution 提示块的边框、文字与背景。
type Colors struct {
	Background, Text, Heading, Muted, Link, Selection              ui.Color
	QuoteBar, QuoteText, QuoteBackground                           ui.Color
	CodeBackground, CodeText, CodeBorder, TableBorder, TableHeader ui.Color
	Border, Surface, AccentFill                                    ui.Color
	ToneBorder, ToneText, ToneBackground                           [5]ui.Color
}

// palette 取自原版默认主题（themeRegistry.ts 的 defaultLight/DarkTokens），
// P1 主题令牌 Go 化后改为读取用户主题。
type palette struct {
	text, heading, muted, link, selection, caret, scrollbar, background ui.Color
	quoteBar, quoteFg, quoteBg                                          ui.Color
	codeBg, codeFg, codeBorder                                          ui.Color
	tableBorder, tableHead                                              ui.Color
	border, surface, accentFill, mark, found, foundCurrent              ui.Color
	tones                                                               [5]tone
}

// apply 用主题配色覆盖默认配色中非零的项。
func (p *palette) apply(c *Colors) {
	set := func(dst *ui.Color, v ui.Color) {
		if v != (ui.Color{}) {
			*dst = v
		}
	}
	set(&p.background, c.Background)
	set(&p.text, c.Text)
	set(&p.caret, c.Text)
	set(&p.heading, c.Heading)
	set(&p.muted, c.Muted)
	set(&p.link, c.Link)
	set(&p.selection, c.Selection)
	set(&p.quoteBar, c.QuoteBar)
	set(&p.quoteFg, c.QuoteText)
	p.quoteBg = c.QuoteBackground
	set(&p.codeBg, c.CodeBackground)
	set(&p.codeFg, c.CodeText)
	set(&p.codeBorder, c.CodeBorder)
	set(&p.tableBorder, c.TableBorder)
	set(&p.tableHead, c.TableHeader)
	set(&p.border, c.Border)
	set(&p.surface, c.Surface)
	set(&p.accentFill, c.AccentFill)
	for i := range p.tones {
		set(&p.tones[i].border, c.ToneBorder[i])
		set(&p.tones[i].fg, c.ToneText[i])
		p.tones[i].bg = c.ToneBackground[i]
	}
}

func paletteFor(t *ui.Theme) palette {
	h := ui.Hex
	if t.Dark {
		return palette{
			text: h("#eef2f5"), heading: h("#ffffff"), muted: h("#9aa4af"), link: h("#79c9d8"),
			selection: ui.RGBA(103, 183, 165, 0.28), caret: h("#eef2f5"), scrollbar: t.Scrollbar, background: t.Background,
			quoteBar: h("#6f938b"), quoteFg: h("#c8d8d4"),
			codeBg: h("#232a32"), codeFg: h("#edf3f6"), codeBorder: h("#333b46"),
			tableBorder: h("#333b46"), tableHead: h("#252c35"),
			border: h("#333b46"), surface: h("#1d2229"), accentFill: h("#2f7d6f"), mark: ui.RGBA(212, 167, 44, 0.35),
			found: ui.RGBA(212, 167, 44, 0.28), foundCurrent: ui.RGBA(255, 150, 50, 0.6),
			tones: [5]tone{{h("#3b82f6"), h("#93c5fd"), ui.Color{}}, {h("#22c55e"), h("#86efac"), ui.Color{}}, {h("#a855f7"), h("#d8b4fe"), ui.Color{}}, {h("#f59e0b"), h("#fcd34d"), ui.Color{}}, {h("#ef4444"), h("#fca5a5"), ui.Color{}}},
		}
	}
	return palette{
		text: h("#202428"), heading: h("#171a1f"), muted: h("#68707a"), link: h("#116d8f"),
		selection: ui.RGBA(47, 125, 111, 0.18), caret: h("#202428"), scrollbar: t.Scrollbar, background: t.Background,
		quoteBar: h("#7f9f97"), quoteFg: h("#465753"),
		codeBg: h("#f5f6f8"), codeFg: h("#383a42"), codeBorder: h("#e0e2e6"),
		tableBorder: h("#dfe3e8"), tableHead: h("#f0f2f4"),
		border: h("#dfe3e8"), surface: h("#ffffff"), accentFill: h("#2f7d6f"), mark: ui.RGBA(255, 214, 10, 0.35),
		found: ui.RGBA(255, 214, 10, 0.4), foundCurrent: ui.RGBA(255, 150, 50, 0.65),
		tones: [5]tone{{h("#3b82f6"), h("#1e40af"), ui.Color{}}, {h("#22c55e"), h("#166534"), ui.Color{}}, {h("#a855f7"), h("#6b21a8"), ui.Color{}}, {h("#f59e0b"), h("#92400e"), ui.Color{}}, {h("#ef4444"), h("#991b1b"), ui.Color{}}},
	}
}

// Editor 把 Document 绑定到窗口中的一个元素：可见区布局、绘制、命中测试与输入。
//
// 生命周期：NewEditor 后在视图函数中每帧调用 View；标签转入后台时调用
// ReleaseLayouts 释放布局缓存（保留行高与阅读锚点）；丢弃即释放，不持有
// 后台任务、定时器或回调。并发：只在 UI 线程使用。
//
// 不变量：len(entries) == len(Doc.Blocks())；cacheBytes 等于各行 bytes 之和。
type Editor struct {
	Doc *Document
	// CacheBudget 是布局缓存的估算字节上限，0 表示 defaultCacheBudget。
	CacheBudget int
	// Intercept 在编辑器处理按键之前调用，返回 true 表示已被外壳处理（如快捷插入菜单打开时
	// 的上下键、回车与 Esc）；nil 表示不拦截。只在 UI 线程调用。
	Intercept func(ev ui.InputEvent) bool
	// PasteData 在粘贴且剪贴板含图片或文件时调用，返回 true 表示外壳已处理（插入图片），
	// 否则按文本粘贴。
	PasteData func(d transfer.Data) bool

	entries    []entry
	cacheBytes int
	tick       uint64
	layoutGen  uint32
	seed       maphash.Seed

	// 阅读锚点：anchor 块顶部位于视口顶部之上 anchorOff DIP。以块为锚，
	// 估算高度被实测值替换时视口不跳动。
	anchor    int
	anchorOff float32

	viewW, viewH float32
	contentW     float32
	dirty        bool
	reveal       bool
	vsegs        []vseg
	// prefetchedTick 是空闲预排版完成时的帧号；视口或内容变化（tick 递增）后重新预排。
	prefetchedTick uint64
	vblocks        []vblock
	caret          ui.Rect
	caretOK        bool

	// gap* 缓存 gapCaret 的结果，见该方法。
	gapHead, gapAfter int
	gapVer            uint64
	gapOK, gapValid   bool

	goalX   float32
	hasGoal bool
	focused bool
	drag    dragMode
	dragY   float32
	dragPos float32

	// highlights 是查找结果（字节区间，升序），current 为当前项下标，-1 表示无；
	// 由外壳的查找栏设置，编辑后由外壳重新查找。
	highlights [][2]int
	current    int

	pal palette
	// syntax 为代码块的语法配色（随明暗主题切换），tokBuf 为切分代码行的复用缓冲。
	syntax *editor.Theme
	tokBuf []editor.Token
	dark   bool
	themed bool
	// colors 为外壳提供的主题配色（nil 用默认），classic 为经典块样式（引用与提示块为
	// 左边框加底色，对应原版 documentStyleId = nomo-classic）。
	colors  *Colors
	classic bool
	// fontBody / fontMono 为正文与等宽字体族（空串为系统默认），对应原版 documentFontFamily /
	// monoFontFamily。
	fontBody, fontMono string
	lineH              float32 // 正文一行高度，用于间隙光标与空行
	// scale 为字号缩放（正文字号 / 16 × 界面缩放），widthPct 为内容宽度占编辑区宽度的
	// 百分比（0 表示固定最大 760）；由 SetStyle 设置，变化时全部布局作废。
	scale    float32
	widthPct float32
	svc      ui.Services
	hasSvc   bool

	// images 为共用的图片缓存（nil 时行内图片显示为替代文字框），baseDir 为解析相对图片路径的
	// 文档目录（未保存的新文档为空）；由外壳通过 SetImages 设置。
	images  *Images
	baseDir string
	labels  Labels
	imgSeen uint64 // 上次绘制时的图片缓存代数
	// tocVer/tocHash 为上次检查目录卡片时的原文版本与标题摘要；标题变化后目录卡片重新排版。
	tocVer  uint64
	tocHash uint64
}

type dragMode uint8

const (
	dragNone dragMode = iota
	dragText
	dragScrollbar
)

// NewEditor 创建绑定 doc 的编辑器视图。
func NewEditor(doc *Document) *Editor {
	e := &Editor{Doc: doc, seed: maphash.MakeSeed(), dirty: true, anchorOff: -padTop, current: -1}
	e.scale = 1
	e.measureLine()
	e.entries = make([]entry, len(doc.blocks))
	for i := range e.entries {
		e.entries[i] = e.estimate(doc.blocks[i])
	}
	return e
}

// View 在当前作用域中构建编辑区元素，元素填满父容器。
func (e *Editor) View(c *ui.Context) ui.Element {
	t := c.Theme()
	e.setTheme(t)
	e.svc, e.hasSvc = c.Services(), true
	box := ui.Box(c).Fill().Clip().Background(e.pal.background).
		Role(ui.RoleTextField).Label("Markdown").Cursor(ui.CursorText).
		HandleTextInput(e).HandleInput(e.input).Draw(e.draw)
	e.focused = box.Focused()
	return box
}

// ReleaseLayouts 释放全部布局与投影，保留行高和阅读锚点；下次绘制时按需重建。
func (e *Editor) ReleaseLayouts() {
	for i := range e.entries {
		for k := range e.entries[i].rows {
			r := &e.entries[i].rows[k]
			r.proj, r.layout, r.bytes, r.objRects, r.objSizes = nil, nil, 0, nil, nil
		}
	}
	e.cacheBytes = 0
	e.dirty = true
}

// SetStyle 设置正文字号（DIP）、缩放百分比与内容宽度百分比，与原版设置 fontSize、
// zoomPercent、contentWidthPercent 对应；值未变时不做任何事。
func (e *Editor) SetStyle(fontSize, zoomPercent, widthPercent float32) {
	scale := fontSize / bodySize * zoomPercent / 100
	if scale == e.scale && widthPercent == e.widthPct {
		return
	}
	e.scale, e.widthPct = scale, widthPercent
	e.measureLine()
	e.contentW, e.viewW = 0, 0 // 下次 prepare 按新宽度重算
	e.layoutGen++
	e.dirty = true
}

func (e *Editor) measureLine() {
	m := ui.Font{Family: e.fontBody, Size: bodySize * e.scale}.Metrics()
	e.lineH = m.Ascent + m.Descent + m.LineGap
}

// CacheBytes 返回布局缓存的估算字节数（诊断用）。
func (e *Editor) CacheBytes() int { return e.cacheBytes }

// SetColors 设置主题配色与块样式；与当前值相同时不做任何事。
func (e *Editor) SetColors(c *Colors, classic bool) {
	if e.colors != nil && c != nil && *e.colors == *c && e.classic == classic {
		return
	}
	e.colors, e.classic, e.themed = c, classic, false
	e.dirty = true
}

// SetFonts 设置正文与等宽字体族（空串为系统默认）；变化时全部布局作废。
func (e *Editor) SetFonts(body, mono string) {
	if body == e.fontBody && mono == e.fontMono {
		return
	}
	e.fontBody, e.fontMono = body, mono
	e.measureLine()
	e.layoutGen++
	e.dirty = true
}

func (e *Editor) setTheme(t *ui.Theme) {
	if e.themed && e.dark == t.Dark {
		return
	}
	e.themed, e.dark = true, t.Dark
	e.pal = paletteFor(t)
	e.syntax = editor.DefaultTheme(t)
	if c := e.colors; c != nil {
		e.pal.apply(c)
	}
	// 颜色写入了布局的 Span，换主题时旧布局作废。
	e.layoutGen++
	e.dirty = true
}

func (e *Editor) width() float32 {
	if e.contentW > 0 {
		return e.contentW
	}
	return maxContentW
}

// textWidth 按字符数估算文本宽度：宽字符约 1 em、窄字符约 0.55 em。
func textWidth(s string, fs float32) float32 {
	wide, narrow := 0, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 0xE0:
			wide++
		case c < 0x80:
			narrow++
		}
	}
	return float32(wide)*fs + float32(narrow)*fs*0.55
}

// estimate 估算未展开块的高度，只用于屏幕外的块，进入可见区后由实测高度替换。
func (e *Editor) estimate(b Block) entry {
	s := e.Doc.Source()[b.Start:b.End]
	lines := 1
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines++
		}
	}
	fs := float32(bodySize)
	if b.Kind == parser.Heading {
		fs = 26
	}
	lines += int(textWidth(s, fs) / e.width())
	h := float32(lines)*fs*1.4 + paraAfter
	switch b.Kind {
	case parser.CodeBlock, parser.Table, parser.Quote, KindFrontMatter, KindToc:
		h += 2 * blockMargin
	}
	return entry{height: h}
}

// applyEdit 按编辑结果替换受影响块的视图缓存并调整阅读锚点。单块内的编辑按行哈希
// 复用未变行的布局，大代码块或长列表中按键只重排被编辑的行。
func (e *Editor) applyEdit(ed Edit) {
	if !ed.Applied {
		return
	}
	var reuse []row
	var oldFrames []frame
	if ed.Removed == 1 && ed.Inserted == 1 && e.entries[ed.First].split {
		reuse, oldFrames = e.entries[ed.First].rows, e.entries[ed.First].frames
	}
	for _, en := range e.entries[ed.First : ed.First+ed.Removed] {
		for _, r := range en.rows {
			e.cacheBytes -= r.bytes
		}
	}
	fresh := make([]entry, ed.Inserted)
	for k := range fresh {
		fresh[k] = e.estimate(e.Doc.blocks[ed.First+k])
	}
	e.entries = slices.Replace(e.entries, ed.First, ed.First+ed.Removed, fresh...)
	if reuse != nil {
		e.split(ed.First, reuse, oldFrames)
	}
	switch {
	case e.anchor >= ed.First+ed.Removed:
		e.anchor += ed.Inserted - ed.Removed
	case e.anchor >= ed.First:
		e.anchor = min(e.anchor, max(ed.First, ed.First+ed.Inserted-1))
	}
	e.hasGoal = false
	e.reveal = true
	e.dirty = true
}

// split 把顶层块 i 展开为行并估算行高；reuse 非空时按行哈希做前后缀匹配，
// 继承未变行的投影、布局与实测高度。
func (e *Editor) split(i int, reuse []row, oldFrames []frame) {
	en := &e.entries[i]
	top := e.Doc.blocks[i]
	rows, frames := buildRows(e.Doc, top, e.seed)
	// 同一块重新展开（块内编辑）时保留表格的横向滚动位置。
	for k := range frames {
		if k < len(oldFrames) && oldFrames[k].kind == frames[k].kind {
			frames[k].scrollX = oldFrames[k].scrollX
		}
	}
	e.layoutTables(e.Doc.Source(), top.Start, rows, frames, e.width())
	en.tablesW = e.width()
	for k := range rows {
		r := &rows[k]
		_, w := e.rowBox(r)
		f := e.rowFont(r)
		lines := 1 + int(textWidth(e.Doc.Source()[top.Start+r.a:top.Start+r.b], f.Size)/w)
		r.height = r.padTop + r.padBottom + float32(lines)*f.Size*1.4
	}
	if reuse != nil {
		p := 0
		for p < len(reuse) && p < len(rows) && reuse[p].hash == rows[p].hash {
			rows[p] = inherit(rows[p], reuse[p])
			p++
		}
		for s := 0; s < len(reuse)-p && s < len(rows)-p; s++ {
			o, n := reuse[len(reuse)-1-s], &rows[len(rows)-1-s]
			if o.hash != n.hash {
				break
			}
			*n = inherit(*n, o)
		}
		for _, r := range rows {
			e.cacheBytes += r.bytes
		}
	}
	en.rows, en.frames, en.split = rows, frames, true
	e.layoutRows(en)
}

// inherit 把旧行的缓存（投影、布局、实测高度）移到结构相同的新行上。
func inherit(n, o row) row {
	n.proj, n.layout, n.gen, n.sig = o.proj, o.layout, o.gen, o.sig
	n.height, n.measured, n.bytes, n.used = o.height, o.measured, o.bytes, o.used
	n.hlIn, n.hlOut, n.hlKnown = o.hlIn, o.hlOut, o.hlKnown
	n.objSizes, n.objRects, n.imgGen, n.shapeW = o.objSizes, o.objRects, o.imgGen, o.shapeW
	return n
}

// layoutRows 按水平带自上而下计算各行位置与块高度。
func (e *Editor) layoutRows(en *entry) {
	y := float32(0)
	for k := 0; k < len(en.rows); {
		band := en.rows[k].band
		y += en.rows[k].before
		j := k
		h := float32(0)
		for j < len(en.rows) && en.rows[j].band == band {
			h = max(h, en.rows[j].height)
			j++
		}
		for m := k; m < j; m++ {
			en.rows[m].y, en.rows[m].bandH = y, h
		}
		y += h + en.rows[j-1].after
		k = j
	}
	en.height = y
	en.ydirty = false
}

// rowBox 返回行文字相对内容区左侧的 x 与可用宽度。表格单元格按列均分，最小 90。
func (e *Editor) rowBox(r *row) (x, w float32) {
	cw := e.width()
	if r.ncol > 0 {
		return r.indent + r.cellX - r.shift + cellPadX, max(1, r.cellW-2*cellPadX)
	}
	w = cw - r.indent
	if r.role == roleCode || r.role == roleFront {
		w -= codePadX
	}
	return r.indent, max(40, w)
}

// expansionFor 返回行的展开状态：光标位于行内时就地展开光标处的标记。
func (e *Editor) expansionFor(top Block, r *row) expansion {
	h := e.Doc.Sel.Head - top.Start
	if h >= r.a && h <= r.b {
		return expansion{inBlock: true, caret: h - r.a}
	}
	return expansion{}
}

// signature 概括展开状态对投影的影响：光标不在行内为 0；在行内时由光标触及的
// 行内节点决定（含实体与转义所在的文本节点），在纯文本中移动不会让布局失效。
func (e *Editor) signature(top Block, r *row, ex expansion) uint64 {
	if !ex.inBlock {
		return 0
	}
	if r.kind != rowText {
		return 1
	}
	var h maphash.Hash
	h.SetSeed(e.seed)
	caret := top.Start + r.a + ex.caret
	var buf [8]byte
	for i, in := range e.Doc.md.Inlines(top.Index + r.rel) {
		if caret < in.Start || caret > in.End {
			continue
		}
		switch {
		case in.Kind == parser.Text && len(in.Text) != in.End-in.Start:
			off := caret - in.Start
			buf[4], buf[5], buf[6], buf[7] = byte(off), byte(off>>8), byte(off>>16), byte(off>>24)
			fallthrough
		case in.Kind != parser.Text && in.Kind != parser.SoftBreak:
			buf[0], buf[1], buf[2], buf[3] = byte(i), byte(i>>8), byte(i>>16), byte(i>>24)
			h.Write(buf[:])
		}
	}
	return h.Sum64() | 1
}

// ensure 返回块 i 的视图缓存，必要时展开为行；行布局由 ensureRow 按需生成。
func (e *Editor) ensure(i int) *entry {
	en := &e.entries[i]
	en.used = e.tick
	if !en.split {
		e.split(i, nil, nil)
	} else if en.tablesW != e.width() {
		// 内容区宽度变化：重算表格列宽（单元格行宽变化后，其布局在 ensureRow 中按 shapeW 作废）。
		e.layoutTables(e.Doc.Source(), e.Doc.blocks[i].Start, en.rows, en.frames, e.width())
		en.tablesW = e.width()
	}
	if en.ydirty {
		e.layoutRows(en)
	}
	return en
}

// fenceHidden 报告代码块围栏行当前是否隐藏（光标不在该代码块内）。
func (e *Editor) fenceHidden(top Block, r *row) bool {
	if !r.fence {
		return false
	}
	h := e.Doc.Sel.Head - top.Start
	return h < r.blockA || h > r.blockB
}

// ensureRow 确保块 i 第 k 行的投影与布局为最新，返回是否重建。
func (e *Editor) ensureRow(i, k int) bool {
	en := &e.entries[i]
	r := &en.rows[k]
	r.used = e.tick
	top := e.Doc.blocks[i]
	ex := e.expansionFor(top, r)
	if r.htmlImg || r.htmlKind != previewNone {
		// 图片块、HTML 预览与注释卡片只在光标严格位于块内时展开（与行内图片一致，见 projBuilder.inside）。
		ex.inBlock = ex.inBlock && ex.caret > 0 && ex.caret < r.b-r.a
	}
	sig := e.signature(top, r, ex)
	hidden := e.fenceHidden(top, r)
	// 代码行的着色取决于行首 Tokenizer 状态，状态变化（如上文开了块注释）时重排。
	var fr *frame
	var hlIn uint64
	if r.role == roleCode && !r.fence {
		if fr = codeFrame(en, k); fr != nil {
			hlIn = e.codeState(i, en, fr, k)
			sig |= (hlIn + 1) << 2
		}
	}
	if hidden {
		sig = 2
	}
	_, w := e.rowBox(r)
	p := r.proj
	var sizes [][2]float32
	if r.layout != nil && r.gen == e.layoutGen && r.sig == sig && r.shapeW == w {
		// 投影有效：只有行内图片的尺寸变化（加载完成）时按原投影重排。
		if len(p.objs) == 0 || r.imgGen == e.imageGen() {
			return false
		}
		r.imgGen = e.imageGen()
		if sizes = e.objSizes(p, w); slices.Equal(sizes, r.objSizes) {
			return false
		}
	} else {
		if r.kind == rowText {
			p = projectLeaf(e.Doc, top.Index+r.rel, top.Start+r.a, top.Start+r.b, ex, leafOpts{prefix: r.role == roleHeading, callout: r.callout})
		} else if r.htmlImg && !ex.inBlock {
			p = projectHTMLImages(e.Doc.Source()[top.Start+r.a : top.Start+r.b])
		} else if r.htmlKind != previewNone && !ex.inBlock {
			p = projectHTMLPreview(e.Doc.Source()[top.Start+r.a:top.Start+r.b], r.htmlKind)
		} else {
			p = projectIdentity(e.Doc.Source()[top.Start+r.a : top.Start+r.b])
		}
		r.imgGen = e.imageGen()
		sizes = e.objSizes(p, w)
	}
	font := e.rowFont(r)
	var spans []ui.Span
	if fr != nil && p.text != "" {
		e.tokBuf, _ = fr.tok.Tokenize(p.text, hlIn, e.tokBuf[:0])
		spans = e.codeSpans(p.text, e.tokBuf)
	} else {
		spans = e.spans(p, sizes)
	}
	l := ui.ShapeRichTextWith(spans, font, ui.TextShape{Width: w, Align: r.align})
	r.objSizes, r.objRects, r.shapeW = sizes, objRects(l, p), w
	_, lh := l.Size()
	m := font.Metrics()
	h := r.padTop + max(lh, m.Ascent+m.Descent+m.LineGap) + r.padBottom
	if hidden {
		h = 0
	}
	if h != r.height {
		en.ydirty = true
	}
	e.cacheBytes -= r.bytes
	r.proj, r.layout, r.gen, r.sig, r.height, r.measured = p, l, e.layoutGen, sig, h, true
	r.bytes = 512 + len(p.text) + 96*p.u16 + 48*max(len(p.runs), len(spans)) + 64*len(p.objs)
	for _, o := range p.objs {
		r.bytes += len(o.src) + len(o.alt)
		if o.html != nil && o.html.lay != nil {
			r.bytes += len(o.html.src) + o.html.lay.bytes
		}
	}
	e.cacheBytes += r.bytes
	return true
}

// spans 把投影转为排版片段；sizes 为各行内图片的显示尺寸（与 p.objs 一一对应）。
func (e *Editor) spans(p *projection, sizes [][2]float32) []ui.Span {
	spans := make([]ui.Span, 0, len(p.runs))
	obj := 0
	for k, r := range p.runs {
		sp := ui.Span{Text: p.text[r.dispByte:p.runEnd(k)]}
		if r.atomic && r.st&stImage != 0 && sp.Text == objChar && obj < len(sizes) {
			sp.ObjectWidth, sp.ObjectHeight = sizes[obj][0], sizes[obj][1]
			obj++
			spans = append(spans, sp)
			continue
		}
		if r.st&stBold != 0 {
			sp.Weight = 700
		}
		sp.Italic = r.st&(stItalic|stImage) != 0
		sp.Strikethrough = r.st&stStrike != 0
		sp.Underline = r.st&(stUnderline|stLink) != 0
		if r.st&stCode != 0 {
			sp.Font = e.monoFont()
			sp.Background = e.pal.codeBg
			sp.Radius = 3
		}
		if r.st&stMark != 0 {
			sp.Background = e.pal.mark
			sp.Radius = 2
		}
		if r.st&stFootRef != 0 {
			sp.Size = bodySize * e.scale * 0.75
			sp.Color, sp.Background, sp.Radius = e.pal.link, e.pal.codeBg, 3
		}
		switch {
		case r.st&(stMarker|stComment) != 0:
			sp.Color = e.pal.muted
		case r.st&stLink != 0:
			sp.Color = e.pal.link
		case r.st&stImage != 0:
			sp.Color = e.pal.muted
		}
		spans = append(spans, sp)
	}
	return spans
}

// rowColor 返回行的默认文字颜色。
func (e *Editor) rowColor(r *row) ui.Color {
	switch {
	case r.role == roleCode || r.role == roleMath:
		return e.pal.codeFg
	case r.role == roleHTML || r.role == roleFront:
		return e.pal.muted
	case r.role == roleHeading:
		return e.pal.heading
	case r.tone >= 0:
		return e.pal.tones[r.tone].fg
	case r.quoteFg:
		return e.pal.quoteFg
	}
	return e.pal.text
}

// ---- 片段：行的临时视图 ----

// seg 是一行的临时视图，只在当前调用中使用，不跨编辑保存。
type seg struct {
	i, k      int
	base, end int // 行在原文中的绝对字节区间
	x, w      float32
	padTop    float32
	layout    *ui.TextLayout
	proj      *projection
	kind      rowKind
}

func (e *Editor) segOf(i, k int) seg {
	en := e.ensure(i)
	k = clamp(k, 0, len(en.rows)-1)
	e.ensureRow(i, k)
	r := &en.rows[k]
	top := e.Doc.blocks[i]
	x, w := e.rowBox(r)
	return seg{i: i, k: k, base: top.Start + r.a, end: top.Start + r.b, x: x, w: w, padTop: r.padTop, layout: r.layout, proj: r.proj, kind: r.kind}
}

// segAt 返回包含字节偏移 pos 的行；pos 位于顶层块间隙时返回 false。pos 落在行之间
// 被隐藏的容器标记里时，取其后的行。
func (e *Editor) segAt(pos int) (seg, bool) {
	i, _ := e.Doc.BlockAt(pos)
	if i < 0 {
		return seg{}, false
	}
	en := e.ensure(i)
	off := pos - e.Doc.blocks[i].Start
	k, _ := slices.BinarySearchFunc(en.rows, off, func(r row, off int) int {
		if r.b < off {
			return -1
		}
		if r.a > off {
			return 1
		}
		return 0
	})
	return e.segOf(i, k), true
}

func (e *Editor) firstSeg(i int) seg { return e.segOf(i, 0) }

func (e *Editor) lastSeg(i int) seg {
	en := e.ensure(i)
	return e.segOf(i, len(en.rows)-1)
}

// neighbor 返回 s 之前（dir<0）或之后的行（文档顺序），跨越块边界。
func (e *Editor) neighbor(s seg, dir int) (seg, bool) {
	if nk := s.k + dir; nk >= 0 && nk < len(e.entries[s.i].rows) {
		return e.segOf(s.i, nk), true
	}
	ni := s.i + dir
	if ni < 0 || ni >= len(e.entries) {
		return seg{}, false
	}
	if dir < 0 {
		return e.lastSeg(ni), true
	}
	return e.firstSeg(ni), true
}

// neighborV 返回 s 上方（dir<0）或下方水平带中横向最接近 x（内容坐标）的行。
func (e *Editor) neighborV(s seg, dir int, x float32) (seg, bool) {
	pick := func(i, band int) (seg, bool) {
		rows := e.ensure(i).rows
		best := -1
		for k := range rows {
			if rows[k].band != band {
				continue
			}
			if rx, _ := e.rowBox(&rows[k]); best < 0 || rx-cellPadX <= x {
				best = k
			}
		}
		if best < 0 {
			return seg{}, false
		}
		return e.segOf(i, best), true
	}
	if s2, ok := pick(s.i, e.entries[s.i].rows[s.k].band+dir); ok {
		return s2, true
	}
	ni := s.i + dir
	if ni < 0 || ni >= len(e.entries) {
		return seg{}, false
	}
	rows := e.ensure(ni).rows
	b := rows[0].band
	if dir < 0 {
		b = rows[len(rows)-1].band
	}
	return pick(ni, b)
}

func (s seg) toDisp(pos int) int { return s.proj.toDisp(clamp(pos-s.base, 0, s.end-s.base)) }

func (s seg) toSrc(u int, preferEnd bool) int { return s.base + s.proj.toSrc(u, preferEnd) }

func (s seg) dispLen() int { return s.proj.u16 }

// caretIn 返回 pos 在行文字坐标中的光标矩形；空行布局没有行高时补足一行。
func (e *Editor) caretIn(s seg, pos int) ui.Rect {
	r := s.layout.Caret(s.toDisp(pos))
	if r.H <= 0 {
		r.H = e.lineH
	}
	return r
}

// ---- 视口 ----

// gapCaret 报告光标是否位于块间隙（空段落位置），以及间隙前一块的序号。
// 结果按（光标、原文版本）缓存，滚动条每帧对全部块求高度时不重复二分查找。
func (e *Editor) gapCaret() (after int, ok bool) {
	if !e.gapValid || e.gapHead != e.Doc.Sel.Head || e.gapVer != e.Doc.version {
		i, a := e.Doc.BlockAt(e.Doc.Sel.Head)
		e.gapAfter, e.gapOK = a, i < 0
		e.gapHead, e.gapVer, e.gapValid = e.Doc.Sel.Head, e.Doc.version, true
	}
	return e.gapAfter, e.gapOK
}

// height 返回块 i 的显示高度；光标位于其后的间隙时，额外留出一行放光标。
func (e *Editor) height(i int) float32 {
	h := e.entries[i].height
	if a, ok := e.gapCaret(); ok && a == i {
		h += e.lineH
	}
	return h
}

// topSpace 是文档首块之上的留白，光标位于首块之前的间隙时含一行。
func (e *Editor) topSpace() float32 {
	if a, ok := e.gapCaret(); ok && a < 0 {
		return padTop + e.lineH
	}
	return padTop
}

func (e *Editor) left() float32 {
	return max(minPadX, (e.viewW-e.contentW)/2)
}

// prepare 按视口尺寸准备可见行的布局与光标几何；状态未变时直接返回。
func (e *Editor) prepare(w, h float32) {
	if w != e.viewW || h != e.viewH {
		e.viewW, e.viewH = w, h
		cw := min(maxContentW*e.scale, w-2*minPadX)
		if e.widthPct > 0 {
			cw = min(w*e.widthPct/100, w-2*minPadX)
		}
		if cw = max(80, cw); cw != e.contentW {
			e.contentW = cw
			e.layoutGen++
		}
		e.dirty = true
	}
	if !e.dirty || w <= 0 {
		return
	}
	e.dirty = false
	e.tick++
	if e.reveal {
		e.reveal = false
		e.revealCaret()
	}
	e.normalize()
	e.vsegs, e.vblocks = e.vsegs[:0], e.vblocks[:0]
	y := -e.anchorOff
	for i := e.anchor; i < len(e.entries) && y < h; i++ {
		en := e.layoutVisible(i, y, h)
		e.vblocks = append(e.vblocks, vblock{i: i, top: y})
		for k := firstVisible(en, y); k < len(en.rows) && y+en.rows[k].y < h; k++ {
			if r := &en.rows[k]; y+r.y+r.bandH > 0 {
				e.vsegs = append(e.vsegs, vseg{i: i, k: k, y: y + r.y})
			}
		}
		y += e.height(i)
	}
	e.caret, e.caretOK = e.caretRect(e.Doc.Sel.Head)
	e.evict()
}

// layoutVisible 布局块 i 中落在视口内的行。行高变化会移动后续行，重复直到稳定
// （最多三轮，估算偏差通常一轮收敛）。
func (e *Editor) layoutVisible(i int, top, h float32) *entry {
	en := e.ensure(i)
	for iter := 0; iter < 3; iter++ {
		changed := false
		for k := firstVisible(en, top); k < len(en.rows) && top+en.rows[k].y < h; k++ {
			if r := &en.rows[k]; top+r.y+r.bandH > 0 && e.ensureRow(i, k) {
				changed = true
			}
		}
		if en.ydirty {
			e.layoutRows(en)
		}
		if !changed {
			break
		}
	}
	return en
}

// firstVisible 返回块（顶部位于 top）中第一个底边进入视口的行；行按 y 单调排列，
// 二分查找使大代码块每帧只访问可见行。
func firstVisible(en *entry, top float32) int {
	return sort.Search(len(en.rows), func(k int) bool { return top+en.rows[k].y+en.rows[k].bandH > 0 })
}

// sync 让 IME 查询看到最新状态：编辑或滚动之后、下一帧之前也能得到正确几何。
func (e *Editor) sync() { e.prepare(e.viewW, e.viewH) }

// normalize 把阅读锚点规整到 [块顶, 块底) 内并限制滚动范围；只处理途经的块。
func (e *Editor) normalize() {
	n := len(e.entries)
	if n == 0 {
		e.anchor, e.anchorOff = 0, -e.topSpace()
		return
	}
	e.anchor = clamp(e.anchor, 0, n-1)
	up := func() {
		for e.anchorOff < 0 && e.anchor > 0 {
			e.anchor--
			e.ensure(e.anchor)
			e.anchorOff += e.height(e.anchor)
		}
	}
	up()
	for e.anchor < n-1 {
		e.ensure(e.anchor)
		hh := e.height(e.anchor)
		if e.anchorOff < hh {
			break
		}
		e.anchorOff -= hh
		e.anchor++
	}
	// 允许末块滚到视口中部，再往下不滚。
	y := -e.anchorOff
	i := e.anchor
	for ; i < n && y < e.viewH; i++ {
		e.ensure(i)
		y += e.height(i)
	}
	if limit := e.viewH * 0.5; i == n && y < limit {
		e.anchorOff -= limit - y
		up()
	}
	if e.anchor == 0 && e.anchorOff < -e.topSpace() {
		e.anchorOff = -e.topSpace()
	}
}

func (e *Editor) visibleSeg(i, k int) (vseg, bool) {
	for _, v := range e.vsegs {
		if v.i == i && v.k == k {
			return v, true
		}
	}
	return vseg{}, false
}

// caretRect 返回字节偏移 pos 的光标矩形（元素坐标），不在可见区时返回 false。
func (e *Editor) caretRect(pos int) (ui.Rect, bool) {
	s, ok := e.segAt(pos)
	if !ok {
		// 间隙光标画在前一块之下（或首块之上）的空行处。
		_, after := e.Doc.BlockAt(pos)
		x := e.left()
		if len(e.entries) == 0 {
			return ui.Rect{X: x, Y: -e.anchorOff - e.lineH, W: 1.5, H: e.lineH}, true
		}
		for _, v := range e.vblocks {
			switch {
			case after < 0 && v.i == 0:
				return ui.Rect{X: x, Y: v.top - e.lineH, W: 1.5, H: e.lineH}, true
			case v.i == after:
				return ui.Rect{X: x, Y: v.top + e.entries[after].height, W: 1.5, H: e.lineH}, true
			}
		}
		return ui.Rect{}, false
	}
	v, ok := e.visibleSeg(s.i, s.k)
	if !ok {
		return ui.Rect{}, false
	}
	r := e.caretIn(s, pos)
	r.X += e.left() + s.x
	r.Y += v.y + s.padTop
	r.W = max(r.W, 1.5)
	return r, true
}

// revealCaret 滚动使光标可见。远距离跳转直接把锚点设到光标块，避免逐块布局。
func (e *Editor) revealCaret() {
	if len(e.entries) == 0 || e.viewH <= 0 {
		return
	}
	pos := e.Doc.Sel.Head
	var bi int
	var cy, ch float32 // 光标相对块 bi 顶部的位置与高度
	if s, ok := e.segAt(pos); ok {
		e.revealCell(s)
		s = e.segOf(s.i, s.k)
		r := e.caretIn(s, pos)
		en := e.ensure(s.i)
		bi, cy, ch = s.i, en.rows[s.k].y+s.padTop+r.Y, r.H
	} else if _, after := e.Doc.BlockAt(pos); after >= 0 {
		bi, cy, ch = after, e.entries[after].height, e.lineH
	} else {
		bi, cy, ch = 0, -e.lineH, e.lineH
	}
	const margin = 24
	if bi < e.anchor || bi-e.anchor > 400 {
		e.anchor, e.anchorOff = bi, cy-e.viewH*0.4
		e.normalize()
		return
	}
	y := -e.anchorOff
	for i := e.anchor; i < bi; i++ {
		e.ensure(i)
		y += e.height(i)
	}
	top := y + cy
	switch {
	case top < margin:
		e.anchorOff -= margin - top
	case top+ch > e.viewH-margin:
		e.anchorOff += top + ch - (e.viewH - margin)
	}
}

// evict 在缓存超出预算时，按最近使用淘汰非当前帧使用的行布局，降到预算的 3/4。
// 被淘汰的行保留实测高度。
func (e *Editor) evict() {
	budget := e.CacheBudget
	if budget <= 0 {
		budget = defaultCacheBudget
	}
	if e.cacheBytes <= budget {
		return
	}
	type victim struct {
		i, k int
		used uint64
	}
	var vs []victim
	for i := range e.entries {
		for k := range e.entries[i].rows {
			if r := &e.entries[i].rows[k]; r.layout != nil && r.used != e.tick {
				vs = append(vs, victim{i, k, r.used})
			}
		}
	}
	slices.SortFunc(vs, func(a, b victim) int {
		switch {
		case a.used < b.used:
			return -1
		case a.used > b.used:
			return 1
		}
		return 0
	})
	for _, v := range vs {
		if e.cacheBytes <= budget*3/4 {
			break
		}
		r := &e.entries[v.i].rows[v.k]
		e.cacheBytes -= r.bytes
		r.proj, r.layout, r.bytes, r.objRects, r.objSizes = nil, nil, 0, nil, nil
	}
}

// scrollMetrics 返回文档总高度与当前滚动位置（DIP），用于滚动条。
func (e *Editor) scrollMetrics() (total, pos float32) {
	total = e.topSpace() + e.viewH*0.5
	for i := range e.entries {
		h := e.height(i)
		if i < e.anchor {
			pos += h
		}
		total += h
	}
	return total, pos + e.anchorOff + e.topSpace()
}

// scrollTo 把滚动位置设为 pos（DIP），按已知或估算高度定位锚点。
func (e *Editor) scrollTo(pos float32) {
	pos -= e.topSpace()
	e.anchor, e.anchorOff = 0, pos
	for e.anchor < len(e.entries)-1 && e.anchorOff >= e.height(e.anchor) {
		e.anchorOff -= e.height(e.anchor)
		e.anchor++
	}
	e.dirty = true
}

func (e *Editor) scrollBy(dy float32) {
	e.anchorOff += dy
	e.dirty = true
	e.sync()
}

// hit 把元素坐标映射为原文字节偏移：先按纵坐标找水平带，再在带内按横坐标找行。
func (e *Editor) hit(pt ui.Point) int {
	e.sync()
	if len(e.vsegs) == 0 {
		return e.Doc.Sel.Head
	}
	k := 0
	for n, c := range e.vsegs {
		if pt.Y >= c.y && e.entries[c.i].rows[c.k].bandH > 0 {
			k = n
		}
	}
	// 落在行框下方的间距里时，归给垂直距离更近的行。
	v := e.vsegs[k]
	if k+1 < len(e.vsegs) {
		bottom := v.y + e.entries[v.i].rows[v.k].bandH
		if next := e.vsegs[k+1]; pt.Y > bottom && next.y-pt.Y < pt.Y-bottom {
			v = next
		}
	}
	// 同一水平带（表格行）中按横坐标选单元格。
	band := e.entries[v.i].rows[v.k].band
	x := pt.X - e.left()
	first := true
	for _, c := range e.vsegs {
		if c.i != v.i || e.entries[c.i].rows[c.k].band != band {
			continue
		}
		if cx, _ := e.rowBox(&e.entries[c.i].rows[c.k]); first || cx-cellPadX <= x {
			v, first = c, false
		}
	}
	s := e.segOf(v.i, v.k)
	return s.toSrc(s.layout.IndexAt(ui.Point{X: x - s.x, Y: pt.Y - v.y - s.padTop}), false)
}

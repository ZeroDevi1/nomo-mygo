package mdedit

import (
	"html"
	"regexp"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown/parser"
)

// 折叠块的视图（框架识别见 details.go）。正文块按所在层级左缩进、右内缩，卡片边框逐块绘制；开始、结束标记块
// 收起时显示为标题行与卡片底边（previewDetails 预览对象），光标进入标记块时显示原文。展开与折叠是临时显示
// 状态（原版 detailsState 的 override），不写入原文、不进撤销历史；原文中的 open 属性是默认状态。
// 折叠的正文高度为 0、不参与布局与绘制，方向键越过它；查找、大纲跳转、撤销等把光标放进折叠区时自动展开。

// 版式取自原版 editor-document.css：.details-block 内边距 0.5em 0.8em、1px 边框、圆角 6px；
// [data-nomo-details-body] 左内边距 1.1em。三角按钮宽度为经验值。
const (
	detailsPadXEm   = 0.8
	detailsPadYEm   = 0.5
	detailsBodyEm   = 1.1
	detailsToggleEm = 1.3
	detailsRadius   = 6
)

// detailsView 是折叠框架在当前原文版本与折叠状态下的派生视图，由 detailsState 按需重算。
type detailsView struct {
	gen    uint64
	ver    uint64 // 计算时的索引版本（索引可被 shiftDetails 就地平移，同一指针版本不同也需重算）
	idx    *detailsIndex
	depth  []int32 // 每个顶层块的外层框架数（严格包含）
	hidden []bool  // 每个顶层块是否因折叠而隐藏
	open   []bool  // 每个框架是否展开
	shown  []bool  // 每个框架的标题是否可见（所有外层框架都展开）
}

// detailsTok 是标记块预览中一个标记的纵向范围（相对预览顶部）；h 为 0 表示该标记当前隐藏。
type detailsTok struct {
	y, h  float32
	frame int
	close bool
}

// detailsLevel 返回每层折叠的左缩进与右内缩（DIP）。
func (e *Editor) detailsLevel() (l, r float32) {
	fs := bodySize * e.scale
	return fs * (detailsPadXEm + detailsBodyEm), fs * detailsPadXEm
}

func (e *Editor) frameOpen(f *detailsFrame) bool {
	if v, ok := e.folds[f.key]; ok {
		return v
	}
	return f.defaultOpen
}

// detailsState 返回当前的折叠视图；文档没有折叠块时返回 nil。成本 O(顶层块数 + 各框架正文块数之和)，只在原文
// 或折叠状态变化后计算一次。
func (e *Editor) detailsState() *detailsView {
	x := e.Doc.details()
	if len(x.frames) == 0 {
		e.dview = nil
		return nil
	}
	if dv := e.dview; dv != nil && dv.idx == x && dv.ver == x.ver && dv.gen == e.foldGen {
		return dv
	}
	n := len(e.Doc.blocks)
	dv := &detailsView{gen: e.foldGen, ver: x.ver, idx: x}
	// 复用上一版本的数组（逐块数组随文档体量增长，每次按键重新分配会增加 GC 负担）。
	if old := e.dview; old != nil && cap(old.depth) >= n && cap(old.open) >= len(x.frames) {
		dv.depth, dv.hidden = old.depth[:n], old.hidden[:n]
		clear(dv.depth)
		clear(dv.hidden)
		dv.open, dv.shown = old.open[:len(x.frames)], old.shown[:len(x.frames)]
	} else {
		dv.depth, dv.hidden = make([]int32, n), make([]bool, n)
		dv.open, dv.shown = make([]bool, len(x.frames)), make([]bool, len(x.frames))
	}
	for f := range x.frames {
		fr := &x.frames[f]
		dv.open[f] = e.frameOpen(fr)
		dv.shown[f] = fr.parent < 0 || dv.shown[fr.parent] && dv.open[fr.parent]
		hide := !(dv.shown[f] && dv.open[f])
		for i := fr.open + 1; i < fr.close; i++ {
			dv.depth[i]++
			if hide {
				dv.hidden[i] = true
			}
		}
	}
	// 标记块：所有标记都不可见时整块隐藏（如折叠框架的结束标记块）。
	for k := range x.markers {
		mb := &x.markers[k]
		i := mb.block
		if dv.hidden[i] {
			continue
		}
		visible := false
		for _, f := range mb.closes {
			visible = visible || dv.shown[f] && dv.open[f]
		}
		for _, f := range mb.opens {
			visible = visible || dv.shown[f]
		}
		dv.hidden[i] = !visible
	}
	e.dview = dv
	return dv
}

// detailsHidden 报告顶层块 i 是否因折叠而隐藏。
func (e *Editor) detailsHidden(i int) bool {
	dv := e.detailsState()
	return dv != nil && i < len(dv.hidden) && dv.hidden[i]
}

// detailsInset 返回顶层块 i 的左缩进与右内缩。
func (e *Editor) detailsInset(i int) (l, r float32) {
	dv := e.detailsState()
	if dv == nil || i >= len(dv.depth) || dv.depth[i] == 0 {
		return 0, 0
	}
	lv, rv := e.detailsLevel()
	return float32(dv.depth[i]) * lv, float32(dv.depth[i]) * rv
}

// visibleNeighbor 返回块 i 之前（dir<0）或之后第一个未被折叠隐藏的块，没有时返回 -1。
func (e *Editor) visibleNeighbor(i, dir int) int {
	for j := i + dir; j >= 0 && j < len(e.entries); j += dir {
		if !e.detailsHidden(j) {
			return j
		}
	}
	return -1
}

// remapFolds 按一次原文替换平移折叠状态的键（“<details”的偏移）；被替换区间内的状态丢弃。
func (e *Editor) remapFolds(ch Change) {
	if len(e.folds) == 0 {
		return
	}
	next := make(map[int]bool, len(e.folds))
	for k, v := range e.folds {
		switch {
		case k < ch.At:
			next[k] = v
		case k >= ch.At+ch.OldLen:
			next[k+ch.NewLen-ch.OldLen] = v
		}
	}
	e.folds = next
	e.foldGen++
}

// setFold 设置框架 f 的临时展开状态。
func (e *Editor) setFold(f *detailsFrame, open bool) {
	if e.folds == nil {
		e.folds = map[int]bool{}
	}
	e.folds[f.key] = open
	e.foldGen++
	e.dirty = true
}

// syncDetails 在每帧布局前同步折叠视图：原文或折叠状态变化后刷新标记块（预览依赖块序号与折叠状态），光标
// 位于折叠隐藏的块内时展开其外层框架。
func (e *Editor) syncDetails() {
	x := e.Doc.details()
	if len(x.frames) == 0 && !e.dhad {
		return
	}
	e.dhad = len(x.frames) > 0
	if dv := e.detailsState(); dv != nil {
		if i, _ := e.Doc.BlockAt(e.Doc.Sel.Head); i >= 0 && dv.hidden[i] {
			for f := range x.frames {
				fr := &x.frames[f]
				if fr.open < i && i <= fr.close && !dv.open[f] {
					e.setFold(fr, true)
				}
			}
			e.detailsState()
		}
	}
	if e.dsyncVer == e.Doc.version {
		return
	}
	e.dsyncVer = e.Doc.version
	// 配对变化会改变 HTML 块的显示方式（标记预览或原文）：显示方式与当前不符的块重新展开为行，已有布局按行哈希
	// 继承。标记行预览依赖的块序号、版本与折叠状态由 ensureRow 检查（detailsStale）。
	src := e.Doc.Source()
	for i, b := range e.Doc.blocks {
		if b.Kind != parser.HTMLBlock || !e.entries[i].split || !isDetailsMarkerHead(src[b.Start:b.End]) {
			continue
		}
		en := &e.entries[i]
		was := len(en.rows) > 0 && en.rows[0].htmlKind == previewDetails
		if was != (x.marker(i) != nil) {
			e.split(i, en.rows, en.frames)
		}
	}
	e.dirty = true
}

// detailsStale 报告标记行 r（块 i）的预览是否需按当前块序号、原文版本或折叠状态重建。
func (e *Editor) detailsStale(i int, r *row) bool {
	if r.htmlKind != previewDetails || r.proj == nil || len(r.proj.objs) == 0 || r.proj.objs[0].html == nil {
		return false
	}
	b := r.proj.objs[0].html
	return b.dblock != i || b.dgen != e.foldGen || b.dver != e.Doc.version
}

// detailsMarker 排版标记块 b.dblock 的预览：结束标记为卡片底部内边距，开始标记为标题行（三角 + 标题）；
// 返回预览高度。标记相对块缩进的横向位置按各自框架的层级计算。
func (c *hpCtx) detailsMarker(b *htmlBlockPreview, w float32) float32 {
	e := c.e
	dv := e.detailsState()
	b.dtok = b.dtok[:0]
	if dv == nil {
		return 0
	}
	mb := dv.idx.marker(b.dblock)
	if mb == nil {
		return 0
	}
	fs := bodySize * e.scale
	padX, padY := fs*detailsPadXEm, fs*detailsPadYEm
	lv, _ := e.detailsLevel()
	base := dv.depth[b.dblock]
	y := float32(0)
	for _, f := range mb.closes {
		var h float32
		if dv.shown[f] && dv.open[f] {
			h = padY
		}
		b.dtok = append(b.dtok, detailsTok{y: y, h: h, frame: f, close: true})
		y += h
	}
	for _, f := range mb.opens {
		fr := &dv.idx.frames[f]
		if !dv.shown[f] {
			b.dtok = append(b.dtok, detailsTok{y: y, frame: f})
			continue
		}
		x := float32(int32(fr.depth)-base) * lv // 行缩进已含卡片左内边距（split）
		tw := fs * detailsToggleEm
		arrow := "▸"
		if dv.open[f] {
			arrow = "▾"
		}
		al := ui.ShapeRichTextWith([]ui.Span{{Text: arrow}}, ui.Font{Family: e.fontBody, Size: fs}, ui.TextShape{})
		spans := summarySpans(fr.summary)
		color := e.pal.heading
		if len(spans) == 0 {
			spans, color = []ui.Span{{Text: e.detailsPlaceholder()}}, e.pal.muted
		}
		tl := ui.ShapeRichTextWith(spans, ui.Font{Family: e.fontBody, Size: fs, Weight: 600}, ui.TextShape{Width: max(40, w-x-tw-padX)})
		_, th := tl.Size()
		_, ah := al.Size()
		lh := max(th, fs*1.5)
		h := padY + lh
		if !dv.open[f] {
			h += padY
		}
		c.lay.ops = append(c.lay.ops,
			hpOp{kind: hpText, rect: ui.Rect{X: x, Y: y + padY + (min(lh, fs*1.5)-ah)/2}, text: al, color: e.pal.muted},
			hpOp{kind: hpText, rect: ui.Rect{X: x + tw, Y: y + padY + (lh-th)/2}, text: tl, color: color})
		b.dtok = append(b.dtok, detailsTok{y: y, h: h, frame: f})
		y += h
	}
	c.lay.bytes += 512 + 64*len(b.dtok)
	return y
}

func (e *Editor) detailsPlaceholder() string {
	if e.labels.DetailsTitle != "" {
		return e.labels.DetailsTitle
	}
	return "Summary"
}

var summaryTagRe = regexp.MustCompile(`<(/?)([a-zA-Z]+)[^>]*>`)

// summarySpans 把 summary 中的行内 HTML 转为排版片段：strong/b 加粗、em/i 斜体、code 等宽、s/del 删除线、
// u 下划线；其余标签去掉，实体解码。空标题返回 nil。
func summarySpans(s string) []ui.Span {
	var spans []ui.Span
	var bold, italic, code, strike, under int
	emit := func(text string) {
		text = html.UnescapeString(text)
		if text == "" {
			return
		}
		sp := ui.Span{Text: text, Italic: italic > 0, Strikethrough: strike > 0, Underline: under > 0}
		if bold > 0 {
			sp.Weight = 750
		}
		if code > 0 {
			sp.Font = monoFamily
		}
		spans = append(spans, sp)
	}
	at := 0
	for _, m := range summaryTagRe.FindAllStringSubmatchIndex(s, -1) {
		emit(s[at:m[0]])
		at = m[1]
		d := 1
		if m[3] > m[2] {
			d = -1
		}
		switch strings.ToLower(s[m[4]:m[5]]) {
		case "strong", "b":
			bold += d
		case "em", "i":
			italic += d
		case "code":
			code += d
		case "s", "del":
			strike += d
		case "u":
			under += d
		}
	}
	emit(s[at:])
	if len(spans) == 1 && strings.TrimSpace(spans[0].Text) == "" {
		return nil
	}
	return spans
}

// markerToks 返回标记块 i 的标记纵向范围（元素坐标，块顶为 top）；该块显示原文（光标在块内）或尚未排版时
// 返回 nil，调用方按整块处理。
func (e *Editor) markerToks(i int, top float32) ([]detailsTok, float32) {
	en := &e.entries[i]
	if !en.split || len(en.rows) == 0 {
		return nil, 0
	}
	r := &en.rows[0]
	if r.htmlKind != previewDetails || r.proj == nil || len(r.proj.objs) == 0 || r.proj.objs[0].html == nil || len(r.objRects) == 0 {
		return nil, 0
	}
	b := r.proj.objs[0].html
	if b.lay == nil || e.detailsStale(i, r) {
		return nil, 0
	}
	return b.dtok, top + r.y + r.padTop + r.objRects[0].Y
}

// drawDetails 画可见块上的折叠卡片边框：每个卡片在其跨越的每个可见块内裁剪绘制同一个圆角矩形，开始标记块
// 内从标题行顶部开始，结束标记块（或折叠时的标题行）处收底。
func (e *Editor) drawDetails(p *ui.Painter, x0, y0 float32) {
	dv := e.detailsState()
	if dv == nil {
		return
	}
	lv, rv := e.detailsLevel()
	cw := e.width()
	for _, v := range e.vblocks {
		i := v.i
		top := y0 + v.top
		bh := e.height(i)
		var toks []detailsTok
		var tokBase float32
		mb := dv.idx.marker(i)
		if mb != nil {
			toks, tokBase = e.markerToks(i, top)
		}
		tokAt := func(f int, close bool) (detailsTok, bool) {
			for _, t := range toks {
				if t.frame == f && t.close == close {
					return t, true
				}
			}
			return detailsTok{}, false
		}
		for f := range dv.idx.frames {
			fr := &dv.idx.frames[f]
			if i < fr.open || i > fr.close || !dv.shown[f] || i > fr.open && !dv.open[f] {
				continue
			}
			a, z := top-4096, top+bh+4096
			if i == fr.open {
				a = top
				if t, ok := tokAt(f, false); ok {
					a = tokBase + t.y
					if !dv.open[f] {
						z = tokBase + t.y + t.h
					}
				} else if !dv.open[f] {
					z = top + bh
				}
			}
			if i == fr.close {
				z = top + bh
				if t, ok := tokAt(f, true); ok {
					z = tokBase + t.y + t.h
				}
			}
			left := x0 + float32(fr.depth)*lv
			right := x0 + cw - float32(fr.depth)*rv
			clip := ui.Rect{X: left - 2, Y: max(top, a), W: right - left + 4, H: min(top+bh, z) - max(top, a)}
			if clip.H <= 0 {
				continue
			}
			p.Clip(clip, 0, func() {
				p.Stroke(ui.Rect{X: left, Y: a, W: right - left, H: z - a}, e.pal.border, detailsRadius, 1)
			})
		}
	}
}

// detailsToggleHit 报告 pt（元素坐标）是否落在某个可见标题行的三角按钮上，是则返回该框架。
func (e *Editor) detailsToggleHit(pt ui.Point) (*detailsFrame, bool) {
	return e.detailsHit(pt, true)
}

// detailsHeaderHit 报告 pt 是否落在某个可见标题行上（任意横向位置）。
func (e *Editor) detailsHeaderHit(pt ui.Point) (*detailsFrame, bool) {
	return e.detailsHit(pt, false)
}

func (e *Editor) detailsHit(pt ui.Point, toggleOnly bool) (*detailsFrame, bool) {
	dv := e.detailsState()
	if dv == nil {
		return nil, false
	}
	e.sync()
	fs := bodySize * e.scale
	lv, _ := e.detailsLevel()
	x0 := e.left()
	for _, v := range e.vblocks {
		if dv.idx.marker(v.i) == nil {
			continue
		}
		toks, base := e.markerToks(v.i, v.top)
		for _, t := range toks {
			if t.close || t.h == 0 || pt.Y < base+t.y || pt.Y >= base+t.y+t.h {
				continue
			}
			fr := &dv.idx.frames[t.frame]
			x := x0 + float32(fr.depth)*lv + fs*detailsPadXEm
			if !toggleOnly || pt.X >= x-4 && pt.X < x+fs*detailsToggleEm {
				return fr, true
			}
		}
	}
	return nil, false
}

// toggleDetails 切换框架的临时展开状态（原版标题行三角按钮）；折叠时选区若在正文内，光标移到标题所在的开始
// 标记块末尾，与原版把选区移到标题一致。
func (e *Editor) toggleDetails(f *detailsFrame) {
	open := !e.frameOpen(f)
	e.setFold(f, open)
	if !open {
		blocks := e.Doc.blocks
		lo, hi := blocks[f.open].End, blocks[f.close].End
		if s := e.Doc.Sel; s.Head > lo && s.Head <= hi || s.Anchor > lo && s.Anchor <= hi {
			e.Doc.SetSelection(Selection{Anchor: lo, Head: lo})
		}
	}
	e.reveal = true
}

const detailsHead = "<details>\n<summary>"

// InsertDetails 插入折叠块（原版 insertDetails）：有选区时把选区所在的顶层块整块包进正文，否则在当前块之后插入
// 空折叠块；光标置于标题中（开始标记块随即显示原文，直接输入标题）。一步撤销。
func (e *Editor) InsertDetails() {
	src := e.Doc.Source()
	a, z := e.Doc.Sel.Range()
	if a != z {
		if i, _ := e.Doc.BlockAt(a); i >= 0 {
			a = e.Doc.blocks[i].Start
		}
		if j, _ := e.Doc.BlockAt(z); j >= 0 {
			z = e.Doc.blocks[j].End
		}
		e.applyEdit(e.Doc.Replace(a, z, detailsHead+"</summary>\n\n"+src[a:z]+"\n\n</details>", editOther))
		p := a + len(detailsHead)
		e.Doc.Sel = Selection{Anchor: p, Head: p}
		e.Doc.hist.seal()
		e.reveal, e.dirty = true, true
		return
	}
	at := a
	if i, _ := e.Doc.BlockAt(a); i >= 0 {
		at = e.Doc.blocks[i].End
	}
	prefix := "\n\n"
	if at == 0 {
		prefix = ""
	}
	text := prefix + detailsHead + "</summary>\n\n</details>"
	if at >= len(src) || src[at] != '\n' {
		text += "\n"
	}
	e.applyEdit(e.Doc.Replace(at, at, text, editOther))
	p := at + len(prefix) + len(detailsHead)
	e.Doc.Sel = Selection{Anchor: p, Head: p}
	e.Doc.hist.seal()
	e.reveal, e.dirty = true, true
}

// detailsFrameByKey 返回开始标记位于 key 的框架。
func (e *Editor) detailsFrameByKey(key int) (*detailsFrame, bool) {
	x := e.Doc.details()
	if f := x.frameAt(key); f >= 0 {
		return &x.frames[f], true
	}
	return nil, false
}

// SetDetailsDefaultOpen 设置折叠块的默认展开（写入或去掉开始标记的 open 属性，原版 setDetailsDefaultOpen），
// 同时清除该块的临时展开状态；设为折叠且选区在正文内时光标移到标题所在块末尾。返回是否找到该折叠块。
func (e *Editor) SetDetailsDefaultOpen(key int, open bool) bool {
	f, ok := e.detailsFrameByKey(key)
	if !ok {
		return false
	}
	src := e.Doc.Source()
	end := strings.IndexByte(src[key:], '>')
	if end < 0 {
		return false
	}
	tag := "<details>"
	if open {
		tag = "<details open>"
	}
	lo, hi := e.Doc.blocks[f.open].End, e.Doc.blocks[f.close].End
	sel := e.Doc.Sel
	e.applyEdit(e.Doc.Replace(key, key+end+1, tag, editOther))
	delta := len(tag) - (end + 1)
	// 替换落在选区之前：选区按长度差平移，不随 Replace 移到插入点。
	shift := func(p int) int {
		if p > key {
			return p + delta
		}
		return p
	}
	sel = Selection{Anchor: shift(sel.Anchor), Head: shift(sel.Head)}
	if !open && (sel.Head > lo && sel.Head <= hi+delta || sel.Anchor > lo && sel.Anchor <= hi+delta) {
		p := lo + delta
		sel = Selection{Anchor: p, Head: p}
	}
	e.Doc.Sel = sel
	e.Doc.hist.seal()
	delete(e.folds, key)
	e.foldGen++
	e.dirty = true
	return true
}

// UnwrapDetails 取消折叠并保留内容（原版 unwrapDetails）：标题（非空时）成为一个段落，正文原样保留，开始与结束
// 标记删除。一步撤销。返回是否找到该折叠块。
func (e *Editor) UnwrapDetails(key int) bool {
	f, ok := e.detailsFrameByKey(key)
	if !ok {
		return false
	}
	src := e.Doc.Source()
	cb := e.Doc.blocks[f.close]
	locs := detailsCloseAll.FindAllStringIndex(strings.ToLower(src[cb.Start:cb.End]), -1)
	if f.closeTok >= len(locs) {
		return false
	}
	closeA, closeZ := cb.Start+locs[f.closeTok][0], cb.Start+locs[f.closeTok][1]
	bodyAt := f.summaryAt + len(f.summary)
	if k := strings.IndexByte(src[bodyAt:], '>'); k >= 0 {
		bodyAt += k + 1 // “</summary>”之后
	}
	body := strings.Trim(src[bodyAt:closeA], "\n")
	text := body
	if title := strings.TrimSpace(f.summary); title != "" {
		text = title
		if body != "" {
			text += "\n\n" + body
		}
	}
	e.applyEdit(e.Doc.Replace(key, closeZ, text, editOther))
	e.Doc.Sel = Selection{Anchor: key, Head: key}
	e.Doc.hist.seal()
	e.reveal, e.dirty = true, true
	return true
}

var detailsCloseAll = regexp.MustCompile(`</details\s*>`)

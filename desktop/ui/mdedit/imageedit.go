package mdedit

import (
	"html"
	"regexp"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown/parser"
)

// 图片编辑（原版图片右键菜单与拖动调整宽度）：定位光标或右键处的图片原文，按与插入相同的规则（ImageSnippet）
// 改写宽度与对齐；拖动图片右边缘调整宽度，松开时写入 width 属性（DIP，与显示尺寸的换算见 parseImageWidth）。
// 每次改写是一次独立的撤销步。

// ImageRef 是原文中的一张图片：[Start, End) 为整段原文（对齐时含外层 <p align>），Standalone 表示图片独占
// 所在块（只有这时才能设置对齐，否则 <p> 会拆开段落）。
type ImageRef struct {
	Start, End        int
	Src, Alt          string
	Width, Align      string
	Standalone, IsTag bool // IsTag：原文为 <img> 标签（而非 ![alt](src)）
}

var (
	imgTagRe   = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	imgAttrRe  = regexp.MustCompile(`(?is)([a-z-]+)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	alignWrap  = regexp.MustCompile(`(?is)^\s*<p\s+align\s*=\s*["']?(left|center|right)["']?\s*>\s*(<img\b[^>]*>)\s*</p>\s*$`)
	altUnsafe  = regexp.MustCompile(`[\[\]\n\r]`)
	destEscape = strings.NewReplacer("&", "&amp;", `\`, `\\`, "(", `\(`, ")", `\)`, "<", `\<`, ">", `\>`)
	attrEscape = strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;")
)

// ImageSnippet 生成图片标记，规则同原版 insertSourceMarkdown / createImageMarkdown：指定对齐时用 <p align> 包裹
// <img>，只指定宽度时用 <img width>，否则为 ![alt](src)。
func ImageSnippet(src, alt, width, align string) string {
	switch {
	case align == "left" || align == "center" || align == "right":
		tag := `<img src="` + attrEscape.Replace(src) + `" alt="` + attrEscape.Replace(alt) + `"`
		if width != "" {
			tag += ` width="` + attrEscape.Replace(width) + `"`
		}
		return `<p align="` + align + "\">\n  " + tag + ">\n</p>"
	case width != "":
		return `<img src="` + attrEscape.Replace(src) + `" alt="` + attrEscape.Replace(alt) + `" width="` + attrEscape.Replace(width) + `">`
	}
	safeAlt := strings.TrimSpace(altUnsafe.ReplaceAllString(alt, " "))
	if safeAlt == "" {
		safeAlt = "image"
	}
	dest := destEscape.Replace(src)
	if strings.ContainsAny(src, " \t\n\r\f\v") {
		dest = "<" + dest + ">"
	}
	return "![" + safeAlt + "](" + dest + ")"
}

// ImageAt 返回包含原文位置 pos 的图片（Markdown 图片或 <img> 标签）。
func (e *Editor) ImageAt(pos int) (ImageRef, bool) {
	d := e.Doc
	i, _ := d.BlockAt(pos)
	if i < 0 {
		return ImageRef{}, false
	}
	b := d.blocks[i]
	src := d.Source()
	block := src[b.Start:b.End]
	// 整块为 <p align> 包裹的单张图片。
	if m := alignWrap.FindStringSubmatchIndex(block); m != nil {
		ref := parseImgTag(block[m[4]:m[5]])
		ref.Start, ref.End, ref.Align, ref.Standalone, ref.IsTag = b.Start, b.End, block[m[2]:m[3]], true, true
		return ref, true
	}
	// 行内 <img> 标签。
	for _, m := range imgTagRe.FindAllStringIndex(block, -1) {
		if a, z := b.Start+m[0], b.Start+m[1]; pos >= a && pos <= z {
			ref := parseImgTag(src[a:z])
			ref.Start, ref.End, ref.IsTag = a, z, true
			ref.Standalone = strings.TrimSpace(block) == src[a:z]
			return ref, true
		}
	}
	// Markdown 图片：在已布局的行内节点中查找。
	s, ok := e.segAt(pos)
	if !ok || s.kind != rowText {
		return ImageRef{}, false
	}
	r := &e.entries[s.i].rows[s.k]
	ins := d.md.Inlines(b.Index + r.rel)
	for k, in := range ins {
		if in.Kind != parser.Image || pos < in.Start || pos > in.End {
			continue
		}
		la, lz := in.Start+2, in.Start+2
		for j := k + 1; j < in.Next && j < len(ins); j++ {
			lz = max(lz, ins[j].End)
		}
		ref := ImageRef{Start: in.Start, End: in.End, Src: in.Dest, Alt: src[la:min(lz, in.End)]}
		ref.Standalone = strings.TrimSpace(block) == src[in.Start:in.End]
		return ref, true
	}
	return ImageRef{}, false
}

func parseImgTag(tag string) ImageRef {
	var ref ImageRef
	for _, m := range imgAttrRe.FindAllStringSubmatch(tag, -1) {
		v := html.UnescapeString(m[2] + m[3])
		switch strings.ToLower(m[1]) {
		case "src":
			ref.Src = v
		case "alt":
			ref.Alt = v
		case "width":
			ref.Width = v
		case "align":
			ref.Align = v
		}
	}
	return ref
}

// SetImage 把 pos 处图片的宽度与对齐改为 width、align（空串为默认）。图片不独占所在块时忽略对齐。
func (e *Editor) SetImage(pos int, width, align string) bool {
	ref, ok := e.ImageAt(pos)
	if !ok {
		return false
	}
	if !ref.Standalone {
		align = ""
	}
	text := ImageSnippet(ref.Src, ref.Alt, width, align)
	if src := e.Doc.Source(); src[ref.Start:ref.End] == text {
		return false
	}
	e.Replace(ref.Start, ref.End, text)
	return true
}

// DeleteImage 删除 pos 处的图片原文（独占所在块时连同块后的空行删除）。
func (e *Editor) DeleteImage(pos int) bool {
	ref, ok := e.ImageAt(pos)
	if !ok {
		return false
	}
	if ref.Standalone {
		return e.DeleteBlock(pos)
	}
	e.Replace(ref.Start, ref.End, "")
	return true
}

// ImageBitmap 返回已加载的图片位图及其原始尺寸（DIP），供外壳预览；未加载、加载失败或为 SVG 时 ok 为 false。
func (e *Editor) ImageBitmap(src string) (bmp *ui.Bitmap, w, h float32, ok bool) {
	en := e.imageEntry(imgObj{src: src})
	if en == nil || en.state != imageReady || en.bmp == nil {
		return nil, 0, 0, false
	}
	return en.bmp, en.w, en.h, true
}

// imageResize 是拖动图片右边缘调整宽度的状态：at 为图片原文中的位置，rect 为按下时图片在编辑区中的矩形，
// w 为当前拖到的宽度（DIP）。
type imageResize struct {
	at   int
	x0   float32
	rect ui.Rect
	w    float32
}

// imageResizeEdge 是图片右边缘可拖动的宽度（DIP）。
const imageResizeEdge = 8

// imageEdgeHit 报告 pt 是否落在普通图片（非公式、图表、HTML 预览）的右边缘，返回图片原文位置与其在编辑区中的矩形。
func (e *Editor) imageEdgeHit(pt ui.Point) (int, ui.Rect, bool) {
	e.sync()
	for _, v := range e.vsegs {
		r := &e.entries[v.i].rows[v.k]
		if len(r.objRects) == 0 || r.proj == nil {
			continue
		}
		s := e.segOf(v.i, v.k)
		ox, oy := e.left()+s.x, v.y+s.padTop
		for k, rc := range r.objRects {
			o := s.proj.objs[k]
			if o.html != nil || o.render != "" || rc.W < 24 {
				continue
			}
			x, y := pt.X-ox, pt.Y-oy
			if y < rc.Y || y >= rc.Y+rc.H || x < rc.X+rc.W-imageResizeEdge || x > rc.X+rc.W+2 {
				continue
			}
			for _, run := range s.proj.runs {
				if run.atomic && run.disp == o.u16 {
					return s.base + run.srcA + 1, ui.Rect{X: rc.X + ox, Y: rc.Y + oy, W: rc.W, H: rc.H}, true
				}
			}
		}
	}
	return 0, ui.Rect{}, false
}

// finishImageResize 结束拖动：宽度变化超过 2 DIP 时写入 width（取整像素），保留原有对齐。
func (e *Editor) finishImageResize() {
	rs := e.resize
	e.resize = nil
	e.dirty = true
	if rs == nil || abs32(rs.w-rs.rect.W) < 2 {
		return
	}
	ref, ok := e.ImageAt(rs.at)
	if !ok {
		return
	}
	e.SetImage(rs.at, strconv.Itoa(int(rs.w+0.5)), ref.Align)
}

// drawImageResize 画拖动中的目标宽度框（按原比例），r 为编辑区矩形。
func (e *Editor) drawImageResize(p *ui.Painter, r ui.Rect) {
	rs := e.resize
	if rs == nil {
		return
	}
	h := rs.rect.H * rs.w / max(1, rs.rect.W)
	rc := ui.Rect{X: r.X + rs.rect.X, Y: r.Y + rs.rect.Y, W: rs.w, H: h}
	p.Fill(rc, e.pal.selection, 2)
	p.StrokeDashed(rc, e.pal.caret, 2, 1)
}

// imageMove 是按住图片拖动换位置的状态：ref 为被拖动的图片原文，moved 为指针已移动超过阈值（否则松开按单击处理，
// 光标进入图片原文），drop 为当前放置位置（原文偏移）。
type imageMove struct {
	ref    ImageRef
	at     int // 单击时光标进入的位置
	x0, y0 float32
	moved  bool
	drop   int
}

// imageMoveStart 是拖动开始的距离阈值（DIP）。
const imageMoveStart = 4

// moveImageTo 把图片原文移到 drop 处（drop 在图片原文内时不移动），作为一次撤销步；独占一块的图片移到段落中间时
// 去掉对齐包裹（<p align> 不能嵌在段落里），移到空行处时前后补空行使其成为独立的块。
func (e *Editor) moveImageTo(ref ImageRef, drop int) bool {
	if drop >= ref.Start && drop <= ref.End {
		return false
	}
	src := e.Doc.Source()
	text := src[ref.Start:ref.End]
	if ref.Standalone && ref.Align != "" {
		text = ImageSnippet(ref.Src, ref.Alt, ref.Width, "")
	}
	// 删除原处（独占一块时连同其后的空行），再在放置处插入；放置点在原处之后时按删除的长度前移。
	a, z := ref.Start, ref.End
	if ref.Standalone {
		for z < len(src) && src[z] == '\n' {
			z++
		}
	}
	at := drop
	if drop > z {
		at -= z - a
	}
	rest := src[:a] + src[z:]
	at = clamp(at, 0, len(rest))
	ins := text
	if lineStart(rest, at) == at && (at == len(rest) || rest[at] == '\n') {
		// 落在空行：作为独立的块
		ins = "\n" + text + "\n"
	}
	next := rest[:at] + ins + rest[at:]
	d := e.Doc
	d.hist.seal()
	p, sfx := commonAffix(src, next)
	e.applyEdit(d.Replace(p, len(src)-sfx, next[p:len(next)-sfx], editOther))
	d.hist.seal()
	pos := at + len(ins)
	d.Sel = Selection{Anchor: pos, Head: pos}
	return true
}

// commonAffix 返回 a、b 的公共前缀与后缀长度（字节，落在 UTF-8 字符边界），把整体替换缩为最小差异。
func commonAffix(a, b string) (prefix, suffix int) {
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	for prefix > 0 && prefix < len(a) && a[prefix]&0xC0 == 0x80 {
		prefix--
	}
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	for suffix > 0 && a[len(a)-suffix]&0xC0 == 0x80 {
		suffix--
	}
	return prefix, suffix
}

// drawImageMove 画拖动中的放置位置（2 DIP 宽的强调色竖线），r 为编辑区矩形。
func (e *Editor) drawImageMove(p *ui.Painter, r ui.Rect) {
	mv := e.move
	if mv == nil || !mv.moved {
		return
	}
	if c, ok := e.caretRect(mv.drop); ok {
		p.Fill(ui.Rect{X: r.X + c.X - 1, Y: r.Y + c.Y, W: 2, H: c.H}, e.pal.link, 1)
	}
}

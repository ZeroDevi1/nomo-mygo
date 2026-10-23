package mdedit

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/ui/icons"
)

// 行内图片的排版与绘制。图片在投影中是一个占位字符（见 projection.object），排版时成为指定尺寸的
// 行内对象，由 Editor 在对象位置绘制缓存中的位图；加载中显示占位底色，失败显示替代文字框。

// 加载中占位尺寸：尚不知道原始尺寸（首次加载）时取一个小框，加载完成后按实际尺寸重排（阅读锚点
// 保持不跳动）；已知尺寸（被淘汰后重新加载）时按真实尺寸占位。
const (
	imagePendingW = 120
	imagePendingH = 72
)

// 失败图片的替代文字框：图标 14 + 间距 6 + 左右内边距各 8。
const (
	imageAltSize = 13
	imageAltIcon = 14
	imageAltPad  = 8 + imageAltIcon + 6 + 8
)

// Labels 是编辑区内需要界面语言的文字，由外壳按当前语言设置。
type Labels struct {
	Comment    string // 注释卡片的标签，原版 t.comment()
	Toc        string // 目录卡片的标题，原版 t.toc()
	NoHeadings string // 目录卡片无标题时的提示，原版 t.documentHasNoHeadings()
}

// SetLabels 设置界面文字；变化时全部布局作废（注释卡片含标签文字）。
func (e *Editor) SetLabels(l Labels) {
	if l == e.labels {
		return
	}
	e.labels = l
	e.layoutGen++
	e.dirty = true
}

// SetImages 设置图片缓存与解析相对路径的文档目录；与当前值相同时不做任何事。目录变化（另存为到
// 别处）后全部布局作废，按新目录重新解析。
func (e *Editor) SetImages(im *Images, dir string) {
	if im == e.images && dir == e.baseDir {
		return
	}
	e.images, e.baseDir = im, dir
	e.layoutGen++
	e.dirty = true
}

func (e *Editor) imageGen() uint64 {
	if e.images == nil {
		return 0
	}
	return e.images.gen
}

func (e *Editor) imageEntry(o imgObj) *imageEntry {
	if e.images == nil {
		return nil
	}
	if o.render != "" {
		return e.images.requestRender(RenderRequest{Kind: o.render, Src: o.src, Dark: e.dark, FontSize: bodySize * e.scale})
	}
	return e.images.request(o.src, e.baseDir)
}

// objSizes 返回投影中各图片的显示尺寸（DIP），maxW 为行宽。
func (e *Editor) objSizes(p *projection, maxW float32) [][2]float32 {
	if len(p.objs) == 0 {
		return nil
	}
	out := make([][2]float32, len(p.objs))
	for k, o := range p.objs {
		if o.html != nil {
			out[k] = [2]float32{maxW, e.htmlLayout(o.html, maxW).h}
			continue
		}
		out[k] = e.objSize(o, e.imageEntry(o), maxW)
	}
	return out
}

// objSize 按原版规则计算显示尺寸：width 属性为像素数或百分比（相对行宽），否则用原始尺寸；
// 宽度不超过行宽（CSS max-width: 100%），高度按比例。
func (e *Editor) objSize(o imgObj, en *imageEntry, maxW float32) [2]float32 {
	if o.render != "" {
		return e.renderSize(o, en, maxW)
	}
	want, hasWant := parseImageWidth(o.width, maxW)
	switch {
	case en == nil || en.state == imageFailed:
		tw, _ := ui.ShapeRichTextWith([]ui.Span{{Text: altLabel(o)}}, ui.Font{Family: e.fontBody, Size: imageAltSize}, ui.TextShape{}).Size()
		return [2]float32{min(maxW, tw+imageAltPad), e.lineH + 4}
	case en.state == imageLoading && en.w == 0:
		w := float32(imagePendingW)
		if hasWant {
			w = want
		}
		return [2]float32{min(maxW, w), imagePendingH}
	}
	w, h := en.w, en.h
	if hasWant && w > 0 {
		h, w = h*want/w, want
	}
	if w > maxW {
		h, w = h*maxW/w, maxW
	}
	return [2]float32{max(1, w), max(1, h)}
}

// renderSize 返回公式或图表对象的排版尺寸：已渲染时为渲染尺寸（宽度超过行宽时等比缩小）；行内公式的对象
// 高度只算基线以上部分（对象立在基线上），基线以下的深度在绘制时向下延伸。渲染中按源码估算占位，失败时显示
// 原文框（与原版错误隔离一致：不影响其余内容）。
func (e *Editor) renderSize(o imgObj, en *imageEntry, maxW float32) [2]float32 {
	switch {
	case en == nil || en.state == imageFailed:
		return e.objSize(imgObj{alt: firstLine(o.src)}, nil, maxW)
	case en.state == imageLoading:
		if o.render == RenderMathInline {
			return [2]float32{min(maxW, max(16, textWidth(o.src, bodySize*e.scale)*0.7)), e.lineH}
		}
		return [2]float32{min(maxW, 240), 48}
	}
	w, h, depth := en.w, en.h, en.depth
	if w > maxW {
		f := maxW / w
		w, h, depth = maxW, h*f, depth*f
	}
	if o.render == RenderMathInline {
		return [2]float32{max(1, w), max(1, h-depth)}
	}
	return [2]float32{max(1, w), max(1, h)}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

// parseImageWidth 解析 width 属性："600"、"600px" 为像素，"60%" 相对 maxW。
func parseImageWidth(v string, maxW float32) (float32, bool) {
	v = strings.TrimSpace(v)
	pct := strings.HasSuffix(v, "%")
	v = strings.TrimSuffix(strings.TrimSuffix(v, "%"), "px")
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 32)
	if err != nil || f <= 0 {
		return 0, false
	}
	if pct {
		return maxW * float32(f) / 100, true
	}
	return float32(f), true
}

func altLabel(o imgObj) string {
	if o.alt != "" {
		return o.alt
	}
	if i := strings.LastIndexAny(o.src, "/\\"); i >= 0 && i+1 < len(o.src) && !strings.HasPrefix(o.src, "data:") {
		return o.src[i+1:]
	}
	return "image"
}

// objRects 返回布局中各行内对象的位置，与 p.objs 一一对应（按显示偏移匹配）。
func objRects(l *ui.TextLayout, p *projection) []ui.Rect {
	if len(p.objs) == 0 {
		return nil
	}
	out := make([]ui.Rect, len(p.objs))
	k := 0
	for _, ob := range l.Objects() {
		for k < len(p.objs) && p.objs[k].u16 < ob.Index {
			k++
		}
		if k < len(p.objs) && p.objs[k].u16 == ob.Index {
			out[k] = ob.Rect
		}
	}
	return out
}

// drawImages 在行 r 的对象位置绘制图片；tx、ty 为文字原点。
func (e *Editor) drawImages(p *ui.Painter, r *row, tx, ty float32) {
	for k, o := range r.proj.objs {
		if k >= len(r.objRects) {
			return
		}
		rc := r.objRects[k]
		if rc.W <= 0 {
			continue
		}
		rc.X += tx
		rc.Y += ty
		if o.html != nil {
			if o.html.lay != nil {
				e.drawHTML(p, o.html, rc)
			}
			continue
		}
		if o.render == RenderMathInline {
			// 行内公式的对象框只含基线以上部分，按渲染比例补回基线以下的深度。
			if en := e.imageEntry(o); en != nil && en.state == imageReady && en.h > en.depth && en.depth > 0 {
				rc.H = rc.H * en.h / (en.h - en.depth)
			}
		}
		e.drawImageAt(p, o, rc)
	}
}

// drawImageAt 在 rc 处绘制一张图片：已加载时画位图或 SVG，加载中画占位底色，失败画替代文字框。
func (e *Editor) drawImageAt(p *ui.Painter, o imgObj, rc ui.Rect) {
	en := e.imageEntry(o) // 同时标记本帧使用，防止可见图片被淘汰
	switch {
	case en != nil && en.state == imageReady && en.bmp != nil:
		// 动画只在被绘制（可见、窗口可见）时预约下一帧，屏幕外与后台标签自然暂停。
		bmp, next := en.frameAt(p.Now())
		p.Image(bmp, rc, ui.FillBox)
		if next > 0 {
			p.After(next)
		}
	case en != nil && en.state == imageReady && en.svg != nil:
		p.Image(en.svg, rc, ui.Contain)
	case en != nil && en.state == imageLoading:
		p.Fill(rc, e.pal.codeBg, 4)
	default:
		p.StrokeDashed(rc, e.pal.muted, 4, 1)
		if ic := icons.Get("image"); ic != nil {
			p.Icon(ic, ui.Rect{X: rc.X + 8, Y: rc.Y + (rc.H-imageAltIcon)/2, W: imageAltIcon, H: imageAltIcon}, e.pal.muted)
		}
		label := altLabel(o)
		if o.render != "" {
			label = firstLine(o.src) // 公式、图表渲染失败时只显示源码首行，与尺寸计算一致
		}
		_, th := p.MeasureText(0, ui.Span{Text: "国", Font: e.fontBody, Size: imageAltSize})
		p.RichText(rc.X+8+imageAltIcon+6, rc.Y+(rc.H-th)/2, max(1, rc.W-imageAltPad), ui.Span{Text: label, Font: e.fontBody, Size: imageAltSize, Color: e.pal.muted})
	}
}

// imageHit 报告 pt 是否落在某张行内图片上，是则返回进入其原文的光标位置（Markdown 图片在“![”之后，
// <img> 在“<”之后），点击后图片展开为原文以便编辑。
func (e *Editor) imageHit(pt ui.Point) (int, bool) {
	e.sync()
	for _, v := range e.vsegs {
		r := &e.entries[v.i].rows[v.k]
		if len(r.objRects) == 0 || r.proj == nil {
			continue
		}
		s := e.segOf(v.i, v.k)
		x, y := pt.X-e.left()-s.x, pt.Y-v.y-s.padTop
		for k, rc := range r.objRects {
			if x < rc.X || x >= rc.X+rc.W || y < rc.Y || y >= rc.Y+rc.H {
				continue
			}
			// 目录卡片：点击条目跳到对应标题（原版 scrollToHeading），点击卡片其余部分进入原文。
			if b := s.proj.objs[k].html; b != nil && b.toc {
				if h, ok := e.tocItemAt(b, x-rc.X, y-rc.Y); ok {
					return -1 - h.Pos, true
				}
			}
			for _, run := range s.proj.runs {
				if run.atomic && run.disp == s.proj.objs[k].u16 {
					at := s.base + run.srcA
					if e.Doc.Source()[at] == '!' {
						return at + 2, true
					}
					return at + 1, true
				}
			}
		}
	}
	return 0, false
}

// htmlImageTags 是只含图片的 HTML 块中允许出现的标签（容器与换行），其余内容（文字、其他标签）
// 使该块按原文显示。
var htmlImageTags = map[string]bool{"img": true, "p": true, "div": true, "a": true, "center": true, "span": true, "br": true, "picture": true}

// htmlTags 依次返回 s 中的 HTML 标签与注释（区间），遇到标签外的非空白文字时 ok 为 false。
func htmlTags(s string, fn func(a, z int)) (ok bool) {
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++
		case strings.HasPrefix(s[i:], "<!--"):
			end := strings.Index(s[i+4:], "-->")
			if end < 0 {
				return false
			}
			i += 4 + end + 3
		case c == '<':
			z, q := i+1, byte(0)
			for ; z < len(s) && (q != 0 || s[z] != '>'); z++ {
				switch {
				case q != 0 && s[z] == q:
					q = 0
				case q == 0 && (s[z] == '"' || s[z] == '\''):
					q = s[z]
				}
			}
			if z >= len(s) {
				return false
			}
			fn(i, z+1)
			i = z + 1
		default:
			return false
		}
	}
	return true
}

// htmlImagesOnly 报告 HTML 块是否只由图片及其容器组成（至少一张有 src 的图片），并返回首个
// align 属性对应的对齐方式。
func htmlImagesOnly(s string) (ui.Align, bool) {
	imgs, bad := 0, false
	align, aligned := ui.Start, false
	ok := htmlTags(s, func(a, z int) {
		tag := s[a:z]
		if strings.HasPrefix(tag, "<!--") {
			return
		}
		name, closing, _, valid := htmlTag(tag)
		if !valid || !htmlImageTags[name] {
			bad = true
			return
		}
		if closing {
			return
		}
		if name == "img" {
			if htmlAttr(tag, "src") == "" {
				bad = true
			}
			imgs++
		}
		if v := strings.ToLower(htmlAttr(tag, "align")); !aligned && v != "" {
			align, aligned = map[string]ui.Align{"center": ui.Center, "right": ui.End}[v], true
		}
	})
	return align, ok && !bad && imgs > 0
}

// projectHTMLImages 为只含图片的 HTML 块生成收起时的投影：每个 <img> 为行内对象，<br> 为换行，
// 两张图片之间有空白时显示一个空格（与浏览器折叠空白一致），其余标签与注释隐藏。
func projectHTMLImages(text string) *projection {
	pb := projBuilder{src: text, p: &projection{}}
	prevImg, gap := false, -1
	htmlTags(text, func(a, z int) {
		tag := text[a:z]
		name, closing, _, _ := htmlTag(tag)
		switch {
		case name == "img" && !closing:
			if prevImg && gap >= 0 && gap < a {
				pb.emit(pb.pos, gap, 0, false)
				pb.replace(gap, a, " ", 0)
			}
			pb.emit(pb.pos, a, 0, false)
			pb.object(a, z, imgObj{src: htmlAttr(tag, "src"), width: htmlAttr(tag, "width"), alt: htmlAttr(tag, "alt")}, 0)
			prevImg = true
		case name == "br":
			pb.emit(pb.pos, a, 0, false)
			pb.replace(a, z, "\n", 0)
			prevImg = false
		default:
			pb.emit(pb.pos, z, 0, false)
		}
		gap = z
	})
	pb.emit(pb.pos, len(text), 0, false)
	return pb.finish()
}

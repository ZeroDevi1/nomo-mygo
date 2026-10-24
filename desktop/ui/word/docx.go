// Package word 把 Markdown 原文导出为 Word（.docx）文档，对应原版的 Word 导出（wordExportService）：
// 标题、正文、行内格式、编号列表、表格、代码、链接与脚注使用 Word 原生内容；任务列表保留勾选符号；已有目录生成
// 可点击的静态目录；图片内嵌，公式与 Mermaid 图表经外壳的离屏渲染转为 PNG；引用、提示块与注释保留文字；编辑器
// 支持的行内 HTML 按语义转换，其余 HTML 保留源码并提示。生成过程只读原文，可在后台 goroutine 中调用。
package word

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"html"
	"image/png"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/egoist/mygo/ui/markdown/parser"

	"nomo/desktop/ui/mdedit"
)

// Options 是一次导出的参数。
type Options struct {
	// Title 写入文档属性。
	Title string
	// BaseDir 是解析相对图片路径的目录；未保存的文档为空，相对图片按失败处理。
	BaseDir string
	// Render 渲染公式与 Mermaid 图表（外壳的离屏渲染服务）；为 nil 时保留源码并提示。
	Render func([]mdedit.RenderRequest) []mdedit.RenderResult
	// Text 返回界面文案（降级说明），为 nil 时用键名。
	Text func(key string, args ...string) string
	// Now 为文档属性中的创建时间，零值取当前时间。
	Now time.Time
	// HTTPClient 读取远程图片，为 nil 时用 8 秒超时的默认客户端（测试可替换）。
	HTTPClient *http.Client
}

// ErrBudget 表示图片超出资源预算（媒体总量 128 MiB 或单图 1600 万像素），导出失败。
var ErrBudget = errBudget

// Build 生成 .docx 文件内容，返回文件字节与降级说明（去重，按出现顺序）。
func Build(markdown string, o Options) ([]byte, []string, error) {
	b := &builder{o: o, mediaByHash: map[[32]byte]string{}, footIDs: map[string]int{}, warnSet: map[string]bool{},
		anchors: map[string]string{}, renders: map[renderKey]mdedit.RenderResult{}}
	b.md = parser.New(parser.Default | parser.ExtDollarMath)
	b.md.Set(stripFrontMatter(markdown))
	// ^上标^、~下标~ 改写为 <sup>、<sub>，与编辑区和 HTML 导出一致。
	if expanded := mdedit.ExpandScripts(b.md); expanded != b.md.Source() {
		b.md.Set(expanded)
	}
	b.src = b.md.Source()
	b.addRel("styles", "styles.xml", false)
	b.addRel("numbering", "numbering.xml", false)
	b.addRel("footnotes", "footnotes.xml", false)
	b.addRel("settings", "settings.xml", false)
	b.collectAnchors()
	b.prerender()

	var body strings.Builder
	b.out = &body
	if err := b.children(0, ctx{}); err != nil {
		return nil, nil, err
	}
	body.WriteString(`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/>` +
		`<w:pgMar w:top="1134" w:right="1134" w:bottom="1134" w:left="1134" w:header="708" w:footer="708" w:gutter="0"/></w:sectPr>`)
	doc := xmlHeader + `<w:document xmlns:w="` + nsW + `" xmlns:r="` + nsR + `" xmlns:wp="` + nsWP + `" xmlns:a="` + nsA +
		`" xmlns:pic="` + nsPic + `"><w:body>` + body.String() + `</w:body></w:document>`

	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := []struct{ name, data string }{
		{"[Content_Types].xml", contentTypes()},
		{"_rels/.rels", packageRels()},
		{"word/document.xml", doc},
		{"word/_rels/document.xml.rels", documentRels(b.rels)},
		{"word/styles.xml", styles()},
		{"word/numbering.xml", numbering(b.ordered)},
		{"word/footnotes.xml", footnotesPart(b.footBodies)},
		{"word/settings.xml", settings()},
		{"docProps/core.xml", coreProps(o.Title, now)},
		{"docProps/app.xml", appProps()},
	}
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			return nil, nil, err
		}
		if _, err := w.Write([]byte(f.data)); err != nil {
			return nil, nil, err
		}
	}
	for _, m := range b.media {
		// 图片已是压缩格式，存储不再压缩。
		w, err := zw.CreateHeader(&zip.FileHeader{Name: m.name, Method: zip.Store})
		if err != nil {
			return nil, nil, err
		}
		if _, err := w.Write(m.data); err != nil {
			return nil, nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), b.warnings, nil
}

// builder 是一次导出的状态，只在 Build 内使用。
type builder struct {
	o   Options
	md  *parser.Document
	src string
	out *strings.Builder // 当前写入位置（正文或脚注）

	rels        []rel
	media       []mediaFile
	mediaByHash map[[32]byte]string
	mediaBytes  int
	docPrID     int

	ordered    []orderedList  // 有序列表编号实例（numId = 下标 + 2）
	footIDs    map[string]int // 脚注标签 → 编号
	footBodies []string
	anchors    map[string]string // 标题锚点（与 HTML 导出相同的 slug）→ 书签名
	headingIdx int               // 已输出的标题数（书签名序号）

	renders  map[renderKey]mdedit.RenderResult
	warnings []string
	warnSet  map[string]bool
}

type renderKey struct{ kind, src string }

// ctx 是块的容器上下文：引用层数、提示块配色、列表层级。
type ctx struct {
	quote int  // 引用嵌套层数
	tone  int  // 提示块配色（calloutTones 下标），-1 为普通引用；quote 为 0 时无意义
	level int  // 列表层级（0 起）
	inner bool // 列表项内首段之后的块（缩进到列表文字位置）
	tight bool // 紧凑列表：段后不留空
}

func (b *builder) t(key string, args ...string) string {
	if b.o.Text == nil {
		return key
	}
	return b.o.Text(key, args...)
}

func (b *builder) warn(msg string) {
	if !b.warnSet[msg] {
		b.warnSet[msg] = true
		b.warnings = append(b.warnings, msg)
	}
}

// warnResource 记录资源未能嵌入（kind 为“图片”“公式”等，source 截断到 60 个字符）。
func (b *builder) warnResource(kind, source string) {
	if r := []rune(source); len(r) > 60 {
		source = string(r[:60]) + "…"
	}
	b.warn(b.t("wordExportResourceWarning", "kind", kind, "source", source))
}

func (b *builder) addRel(typ, target string, external bool) string {
	id := fmt.Sprintf("rId%d", len(b.rels)+1)
	b.rels = append(b.rels, rel{id: id, typ: typ, target: target, external: external})
	return id
}

// ---- 预处理：标题锚点与公式 / 图表渲染 ----

// collectAnchors 为各标题分配书签名：锚点规则与编辑区插入的目录相同（mdedit.HeadingIDs），目录中的 #锚点 链接
// 据此跳转到对应标题。
func (b *builder) collectAnchors() {
	var titles []string
	for i := 0; i < b.md.Len(); i++ {
		if b.md.Block(i).Kind == parser.Heading {
			titles = append(titles, b.plainText(b.md.Inlines(i)))
		}
	}
	for k, id := range mdedit.HeadingIDs(titles) {
		b.anchors[id] = fmt.Sprintf("nomo_h%d", k+1)
	}
}

// prerender 收集全部公式与 Mermaid 图表一次性渲染（离屏页面按批排布），结果按种类与源码查找。
func (b *builder) prerender() {
	if b.o.Render == nil {
		return
	}
	var reqs []mdedit.RenderRequest
	seen := map[renderKey]bool{}
	add := func(kind, src string) {
		k := renderKey{kind, src}
		if src != "" && !seen[k] {
			seen[k] = true
			reqs = append(reqs, mdedit.RenderRequest{Kind: kind, Src: src, FontSize: 15})
		}
	}
	for i := 0; i < b.md.Len(); i++ {
		blk := b.md.Block(i)
		switch {
		case blk.Kind == parser.MathBlock:
			add(mdedit.RenderMathDisplay, strings.TrimSpace(b.md.Lines(i)))
		case blk.Kind == parser.CodeBlock && strings.EqualFold(blk.Language(), "mermaid"):
			add(mdedit.RenderMermaid, b.md.Lines(i))
		case blk.Kind == parser.Paragraph || blk.Kind == parser.Heading || blk.Kind == parser.TableCell:
			for _, in := range b.md.Inlines(i) {
				if in.Kind == parser.Math && !mathEscaped(b.src[in.Start:in.End]) {
					kind := mdedit.RenderMathInline
					if in.Display {
						kind = mdedit.RenderMathDisplay
					}
					add(kind, in.Text)
				}
			}
		}
	}
	// 离屏页面一批放不下的项标记为 Skipped，重新排队直到全部完成或不再有进展。
	for len(reqs) > 0 {
		res := b.o.Render(reqs)
		var next []mdedit.RenderRequest
		for k, r := range res {
			if r.Skipped {
				next = append(next, reqs[k])
				continue
			}
			b.renders[renderKey{reqs[k].Kind, reqs[k].Src}] = r
		}
		if len(next) == len(reqs) {
			break
		}
		reqs = next
	}
}

// mathEscaped 报告 \(…\)、\[…\] 写法：原版按转义的括号处理，不渲染。
func mathEscaped(src string) bool {
	return len(src) >= 2 && src[0] == '\\' && (src[1] == '(' || src[1] == '[')
}

// renderedPicture 返回已渲染的公式 / 图表图片（嵌入为 PNG），没有或失败时 ok 为 false。
func (b *builder) renderedPicture(kind, src string) (picture, float64, bool, error) {
	r, ok := b.renders[renderKey{kind, src}]
	if !ok || r.Err != nil || r.Img == nil || r.W <= 0 {
		return picture{}, 0, false, nil
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, r.Img); err != nil {
		return picture{}, 0, false, nil
	}
	rid, err := b.addMedia(buf.Bytes(), "png")
	if err != nil {
		return picture{}, 0, false, err
	}
	return picture{rid: rid, w: float64(r.W), h: float64(r.H)}, float64(r.Depth) * 0.75, true, nil
}

// ---- 块 ----

// calloutTones 是提示块的标题色与底色（note、tip、important、warning、caution）。
var calloutTones = [5][2]string{{"0969DA", "DDF4FF"}, {"1A7F37", "DAFBE1"}, {"8250DF", "FBEFFF"}, {"9A6700", "FFF8C5"}, {"CF222E", "FFEBE9"}}

func (b *builder) children(parent int, c ctx) error {
	for i := range b.md.Children(parent) {
		if err := b.block(i, c); err != nil {
			return err
		}
	}
	return nil
}

// pPr 返回段落属性（容器的缩进、引用边框与提示块底色），extra 为附加属性（样式、编号、对齐等，须在前）。
func (b *builder) pPr(c ctx, extra string) string {
	var s strings.Builder
	s.WriteString(extra)
	if c.quote > 0 {
		color, fill := "D0D7DE", ""
		if c.tone >= 0 && c.tone < len(calloutTones) {
			color, fill = calloutTones[c.tone][0], calloutTones[c.tone][1]
		}
		s.WriteString(`<w:pBdr><w:left w:val="single" w:sz="18" w:space="8" w:color="` + color + `"/></w:pBdr>`)
		if fill != "" {
			s.WriteString(`<w:shd w:val="clear" w:color="auto" w:fill="` + fill + `"/>`)
		}
	}
	if c.tight {
		s.WriteString(`<w:spacing w:after="0"/>`)
	}
	if left := c.quote*300 + b.listIndent(c); left > 0 && !strings.Contains(extra, "<w:numPr>") {
		fmt.Fprintf(&s, `<w:ind w:left="%d"/>`, left)
	}
	if s.Len() == 0 {
		return ""
	}
	return "<w:pPr>" + s.String() + "</w:pPr>"
}

// listIndent 是列表项内后续块的缩进（与编号文字对齐）。
func (b *builder) listIndent(c ctx) int {
	if c.inner {
		return 420 * (c.level + 1)
	}
	return 0
}

func (b *builder) block(i int, c ctx) error {
	blk := b.md.Block(i)
	switch blk.Kind {
	case parser.Paragraph:
		return b.paragraph(i, c, "", nil)
	case parser.Heading:
		b.headingIdx++
		name := fmt.Sprintf("nomo_h%d", b.headingIdx)
		lvl := min(max(blk.Level, 1), 6)
		b.out.WriteString(`<w:p>` + b.pPr(c, fmt.Sprintf(`<w:pStyle w:val="Heading%d"/>`, lvl)))
		fmt.Fprintf(b.out, `<w:bookmarkStart w:id="%d" w:name="%s"/>`, b.headingIdx, name)
		if err := b.inlines(b.md.Inlines(i), 0, -1, runStyle{}); err != nil {
			return err
		}
		fmt.Fprintf(b.out, `<w:bookmarkEnd w:id="%d"/></w:p>`, b.headingIdx)
	case parser.ThematicBreak:
		b.out.WriteString(`<w:p><w:pPr><w:pBdr><w:bottom w:val="single" w:sz="6" w:space="1" w:color="D0D7DE"/></w:pBdr></w:pPr></w:p>`)
	case parser.CodeBlock:
		if strings.EqualFold(blk.Language(), "mermaid") {
			ok, err := b.displayPicture(mdedit.RenderMermaid, b.md.Lines(i), c)
			if err != nil || ok {
				return err
			}
			b.warnResource(b.t("wordExportFormula"), "mermaid")
		}
		b.codeLines(b.md.Lines(i), c)
	case parser.MathBlock:
		tex := strings.TrimSpace(b.md.Lines(i))
		ok, err := b.displayPicture(mdedit.RenderMathDisplay, tex, c)
		if err != nil || ok {
			return err
		}
		b.warnResource(b.t("wordExportFormula"), tex)
		b.codeLines(tex, c)
	case parser.HTMLBlock:
		return b.htmlBlock(i, c)
	case parser.Quote:
		inner := c
		inner.quote++
		inner.tone = -1
		first := -1
		for k := range b.md.Children(i) {
			first = k
			break
		}
		var skip int // 提示块首段中 [!TYPE] 标记行的结尾（绝对偏移）
		if first >= 0 && b.md.Block(first).Kind == parser.Paragraph {
			fb := b.md.Block(first)
			if tone, _, title, ok := mdedit.ParseCallout(b.src[fb.Start:fb.End]); ok {
				inner.tone = tone
				head := `<w:pPr>` + strings.TrimPrefix(strings.TrimSuffix(b.pPr(inner, `<w:keepNext/>`), "</w:pPr>"), "<w:pPr>") + `</w:pPr>`
				b.out.WriteString(`<w:p>` + head + `<w:r><w:rPr><w:b/><w:color w:val="` + calloutTones[tone][0] + `"/></w:rPr><w:t xml:space="preserve">` + esc(title) + `</w:t></w:r></w:p>`)
				skip = fb.End
				if nl := strings.IndexByte(b.src[fb.Start:fb.End], '\n'); nl >= 0 {
					skip = fb.Start + nl
				}
			}
		}
		for k := range b.md.Children(i) {
			if k == first && skip > 0 {
				if err := b.paragraph(k, inner, "", &skip); err != nil {
					return err
				}
				continue
			}
			if err := b.block(k, inner); err != nil {
				return err
			}
		}
	case parser.List:
		numID := 1
		if blk.Ordered {
			b.ordered = append(b.ordered, orderedList{level: c.level, start: blk.Number})
			numID = len(b.ordered) + 1
		}
		for item := range b.md.Children(i) {
			if err := b.listItem(item, numID, blk.Tight, c); err != nil {
				return err
			}
		}
	case parser.Table:
		return b.table(i, c)
	case parser.FootnoteDefinition:
		// 脚注内容写入 footnotes.xml（见 footnoteRef），正文中不输出。
	}
	return nil
}

func (b *builder) listItem(item, numID int, tight bool, c ctx) error {
	task := b.md.Block(item).Task
	first := true
	for k := range b.md.Children(item) {
		kb := b.md.Block(k)
		switch {
		case first && kb.Kind == parser.Paragraph:
			first = false
			pc := c
			pc.tight, pc.inner = tight, false
			extra := fmt.Sprintf(`<w:numPr><w:ilvl w:val="%d"/><w:numId w:val="%d"/></w:numPr>`, c.level, numID)
			prefix := map[parser.Task]string{parser.Unchecked: "☐ ", parser.Checked: "☑ "}[task]
			b.out.WriteString(`<w:p>` + b.pPr(pc, extra))
			if prefix != "" {
				b.out.WriteString(`<w:r><w:t xml:space="preserve">` + prefix + `</w:t></w:r>`)
			}
			if err := b.inlines(b.md.Inlines(k), 0, -1, runStyle{}); err != nil {
				return err
			}
			b.out.WriteString(`</w:p>`)
		case kb.Kind == parser.List:
			first = false
			nc := c
			nc.level = min(c.level+1, 8)
			nc.inner = false
			if err := b.block(k, nc); err != nil {
				return err
			}
		default:
			first = false
			nc := c
			nc.inner, nc.tight = true, tight
			if err := b.block(k, nc); err != nil {
				return err
			}
		}
	}
	if first { // 空列表项
		b.out.WriteString(`<w:p>` + b.pPr(c, fmt.Sprintf(`<w:numPr><w:ilvl w:val="%d"/><w:numId w:val="%d"/></w:numPr>`, c.level, numID)) + `</w:p>`)
	}
	return nil
}

// paragraph 输出段落；skipTo 非空时跳过结束偏移不超过它的行内节点（提示块首段的标记行）。
func (b *builder) paragraph(i int, c ctx, extra string, skipTo *int) error {
	ins := b.md.Inlines(i)
	from := 0
	if skipTo != nil {
		for from < len(ins) && ins[from].End <= *skipTo+1 {
			from = ins[from].Next
		}
		if from >= len(ins) {
			return nil
		}
	}
	b.out.WriteString(`<w:p>` + b.pPr(c, extra))
	if err := b.inlines(ins, from, -1, runStyle{}); err != nil {
		return err
	}
	b.out.WriteString(`</w:p>`)
	return nil
}

func (b *builder) codeLines(code string, c ctx) {
	lines := strings.Split(strings.TrimRight(code, "\n"), "\n")
	for k, line := range lines {
		extra := `<w:pStyle w:val="Code"/>`
		if k == len(lines)-1 {
			extra += `<w:spacing w:after="120"/>`
		}
		b.out.WriteString(`<w:p>` + b.pPr(c, extra) + textRun(strings.ReplaceAll(line, "\t", "    "), "") + `</w:p>`)
	}
}

// displayPicture 输出居中的公式块或图表图片，未渲染时返回 false。
func (b *builder) displayPicture(kind, src string, c ctx) (bool, error) {
	pic, _, ok, err := b.renderedPicture(kind, src)
	if err != nil || !ok {
		return false, err
	}
	b.out.WriteString(`<w:p>` + b.pPr(c, `<w:jc w:val="center"/>`) + b.drawing(pic, src, contentEMU, 0) + `</w:p>`)
	return true, nil
}

func (b *builder) table(i int, c ctx) error {
	blk := b.md.Block(i)
	cols := len(blk.Align)
	if cols == 0 {
		return nil
	}
	w := contentTwips - c.quote*300
	b.out.WriteString(`<w:tbl><w:tblPr><w:tblStyle w:val="TableGrid"/><w:tblW w:w="5000" w:type="pct"/>`)
	if c.quote > 0 {
		fmt.Fprintf(b.out, `<w:tblInd w:w="%d" w:type="dxa"/>`, c.quote*300)
	}
	b.out.WriteString(`<w:tblLook w:val="04A0" w:firstRow="1" w:lastRow="0" w:firstColumn="0" w:lastColumn="0" w:noHBand="0" w:noVBand="1"/></w:tblPr><w:tblGrid>`)
	for range cols {
		fmt.Fprintf(b.out, `<w:gridCol w:w="%d"/>`, w/cols)
	}
	b.out.WriteString(`</w:tblGrid>`)
	for row := range b.md.Children(i) {
		header := b.md.Block(row).Header
		b.out.WriteString(`<w:tr>`)
		if header {
			b.out.WriteString(`<w:trPr><w:tblHeader/></w:trPr>`)
		}
		n := 0
		for cell := range b.md.Children(row) {
			if n >= cols {
				break
			}
			jc := map[parser.Align]string{parser.AlignCenter: "center", parser.AlignRight: "right"}[blk.Align[n]]
			b.out.WriteString(`<w:tc><w:tcPr><w:tcW w:w="` + fmt.Sprint(w/cols) + `" w:type="dxa"/>`)
			if header {
				b.out.WriteString(`<w:shd w:val="clear" w:color="auto" w:fill="F6F8FA"/>`)
			}
			b.out.WriteString(`</w:tcPr><w:p><w:pPr><w:spacing w:after="0"/>`)
			if jc != "" {
				b.out.WriteString(`<w:jc w:val="` + jc + `"/>`)
			}
			b.out.WriteString(`</w:pPr>`)
			if err := b.inlines(b.md.Inlines(cell), 0, -1, runStyle{bold: header}); err != nil {
				return err
			}
			b.out.WriteString(`</w:p></w:tc>`)
			n++
		}
		for ; n < cols; n++ { // 缺少的单元格补空，Word 要求每行单元格数一致
			b.out.WriteString(`<w:tc><w:tcPr><w:tcW w:w="` + fmt.Sprint(w/cols) + `" w:type="dxa"/></w:tcPr><w:p/></w:tc>`)
		}
		b.out.WriteString(`</w:tr>`)
	}
	b.out.WriteString(`</w:tbl><w:p><w:pPr><w:spacing w:after="0"/></w:pPr></w:p>`)
	return nil
}

var (
	htmlCommentRe = regexp.MustCompile(`(?s)^\s*<!--(.*?)-->\s*$`)
	htmlSummaryRe = regexp.MustCompile(`(?is)<summary[^>]*>(.*?)</summary>`)
	htmlDetailsRe = regexp.MustCompile(`(?is)^\s*(</?details[^>]*>\s*)+$`)
	htmlImgRe     = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	htmlSVGRe     = regexp.MustCompile(`(?is)<svg\b.*?</svg>`)
	htmlTagRe     = regexp.MustCompile(`(?s)<[^>]+>`)
	htmlAlignRe   = regexp.MustCompile(`(?i)\balign\s*=\s*["']?(center|right)`)
)

// htmlBlock 按语义转换 HTML 块：注释保留文字（目录标记略去），折叠块的标题输出为粗体段落、标记本身略去，只含图片
// （<img>、内嵌 <svg>）的块输出图片，其余保留源码并提示。
func (b *builder) htmlBlock(i int, c ctx) error {
	raw := b.md.Lines(i)
	if m := htmlCommentRe.FindStringSubmatch(raw); m != nil {
		text := strings.TrimSpace(m[1])
		if text == "" || text == "toc" || text == "/toc" {
			return nil
		}
		b.out.WriteString(`<w:p>` + b.pPr(c, "") + `<w:r><w:rPr><w:i/><w:color w:val="6E7781"/></w:rPr><w:t xml:space="preserve">` + esc(text) + `</w:t></w:r></w:p>`)
		return nil
	}
	if htmlDetailsRe.MatchString(raw) {
		return nil
	}
	if m := htmlSummaryRe.FindStringSubmatch(raw); m != nil && strings.TrimSpace(htmlSummaryRe.ReplaceAllString(htmlTagRe.ReplaceAllString(strings.ReplaceAll(raw, m[0], ""), ""), "")) == "" {
		b.out.WriteString(`<w:p>` + b.pPr(c, `<w:keepNext/>`) + `<w:r><w:rPr><w:b/></w:rPr><w:t xml:space="preserve">` + esc(htmlText(m[1])) + `</w:t></w:r></w:p>`)
		return nil
	}
	// 只含图片：去掉 <img>、<svg> 与其余标签后没有文字。
	rest := htmlTagRe.ReplaceAllString(htmlSVGRe.ReplaceAllString(htmlImgRe.ReplaceAllString(raw, ""), ""), "")
	if strings.TrimSpace(rest) == "" && (htmlImgRe.MatchString(raw) || htmlSVGRe.MatchString(raw)) {
		jc := ""
		if m := htmlAlignRe.FindStringSubmatch(raw); m != nil {
			jc = `<w:jc w:val="` + strings.ToLower(m[1]) + `"/>`
		}
		b.out.WriteString(`<w:p>` + b.pPr(c, jc))
		for _, loc := range regexp.MustCompile(`(?is)<img\b[^>]*>|<svg\b.*?</svg>`).FindAllStringIndex(raw, -1) {
			if err := b.htmlImage(raw[loc[0]:loc[1]]); err != nil {
				return err
			}
		}
		b.out.WriteString(`</w:p>`)
		return nil
	}
	b.warn(b.t("wordExportHtmlWarning"))
	b.codeLines(raw, c)
	return nil
}

// htmlImage 输出 <img> 或内嵌 <svg> 元素的图片；失败时输出替代文字。
func (b *builder) htmlImage(tag string) error {
	if strings.HasPrefix(strings.ToLower(tag), "<svg") {
		pic, err := b.embedSVG([]byte(tag))
		if errors.Is(err, errBudget) {
			return err
		}
		if err != nil {
			b.warnResource(b.t("wordExportImage"), "<svg>")
			return nil
		}
		b.out.WriteString(b.drawing(pic, "SVG", contentEMU, 0))
		return nil
	}
	src, alt := mdedit.HTMLAttr(tag, "src"), mdedit.HTMLAttr(tag, "alt")
	pic, ok, err := b.embedImage(src)
	if err != nil || !ok {
		if err == nil && alt != "" {
			b.out.WriteString(textRun(alt, ""))
		}
		return err
	}
	if wv := mdedit.HTMLAttr(tag, "width"); wv != "" {
		pic = scaleWidth(pic, wv)
	}
	b.out.WriteString(b.drawing(pic, alt, contentEMU, 0))
	return nil
}

// scaleWidth 按 <img width> 调整显示尺寸：像素值直接使用，百分比按版心宽度。
func scaleWidth(p picture, v string) picture {
	v = strings.TrimSpace(v)
	var w float64
	if strings.HasSuffix(v, "%") {
		var pct float64
		if _, err := fmt.Sscan(strings.TrimSuffix(v, "%"), &pct); err != nil || pct <= 0 {
			return p
		}
		w = float64(contentEMU) / emuPerPx * pct / 100
	} else if _, err := fmt.Sscan(strings.TrimSuffix(v, "px"), &w); err != nil || w <= 0 {
		return p
	}
	if p.w > 0 {
		p.h, p.w = p.h*w/p.w, w
	}
	return p
}

// htmlText 返回 HTML 片段的纯文本（去掉标签、解码实体）。
func htmlText(s string) string {
	return strings.TrimSpace(html.UnescapeString(htmlTagRe.ReplaceAllString(s, "")))
}

// ---- 行内 ----

// runStyle 是行内格式。
type runStyle struct {
	bold, italic, strike, underline, code, mark, sup, sub bool
	color                                                 string // 十六进制 RRGGBB，空为默认
	link                                                  bool
}

func (s runStyle) rPr() string {
	var b strings.Builder
	if s.code {
		b.WriteString(`<w:rStyle w:val="CodeChar"/>`)
	} else if s.link {
		b.WriteString(`<w:rStyle w:val="Hyperlink"/>`)
	}
	if s.bold {
		b.WriteString(`<w:b/><w:bCs/>`)
	}
	if s.italic {
		b.WriteString(`<w:i/><w:iCs/>`)
	}
	if s.strike {
		b.WriteString(`<w:strike/>`)
	}
	if s.color != "" {
		b.WriteString(`<w:color w:val="` + s.color + `"/>`)
	}
	if s.underline {
		b.WriteString(`<w:u w:val="single"/>`)
	}
	if s.mark {
		b.WriteString(`<w:highlight w:val="yellow"/>`)
	}
	if s.sup {
		b.WriteString(`<w:vertAlign w:val="superscript"/>`)
	} else if s.sub {
		b.WriteString(`<w:vertAlign w:val="subscript"/>`)
	}
	return b.String()
}

// textRun 返回一段文字的 run；换行转为 <w:br/>，制表符转为 <w:tab/>。
func textRun(text, rPr string) string {
	if text == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("<w:r>")
	if rPr != "" {
		b.WriteString("<w:rPr>" + rPr + "</w:rPr>")
	}
	for k, line := range strings.Split(text, "\n") {
		if k > 0 {
			b.WriteString("<w:br/>")
		}
		for j, part := range strings.Split(line, "\t") {
			if j > 0 {
				b.WriteString("<w:tab/>")
			}
			if part != "" {
				b.WriteString(`<w:t xml:space="preserve">` + esc(part) + `</w:t>`)
			}
		}
	}
	b.WriteString("</w:r>")
	return b.String()
}

// inlines 输出 ins[i:end) 的 run（end 为 -1 表示到末尾）。同级 HTML 开始标签为其后的兄弟节点叠加格式，直到对应的
// 结束标签（规则同编辑区投影）。
func (b *builder) inlines(ins []parser.Inline, i, end int, st runStyle) error {
	if end < 0 {
		end = len(ins)
	}
	type frame struct {
		name string
		st   runStyle
	}
	var stack []frame
	cur := st
	for i < end {
		in := ins[i]
		switch in.Kind {
		case parser.Text:
			b.out.WriteString(textRun(in.Text, cur.rPr()))
		case parser.SoftBreak:
			// 中文之间的软换行不加空格（与浏览器折叠规则一致的近似）。
			if !(i > 0 && i+1 < len(ins) && cjkEdge(ins[i-1].Text, true) && cjkEdge(ins[i+1].Text, false)) {
				b.out.WriteString(textRun(" ", cur.rPr()))
			}
		case parser.LineBreak:
			b.out.WriteString(`<w:r><w:br/></w:r>`)
		case parser.Code:
			cs := cur
			cs.code = true
			b.out.WriteString(textRun(in.Text, cs.rPr()))
		case parser.Emphasis, parser.Strong, parser.Strikethrough:
			s := cur
			switch in.Kind {
			case parser.Emphasis:
				s.italic = true
			case parser.Strong:
				s.bold = true
			default:
				s.strike = true
			}
			if err := b.inlines(ins, i+1, in.Next, s); err != nil {
				return err
			}
		case parser.Link:
			if err := b.link(ins, i, cur); err != nil {
				return err
			}
		case parser.Image:
			alt := b.plainText(ins[i+1 : in.Next])
			pic, ok, err := b.embedImage(in.Dest)
			if err != nil {
				return err
			}
			if ok {
				b.out.WriteString(b.drawing(pic, alt, contentEMU, 0))
			} else {
				b.out.WriteString(textRun(alt, cur.rPr()))
			}
		case parser.HTML:
			raw := b.src[in.Start:in.End]
			if strings.HasPrefix(raw, "<!--") {
				break
			}
			name, closing, self, color, hasColor, ok := mdedit.InlineTag(raw)
			switch {
			case ok && (name == "img" || name == "svg") && !closing:
				tag := raw
				if name == "svg" && !self {
					// 行内 <svg>：连同配对的结束标签整体栅格化。
					j := svgCloseIndex(ins, i, end, b.src)
					if j < 0 {
						b.out.WriteString(textRun(raw, cur.rPr()))
						break
					}
					tag = b.src[in.Start:ins[j].End]
					if err := b.htmlImage(tag); err != nil {
						return err
					}
					i = ins[j].Next
					continue
				}
				if err := b.htmlImage(tag); err != nil {
					return err
				}
			case ok && name == "br":
				b.out.WriteString(`<w:r><w:br/></w:r>`)
			case ok && closing:
				for k := len(stack) - 1; k >= 0; k-- {
					if stack[k].name == name {
						cur = stack[k].st
						stack = stack[:k]
						break
					}
				}
			case ok && !self:
				s, known := applyTag(cur, name)
				if !known {
					b.warn(b.t("wordExportHtmlWarning"))
					b.out.WriteString(textRun(raw, cur.rPr()))
					break
				}
				if hasColor {
					s.color = fmt.Sprintf("%02X%02X%02X", color.R, color.G, color.B)
				}
				stack = append(stack, frame{name, cur})
				cur = s
			default:
				b.warn(b.t("wordExportHtmlWarning"))
				b.out.WriteString(textRun(raw, cur.rPr()))
			}
		case parser.Math:
			src := b.src[in.Start:in.End]
			if mathEscaped(src) {
				b.out.WriteString(textRun(src[1:len(src)-2]+src[len(src)-1:], cur.rPr()))
				break
			}
			kind := mdedit.RenderMathInline
			if in.Display {
				kind = mdedit.RenderMathDisplay
			}
			pic, depth, ok, err := b.renderedPicture(kind, in.Text)
			if err != nil {
				return err
			}
			if !ok {
				b.warnResource(b.t("wordExportFormula"), in.Text)
				cs := cur
				cs.code = true
				b.out.WriteString(textRun(src, cs.rPr()))
				break
			}
			if kind == mdedit.RenderMathInline {
				b.out.WriteString(b.drawing(pic, in.Text, contentEMU, depth))
			} else {
				b.out.WriteString(b.drawing(pic, in.Text, contentEMU, 0))
			}
		case parser.FootnoteRef:
			if err := b.footnoteRef(in, cur); err != nil {
				return err
			}
		}
		i = in.Next
	}
	return nil
}

// applyTag 返回行内 HTML 开始标签 name 叠加后的格式；不认识的标签 known 为 false（保留源码）。
func applyTag(s runStyle, name string) (runStyle, bool) {
	switch name {
	case "strong", "b":
		s.bold = true
	case "em", "i", "cite", "dfn", "var":
		s.italic = true
	case "u", "ins":
		s.underline = true
	case "s", "del", "strike":
		s.strike = true
	case "mark":
		s.mark = true
	case "code", "kbd", "samp":
		s.code = true
	case "sup":
		s.sup = true
	case "sub":
		s.sub = true
	case "span", "font", "small", "abbr", "time", "bdi", "a":
	default:
		return s, false
	}
	return s, true
}

func svgCloseIndex(ins []parser.Inline, i, end int, src string) int {
	depth := 0
	for j := i; j < end; j = ins[j].Next {
		if ins[j].Kind != parser.HTML {
			continue
		}
		if name, closing, self, _, _, ok := mdedit.InlineTag(src[ins[j].Start:ins[j].End]); ok && name == "svg" {
			switch {
			case closing:
				depth--
			case !self:
				depth++
			}
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// link 输出超链接：#锚点 指向标题书签，其余为外部链接；不安全的协议只输出文字。
func (b *builder) link(ins []parser.Inline, i int, cur runStyle) error {
	in := ins[i]
	dest := strings.TrimSpace(in.Dest)
	ls := cur
	ls.link = true
	open := ""
	switch lower := strings.ToLower(dest); {
	case strings.HasPrefix(dest, "#"):
		frag := dest[1:]
		if u, err := url.PathUnescape(frag); err == nil {
			frag = u
		}
		if name, ok := b.anchors[frag]; ok {
			open = `<w:hyperlink w:anchor="` + name + `" w:history="1">`
		}
	case strings.HasPrefix(lower, "javascript:"), strings.HasPrefix(lower, "vbscript:"), strings.HasPrefix(lower, "data:"), dest == "":
	default:
		open = `<w:hyperlink r:id="` + b.addRel("hyperlink", dest, true) + `" w:history="1">`
	}
	if open == "" {
		ls.link = false
	}
	b.out.WriteString(open)
	if err := b.inlines(ins, i+1, in.Next, ls); err != nil {
		return err
	}
	if open != "" {
		b.out.WriteString(`</w:hyperlink>`)
	}
	return nil
}

// footnoteRef 输出脚注引用；首次引用时把定义的内容写入 footnotes.xml。缺少定义时保留引用文字并提示。
func (b *builder) footnoteRef(in parser.Inline, cur runStyle) error {
	label := in.Text
	id, ok := b.footIDs[label]
	if !ok {
		def, found := b.md.Footnote(label)
		if !found {
			b.warn(b.t("wordExportFootnoteWarning", "id", label))
			b.out.WriteString(textRun(b.src[in.Start:in.End], cur.rPr()))
			return nil
		}
		id = len(b.footBodies) + 1
		b.footIDs[label] = id
		b.footBodies = append(b.footBodies, "") // 先占位，内容中引用其他脚注时编号保持递增
		saved := b.out
		var body strings.Builder
		b.out = &body
		firstPara := true
		for k := range b.md.Children(def) {
			if b.md.Block(k).Kind == parser.Paragraph {
				body.WriteString(`<w:p><w:pPr><w:pStyle w:val="FootnoteText"/></w:pPr>`)
				if firstPara {
					body.WriteString(`<w:r><w:rPr><w:rStyle w:val="FootnoteReference"/></w:rPr><w:footnoteRef/></w:r><w:r><w:t xml:space="preserve"> </w:t></w:r>`)
					firstPara = false
				}
				if err := b.inlines(b.md.Inlines(k), 0, -1, runStyle{}); err != nil {
					b.out = saved
					return err
				}
				body.WriteString(`</w:p>`)
				continue
			}
			if err := b.block(k, ctx{}); err != nil {
				b.out = saved
				return err
			}
		}
		if firstPara {
			body.WriteString(`<w:p><w:pPr><w:pStyle w:val="FootnoteText"/></w:pPr><w:r><w:rPr><w:rStyle w:val="FootnoteReference"/></w:rPr><w:footnoteRef/></w:r></w:p>`)
		}
		b.out = saved
		b.footBodies[id-1] = body.String()
	}
	fmt.Fprintf(b.out, `<w:r><w:rPr><w:rStyle w:val="FootnoteReference"/></w:rPr><w:footnoteReference w:id="%d"/></w:r>`, id)
	return nil
}

// plainText 返回行内节点的纯文本（图片说明、标题锚点）。
func (b *builder) plainText(ins []parser.Inline) string {
	var s strings.Builder
	for _, in := range ins {
		switch in.Kind {
		case parser.Text, parser.Code, parser.Math:
			s.WriteString(in.Text)
		case parser.SoftBreak, parser.LineBreak:
			s.WriteByte(' ')
		}
	}
	return s.String()
}

// cjkEdge 报告文字的末字（end 为 true）或首字是否为中日韩字符。
func cjkEdge(s string, end bool) bool {
	r := []rune(s)
	if len(r) == 0 {
		return false
	}
	c := r[0]
	if end {
		c = r[len(r)-1]
	}
	return unicode.Is(unicode.Han, c) || unicode.Is(unicode.Hiragana, c) || unicode.Is(unicode.Katakana, c) ||
		unicode.Is(unicode.Hangul, c) || c >= 0x3000 && c <= 0x303F || c >= 0xFF00 && c <= 0xFFEF
}

func stripFrontMatter(src string) string {
	if !strings.HasPrefix(src, "---\n") && !strings.HasPrefix(src, "---\r\n") {
		return src
	}
	rest := src[strings.IndexByte(src, '\n')+1:]
	for _, end := range []string{"\n---\n", "\n...\n", "\n---\r\n", "\n...\r\n"} {
		if i := strings.Index("\n"+rest, end); i >= 0 {
			return strings.TrimLeft(rest[i+len(end)-1:], "\r\n")
		}
	}
	return src
}

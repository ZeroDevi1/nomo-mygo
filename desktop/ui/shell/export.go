package shell

import (
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown/parser"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/docfs"
	"nomo/desktop/ui/icons"
	"nomo/desktop/ui/mdedit"
	"nomo/desktop/ui/themes"
	"nomo/desktop/ui/word"
)

// 导出 HTML / PDF（对应原版 exportService）：正文由解析器按 cmark-gfm 规则渲染为 HTML，套用原版
// export-document.css 与当前颜色主题的 CSS 变量，本地图片内联为 data URL，得到单个自包含文件。
// PDF 把同一份 HTML 交给 Options.ExportPDF（主程序用按需创建的隐藏 WebView 打印，导出完即销毁，
// 不常驻）；标题前插入书签标记，由主程序转换为 PDF 书签。
//
// 公式与 Mermaid 不渲染（没有离屏渲染页）：公式导出 TeX 原文（等宽显示），Mermaid 导出为代码块。
//
// 与原版的差异：代码高亮用 MyGo editor 的 Tokenizer 与 GitHub 配色（原版为 Shiki），未知语言不高亮；
// 提示块按引用块显示，不附带精细排版运行时。

// PDFOutlineEntry 是 PDF 书签：MarkerURI 为标题前标记链接的地址，主程序据此定位页面。
type PDFOutlineEntry struct {
	MarkerURI string
	Title     string
	Level     uint8
}

// maxInlineImage 是内联到导出文件的单张本地图片上限；更大的图片保留原地址，避免导出文件过大。
const maxInlineImage = 20 << 20

const pdfMarkerHost = "nomo-pdf-outline.invalid"

var (
	headingTagRe      = regexp.MustCompile(`(?s)<h([1-6])>(.*?)</h[1-6]>`)
	imgSrcRe          = regexp.MustCompile(`<img src="([^"]*)"`)
	detailsTagRe      = regexp.MustCompile(`(?i)<details(?:\s[^>]*)?>`)
	detailsOpenAttrRe = regexp.MustCompile(`(?i)\sopen\b`)
	codeBlockRe       = regexp.MustCompile(`(?s)<pre><code class="language-([^"]*)">(.*?)</code></pre>`)
	tagRe             = regexp.MustCompile(`<[^>]+>`)
)

// exportDocument 生成导出 HTML；forPDF 时插入书签标记并返回书签列表。
func (s *Shell) exportDocument(tab *Tab, dark, forPDF bool) (string, []PDFOutlineEntry) {
	src := stripFrontMatter(tab.Doc.Source())
	body := parser.New(parser.Default | parser.ExtDollarMath)
	body.Set(src)
	// ^上标^、~下标~ 不是解析器语法，先改写为 <sup>、<sub> 再生成 HTML（规则同编辑区，见 mdedit/script.go）。
	if expanded := mdedit.ExpandScripts(body); expanded != src {
		body.Set(expanded)
	}
	out := highlightCodeHTML(sanitizeExportHTML(body.HTML()), dark)
	if forPDF {
		// 打印前展开全部折叠块（原版 exportPrint 在布局前把 details.open 设为 true），PDF 中正文完整。
		out = detailsTagRe.ReplaceAllStringFunc(out, func(m string) string {
			if detailsOpenAttrRe.MatchString(m) {
				return m
			}
			return "<details open" + m[len("<details"):]
		})
	}

	out = exportCallouts(out)

	// 标题：加 id（与原版目录锚点一样按标题文字生成，重复时追加序号）与 PDF 书签标记。
	var outline []PDFOutlineEntry
	job := strconv.FormatInt(time.Now().UnixNano(), 36)
	ids := map[string]int{}
	out = headingTagRe.ReplaceAllStringFunc(out, func(m string) string {
		sub := headingTagRe.FindStringSubmatch(m)
		level, inner := sub[1], sub[2]
		title := strings.Join(strings.Fields(html.UnescapeString(tagRe.ReplaceAllString(inner, ""))), " ")
		id := slugify(title)
		if n := ids[id]; n > 0 {
			ids[id] = n + 1
			id += "-" + strconv.Itoa(n)
		} else {
			ids[id] = 1
		}
		marker := ""
		if forPDF && title != "" {
			uri := fmt.Sprintf("https://%s/%s/%d", pdfMarkerHost, job, len(outline))
			lv, _ := strconv.Atoi(level)
			outline = append(outline, PDFOutlineEntry{MarkerURI: uri, Title: title, Level: uint8(lv)})
			marker = `<a class="nomo-pdf-outline-marker" href="` + uri + `" aria-hidden="true" tabindex="-1"></a>`
		}
		return `<h` + level + ` id="` + html.EscapeString(id) + `" class="nomo-pdf-outline-heading">` + marker + inner + `</h` + level + `>`
	})

	// 本地图片内联。
	dir := ""
	if tab.Path != "" {
		dir = filepath.Dir(tab.Path)
	}
	out = imgSrcRe.ReplaceAllStringFunc(out, func(m string) string {
		raw := html.UnescapeString(imgSrcRe.FindStringSubmatch(m)[1])
		if data := inlineImage(dir, raw); data != "" {
			return `<img src="` + data + `"`
		}
		return m
	})

	t := s.currentTheme()
	scheme := "light"
	if dark {
		scheme = "dark"
	}
	vars := t.CSSDeclarations(dark) +
		fmt.Sprintf("--md-editor-font-size: %.0fpx;--md-editor-line-height: %g;--nomo-export-width: 960px;color-scheme: %s;",
			s.prefs.FontSize, s.prefs.LineHeight, scheme)
	if f := s.prefs.DocumentFontFamily; f != "" {
		vars += "--md-editor-font-document: " + f + ";"
	}
	// 公式与图表在写出时由 renderExportMarkup 替换为渲染结果；渲染失败而保留的 TeX 原文按等宽字体显示。
	css := themes.ExportCSS + "\n:root{" + vars + "}" +
		"\n.markdown-body .math{font-family:var(--md-editor-font-mono,ui-monospace,monospace);white-space:pre-wrap}"
	title := strings.TrimSuffix(tab.Name(s.untitledName), filepath.Ext(tab.Name(s.untitledName)))
	page := `<article class="nomo-export markdown-body">` + out + `</article>`
	if forPDF {
		css += "\n.nomo-pdf-outline-heading{position:relative}.nomo-pdf-outline-marker{position:absolute;top:0;left:0;display:block;" +
			"width:1px;height:1px;overflow:hidden;color:transparent;font-size:0;line-height:0;text-decoration:none}"
		var extra string
		page, extra = s.pdfLayoutHTML(page)
		css += extra
	}
	block := "modern"
	if s.prefs.DocumentStyleID == "nomo-classic" {
		block = "classic"
	}
	doc := `<!DOCTYPE html>
<html lang="` + s.locale.Tag() + `" data-theme="` + scheme + `" data-color-theme="` + html.EscapeString(t.ID) +
		`" data-theme-style="` + html.EscapeString(t.StyleProfile) + `" data-document-style="` + html.EscapeString(s.prefs.DocumentStyleID) +
		`" data-block-style="` + block + `">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>` + html.EscapeString(title) + `</title>
  <style>` + strings.ReplaceAll(css, "</style", `<\/style`) + `</style>
</head>
<body>` + page + `
</body>
</html>
`
	return doc, outline
}

// PDFLayout 是 PDF 的版式参数（设置“PDF 导出”）：纸张（A4 / A5 / Letter / Legal）、横向、四边页边距（毫米）、
// 页码位置（none / center / right）与已替换占位符的页眉、页脚文字。页码与页眉页脚由主程序在打印后逐页盖印
// （WebKit 打印不逐页重复固定定位元素与表头）；分页在导出 HTML 中实现（pdfLayoutHTML）。
type PDFLayout struct {
	Paper          string
	Landscape      bool
	MarginMm       float64
	PageNumbers    string
	Header, Footer string
}

// pdfLayout 返回当前设置的版式；title 替换页眉页脚中的 {title}，{date} 为导出日期。
func (s *Shell) pdfLayout(title string) PDFLayout {
	p := s.prefs
	fill := func(t string) string {
		return strings.TrimSpace(strings.NewReplacer("{title}", title, "{date}", time.Now().Format("2006-01-02")).Replace(t))
	}
	return PDFLayout{Paper: p.PDFPaperSize, Landscape: p.PDFOrientation == "landscape", MarginMm: p.PDFMarginMm,
		PageNumbers: p.PDFPageNumbers, Header: fill(p.PDFHeader), Footer: fill(p.PDFFooter)}
}

// calloutOpenRe 匹配解析器输出的提示块开头：引用块的第一个段落以 [!TYPE]（可带 +/- 与标题）开始。
var calloutOpenRe = regexp.MustCompile(`<blockquote>\s*<p>(\[![^\]\s]+\][+-]?[^\n<]*)(?:\n|<br\s*/?>\s*)?`)

// exportCallouts 把提示块的引用块换成原版导出的结构（callout-card > callout-header（图标、标题）+ callout-body），
// 导出样式按 data-callout-type 着色。只含标记行的段落随之去掉；对应的 </blockquote> 按嵌套层级找到并换成卡片结尾。
func exportCallouts(out string) string {
	var b strings.Builder
	at := 0
	for {
		m := calloutOpenRe.FindStringSubmatchIndex(out[at:])
		if m == nil {
			break
		}
		start, end := at+m[0], at+m[1]
		tone, typ, title, ok := mdedit.ParseCallout(html.UnescapeString(out[at+m[2] : at+m[3]]))
		closeAt := matchingBlockquoteClose(out, start)
		if !ok || closeAt < 0 {
			b.WriteString(out[at:end])
			at = end
			continue
		}
		b.WriteString(out[at:start])
		b.WriteString(`<div class="callout-card" data-callout-type="` + typ + `"><div class="callout-header"><span class="callout-icon">` +
			icons.CalloutSource(tone) + `</span><span class="callout-title">` + html.EscapeString(title) + `</span></div><div class="callout-body"><p>`)
		body := out[end:closeAt]
		body = strings.Replace(body, "<p></p>", "", 1) // 只有标记行的段落
		b.WriteString(exportCallouts(body))
		b.WriteString("</div></div>")
		at = closeAt + len("</blockquote>")
	}
	b.WriteString(out[at:])
	return strings.ReplaceAll(b.String(), `<div class="callout-body"><p></p>`, `<div class="callout-body">`)
}

// matchingBlockquoteClose 返回从 start（一个 <blockquote> 的位置）起与之配对的 </blockquote> 的位置，找不到时为 -1。
func matchingBlockquoteClose(s string, start int) int {
	depth := 0
	for i := start; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "<blockquote>"):
			depth++
			i += len("<blockquote>")
		case strings.HasPrefix(s[i:], "</blockquote>"):
			depth--
			if depth == 0 {
				return i
			}
			i += len("</blockquote>")
		default:
			i++
		}
	}
	return -1
}

// pageBreakRe 匹配手动分页符 <!-- pagebreak -->（解析器原样输出 HTML 注释块）。
var pageBreakRe = regexp.MustCompile(`(?i)<!--\s*page-?break\s*-->`)

// pdfLayoutHTML 按 PDF 设置改写导出正文并返回附加样式：手动分页符换成分页元素；开启时一级标题前分页（第一个除外）。
func (s *Shell) pdfLayoutHTML(body string) (string, string) {
	body = pageBreakRe.ReplaceAllString(body, `<div class="nomo-page-break"></div>`)
	css := "\n.nomo-page-break{break-after:page;page-break-after:always;height:0}"
	if s.prefs.PDFBreakBeforeH1 {
		css += "\n.nomo-export h1~h1{break-before:page;page-break-before:always}"
	}
	return body, css
}

// stripFrontMatter 去掉开头的 YAML front matter（导出与原版一样不包含元数据）。
// highlightCodeHTML 为解析器输出中带语言的代码块加语法着色：逐行切分，着色片段包成带内联颜色的
// <span>，文字重新转义。未知语言的代码块原样保留。
func highlightCodeHTML(out string, dark bool) string {
	ct := editor.DefaultTheme(&ui.Theme{Dark: dark})
	return codeBlockRe.ReplaceAllStringFunc(out, func(m string) string {
		sub := codeBlockRe.FindStringSubmatch(m)
		l := editor.LanguageNamed(html.UnescapeString(sub[1]))
		if l == nil || l.Tokenizer == nil {
			return m
		}
		var b strings.Builder
		b.WriteString(`<pre><code class="language-` + sub[1] + `">`)
		var state uint64
		var toks []editor.Token
		for k, line := range strings.Split(html.UnescapeString(sub[2]), "\n") {
			if k > 0 {
				b.WriteByte('\n')
			}
			toks, state = l.Tokenizer.Tokenize(line, state, toks[:0])
			at := 0
			for _, t := range toks {
				z := min(t.End, len(line))
				if z <= at {
					continue
				}
				st := ct.Tokens[t.Kind]
				if st.Color.A == 0 {
					b.WriteString(html.EscapeString(line[at:z]))
				} else {
					style := fmt.Sprintf("color:#%02x%02x%02x", st.Color.R, st.Color.G, st.Color.B)
					if st.Italic {
						style += ";font-style:italic"
					}
					if st.Bold {
						style += ";font-weight:700"
					}
					b.WriteString(`<span style="` + style + `">` + html.EscapeString(line[at:z]) + `</span>`)
				}
				at = z
			}
			b.WriteString(html.EscapeString(line[at:]))
		}
		b.WriteString("</code></pre>")
		return b.String()
	})
}

var (
	exportMathRe    = regexp.MustCompile(`(?s)<span class="math (inline|display)">(.*?)</span>|<div class="math display">(.*?)</div>`)
	exportMermaidRe = regexp.MustCompile(`(?s)<pre><code class="language-mermaid">(.*?)</code></pre>`)
)

// renderExportMarkup 把导出 HTML 中的公式与 Mermaid 源码替换为渲染结果（在后台调用，可能耗时数秒）。没有
// 公式与图表、或离屏渲染不可用（测试中没有窗口）时原样返回；单项失败时保留源码并附错误说明（原版同样处理）。
func (s *Shell) renderExportMarkup(doc string, dark bool) string {
	if s.win == nil || !strings.Contains(doc, `class="math `) && !strings.Contains(doc, "language-mermaid") {
		return doc
	}
	type hit struct {
		a, z int
		kind string
		src  string
	}
	var hits []hit
	for _, m := range exportMathRe.FindAllStringSubmatchIndex(doc, -1) {
		h := hit{a: m[0], z: m[1]}
		if m[2] >= 0 {
			h.kind, h.src = map[string]string{"inline": mdedit.RenderMathInline, "display": mdedit.RenderMathDisplay}[doc[m[2]:m[3]]], doc[m[4]:m[5]]
		} else {
			h.kind, h.src = mdedit.RenderMathDisplay, doc[m[6]:m[7]]
		}
		hits = append(hits, h)
	}
	for _, m := range exportMermaidRe.FindAllStringSubmatchIndex(doc, -1) {
		hits = append(hits, hit{a: m[0], z: m[1], kind: mdedit.RenderMermaid, src: doc[m[2]:m[3]]})
	}
	if len(hits) == 0 {
		return doc
	}
	slices.SortFunc(hits, func(x, y hit) int { return x.a - y.a })
	reqs := make([]mdedit.RenderRequest, len(hits))
	for k, h := range hits {
		reqs[k] = mdedit.RenderRequest{Kind: h.kind, Src: html.UnescapeString(h.src)}
	}
	items, css, err := s.rend.markup(reqs, dark)
	if err != nil {
		applog.Warn("Export", "导出时渲染公式与图表失败，保留源码："+err.Error())
		return doc
	}
	var b strings.Builder
	at := 0
	for k, h := range hits {
		b.WriteString(doc[at:h.a])
		it := items[k]
		tag, class := "div", "math-block"
		if h.kind == mdedit.RenderMathInline {
			tag, class = "span", "math-inline"
		}
		if it.err != "" {
			b.WriteString(doc[h.a:h.z] + `<span class="nomo-render-error">` + html.EscapeString(it.err) + `</span>`)
		} else if h.kind == mdedit.RenderMermaid {
			// 与原版导出结构一致：.mermaid-block > .mermaid-block-rendered（export-document.css 居中、限宽）。
			b.WriteString(`<div class="mermaid-block"><div class="mermaid-block-rendered">` + it.html + `</div></div>`)
		} else {
			b.WriteString("<" + tag + ` class="` + class + `">` + it.html + "</" + tag + ">")
		}
		at = h.z
	}
	b.WriteString(doc[at:])
	out := b.String()
	if css != "" {
		out = strings.Replace(out, "</style>", "\n"+strings.ReplaceAll(css, "</style", `<\/style`)+"</style>", 1)
	}
	return out
}

func stripFrontMatter(src string) string {
	if !strings.HasPrefix(src, "---\n") && !strings.HasPrefix(src, "---\r\n") {
		return src
	}
	rest := src[strings.IndexByte(src, '\n')+1:]
	for _, end := range []string{"\n---\n", "\n...\n"} {
		if i := strings.Index("\n"+rest, end); i >= 0 {
			return strings.TrimLeft(rest[min(len(rest), i+len(end)-1):], "\r\n")
		}
	}
	return src
}

// slugify 按标题文字生成锚点：小写、空白转连字符、去掉标点（保留各语言文字与数字）。
func slugify(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		switch {
		case r == ' ' || r == '-':
			b.WriteByte('-')
		case r == '_' || r > 127 || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// inlineImage 把文档目录下的本地图片读成 data URL；远程地址、data URL、找不到或过大的图片返回空串。
func inlineImage(dir, src string) string {
	if src == "" || strings.HasPrefix(src, "data:") || strings.Contains(src, "://") && !strings.HasPrefix(src, "file://") {
		return ""
	}
	p := src
	if u, err := url.Parse(src); err == nil && u.Scheme == "file" {
		p = u.Path
	} else if dec, err := url.PathUnescape(src); err == nil {
		p = dec
	}
	if !filepath.IsAbs(p) {
		if dir == "" {
			return ""
		}
		p = filepath.Join(dir, p)
	}
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() || fi.Size() > maxInlineImage {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	typ := mime.TypeByExtension(strings.ToLower(filepath.Ext(p)))
	if typ == "" {
		typ = "application/octet-stream"
	}
	return "data:" + typ + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// exportHTML 选择保存位置后写出 HTML（配色跟随当前明暗外观）。
func (s *Shell) exportHTML() {
	tab := s.activeTab()
	if tab == nil {
		return
	}
	s.exportTo(tab, "html", "HTML", "exportHtmlSuccess", func(path string) func() (string, error) {
		doc, _ := s.exportDocument(tab, s.dark, false)
		return func() (string, error) {
			return "", docfs.WriteFileAtomically(path, []byte(s.renderExportMarkup(doc, s.dark)))
		}
	})
}

// exportPDF 选择保存位置后经 Options.ExportPDF 打印为 PDF（浅色配色；纸张与边距由主程序按原版
// A4、纵向、20mm 设置）。
func (s *Shell) exportPDF() {
	tab := s.activeTab()
	if tab == nil || s.pdfExporter == nil {
		return
	}
	s.exportTo(tab, "pdf", "PDF", "exportPdfSuccess", func(path string) func() (string, error) {
		layout := s.pdfLayout(strings.TrimSuffix(tab.Name(s.untitledName), filepath.Ext(tab.Name(s.untitledName))))
		doc, outline := s.exportDocument(tab, false, true)
		return func() (string, error) {
			return "", s.pdfExporter(s.renderExportMarkup(doc, false), path, outline, layout)
		}
	})
}

// exportWord 导出 .docx（原版 wordExportService，见 desktop/ui/word）：公式与 Mermaid 图表经离屏渲染转为图片（浅色
// 配色），相对图片按文档所在目录解析。写出成功后在状态栏附上降级说明（最多列出前三项）。
func (s *Shell) exportWord() {
	tab := s.activeTab()
	if tab == nil {
		return
	}
	s.exportTo(tab, "docx", "Word", "exportWordSuccess", func(path string) func() (string, error) {
		dir := ""
		if tab.Path != "" {
			dir = filepath.Dir(tab.Path)
		}
		md := tab.Doc.Source()
		title := strings.TrimSuffix(tab.Name(s.untitledName), filepath.Ext(tab.Name(s.untitledName)))
		opts := word.Options{Title: title, BaseDir: dir, Text: s.tr}
		if s.win != nil {
			opts.Render = s.rend.render
		}
		return func() (string, error) {
			data, warnings, err := word.Build(md, opts)
			if errors.Is(err, word.ErrBudget) {
				return "", errors.New(s.tr("wordExportBudgetError"))
			}
			if err != nil {
				return "", err
			}
			if err := docfs.WriteFileAtomically(path, data); err != nil {
				return "", err
			}
			if len(warnings) == 0 {
				return "", nil
			}
			applog.Info("Export", "Word 导出降级："+strings.Join(warnings, "；"))
			shown := warnings[:min(3, len(warnings))]
			return "  " + s.tr("wordExportWarnings", "count", strconv.Itoa(len(warnings))) + strings.Join(shown, "；"), nil
		}
	})
}

// exportTo 在后台弹出保存框；选定路径后在 UI 线程调用 render 生成文档（读取标签状态），
// 再在后台执行其返回的写出函数（返回附加在成功提示后的说明），结果写入状态栏（success 为成功文案键）。
func (s *Shell) exportTo(t *Tab, ext, filter, success string, render func(path string) func() (string, error)) {
	if t.Kind() != kindMarkdown {
		return
	}
	t.syncFromSource()
	name := strings.TrimSuffix(t.Name(s.untitledName), filepath.Ext(t.Name(s.untitledName))) + "." + ext
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: s.win, DefaultPath: name, CreateDirectories: true,
			Filters: []mygo.FileFilter{{Name: filter, Extensions: []string{ext}}}})
		if err != nil || path == "" {
			return
		}
		if filepath.Ext(path) == "" {
			path += "." + ext
		}
		s.postToSettings(func() {
			if s.tabWithID(t.ID) == nil {
				return
			}
			write := render(path)
			go func() {
				note, err := write()
				s.postToSettings(func() {
					if err != nil {
						applog.Warn("Export", err.Error())
						s.status = s.tr("exportFailed") + "：" + err.Error()
						return
					}
					s.status = s.tr(success, "path", path) + note
				})
			}()
		})
	}()
}

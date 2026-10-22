package shell

import (
	"encoding/base64"
	"fmt"
	"html"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui/markdown/parser"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/docfs"
	"nomo/desktop/ui/themes"
)

// 导出 HTML / PDF（对应原版 exportService）：正文由解析器按 cmark-gfm 规则渲染为 HTML，套用原版
// export-document.css 与当前颜色主题的 CSS 变量，本地图片内联为 data URL，得到单个自包含文件。
// PDF 把同一份 HTML 交给 Options.ExportPDF（主程序用按需创建的隐藏 WebView 打印，导出完即销毁，
// 不常驻）；标题前插入书签标记，由主程序转换为 PDF 书签。
//
// 与原版的差异：代码不做语法高亮，公式与 Mermaid 以源码显示，提示块按引用块显示，不附带精细排版
// 运行时。

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
	headingTagRe = regexp.MustCompile(`(?s)<h([1-6])>(.*?)</h[1-6]>`)
	imgSrcRe     = regexp.MustCompile(`<img src="([^"]*)"`)
	tagRe        = regexp.MustCompile(`<[^>]+>`)
)

// exportDocument 生成导出 HTML；forPDF 时插入书签标记并返回书签列表。
func (s *Shell) exportDocument(tab *Tab, dark, forPDF bool) (string, []PDFOutlineEntry) {
	src := stripFrontMatter(tab.Doc.Source())
	body := parser.New(parser.Default | parser.ExtDollarMath)
	body.Set(src)
	out := body.HTML()

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
	css := themes.ExportCSS + "\n:root{" + vars + "}"
	if forPDF {
		css += "\n.nomo-pdf-outline-heading{position:relative}.nomo-pdf-outline-marker{position:absolute;top:0;left:0;display:block;" +
			"width:1px;height:1px;overflow:hidden;color:transparent;font-size:0;line-height:0;text-decoration:none}"
	}
	block := "modern"
	if s.prefs.DocumentStyleID == "nomo-classic" {
		block = "classic"
	}
	title := strings.TrimSuffix(tab.Name(s.untitledName), filepath.Ext(tab.Name(s.untitledName)))
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
<body><article class="nomo-export markdown-body">` + out + `</article>
</body>
</html>
`
	return doc, outline
}

// stripFrontMatter 去掉开头的 YAML front matter（导出与原版一样不包含元数据）。
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
	s.exportTo(tab, "html", "HTML", "exportHtmlSuccess", func(path string) func() error {
		doc, _ := s.exportDocument(tab, s.dark, false)
		return func() error { return docfs.WriteFileAtomically(path, []byte(doc)) }
	})
}

// exportPDF 选择保存位置后经 Options.ExportPDF 打印为 PDF（浅色配色；纸张与边距由主程序按原版
// A4、纵向、20mm 设置）。
func (s *Shell) exportPDF() {
	tab := s.activeTab()
	if tab == nil || s.pdfExporter == nil {
		return
	}
	s.exportTo(tab, "pdf", "PDF", "exportPdfSuccess", func(path string) func() error {
		doc, outline := s.exportDocument(tab, false, true)
		return func() error { return s.pdfExporter(doc, path, outline) }
	})
}

// exportTo 在后台弹出保存框；选定路径后在 UI 线程调用 render 生成文档（读取标签状态），
// 再在后台执行其返回的写出函数，结果写入状态栏（success 为成功文案键）。
func (s *Shell) exportTo(t *Tab, ext, filter, success string, render func(path string) func() error) {
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
				err := write()
				s.postToSettings(func() {
					if err != nil {
						applog.Warn("Export", err.Error())
						s.status = s.tr("exportFailed") + "：" + err.Error()
						return
					}
					s.status = s.tr(success, "path", path)
				})
			}()
		})
	}()
}

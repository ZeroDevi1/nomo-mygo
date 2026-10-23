package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/docfs"
)

// PDF 导出：原生外壳生成的导出 HTML 由隐藏窗口加载后用系统 WebView 原生打印，再根据标题定位链接写入
// 文档书签（pdf_outline.go）。HTML 与 Word 的生成见 desktop/ui/shell/export.go。

const (
	pdfExportTimeout   = 30 * time.Second
	millimetersPerInch = 25.4
	defaultPDFMarginMM = 20
)

// pdfExportMu 让 PDF 导出串行执行：每次导出都占用一个隐藏 WebView 与原生打印会话。
var pdfExportMu sync.Mutex

type exportResult struct {
	FilePath     string   `json:"file_path"`
	BytesWritten int      `json:"bytes_written"`
	Warnings     []string `json:"warnings,omitempty"`
}

type pdfMarginsInput struct {
	Top    float64 `json:"top"`
	Right  float64 `json:"right"`
	Bottom float64 `json:"bottom"`
	Left   float64 `json:"left"`
}

type pdfOutlineEntry struct {
	MarkerURI string `json:"marker_uri"`
	Title     string `json:"title"`
	Level     uint8  `json:"level"`
}

type exportPDFInput struct {
	HTMLContent     string            `json:"html_content"`
	FilePath        string            `json:"file_path"`
	PaperSize       *string           `json:"paper_size"`
	Orientation     *string           `json:"orientation"`
	Margins         *pdfMarginsInput  `json:"margins"`
	PrintBackground *bool             `json:"print_background"`
	Outline         []pdfOutlineEntry `json:"outline"`
}

// runPDFExport 在 30 秒期限内完成：渲染 → 校验 → 书签 → 原子写入目标文件。
func runPDFExport(input exportPDFInput) (*exportResult, error) {
	pdfExportMu.Lock()
	defer pdfExportMu.Unlock()
	started := time.Now()
	paper := "A4"
	if input.PaperSize != nil {
		paper = *input.PaperSize
	}
	orientation := "portrait"
	if input.Orientation != nil {
		orientation = *input.Orientation
	}
	applog.Info("Export", fmt.Sprintf("开始原生 PDF 导出：outline=%d paper=%s orientation=%s", len(input.Outline), paper, orientation))
	tempDir, err := os.MkdirTemp("", fmt.Sprintf("nomo-pdf-export-%d-", os.Getpid()))
	if err != nil {
		return nil, fmt.Errorf("创建临时导出目录失败：%w", err)
	}
	defer os.RemoveAll(tempDir)
	htmlPath := filepath.Join(tempDir, "index.html")
	if err := os.WriteFile(htmlPath, []byte(input.HTMLContent), 0o644); err != nil {
		return nil, fmt.Errorf("写入临时 HTML 失败：%w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), pdfExportTimeout)
	defer cancel()
	raw, err := renderHTMLToPDF(ctx, htmlPath, input)
	if err != nil {
		applog.Warn("Export", "PDF 原生渲染失败："+err.Error())
		return nil, err
	}
	applog.Info("Export", fmt.Sprintf("PDF 原生渲染完成：%d ms", time.Since(started).Milliseconds()))
	if err := validatePDF(raw); err != nil {
		return nil, fmt.Errorf("生成的 PDF 无效：%w", err)
	}
	if ctx.Err() != nil {
		return nil, errPDFExportTimeout
	}
	final := raw
	var warnings []string
	if len(input.Outline) > 0 {
		outlined, err := addDocumentOutline(raw, input.Outline)
		if err != nil {
			applog.Warn("Export", "PDF 文档大纲生成失败，保留原始 PDF："+err.Error())
			warnings = append(warnings, "PDF 已导出，但未能生成文档书签")
		} else {
			applog.Info("Export", fmt.Sprintf("PDF 文档大纲生成完成：%d 项，%d ms", len(input.Outline), time.Since(started).Milliseconds()))
			final = outlined
		}
	}
	if ctx.Err() != nil {
		return nil, errPDFExportTimeout
	}
	if err := docfs.WriteFileAtomically(input.FilePath, final); err != nil {
		return nil, err
	}
	applog.Info("Export", fmt.Sprintf("PDF 导出完成：%s (%d bytes, %d ms, warnings=%d)", input.FilePath, len(final), time.Since(started).Milliseconds(), len(warnings)))
	return &exportResult{FilePath: input.FilePath, BytesWritten: len(final), Warnings: warnings}, nil
}

var errPDFExportTimeout = errors.New("PDF 导出超时，请稍后重试。")

// renderHTMLToPDF 用隐藏窗口加载导出 HTML，等排版运行时、字体与图片就绪后原生打印为 PDF。
func renderHTMLToPDF(ctx context.Context, htmlPath string, input exportPDFInput) ([]byte, error) {
	pageWidth, pageHeight := paperSizeInches(input.PaperSize)
	landscape := input.Orientation != nil && *input.Orientation == "landscape"
	if landscape {
		pageWidth, pageHeight = pageHeight, pageWidth
	}
	margins := mygo.Margins{
		Top:    marginInches(input.Margins, func(m *pdfMarginsInput) float64 { return m.Top }),
		Right:  marginInches(input.Margins, func(m *pdfMarginsInput) float64 { return m.Right }),
		Bottom: marginInches(input.Margins, func(m *pdfMarginsInput) float64 { return m.Bottom }),
		Left:   marginInches(input.Margins, func(m *pdfMarginsInput) float64 { return m.Left }),
	}
	fileURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(htmlPath)}).String()
	if !strings.HasPrefix(filepath.ToSlash(htmlPath), "/") {
		fileURL = "file:///" + filepath.ToSlash(htmlPath)
	}
	loaded := make(chan error, 1)
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:       "Nomo PDF Export",
		Width:       1000,
		Height:      1200,
		Hidden:      true,
		Frameless:   true,
		SkipTaskbar: true,
	})
	defer win.Destroy()
	page := win.Page()
	page.OnDidFinishLoad(func() {
		select {
		case loaded <- nil:
		default:
		}
	})
	page.OnDidFailLoad(func(e *mygo.LoadError) {
		select {
		case loaded <- fmt.Errorf("PDF 导出页面加载失败：%v", e):
		default:
		}
	})
	page.LoadURL(fileURL)
	select {
	case err := <-loaded:
		if err != nil {
			return nil, err
		}
	case <-ctx.Done():
		return nil, errPDFExportTimeout
	}
	contentWidth := (pageWidth - margins.Left - margins.Right) * millimetersPerInch
	contentHeight := (pageHeight - margins.Top - margins.Bottom) * millimetersPerInch
	script := strings.NewReplacer(
		"__CONTENT_WIDTH__", strconv.FormatFloat(contentWidth, 'f', -1, 64),
		"__CONTENT_HEIGHT__", strconv.FormatFloat(contentHeight, 'f', -1, 64),
	).Replace(preparePrintScript)
	prepared := make(chan error, 1)
	go func() {
		_, err := page.Eval(script)
		prepared <- err
	}()
	select {
	case err := <-prepared:
		if err != nil {
			return nil, fmt.Errorf("等待 PDF 页面字体和图片完成失败：%w", err)
		}
	case <-ctx.Done():
		return nil, errPDFExportTimeout
	}
	background := input.PrintBackground == nil || *input.PrintBackground
	type printResult struct {
		data []byte
		err  error
	}
	printed := make(chan printResult, 1)
	go func() {
		data, err := page.PrintToPDF(mygo.PDFOptions{
			// PageSize 以纵向尺寸描述，方向由 Landscape 决定。
			PageSize:   mygo.PageSize{Width: min(pageWidth, pageHeight), Height: max(pageWidth, pageHeight)},
			Landscape:  landscape,
			Margins:    &margins,
			Background: background,
		})
		printed <- printResult{data, err}
	}()
	select {
	case result := <-printed:
		if result.err != nil {
			return nil, fmt.Errorf("生成 PDF 失败：%w", result.err)
		}
		return result.data, nil
	case <-ctx.Done():
		return nil, errPDFExportTimeout
	}
}

// preparePrintScript 等待导出页面的排版运行时按版心尺寸重新分页，再等字体与图片加载完成。
const preparePrintScript = `
if (document.getElementById('nomo-typography-options')) {
  if (!window.__NOMO_PREPARE_PRINT__) throw new Error('Typography runtime unavailable');
  await window.__NOMO_PREPARE_PRINT__(__CONTENT_WIDTH__, __CONTENT_HEIGHT__);
}
if (document.fonts && document.fonts.ready) {
  await document.fonts.ready;
}
await Promise.all(Array.from(document.images).map((image) => {
  if (image.complete) return Promise.resolve();
  return new Promise((resolve) => {
    image.addEventListener('load', resolve, { once: true });
    image.addEventListener('error', resolve, { once: true });
  });
}));
return true;
`

// paperSizeInches 返回纵向纸张尺寸；只支持 A4 与 Letter，其他值回退 A4。
func paperSizeInches(paper *string) (float64, float64) {
	if paper != nil && strings.EqualFold(*paper, "letter") {
		return 8.5, 11
	}
	if paper != nil && !strings.EqualFold(*paper, "a4") {
		applog.Warn("Export", "暂不支持的纸张大小："+*paper+"，使用 A4")
	}
	return 210 / millimetersPerInch, 297 / millimetersPerInch
}

// marginInches 把毫米边距换算为英寸；缺省、负值或非有限值使用 20mm。
func marginInches(margins *pdfMarginsInput, pick func(*pdfMarginsInput) float64) float64 {
	value := float64(defaultPDFMarginMM)
	if margins != nil {
		if candidate := pick(margins); candidate >= 0 && candidate < 1e6 {
			value = candidate
		}
	}
	return value / millimetersPerInch
}

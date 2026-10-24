package shell

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestStartupFileOpensItsFolder 从系统打开文档且窗口未打开文件夹时，资源管理器显示文档所在目录（原版冷启动行为）。
func TestStartupFileOpensItsFolder(t *testing.T) {
	path := writeDoc(t, "# 标题\n\n正文\n")
	s, tt := newTestShell(t, path)
	if s.folder == nil || s.folder.path != filepath.Dir(path) {
		t.Fatalf("应打开文档所在目录：%+v", s.folder)
	}
	snapshot(t, tt, "toolbar")
	// 已在工作的窗口只打开文档，不改变文件夹。
	other := writeDoc(t, "另一篇")
	s.OpenExternal([]string{other})
	if s.folder.path != filepath.Dir(path) {
		t.Fatalf("已有文件夹时不应切换：%s", s.folder.path)
	}
}

// TestToolbarPopovers 内容宽度与缩放弹层按 5%/1% 写回设置，表格网格悬停预览尺寸。
func TestToolbarPopovers(t *testing.T) {
	path := writeDoc(t, "正文\n")
	s, tt := newTestShell(t, path)
	if err := tt.Click("60%"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !s.tbar.widthOpen || !tt.HasText(s.tr("contentWidth")) {
		t.Fatal("单击内容宽度按钮应弹出滑块")
	}
	snapshot(t, tt, "width")
	s.tbar.widthOpen = false
	if err := tt.Click("100%"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("+"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if s.prefs.ZoomPercent != 105 {
		t.Fatalf("放大一档应为 105%%：%v", s.prefs.ZoomPercent)
	}
	snapshot(t, tt, "zoom")
	s.tbar.zoomOpen = false
	s.tbar.tableOpen, s.tbar.tableRows, s.tbar.tableCols = true, 3, 3
	tt.Frame()
	if !tt.HasText("3 × 3") {
		t.Fatal("表格网格应显示预览尺寸")
	}
	snapshot(t, tt, "table")
}

// TestWindowsTitleBarLayout Windows 布局：标题栏只有侧栏开关、应用名、菜单与右侧小窗/主题/设置，模式切换在工具栏；
// 标题栏与侧栏不透明（不透出系统强调色）；窄窗口按宽度收起工具栏分组。
func TestWindowsTitleBarLayout(t *testing.T) {
	path := writeDoc(t, "# 标题\n\n正文\n")
	s, tt := newTestShell(t, path)
	s.pf = platform{windows: true, menusInWindow: true, titleH: 40}
	tt.SetTitleBar(ui.TitleBar{Height: 32, Right: 138})
	for _, size := range [][2]int{{1330, 870}, {1180, 760}, {920, 700}} {
		tt.SetSize(size[0], size[1])
		tt.Frame()
		tt.Frame() // 第二帧按上一帧的工具栏宽度收起分组
		if s.ch.material {
			t.Fatal("Windows 外壳不应使用透明材质")
		}
		mode, ok := tt.Find(s.tr("semanticEditing"))
		if !ok || mode.Y < 40 {
			t.Fatalf("%v：模式切换应在标题栏下方的工具栏中：%v %v", size, mode, ok)
		}
		if tt.HasText(filepath.Base(path) + " — ") {
			t.Fatalf("%v：标题栏不应再显示居中文档名", size)
		}
		snapshot(t, tt, "windows-"+strconv.Itoa(size[0]))
	}
}

// TestPDFLayoutHTML：手动分页符与一级标题分页的样式；页眉页脚的占位符替换。
func TestPDFLayoutHTML(t *testing.T) {
	s, _ := newTestShell(t)
	body := `<article><h1>A</h1><!-- pagebreak --><h1>B</h1></article>`
	out, css := s.pdfLayoutHTML(body)
	if !strings.Contains(out, `<div class="nomo-page-break"></div>`) || strings.Contains(css, "h1~h1") {
		t.Fatalf("默认版式 %q css=%q", out, css)
	}
	s.prefs.PDFBreakBeforeH1, s.prefs.PDFHeader, s.prefs.PDFFooter = true, "{title} <x>", " 页脚 "
	if _, css = s.pdfLayoutHTML(body); !strings.Contains(css, "h1~h1{break-before:page") {
		t.Fatalf("一级标题分页 css=%q", css)
	}
	if l := s.pdfLayout("标题"); l.Header != "标题 <x>" || l.Footer != "页脚" {
		t.Fatalf("页眉页脚 %+v", l)
	}
}

// TestExportCallouts：提示块引用块换成 callout-card（类型、图标、自定义标题），普通引用块与嵌套引用不受影响。
func TestExportCallouts(t *testing.T) {
	in := "<blockquote>\n<p>[!warning]- 小心\n正文</p>\n<blockquote>\n<p>内层</p>\n</blockquote>\n</blockquote>\n<blockquote>\n<p>普通</p>\n</blockquote>\n<blockquote>\n<p>[!bug]</p>\n<p>第二段</p>\n</blockquote>"
	out := exportCallouts(in)
	if !strings.Contains(out, `<div class="callout-card" data-callout-type="warning">`) || !strings.Contains(out, `<span class="callout-title">小心</span>`) ||
		!strings.Contains(out, `<div class="callout-body"><p>正文</p>`) || !strings.Contains(out, "<blockquote>\n<p>内层</p>\n</blockquote>") {
		t.Fatalf("提示块 %q", out)
	}
	if !strings.Contains(out, "<blockquote>\n<p>普通</p>\n</blockquote>") {
		t.Fatalf("普通引用被改写：%q", out)
	}
	if !strings.Contains(out, `data-callout-type="caution">`) || !strings.Contains(out, `<span class="callout-title">Bug</span>`) || strings.Contains(out, "[!bug]") {
		t.Fatalf("扩展类型 %q", out)
	}
}

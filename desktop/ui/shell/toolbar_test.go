package shell

import (
	"path/filepath"
	"strconv"
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

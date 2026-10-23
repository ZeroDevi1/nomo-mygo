package shell

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"nomo/desktop/internal/config"
	"nomo/desktop/internal/i18n"
)

const shellDetailsDoc = "<details>\n<summary>标题</summary>\n\n正文\n\n</details>\n\n尾段\n"

func newDetailsShell(t *testing.T, text string) (*Shell, *Tab) {
	t.Helper()
	cfg, err := config.Load(filepath.Join(t.TempDir(), config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Config: cfg, Locale: i18n.ZhCN, Files: []string{writeDoc(t, text)}})
	return s, s.activeTab()
}

// TestDetailsContextMenu 在标题行右键：菜单给出“默认展开”“取消折叠并保留内容”，选择后改写原文。
func TestDetailsContextMenu(t *testing.T) {
	s, tab := newDetailsShell(t, shellDetailsDoc)
	tt := ui.NewTester(func(c *ui.Context) { s.editorView(c, tab).Fill() }, 800, 600)
	tt.Frame()
	for y := float32(20); y < 160; y += 3 {
		tt.RightClickAt(300, y)
		tt.Frame()
		if slices.Contains(tt.Menu(), s.tr("detailsDefaultOpen")) {
			if err := tt.ChooseMenuItem(s.tr("detailsDefaultOpen")); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if !strings.HasPrefix(tab.Doc.Source(), "<details open>\n<summary>标题</summary>") {
				t.Fatalf("默认展开后 %q", tab.Doc.Source())
			}
			return
		}
		tt.CloseMenu()
	}
	t.Fatal("标题行右键菜单中没有折叠块项")
}

func TestDetailsExportAndSourceInsert(t *testing.T) {
	s, tab := newDetailsShell(t, shellDetailsDoc+"\n<details open>\n<summary>B</summary>\n\nx\n\n</details>\n")
	pdf, _ := s.exportDocument(tab, false, true)
	if strings.Count(pdf, "<details open") != 2 {
		t.Fatalf("PDF 应展开全部折叠块：%s", pdf)
	}
	htmlOut, _ := s.exportDocument(tab, false, false)
	if strings.Count(htmlOut, "<details open") != 1 {
		t.Fatalf("HTML 应保留原有展开状态：%s", htmlOut)
	}
	// 源码模式插入：选中内容进入正文，光标在标题中。
	s.setMode(modeSource)
	src := tab.Source()
	at := strings.Index(src.Buffer.String(), "尾段")
	src.SetSelections(editor.Selection{Anchor: at, Head: at + len("尾段")})
	s.run("details")
	text := src.Buffer.String()
	if !strings.Contains(text, "<details>\n<summary></summary>\n\n尾段\n\n</details>") {
		t.Fatalf("源码模式插入后 %q", text)
	}
	if sel := src.Selection(); !strings.HasSuffix(text[:sel.Head], "<summary>") {
		t.Fatalf("光标应在标题中：%q", text[:sel.Head])
	}
}

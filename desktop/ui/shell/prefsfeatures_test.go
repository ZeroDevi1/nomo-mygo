package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nomo/desktop/internal/config"
	"nomo/desktop/internal/i18n"
)

func TestPrefsFeatures(t *testing.T) {
	doc := "# A\n\n## A1\n\n### A1a\n\n## A2\n\n# B\n\n" + strings.Repeat("word ", 600) + "\n"
	s, tab := newDetailsShell(t, doc)
	// 阅读时间：600 词按每分钟 280 词向上取整为 3 分钟。
	s.prefs.ReadingTimeVisible = true
	st := tab.Stats()
	if got := s.statsText(st.Words, st.Chars, st.Lines); !strings.Contains(got, s.tr("estimatedReadingMinutes", "minutes", "3")) {
		t.Fatalf("状态栏统计 %q 应含阅读时间", got)
	}
	// Ctrl 滚轮：累计到一档才缩放；关闭设置后不处理。
	z := s.prefs.ZoomPercent
	if !s.ctrlWheel(-20) || s.prefs.ZoomPercent != z || !s.ctrlWheel(-20) || s.prefs.ZoomPercent != z+10 {
		t.Fatalf("Ctrl 滚轮缩放：%v → %v", z, s.prefs.ZoomPercent)
	}
	s.prefs.CtrlWheelZoomEnabled = false
	if s.ctrlWheel(-100) {
		t.Fatal("关闭 Ctrl 滚轮缩放后不应处理")
	}
	// 大纲默认展开到 H2：H3 隐藏；展开 A1 后可见；改为 H3 后全部可见。
	hs := tab.Outline()
	s.prefs.OutlineDefaultExpandLevel = 2
	if rows := s.outlineRows(tab, hs); len(rows) != 4 {
		t.Fatalf("默认层级 H2 时可见 %v", rows)
	}
	s.toggleOutlineItem(tab, 1)
	if rows := s.outlineRows(tab, hs); len(rows) != 5 {
		t.Fatalf("展开 A1 后可见 %v", rows)
	}
	s.toggleOutlineItem(tab, 0)
	if rows := s.outlineRows(tab, hs); len(rows) != 2 {
		t.Fatalf("折叠 A 后可见 %v", rows)
	}
	// 设置传给编辑区：代码块默认语言与图表类型。
	s.prefs.DefaultCodeBlockLanguage, s.prefs.DefaultDiagramType = "go", "pie"
	if o := s.editorOptions(); o.CodeLanguage != "go" || !strings.HasPrefix(o.DiagramCode, "pie") || o.HideLineNumbers {
		t.Fatalf("编辑区选项 %+v", o)
	}
}

func TestFilePreviewTab(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.md"), filepath.Join(dir, "b.md")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("# x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(filepath.Join(t.TempDir(), config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Config: cfg, Locale: i18n.ZhCN})
	s.openPreview(a)
	ta := s.activeTab()
	if ta == nil || !ta.preview {
		t.Fatal("单击打开应为预览标签")
	}
	s.openPreview(b)
	if ta.preview || !s.activeTab().preview {
		t.Fatal("打开另一文件后上一预览应固定，新标签为预览")
	}
	s.prefs.FilePreviewEnabled = false
	s.openPreview(a)
	if s.activeTab() != ta || ta.preview {
		t.Fatal("已打开的文件只切换，不再标为预览")
	}
}

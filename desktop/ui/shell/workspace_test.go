package shell

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"nomo/desktop/internal/config"
	"nomo/desktop/internal/i18n"
)

// TestWorkspaceRestore：主窗口记录的标签、活动标签与未保存副本在下次启动时恢复；新文档的副本恢复为未保存的新文档。
func TestWorkspaceRestore(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.Load(filepath.Join(dir, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	a, b := writeDoc(t, "# A\n"), writeDoc(t, "# B\n")
	s := New(Options{Config: cfg, Locale: i18n.ZhCN, Primary: true, Files: []string{a, b}})
	s.tabs[0].Ed.SetSource("# A edited\n")
	s.NewDocument()
	s.activeTab().Ed.SetSource("draft body\n")
	s.activate(1)
	s.persistWorkspace(s.tabs, true, false)

	r := New(Options{Config: cfg, Locale: i18n.ZhCN, Primary: true})
	if len(r.tabs) != 3 {
		t.Fatalf("恢复了 %d 个标签，应为 3", len(r.tabs))
	}
	if got := r.tabs[0].text(); got != "# A edited\n" || !r.tabs[0].Dirty() {
		t.Fatalf("副本未恢复：%q dirty=%v", got, r.tabs[0].Dirty())
	}
	if r.tabs[1].Dirty() || r.active != 1 {
		t.Fatalf("干净标签或活动标签不对：dirty=%v active=%d", r.tabs[1].Dirty(), r.active)
	}
	if nt := r.tabs[2]; nt.Path != "" || nt.text() != "draft body\n" || !nt.Dirty() {
		t.Fatalf("新文档副本未恢复：path=%q text=%q", nt.Path, nt.text())
	}

	// 保存后不再引用的副本随下次写入删除。
	r.tabs[0].markSaved(r.tabs[0].text())
	r.persistWorkspace(r.tabs, true, false)
	p, _ := config.MarkdownStorePath(r.draftsDir(), config.HashText("# A edited\n"))
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("旧副本未删除：%v", err)
	}
}

// TestWorkspaceDraftConflict：写副本后文件又被外部修改时按磁盘内容打开并排队询问，选择“恢复副本”后载入副本。
func TestWorkspaceDraftConflict(t *testing.T) {
	dir := t.TempDir()
	cfg, _ := config.Load(filepath.Join(dir, config.FileName))
	a := writeDoc(t, "disk v1\n")
	s := New(Options{Config: cfg, Locale: i18n.ZhCN, Primary: true, Files: []string{a}})
	s.tabs[0].Ed.SetSource("draft\n")
	s.persistWorkspace(s.tabs, true, false)
	later := time.Now().Add(5 * time.Second)
	if err := os.WriteFile(a, []byte("disk v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(a, later, later)

	r := New(Options{Config: cfg, Locale: i18n.ZhCN, Primary: true})
	if len(r.ws.conflicts) != 1 || r.tabs[0].text() != "disk v2\n" {
		t.Fatalf("冲突未识别：conflicts=%d text=%q", len(r.ws.conflicts), r.tabs[0].text())
	}
	r.ws.conflicts[0].tab.replaceText(r.ws.conflicts[0].draft)
	if r.tabs[0].text() != "draft\n" || !r.tabs[0].Dirty() {
		t.Fatalf("恢复副本失败：%q", r.tabs[0].text())
	}
}

// TestWorkspaceCloseDropsDiscarded：关闭主窗口时，放弃的新文档不记录，已有路径的文档不留副本。
func TestWorkspaceCloseDropsDiscarded(t *testing.T) {
	dir := t.TempDir()
	cfg, _ := config.Load(filepath.Join(dir, config.FileName))
	a := writeDoc(t, "a\n")
	s := New(Options{Config: cfg, Locale: i18n.ZhCN, Primary: true, Files: []string{a}})
	s.tabs[0].Ed.SetSource("changed\n")
	s.NewDocument()
	s.activeTab().Ed.SetSource("x\n")
	s.beginWorkspaceClose()
	s.finishWorkspaceClose()

	r := New(Options{Config: cfg, Locale: i18n.ZhCN, Primary: true})
	if len(r.tabs) != 1 || r.tabs[0].Dirty() || r.tabs[0].text() != "a\n" {
		t.Fatalf("关闭后恢复不对：%d 个标签", len(r.tabs))
	}
}

// TestTabDragReorder：把第一个标签拖到第二个标签上，排到它之后，活动标签（第三个）不变。
func TestTabDragReorder(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for _, n := range []string{"alpha.md", "beta.md", "gamma.md"} {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	s, tt := newTestShell(t, paths...)
	s.prefs.SidebarHidden = true // 资源管理器里也有同名项，Find 会先找到它
	tt.Frame()
	active := s.activeTab()
	first, _ := tt.Find(s.tabLabel(s.tabs[0]))
	second, _ := tt.Find(s.tabLabel(s.tabs[1])) // 活动标签的名字也在标题栏中，不用它
	ids := []int{s.tabs[0].ID, s.tabs[1].ID, s.tabs[2].ID}
	tt.Press(first.X+first.W/2, first.Y+first.H/2)
	for k := 1; k <= 10; k++ {
		f := float32(k) / 10
		tt.Move(first.X+first.W/2+(second.X-first.X)*f, first.Y+first.H/2)
		tt.Frame()
	}
	tt.Release(second.X+second.W/2, second.Y+second.H/2)
	tt.Frame()
	tt.Frame()
	got := []int{s.tabs[0].ID, s.tabs[1].ID, s.tabs[2].ID}
	if got[0] != ids[1] || got[1] != ids[0] || got[2] != ids[2] {
		t.Fatalf("拖动后顺序 %v，应为 %v", got, []int{ids[1], ids[0], ids[2]})
	}
	if s.activeTab() != active {
		t.Fatal("拖动改变了活动标签")
	}
}

// TestDropOnEmptyWorkspace：拖到空白工作区的 Markdown 打开为标签，图片等不支持的文件不打开并提示。
func TestDropOnEmptyWorkspace(t *testing.T) {
	s, tt := newTestShell(t)
	dir := t.TempDir()
	img := filepath.Join(dir, "a.png")
	_ = os.WriteFile(img, []byte("x"), 0o644)
	if !tt.DropFiles(700, 400, img) {
		t.Fatal("空白工作区不接收拖放")
	}
	tt.Frame()
	if len(s.tabs) != 0 {
		t.Fatal("图片被当作文档打开")
	}
	md := writeDoc(t, "# hi\n")
	tt.DropFiles(700, 400, md)
	tt.Frame()
	if len(s.tabs) != 1 || s.tabs[0].Path != md {
		t.Fatalf("拖放的文档未打开：%d 个标签", len(s.tabs))
	}
}

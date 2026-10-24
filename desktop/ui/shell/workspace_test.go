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
	s.persistWorkspace(false)

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
	r.persistWorkspace(false)
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
	s.persistWorkspace(false)
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

// TestTabDragReorder：标签拖到另一标签上排到它之后（从左往右），活动标签不变。拖动本身是系统拖放（DragData，
// 为了跨窗口），无头测试器不模拟回投，这里验证放下后的排序。
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
	s, _ := newTestShell(t, paths...)
	active := s.activeTab()
	ids := []int{s.tabs[0].ID, s.tabs[1].ID, s.tabs[2].ID}
	s.moveTab(ids[0], ids[1])
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

// TestWorkspaceMultiWindowAndGroups：两个窗口都写入工作区（主窗口在前），组合标签关系与左栏比例随之恢复；
// 单独关闭非最后一个窗口后不再记录它。
func TestWorkspaceMultiWindowAndGroups(t *testing.T) {
	dir := t.TempDir()
	cfg, _ := config.Load(filepath.Join(dir, config.FileName))
	docs := t.TempDir()
	path := func(n string) string {
		p := filepath.Join(docs, n)
		_ = os.WriteFile(p, []byte("# "+n+"\n"), 0o644)
		return p
	}
	a, b, c := path("a.md"), path("b.md"), path("c.md")
	main := New(Options{Config: cfg, Locale: i18n.ZhCN, Primary: true, Files: []string{a, b}})
	other := New(Options{Config: cfg, Locale: i18n.ZhCN, Files: []string{c}})
	main.combine(main.tabs[0], main.tabs[1])
	main.tabs[0].comparePct = 30
	register(main)
	register(other)
	defer func() { unregister(main); unregister(other) }()
	main.persistWorkspace(false)

	r := New(Options{Config: cfg, Locale: i18n.ZhCN, Primary: true})
	if len(r.tabs) != 2 || len(r.ws.pending) != 1 {
		t.Fatalf("主窗口 %d 个标签，待恢复窗口 %d 个", len(r.tabs), len(r.ws.pending))
	}
	left, right, ok := r.comparisonOf(r.tabs[0])
	if !ok || left.Path != a || right.Path != b || left.comparePct != 30 {
		t.Fatalf("组合标签未恢复：ok=%v", ok)
	}
	w2 := New(Options{Config: cfg, Locale: i18n.ZhCN})
	if !w2.restoreWindow(r.ws.pending[0]) || w2.tabs[0].Path != c {
		t.Fatal("第二个窗口未恢复")
	}

	// 还有主窗口时单独关闭第二个窗口：工作区只剩主窗口。
	other.finishWorkspaceClose()
	unregister(other)
	if ws := main.readWorkspace(); len(ws) != 1 || len(ws[0].Tabs) != 2 {
		t.Fatalf("关闭第二个窗口后工作区 %d 个窗口", len(ws))
	}
}

// TestAdoptTabsAcrossWindows：把另一窗口的组合标签整组移入本窗口，重新编号后组合关系保留，来源窗口不再显示它们。
func TestAdoptTabsAcrossWindows(t *testing.T) {
	docs := t.TempDir()
	path := func(n string) string {
		p := filepath.Join(docs, n)
		_ = os.WriteFile(p, []byte("# "+n+"\n"), 0o644)
		return p
	}
	from, _ := newTestShell(t, path("a.md"), path("b.md"), path("c.md"))
	to, _ := newTestShell(t, path("d.md"))
	from.combine(from.tabs[0], from.tabs[1])
	to.adoptTabs(from, from.tabs[0].ID, to.tabs[0].ID)
	if len(from.tabs) != 1 || len(to.tabs) != 3 {
		t.Fatalf("移动后来源 %d 个、目标 %d 个标签", len(from.tabs), len(to.tabs))
	}
	left, right, ok := to.comparisonOf(to.tabs[0])
	if !ok || filepath.Base(left.Path) != "a.md" || filepath.Base(right.Path) != "b.md" || to.tabs[2].Path != path("d.md") {
		t.Fatalf("组合关系或位置不对：ok=%v", ok)
	}
	if to.activeTab() != to.tabs[1] && to.activeTab() != to.tabs[0] {
		t.Fatal("移入的标签应为活动标签")
	}
}

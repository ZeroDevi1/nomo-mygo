package shell

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/config"
	"nomo/desktop/internal/i18n"
)

func newTestShell(t *testing.T, files ...string) (*Shell, *ui.Tester) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Load(filepath.Join(dir, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Config: cfg, Locale: i18n.ZhCN, Files: files})
	tt := ui.NewTester(s.view, 1200, 800)
	tt.Frame()
	return s, tt
}

func writeDoc(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func snapshot(t *testing.T, tt *ui.Tester, name string) {
	out := os.Getenv("NOMO_SHELL_SNAPSHOT")
	if out == "" {
		return
	}
	f, err := os.Create(filepath.Join(out, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_ = png.Encode(f, tt.Image())
}

func TestEmptyWorkspaceAndNewDocument(t *testing.T) {
	s, tt := newTestShell(t)
	if !tt.HasText("未打开文档") {
		t.Fatal("空白工作区缺少提示")
	}
	snapshot(t, tt, "empty")
	if err := tt.Click("新建 Markdown"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(s.tabs) != 1 || s.activeTab().Path != "" || !tt.HasText("无标题.md") {
		t.Fatalf("新建文档失败：%d 个标签", len(s.tabs))
	}
}

func TestOpenEditSwitchModesAndClose(t *testing.T) {
	p := writeDoc(t, "# 标题\n\n- 列表 **粗**\n")
	s, tt := newTestShell(t, p)
	tab := s.activeTab()
	if tab == nil || tab.Path != p {
		t.Fatal("未打开启动文档")
	}
	snapshot(t, tt, "semantic")
	// 语义模式编辑。
	i := strings.Index(tab.Doc.Source(), "列表") + len("列表")
	tab.Doc.SetSelection(selection(i))
	tt.Type("甲")
	tt.Frame()
	if !tab.Dirty() || !strings.Contains(tab.Doc.Source(), "列表甲") {
		t.Fatalf("语义编辑：%q dirty=%v", tab.Doc.Source(), tab.Dirty())
	}
	// 切到源码模式：缓冲区与原文一致；在源码中编辑后切回，原文同步。
	s.setMode(modeSource)
	tt.Frame()
	if got := tab.Source().Buffer.String(); got != tab.Doc.Source() {
		t.Fatalf("源码缓冲区未同步：%q", got)
	}
	snapshot(t, tt, "source")
	buf := tab.Source().Buffer
	buf.Edit(editorEdit(buf.Len(), "\n新段落\n"))
	tt.Frame()
	s.setMode(modeSemantic)
	tt.Frame()
	if !strings.HasSuffix(tab.Doc.Source(), "\n新段落\n") {
		t.Fatalf("源码编辑未写回：%q", tab.Doc.Source())
	}
	// 撤销回保存时的内容不算未保存。
	for tab.Dirty() && tab.Doc.Version() > 0 {
		if !tab.Ed.Run("undo") {
			break
		}
		if tab.Doc.Source() == "# 标题\n\n- 列表 **粗**\n" {
			break
		}
	}
	if tab.Doc.Source() == "# 标题\n\n- 列表 **粗**\n" && tab.Dirty() {
		t.Fatal("撤销回原样后仍标记未保存")
	}
	// 有未保存修改时关闭：弹出确认，选择不保存后关闭。
	tt.Type("乙")
	tt.Frame()
	s.requestClose([]int{tab.ID}, false)
	tt.Frame()
	if !s.confirmOpen {
		t.Fatal("关闭未保存文档时没有确认")
	}
	snapshot(t, tt, "confirm")
	s.confirmOpen = false
	s.resolveClose(1)
	tt.Frame()
	if len(s.tabs) != 0 {
		t.Fatal("选择不保存后标签未关闭")
	}
}

func TestFindHighlightsAndReplaceAll(t *testing.T) {
	p := writeDoc(t, "苹果 和 苹果 还有 Apple\n")
	s, tt := newTestShell(t, p)
	s.openFind(true)
	s.query = "苹果"
	tt.Frame()
	if len(s.matches) != 2 {
		t.Fatalf("匹配数 %d", len(s.matches))
	}
	snapshot(t, tt, "find")
	s.activeTab().Ed.ReplaceRanges(s.matches, "梨")
	tt.Frame()
	if got := s.activeTab().Doc.Source(); got != "梨 和 梨 还有 Apple\n" {
		t.Fatalf("全部替换 %q", got)
	}
	s.query = "apple"
	tt.Frame()
	if len(s.matches) != 1 {
		t.Fatalf("不区分大小写匹配数 %d", len(s.matches))
	}
}

func TestPreferencesPersist(t *testing.T) {
	dir := t.TempDir()
	cfg, _ := config.Load(filepath.Join(dir, config.FileName))
	s := New(Options{Config: cfg, Locale: i18n.ZhCN})
	s.toggleOutline()
	s.setMode(modeSource)
	s.toggleTheme(false)
	cfg2, _ := config.Load(filepath.Join(dir, config.FileName))
	s2 := New(Options{Config: cfg2, Locale: i18n.ZhCN})
	// outlineVisible 默认 true（与原版一致），切换一次后为 false。
	if s2.prefs.OutlineVisible || s2.prefs.EditorMode != modeSource || s2.prefs.ThemeMode != "dark" {
		t.Fatalf("偏好未持久化：outline=%v mode=%s theme=%s", s2.prefs.OutlineVisible, s2.prefs.EditorMode, s2.prefs.ThemeMode)
	}
}

func TestPrefsNormalizeLikeOriginal(t *testing.T) {
	dir := t.TempDir()
	cfg, _ := config.Load(filepath.Join(dir, config.FileName))
	_ = cfg.Update(func(c *config.AppConfig) {
		config.PutSetting(c, "fontSize", "99")
		config.PutSetting(c, "themeMode", `"purple"`)
		config.PutSetting(c, "autoSaveDelayMs", "2000")
		config.PutSetting(c, "contentWidthPercent", `"wide"`)
	})
	p := loadPrefs(cfg)
	if p.FontSize != 16 || p.ThemeMode != "system" || p.AutoSaveDelayMs != 2000 || p.ContentWidthPercent != 60 {
		t.Fatalf("规范化结果 %+v", p)
	}
}

func TestSettingsPagesRender(t *testing.T) {
	s, _ := newTestShell(t)
	sw := newSettingsWindow(s)
	tt := ui.NewTester(sw.view, 780, 560)
	for _, cat := range s.settingsCategories() {
		sw.cat = cat.id
		tt.Frame()
		if !tt.HasText(s.tr(cat.label)) {
			t.Fatalf("设置分类 %s 未显示标题", cat.id)
		}
		snapshot(t, tt, "settings-"+cat.id)
	}
	// 切换开关立即写入配置。
	sw.cat = "stats"
	tt.Frame()
	before := s.prefs.WritingStatsVisible
	s.prefs.WritingStatsVisible = !before
	s.setPref("writingStatsVisible")
	if v, _ := s.cfg.Setting("writingStatsVisible"); v != map[bool]string{true: "true", false: "false"}[!before] {
		t.Fatalf("设置未写入：%s", v)
	}
	// 文本设置：不合法的触发符不提交，合法的回车提交。
	sw.cat = "editor"
	tt.Frame()
	*sw.drafts["quickInsertTrigger"] = "ab"
	sw.focused["quickInsertTrigger"] = true
	tt.Frame()
	if s.prefs.QuickInsertTrigger != "/" || !tt.HasText(s.tr("quickInsertTriggerInvalid")) {
		t.Fatalf("不合法触发符：%q", s.prefs.QuickInsertTrigger)
	}
	*sw.drafts["quickInsertTrigger"] = "@"
	sw.focused["quickInsertTrigger"] = true
	tt.Frame()
	if v, _ := s.cfg.Setting("quickInsertTrigger"); s.prefs.QuickInsertTrigger != "@" || v != `"@"` {
		t.Fatalf("触发符未提交：%q %s", s.prefs.QuickInsertTrigger, v)
	}
}

func TestAutoSaveAfterDelay(t *testing.T) {
	p := writeDoc(t, "原文\n")
	s, tt := newTestShell(t, p)
	done := make(chan func(), 1)
	s.post = func(fn func()) { done <- fn }
	s.prefs.AutoSaveEnabled, s.prefs.AutoSaveDelayMs = true, 500
	tab := s.activeTab()
	tab.Doc.SetSelection(selection(len("原文")))
	tt.Type("改")
	tt.Frame()
	if !tab.Dirty() || tab.saving {
		t.Fatal("刚编辑时应未保存且尚未到期")
	}
	tab.autoAt = tab.autoAt.Add(-time.Second) // 模拟延时已过
	tt.Frame()
	if !tab.saving {
		t.Fatal("到期后应发起自动保存")
	}
	(<-done)() // 在“UI 线程”完成保存回调
	if b, _ := os.ReadFile(p); string(b) != "原文改\n" || tab.Dirty() {
		t.Fatalf("自动保存结果 %q dirty=%v", b, tab.Dirty())
	}
}

// TestPlatformLayoutsRender 以 macOS 与 Windows 两种风格各渲染一次（截图供人工检查）。
func TestPlatformLayoutsRender(t *testing.T) {
	p := writeDoc(t, "# 标题\n\n- 列表 **粗**\n\n> [!TIP]\n> 提示\n")
	for name, pf := range map[string]platform{
		"mac": {mac: true, titleH: 52}, "windows": {windows: true, menusInWindow: true, titleH: 40},
	} {
		s, tt := newTestShell(t, p, p+"2")
		s.pf = pf
		s.NewDocument()
		s.activate(0)
		tt.Frame()
		if !tt.HasText("doc.md") {
			t.Fatalf("%s 布局缺少标签", name)
		}
		snapshot(t, tt, "platform-"+name)
		s.prefs.ThemeMode = "dark"
		tt.Frame()
		snapshot(t, tt, "platform-"+name+"-dark")
	}
}

func TestExplorerTreeRowsAndToggle(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a.md", "b.json", "c.txt", "sub/d.md"} {
		p := filepath.Join(dir, f)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("x"), 0o644)
	}
	s, tt := newTestShell(t)
	s.openFolder(dir)
	tt.Frame()
	for _, name := range []string{"sub", "a.md", "b.json", "c.txt"} {
		if !tt.HasText(name) {
			t.Fatalf("目录树缺少 %s", name)
		}
	}
	if tt.HasText("d.md") {
		t.Fatal("子目录默认应收起")
	}
	if err := tt.Click("sub"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("d.md") {
		t.Fatal("点击目录后应展开")
	}
	snapshot(t, tt, "explorer")
	if err := tt.Click("b.json"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tab := s.activeTab(); tab == nil || filepath.Base(tab.Path) != "b.json" {
		t.Fatal("点击文件应在标签中打开")
	}
}

func TestQuickInsertMenu(t *testing.T) {
	p := writeDoc(t, "段落\n\n")
	s, tt := newTestShell(t, p)
	tab := s.activeTab()
	tab.Doc.SetSelection(selection(len(tab.Doc.Source())))
	tt.Type("/")
	tt.Frame()
	if !s.quick.open || !tt.HasText("标题 1") {
		t.Fatal("输入触发符后未弹出快捷插入菜单")
	}
	snapshot(t, tt, "quickinsert")
	// 筛选到“无序”，回车插入：触发符与筛选文字被删除，当前行变为列表。
	tt.Type("无序")
	tt.Frame()
	if tt.HasText("标题 1") || !tt.HasText("无序") {
		t.Fatal("筛选未生效")
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if s.quick.open || tab.Doc.Source() != "段落\n\n- " {
		t.Fatalf("插入结果：%q open=%v", tab.Doc.Source(), s.quick.open)
	}
	// Esc 关闭后保留输入的触发符。
	tt.Key(0, ui.KeyEnter)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Type("/")
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if s.quick.open || !strings.HasSuffix(tab.Doc.Source(), "/") {
		t.Fatalf("Esc 后：%q open=%v", tab.Doc.Source(), s.quick.open)
	}
}

func TestImageSnippetLikeOriginal(t *testing.T) {
	cases := []struct{ src, alt, width, align, want string }{
		{"./assets/a b.png", "a]b", "", "none", "![a b](<./assets/a b.png>)"},
		{"x(1).png", "", "", "none", `![image](x\(1\).png)`},
		{"a.png", "a", "300", "none", `<img src="a.png" alt="a" width="300">`},
		{"a.png", `"a"`, "", "center", "<p align=\"center\">\n  <img src=\"a.png\" alt=\"&quot;a&quot;\">\n</p>"},
	}
	for _, c := range cases {
		if got := imageSnippet(c.src, c.alt, c.width, c.align); got != c.want {
			t.Errorf("imageSnippet(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}

func TestPasteImageCopiesToAssets(t *testing.T) {
	p := writeDoc(t, "段落")
	s, tt := newTestShell(t, p)
	posted := make(chan func(), 1)
	s.post = func(fn func()) { posted <- fn }
	tab := s.activeTab()
	tab.Doc.SetSelection(selection(len("段落")))
	if !s.pasteData(tab, transfer.New(transfer.NewItem(transfer.Bytes(transfer.PNG, []byte("\x89PNG\r\n\x1a\nfake"))))) {
		t.Fatal("剪贴板位图未被处理")
	}
	(<-posted)()
	tt.Frame()
	want := "段落\n![image.png](./assets/image.png)"
	if got := tab.Doc.Source(); got != want {
		t.Fatalf("插入结果：%q", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(p), "assets", "image.png")); err != nil {
		t.Fatal(err)
	}
	if s.pasteData(tab, transfer.TextData("文本")) {
		t.Fatal("纯文本不应按图片处理")
	}
}

func TestMarkdownLintStatusAndJump(t *testing.T) {
	p := writeDoc(t, "# A\n\n### B\n\n正文\n")
	s, tt := newTestShell(t)
	posted := make(chan func(), 4)
	s.post = func(fn func()) { posted <- fn }
	s.prefs.MarkdownLintEnabled, s.prefs.MarkdownLintRuleSet = true, "relaxed"
	s.OpenFiles([]string{p})
	tt.Frame()
	(<-posted)()
	tt.Frame()
	tab := s.activeTab()
	if tab.lint.status != "issues" || tab.lint.total != 1 || tab.lint.issues[0].Rule != "MD001" {
		t.Fatalf("检查结果：%+v", tab.lint)
	}
	if err := tt.Click("1 个格式问题"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	snapshot(t, tt, "lint")
	if err := tt.Click("MD001 " + s.tr("lintRule.MD001")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := tab.Doc.Sel.Head; got != strings.Index(tab.Doc.Source(), "### B") {
		t.Fatalf("跳转位置 %d", got)
	}
	// 修改后防抖：未到 500ms 不重新检查。
	tab.Doc.SetSelection(selection(len(tab.Doc.Source())))
	tt.Type("x")
	tt.Frame()
	if tab.lint.running {
		t.Fatal("防抖期内不应重新检查")
	}
	tt.Advance(600 * time.Millisecond)
	tt.Frame()
	(<-posted)()
	tt.Frame()
	if tab.lint.ver != tab.Doc.Version() {
		t.Fatal("防抖后未重新检查")
	}
}

func TestExportDocument(t *testing.T) {
	p := writeDoc(t, "---\ntitle: x\n---\n# 标题 A\n\n## 标题 A\n\n![图](./a.png) <b>\n")
	if err := os.WriteFile(filepath.Join(filepath.Dir(p), "a.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := newTestShell(t, p)
	doc, outline := s.exportDocument(s.activeTab(), false, true)
	for _, want := range []string{`<h1 id="标题-a"`, `<h2 id="标题-a-1"`, `src="data:image/png;base64,cG5n"`, ".nomo-export", "--md-editor-bg", `class="nomo-pdf-outline-marker"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("导出文档缺少 %q", want)
		}
	}
	if strings.Contains(doc, "title: x") {
		t.Error("front matter 不应导出")
	}
	if len(outline) != 2 || outline[1].Level != 2 || outline[1].Title != "标题 A" {
		t.Errorf("书签：%+v", outline)
	}
	if html, _ := s.exportDocument(s.activeTab(), true, false); strings.Contains(html, "nomo-pdf-outline-marker") || !strings.Contains(html, `data-theme="dark"`) {
		t.Error("HTML 导出不应含书签标记，且应使用深色配色")
	}
}

func TestTextAndJSONDocumentsUseSourceEditor(t *testing.T) {
	p := filepath.Join(t.TempDir(), "data.json")
	if err := os.WriteFile(p, []byte("{\"a\": 1}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, tt := newTestShell(t, p)
	s.prefs.MarkdownLintEnabled = true
	tt.Frame()
	tab := s.activeTab()
	if tab.Kind() != kindJSON || s.mode() != modeSource || tab.Source().Label != "JSON" {
		t.Fatalf("kind=%s mode=%s", tab.Kind(), s.mode())
	}
	if tt.HasText(s.tr("semanticEditing")) || tab.lint.running || tab.lint.status != "" {
		t.Fatal("JSON 文档不应显示语义模式切换或进行 Markdown 检查")
	}
	buf := tab.Source().Buffer
	buf.Edit(editorEdit(buf.Len(), "\n"))
	tt.Frame()
	if tab.Doc.Source() != "{\"a\": 1}\n\n" || !tab.Dirty() {
		t.Fatalf("源码编辑未写回：%q", tab.Doc.Source())
	}
}

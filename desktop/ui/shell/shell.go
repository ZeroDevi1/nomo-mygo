// Package shell 是 Nomo 的原生外壳（计划 P2）：标题栏、标签页、资源管理器、工具栏、
// 编辑区、大纲、状态栏与文件操作，直接调用 Go 后端，不经过 WebView 与 IPC。
//
// 生命周期：New 读取偏好与最近记录；Open 创建窗口；窗口关闭（含确认未保存）后进程退出。
// 并发：Shell 的状态只在 UI 线程读写；对话框与文件写入在后台 goroutine 运行，
// 结果通过 Window.Update 回到 UI 线程。
package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/config"
	"nomo/desktop/internal/docfs"
	"nomo/desktop/internal/emoji"
	"nomo/desktop/internal/i18n"
	"nomo/desktop/internal/memtrim"
	"nomo/desktop/ui/mdedit"
	"nomo/desktop/ui/themes"
)

// 编辑模式，取值与原版设置 editorMode 一致。
const (
	modeSemantic = "semantic"
	modeSource   = "source"
	modeSplit    = "split" // 双栏：语义与源码同时显示，mode() 返回活动一侧
)

// Shell 是一个原生文档窗口的全部界面状态。
type Shell struct {
	cfg    *config.Manager
	win    *mygo.Window
	locale i18n.Locale

	tabs     []*Tab
	active   int // 当前标签下标，没有标签时为 -1
	nextID   int
	untitled int

	prefs    *Prefs   // 与设置窗口及同一进程的其他窗口共享（windows.go），修改后 Store 写回 config.json
	pf       platform // 当前平台的原生风格
	ch       chrome   // 本帧外壳配色（随明暗与窗口材质变化）
	sidebarW float32
	settings *settingsWindow // 设置窗口；nil 表示未打开
	themes   []*themes.Theme // 内置与用户颜色主题，见 appearance.go

	systemLocale string        // 系统首选语言，界面语言为“跟随系统”时使用
	version      string        // 应用版本，显示在设置的“关于”中
	appIcon      [2]*ui.Bitmap // 浅色、深色外观的应用图标；解码失败或未提供时为 nil

	folder *folderNode // 资源管理器根目录；nil 表示未打开文件夹
	recent []config.RecentEntry

	// 查找替换（语义模式；源码模式使用 ui/editor 自带的查找栏）。
	finding, replacing   bool
	query, replaceWith   string
	matchCase, wholeWord bool
	match                int
	matches              [][2]int
	foundVer             uint64
	foundQuery           string
	foundTab             int
	foundOpts            [2]bool

	// 关闭确认：closing 为待关闭的标签 ID，closingAll 表示关闭窗口。
	confirmOpen bool
	closing     []int
	closingAll  bool

	// 重命名对话框。
	renaming             bool
	renameFrom, renameTo string

	// post 把后台任务（对话框、文件写入）的结果交回 UI 线程；默认为 win.Update，测试中可替换。
	post func(func())

	mini miniState // Markdown 小窗（mini.go）
	opts Options   // 创建参数，“移动到新窗口”用它创建新窗口（windows.go）
	// 托盘（tray.go）：trayOK 为托盘是否可用；quitting 为正在退出（不再按关闭到托盘拦截）；closeChoice* 为
	// “关闭 Nomo”选择框的状态。
	installTray         func(show, quit func()) bool
	trayOK, quitting    bool
	closeEmpty          bool // 标签全部拖到其他窗口后关闭本窗口（不按关闭到托盘拦截）
	hiddenToTray        bool // 窗口已隐藏到托盘
	legacyPrompt        bool // Store 版检测到旧安装版的提示框（winintegration.go）
	closeChoiceOpen     bool
	closeChoiceRemember bool
	// images 是本窗口各标签共用的语义模式图片缓存（字节预算 mdedit.DefaultImageBudget），窗口关闭时释放。
	images *mdedit.Images
	// rend 是公式与 Mermaid 的离屏渲染服务（见 render.go），窗口打开后接入图片缓存，窗口关闭时释放。
	rend renderService

	quick   quickState     // 快捷插入菜单，见 quickinsert.go
	link    linkEditor     // 链接快速编辑浮层，见 linkedit.go
	snaps   snapshotsState // 历史快照窗口，见 snapshots.go
	preview imagePreview   // 全屏图片预览，见 imagemenu.go
	upd     updateState    // 软件更新的检查、下载与提示，见 update.go
	// 双栏模式（split.go）：splitActive 为活动一侧（modeSemantic / modeSource，随焦点切换）；splitPct 为左侧
	// 宽度百分比的界面值，拖动结束后写入 prefs.SplitLeftPercent；splitSync 记录上次同步滚动时两侧的位置。
	splitActive string
	splitPct    float32
	splitSave   *time.Timer
	splitSync   splitScroll
	emo         emojiState   // 表情建议与选择器，见 emoji.go
	tbar        toolbarState // 工具栏的表格尺寸与内容宽度弹层，见 toolbar.go
	// editorOrigin 为语义编辑区左上角（窗口坐标），每帧更新；按键回调中定位浮层用。
	editorOrigin ui.Point
	// loadEmoji 加载表情目录（主程序提供，可能耗时十余秒，在后台调用）；为空时不提供表情功能。
	loadEmoji func(retry bool) ([]emoji.Entry, error)

	pdfExporter func(html, path string, outline []PDFOutlineEntry, layout PDFLayout) error
	checkUpdate func() (UpdateInfo, error)
	dark        bool // 最近一帧的明暗外观（导出 HTML 配色用），在 applyDocumentAppearance 中更新
	iconSynced  bool // 已把 dark 交给 Options.SetIconTheme；dark 变化时重新同步

	ws windowWS // 本窗口的工作区关闭快照、副本冲突与待恢复窗口，见 workspace.go
	// closedForWorkspace 为本窗口已按关闭写入工作区（之后不再参与签名与写入）。
	closedForWorkspace bool

	focusSaved *[3]bool // 专注模式前的侧栏隐藏、大纲显示、工具栏隐藏；nil 表示不在专注模式（不写盘）

	svc         *ui.Services // 剪贴板等窗口服务，每帧由视图更新
	focusEditor bool         // 下一帧让编辑区获得焦点（切换标签、打开文档后）
	outlineList ui.ListState // 大纲面板的虚拟列表位置（各标签共用）
	wheelAcc    float32      // Ctrl 滚轮缩放的累计滚动量（ctrlWheel）
	status      string
}

// Options 是创建外壳的参数。
type Options struct {
	Config       *config.Manager
	Locale       i18n.Locale
	SystemLocale string
	Version      string
	// IconLight / IconDark 是应用图标 PNG（浅色 / 深色外观），用于“关于”页；可为空。
	IconLight, IconDark []byte
	// ExportPDF 把导出 HTML 打印为 PDF 写到 path（主程序提供，按需创建隐藏 WebView）；为空时不提供 PDF 导出。
	ExportPDF func(html, path string, outline []PDFOutlineEntry, layout PDFLayout) error
	// CheckUpdate 检查新版本（主程序提供，可能耗时数秒，在后台调用）；为空时不显示检查更新。
	CheckUpdate func() (UpdateInfo, error)
	// DownloadUpdate 下载最近一次检查到的安装包并校验（主程序提供，在后台调用），progress 报告已下载与总字节；
	// InstallUpdate 启动安装器并退出应用；ReleaseNotes 读取某个版本的发布说明。为空时不提供对应功能。
	DownloadUpdate func(progress func(done, total int64)) error
	InstallUpdate  func() error
	ReleaseNotes   func(version string) (string, error)
	// InstallTray 创建托盘图标（主程序提供）：show 恢复窗口，quit 退出；返回托盘是否可用。为空或失败时
	// “关闭到托盘”不可用，关闭按钮总是关闭窗口。
	InstallTray func(show, quit func()) bool
	// SetIconTheme 让 Dock 与托盘图标跟随应用的明暗外观（主程序提供，原版 set_desktop_icon_theme）；首帧与外观
	// 变化时在后台调用，相同值由主程序去重。为空时图标保持打包版本。
	SetIconTheme func(dark bool)
	// SetTrayActive 切换托盘图标的激活状态：所有窗口都隐藏到托盘时为 false（主程序提供，可为空）。
	SetTrayActive func(active bool)
	// LoadEmoji 加载表情目录（主程序提供，读缓存或下载，在后台调用）；retry 为 true 时忽略失败冷却。为空时不提供表情功能。
	LoadEmoji func(retry bool) ([]emoji.Entry, error)
	// Primary 表示应用的主窗口：记录工作区（打开的文件夹、标签与未保存文档的恢复副本），没有启动参数时恢复上次的
	// 工作区并处理首次启动示例文档（见 workspace.go）。“移动到新窗口”创建的窗口不设置。
	Primary bool
	// Windows 为 Windows 文件关联与右键菜单操作（主程序提供，只在 Windows 上非空），显示在设置的“文件”页。
	Windows *WindowsIntegration
	// SampleDocument 返回随包分发的示例文档路径（主程序提供）；为空时首次启动不打开示例。
	SampleDocument func() (string, error)
	Files          []string // 启动时打开的文档
	Folders        []string // 启动时打开的文件夹（取第一个）
}

// UpdateInfo 是检查更新的结果：Supported 为 false 表示当前安装方式不由应用检查更新（如商店版、开发构建）；
// Available 时 Version 为新版本号、Notes 为其发布说明（Markdown）；Installable 表示可在应用内下载并安装（Windows
// 安装版），否则引导到发布页。
type UpdateInfo struct {
	Supported, Available, Installable bool
	Version, Notes                    string
}

// New 读取偏好与最近记录，并打开启动参数中的文档。
func New(o Options) *Shell {
	s := &Shell{opts: o, cfg: o.Config, locale: o.Locale, active: -1, sidebarW: 240,
		prefs: sharedPrefs(o.Config), pf: currentPlatform(), systemLocale: o.SystemLocale, version: o.Version, pdfExporter: o.ExportPDF, checkUpdate: o.CheckUpdate, loadEmoji: o.LoadEmoji, installTray: o.InstallTray}
	s.loadThemes()
	s.images = mdedit.NewImages(func(fn func()) {
		if s.post != nil {
			s.post(fn)
		}
	}, mdedit.DefaultImageBudget)
	for i, png := range [][]byte{o.IconLight, o.IconDark} {
		if len(png) > 0 {
			s.appIcon[i], _ = ui.DecodeBitmap(png)
		}
	}
	s.recent = s.cfg.Get().Recent.Entries
	slices.SortStableFunc(s.recent, func(a, b config.RecentEntry) int { return int(b.OpenedAt - a.OpenedAt) })
	if o.Primary && len(o.Files) == 0 && len(o.Folders) == 0 {
		s.restoreWorkspace()
		s.openFirstRunSample()
		return s
	}
	if len(o.Folders) > 0 {
		s.openFolder(o.Folders[0])
	}
	s.OpenExternal(o.Files)
	return s
}

// mode 返回当前编辑模式；原版的 split（双栏）在原生外壳实现前按语义模式显示。
func (s *Shell) mode() string {
	t := s.activeTab()
	switch {
	case s.prefs.EditorMode == modeSource || t != nil && t.Kind() != kindMarkdown:
		return modeSource
	case s.prefs.EditorMode == modeSplit && s.splitActive == modeSource:
		// 双栏时命令、插入与查找作用于活动一侧（原版 getActiveEditorMode）。
		return modeSource
	}
	return modeSemantic
}

// split 报告当前是否为双栏模式（只对 Markdown 文档生效）。
func (s *Shell) split() bool {
	t := s.activeTab()
	return s.prefs.EditorMode == modeSplit && t != nil && t.Kind() == kindMarkdown && t.partner == 0 // 组合标签不用双栏（原版 splitModeDisabled）
}

// setPref 修改一项偏好并写盘；同时刷新主窗口（设置窗口的修改需要主窗口重绘）。
func (s *Shell) setPref(key string) {
	s.prefs.Store(s.cfg, key)
	if key == "themeMode" && s.win != nil {
		// 只在窗口（即应用主循环）存在时切换：SetSource 需要主线程处理，无窗口时会一直等待。
		applyAppearance(s.prefs.ThemeMode)
	}
	// 偏好由各窗口共享：全部重绘，让其他窗口也立即按新设置显示。
	for _, other := range registry.shells {
		if other.win != nil {
			other.win.Update(func() {})
		}
	}
	if s.win != nil {
		s.win.Update(func() {})
	}
}

// applyAppLocale 让 MyGo 自身的文字（系统菜单的“关于 / 隐藏 / 退出”、编辑与窗口菜单、对话框按钮）以及文字引擎的
// 汉字字形选择跟随界面语言：声明 Nomo 文案覆盖的语言，再按设置 interfaceLanguage 指定或跟随系统（"system"）。
// 不声明时 MyGo 视应用为纯英文，中文界面下的系统菜单项显示英文。
func applyAppLocale(pref string) {
	mygo.App.SetLanguages("en", "zh-Hans", "zh-Hant", "ja")
	tag := map[string]string{"zh-CN": "zh-Hans", "zh-TW": "zh-Hant", "en-US": "en", "ja-JP": "ja"}[pref]
	mygo.App.SetLocale(tag)
}

// applyAppearance 让整个应用（窗口材质、系统控件、标题栏按钮）使用 themeMode 指定的明暗，
// 而不只是改控件主题；否则强制深色时 macOS 的 vibrancy 材质仍是浅色。
func applyAppearance(mode string) {
	switch mode {
	case "light":
		mygo.Theme.SetSource(mygo.ThemeLight)
	case "dark":
		mygo.Theme.SetSource(mygo.ThemeDark)
	default:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	}
}

// Open 创建并显示外壳窗口。
func (s *Shell) Open() *mygo.Window {
	applyAppearance(s.prefs.ThemeMode)
	first := len(registry.shells) == 0
	stateKey := "native-main" // 只有第一个窗口记住位置与尺寸，后开的窗口用默认几何
	if !first {
		stateKey = ""
	}
	s.win = mygo.NewWindow(mygo.WindowOptions{
		Title: "Nomo", Width: 1180, Height: 760, MinWidth: 920, MinHeight: 640, // 与 WebView 版文档窗口一致
		StateKey: stateKey, TitleBarStyle: mygo.TitleBarHidden, Vibrancy: s.pf.vibrancy, Content: ui.View(s.view),
	})
	s.post = s.win.Update
	s.images.SetRenderer(s.rend.render)
	s.win.OnClose(func(e *mygo.CloseEvent) {
		if s.closingAll && len(s.closing) == 0 {
			return
		}
		if s.handleWindowClose(e) {
			return
		}
		if s.anyDirty() {
			e.PreventDefault()
			s.win.Update(func() { s.requestClose(s.tabIDs(), true) })
		}
	})
	register(s)
	s.win.OnFocus(func() {
		registry.focused = s
		s.win.Update(func() {
			s.checkExternalChanges()
			s.images.Revalidate()
		})
	})
	// 主窗口关闭时一并关闭设置窗口，进程随最后一个窗口退出。
	s.win.OnClosed(func() {
		s.finishWorkspaceClose()
		unregister(s)
		s.images.Close()
		s.rend.close()
		if s.settings != nil {
			s.settings.win.Close()
		}
	})
	if first {
		applyAppLocale(s.prefs.InterfaceLanguage)
		s.installMenu()
		s.startUpdateTasks()
		s.checkLegacyInstaller()
	}
	s.openPendingWindows()
	s.updateTitle()
	if s.installTray != nil && !registry.trayInstalled {
		registry.trayOK = s.installTray(func() {
			if c := Current(); c != nil {
				c.win.Update(c.showFromTray)
			}
		}, quitAll)
		registry.trayInstalled = true
	}
	s.trayOK = registry.trayOK
	return s.win
}

func (s *Shell) tabIDs() []int {
	ids := make([]int, len(s.tabs))
	for i, t := range s.tabs {
		ids[i] = t.ID
	}
	return ids
}

func (s *Shell) anyDirty() bool {
	for _, t := range s.tabs {
		if t.Dirty() {
			return true
		}
	}
	return false
}

func (s *Shell) activeTab() *Tab {
	if s.active < 0 || s.active >= len(s.tabs) {
		return nil
	}
	return s.tabs[s.active]
}

func (s *Shell) untitledName(n int) string {
	name := s.tr("untitledMarkdown")
	if n <= 1 {
		return name
	}
	ext := filepath.Ext(name)
	return fmt.Sprintf("%s %d%s", strings.TrimSuffix(name, ext), n, ext)
}

func (s *Shell) updateTitle() {
	if s.win == nil {
		return
	}
	title := "Nomo"
	if t := s.activeTab(); t != nil {
		title = t.Name(s.untitledName) + " - Nomo"
	}
	s.win.SetTitle(title)
}

// activate 切换到标签 i；离开的标签释放布局缓存（保留原文、撤销与阅读位置）。
func (s *Shell) activate(i int) {
	if i == s.active || i < 0 || i >= len(s.tabs) {
		return
	}
	if prev := s.activeTab(); prev != nil {
		prev.syncFromSource()
		// 同一组合内切换活动文档时两篇都在显示，不释放布局。
		if prev.partner != s.tabs[i].ID {
			prev.Ed.ReleaseLayouts()
		}
	}
	s.active = i
	if s.mode() == modeSource {
		s.tabs[i].syncToSource()
	}
	s.focusEditor = true
	s.updateTitle()
	s.checkExternalChanges()
}

// ---- 文件操作 ----

// NewDocument 新建无标题文档。
func (s *Shell) NewDocument() {
	s.nextID++
	s.untitled++
	t := newTab(s.nextID, "", "", docfs.EncodingUTF8)
	t.Untitled = s.untitled
	s.addTab(t)
}

// addTab 加入并激活标签，接上外壳对编辑器的钩子（粘贴图片）。
func (s *Shell) addTab(t *Tab) {
	s.wirePaste(t)
	s.tabs = append(s.tabs, t)
	s.activate(len(s.tabs) - 1)
}

// wirePaste 接上外壳对编辑器的钩子：粘贴图片、双击图片预览。
func (s *Shell) wirePaste(t *Tab) {
	t.Ed.PasteData = func(d transfer.Data) bool { return s.pasteData(t, d) }
	t.Ed.OnImageOpen = func(src string) { s.openImagePreview(t, src) }
}

// OpenExternal 打开从系统传入的文档（启动参数、Finder 双击、第二实例）：窗口尚未打开文件夹且没有已保存路径或
// 未保存修改的标签时，先把第一个文档所在目录作为资源管理器根目录（原版 openStartupExternalMarkdownPaths：双击文档
// 冷启动时打开其所在目录），再打开文档。已在工作的窗口只打开文档，不改变文件夹。
func (s *Shell) OpenExternal(paths []string) {
	if s.folder == nil && !slices.ContainsFunc(s.tabs, func(t *Tab) bool { return t.Path != "" || t.Dirty() }) {
		for _, p := range paths {
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				s.openFolder(filepath.Dir(p))
				break
			}
		}
	}
	s.OpenFiles(paths)
}

// OpenFiles 打开文档；已打开的切换过去。
func (s *Shell) OpenFiles(paths []string) {
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			s.openFolder(p)
			continue
		}
		if i := s.findTab(p); i >= 0 {
			s.activate(i)
			continue
		}
		doc, err := docfs.ReadDocument(p)
		if err != nil {
			s.status = err.Error()
			continue
		}
		s.nextID++
		s.addTab(newTab(s.nextID, doc.Path, doc.Markdown, doc.Encoding))
		s.remember(doc.Path, "file")
	}
}

// openPreview 从资源管理器单击打开文件（原版 filePreviewEnabled）：新打开的标签显示为预览标签（标题斜体），之前的
// 预览标签随之固定；编辑或双击标题后也固定。关闭该设置时与普通打开相同。
func (s *Shell) openPreview(path string) {
	if i := s.findTab(path); i >= 0 || !s.prefs.FilePreviewEnabled {
		s.OpenFiles([]string{path})
		return
	}
	n := len(s.tabs)
	s.OpenFiles([]string{path})
	if len(s.tabs) > n {
		for _, t := range s.tabs {
			t.preview = false
		}
		s.tabs[len(s.tabs)-1].preview = true
	}
}

func (s *Shell) findTab(path string) int {
	for i, t := range s.tabs {
		if t.Path != "" && samePath(t.Path, path) {
			return i
		}
	}
	return -1
}

// samePath 比较路径：macOS 与 Windows 的默认文件系统不区分大小写。
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "linux" {
		return a == b
	}
	return strings.EqualFold(a, b)
}

func (s *Shell) openFolder(path string) {
	root, err := newFolderNode(path, path)
	if err != nil {
		s.status = err.Error()
		return
	}
	s.folder = root
	s.prefs.SidebarHidden = false
	s.remember(path, "folder")
}

// remember 记录最近打开（与原版 recent 条目格式一致）。
func (s *Shell) remember(path, kind string) {
	entry := config.RecentEntry{Path: path, EntryType: kind, OpenedAt: config.NowTS()}
	if kind == "file" {
		entry.ModifiedAt = docfs.ModifiedAt(path)
	}
	err := s.cfg.Update(func(c *config.AppConfig) {
		kept := []config.RecentEntry{entry}
		for _, e := range c.Recent.Entries {
			if !samePath(e.Path, path) {
				kept = append(kept, e)
			}
		}
		c.Recent.Entries = kept
	})
	if err != nil {
		applog.Warn("Native", "记录最近打开失败："+err.Error())
		return
	}
	s.recent = s.cfg.Get().Recent.Entries
}

// forget 从最近打开中移除 path（只删记录，不动文件）。
func (s *Shell) forget(path string) {
	err := s.cfg.Update(func(c *config.AppConfig) {
		c.Recent.Entries = slices.DeleteFunc(c.Recent.Entries, func(e config.RecentEntry) bool { return samePath(e.Path, path) })
	})
	if err != nil {
		applog.Warn("Native", "更新最近打开失败："+err.Error())
		return
	}
	s.recent = slices.DeleteFunc(s.recent, func(e config.RecentEntry) bool { return samePath(e.Path, path) })
}

// openDropped 打开从其他应用拖到编辑区之外的文件：文件夹作为资源管理器根目录，Markdown / 文本 / JSON 打开为标签；
// 其他文件（含图片，图片须拖到编辑区插入）不打开，全部不支持时提示。
func (s *Shell) openDropped(paths []string) {
	var open []string
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && (fi.IsDir() || docfs.IsSupportedDocument(p)) {
			open = append(open, p)
		}
	}
	if len(open) == 0 {
		s.status = s.tr("dragDropNoMarkdown")
		return
	}
	s.OpenExternal(open)
}

func (s *Shell) markdownFilters() []mygo.FileFilter {
	return []mygo.FileFilter{{Name: "Markdown", Extensions: []string{"md", "markdown", "mdown", "mkd"}},
		{Name: "Text / JSON", Extensions: []string{"txt", "json"}}}
}

// openDialog 在后台显示打开对话框，选择后回到 UI 线程打开。
func (s *Shell) openDialog(directory bool) {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: s.win, Directory: directory, Multiple: !directory, Filters: s.filtersFor(directory)})
		if err != nil || len(paths) == 0 {
			return
		}
		s.post(func() {
			if directory {
				s.openFolder(paths[0])
			} else {
				s.OpenFiles(paths)
			}
		})
	}()
}

func (s *Shell) filtersFor(directory bool) []mygo.FileFilter {
	if directory {
		return nil
	}
	return s.markdownFilters()
}

// save 保存标签；没有路径或 as 为 true 时先选择保存位置。then 在保存成功后于 UI 线程调用。
// 原文快照（不可变字符串）交给后台写入，界面线程不等待磁盘同步。
func (s *Shell) save(t *Tab, as bool, then func()) {
	if t == nil || t.saving {
		return
	}
	t.syncFromSource()
	// 目录区列表随标题更新（保存时改写一次，可撤销）；源码模式的缓冲随之同步。
	if !t.plain && t.Ed.RefreshToc() && t.source != nil {
		t.syncToSource()
	}
	if t.Path == "" || as {
		name := t.Name(s.untitledName)
		go func() {
			path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: s.win, DefaultPath: name, Filters: s.markdownFilters(), CreateDirectories: true})
			if err != nil || path == "" {
				return
			}
			if filepath.Ext(path) == "" {
				path += ".md"
			}
			s.post(func() {
				t.Path = path
				s.updateTitle()
				s.save(t, false, then)
			})
		}()
		return
	}
	text, ver, path, enc := t.text(), t.version(), t.Path, t.Encoding
	disk := t.diskText(text)
	snapshot := s.prefs.CreateSnapshotBeforeSave
	t.saving = true
	go func() {
		// 保存前快照（createSnapshotBeforeSave）：与原版一致，快照失败不阻止保存，只记日志。
		if snapshot {
			if err := s.cfg.CreateSnapshot(path, disk, "before-save"); err != nil {
				applog.Warn("Native", "创建保存前快照失败："+err.Error())
			}
		}
		_, err := docfs.WriteDocument(path, disk, enc)
		s.post(func() {
			t.saving = false
			if err != nil {
				s.status = s.tr("saveFileFailed") + "：" + err.Error()
				return
			}
			switch {
			case t.version() == ver:
				t.markSaved(text)
			case !t.plain:
				t.savedVersion, t.savedText, t.dirtyVer = ver, text, 0
			}
			t.diskModified = docfs.ModifiedAt(path)
			s.remember(path, "file")
			if then != nil {
				then()
			}
		})
	}()
}

// requestClose 关闭标签（all 为 true 时随后关闭窗口）；有未保存修改时先确认。
func (s *Shell) requestClose(ids []int, all bool) {
	if all {
		s.beginWorkspaceClose()
	}
	s.closing, s.closingAll = ids, all
	s.continueClose()
}

// continueClose 依次关闭 closing 中的标签，遇到未保存的停下询问。
func (s *Shell) continueClose() {
	for len(s.closing) > 0 {
		i := slices.IndexFunc(s.tabs, func(t *Tab) bool { return t.ID == s.closing[0] })
		if i < 0 {
			s.closing = s.closing[1:]
			continue
		}
		t := s.tabs[i]
		t.syncFromSource()
		if t.Dirty() {
			s.activate(i)
			s.confirmOpen = true
			return
		}
		s.removeTab(i)
		s.closing = s.closing[1:]
	}
	if s.closingAll {
		s.win.Close()
	}
}

// resolveClose 处理确认框的选择：0 取消，1 不保存，2 保存。
func (s *Shell) resolveClose(choice int) {
	if len(s.closing) == 0 {
		return
	}
	i := slices.IndexFunc(s.tabs, func(t *Tab) bool { return t.ID == s.closing[0] })
	switch {
	case choice == 1 && i >= 0:
		s.removeTab(i)
		s.closing = s.closing[1:]
		s.continueClose()
	case choice == 2 && i >= 0:
		s.save(s.tabs[i], false, s.continueClose)
	default:
		if s.closingAll {
			s.cancelWorkspaceClose()
		}
		s.closing, s.closingAll = nil, false
		s.quitting = false // 取消了退出：之后点关闭按钮仍按“关闭窗口行为”处理
		s.focusEditor = true
	}
}

func (s *Shell) removeTab(i int) {
	s.ungroup(s.tabs[i])
	if s.tabs[i].size() >= memtrim.Threshold {
		memtrim.Request()
	}
	s.tabs[i].Ed.ReleaseLayouts()
	s.tabs = slices.Delete(s.tabs, i, i+1)
	switch {
	case len(s.tabs) == 0:
		s.active = -1
	case s.active >= len(s.tabs) || i < s.active:
		s.active = max(0, s.active-1)
		s.focusEditor = true
	default:
		s.focusEditor = true
	}
	s.updateTitle()
}

// checkExternalChanges 检查当前文档是否被外部程序修改：未修改的文档直接重新载入，
// 有未保存修改时只提示，不覆盖用户的编辑。
func (s *Shell) checkExternalChanges() {
	t := s.activeTab()
	if t == nil || t.Path == "" || t.saving {
		return
	}
	mod := docfs.ModifiedAt(t.Path)
	if mod == 0 || mod == t.diskModified {
		return
	}
	if s.prefs.ExternalFileChangeBehavior == "ignore" {
		t.diskModified = mod
		return
	}
	if t.Dirty() || s.prefs.ExternalFileChangeBehavior == "overwrite-external" {
		// 有未保存修改（或设置为以编辑器内容为准）：不覆盖用户的编辑，只提示。
		s.status = s.tr("externalFileChanged")
		t.diskModified = mod
		return
	}
	doc, err := docfs.ReadDocument(t.Path)
	if err != nil {
		return
	}
	t.syncFromSource()
	text := t.adoptLineEndings(doc.Markdown)
	t.replaceText(text)
	t.markSaved(text)
	t.diskModified = mod
}

// setMode 切换语义 / 源码模式；两侧原文先同步再切换。
func (s *Shell) setMode(mode string) {
	if mode == s.prefs.EditorMode {
		return
	}
	if t := s.activeTab(); t != nil {
		switch mode {
		case modeSource:
			t.syncFromSource()
			t.syncToSource()
		case modeSplit:
			t.syncFromSource()
			t.syncToSource()
			s.splitActive = modeSemantic
		default:
			t.syncFromSource()
			t.Ed.ReleaseLayouts()
		}
	}
	s.prefs.EditorMode = mode
	s.focusEditor = true
	s.setPref("editorMode")
}

// run 在当前文档上执行编辑命令；源码模式下格式命令不可用。
func (s *Shell) run(name string) {
	t := s.activeTab()
	if t != nil && name == "details" && s.mode() == modeSource && t.Kind() == kindMarkdown {
		s.insertSourceDetails(t)
		return
	}
	if t == nil || s.mode() != modeSemantic {
		return
	}
	if name == "link" {
		s.openLinkEditor()
		return
	}
	t.Ed.Run(name)
	s.focusEditor = true
}

// autoSave 按原版设置（autoSaveEnabled、autoSaveDelayMs）在最后一次修改后延时保存已有路径的文档；
// 未到期时用 c.After 预约下一帧，不常驻定时器。新文档（无路径）不自动保存，避免弹出保存对话框。
func (s *Shell) autoSave(c *ui.Context) {
	if !s.prefs.AutoSaveEnabled {
		return
	}
	now := c.Now()
	delay := time.Duration(s.prefs.AutoSaveDelayMs) * time.Millisecond
	for _, t := range s.tabs {
		if t.Path == "" || t.saving || !t.Dirty() {
			continue
		}
		if v := t.version(); v != t.autoVer {
			t.autoVer, t.autoAt = v, now
		}
		if due := t.autoAt.Add(delay); !now.Before(due) {
			s.save(t, false, nil)
		} else {
			c.After(due.Sub(now))
		}
	}
}

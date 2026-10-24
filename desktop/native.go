package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/egoist/mygo"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/config"
	"nomo/desktop/internal/emoji"
	"nomo/desktop/ui/shell"
)

// runNativeShell 打开原生界面窗口并运行到退出。数据目录、配置、日志与单实例锁已由 main
// 初始化；外部打开（启动参数、第二实例、macOS 打开文件事件）直接交给外壳。
func runNativeShell(args []string) {
	// 渲染模式（原版设置 renderMode）：选择软件渲染时让 MyGo 在 CPU 上绘制，须在创建窗口前设置。
	if config.IsSoftwareRenderMode(app.config.DataDir()) {
		os.Setenv("MYGO_GPU", "0")
		applog.Info("Native", "已启用软件渲染模式")
	}
	targets := collectExternalOpenTargets(args, mustGetwd())
	var sh *shell.Shell
	var win *mygo.Window
	// 第二实例与打开文件事件投递到最近聚焦的外壳窗口（多窗口时第一个窗口可能已关闭）。
	deliver := func(t externalOpenTargets) {
		target, w := sh, win
		if cur := shell.Current(); cur != nil {
			target, w = cur, cur.Window()
		}
		if w == nil {
			return
		}
		w.Update(func() {
			target.OpenExternal(append(t.folderPaths, t.markdownPaths...))
			target.ShowWindow()
		})
	}
	mygo.App.OnOpenFile(func(path string) { deliver(externalOpenTargets{markdownPaths: []string{path}}) })
	mygo.App.OnSecondInstance(func(args []string, workingDir string) {
		deliver(collectExternalOpenTargets(args, workingDir))
	})
	// 关闭最后一个窗口即退出；“关闭到托盘”时窗口只是隐藏，不触发此事件。
	mygo.App.OnWindowAllClosed(func() { mygo.App.Quit() })
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) { destroyAppTray() })
	mygo.App.WhenReady(func() {
		system := ""
		if languages := mygo.App.PreferredLanguages(); len(languages) > 0 {
			system = languages[0]
		}
		sh = shell.New(shell.Options{Config: app.config, Locale: effectiveLocale(), SystemLocale: system, Version: appVersion(),
			Files: targets.markdownPaths, Folders: targets.folderPaths, IconLight: appIconLight, IconDark: appIconDark,
			ExportPDF: exportNativePDF, CheckUpdate: checkNativeUpdate,
			LoadEmoji:   func(retry bool) ([]emoji.Entry, error) { return emoji.Catalog(appIdentifier, retry) },
			InstallTray: installNativeTray, SetIconTheme: setDesktopIconTheme, SetTrayActive: setTrayActive,
			Primary: true, SampleDocument: resolveSampleDocument, Windows: nativeWindowsIntegration(),
			DownloadUpdate: downloadUpdate, InstallUpdate: installUpdate, ReleaseNotes: releaseNotes})
		win = sh.Open()
		applyAppIcon(win)
		applog.Info("Native", "原生外壳已启动")
	})
	if err := mygo.App.Run(); err != nil {
		applog.Error("App", err.Error())
		os.Exit(1)
	}
}

// applyAppIcon 在 Windows / Linux 把应用图标设到窗口（标题栏与任务栏）。macOS 的 Dock 图标由外壳首帧经
// setDesktopIconTheme 按应用明暗设置（见 tray.go 的 applyDockIcon）；应用未运行时 Finder / Dock 用 Assets.car。
func applyAppIcon(win *mygo.Window) {
	if runtime.GOOS == "darwin" {
		return
	}
	if err := win.SetIcon(appIconLight); err != nil {
		applog.Warn("Native", "设置窗口图标失败："+err.Error())
	}
}

// exportNativePDF 用隐藏窗口打印原生外壳生成的 HTML（含书签后处理），纸张、方向、边距与页码按设置“PDF 导出”。
func exportNativePDF(html, path string, outline []shell.PDFOutlineEntry, layout shell.PDFLayout) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建导出目录失败：%w", err)
	}
	paper, orientation, background := layout.Paper, "portrait", true
	if layout.Landscape {
		orientation = "landscape"
	}
	m := layout.MarginMm
	entries := make([]pdfOutlineEntry, len(outline))
	for i, o := range outline {
		entries[i] = pdfOutlineEntry{MarkerURI: o.MarkerURI, Title: o.Title, Level: o.Level}
	}
	_, err := runPDFExport(exportPDFInput{HTMLContent: html, FilePath: path, PaperSize: &paper, Orientation: &orientation,
		Margins: &pdfMarginsInput{Top: m, Right: m, Bottom: m, Left: m}, PrintBackground: &background, Outline: entries,
		PageNumbers: layout.PageNumbers, Header: layout.Header, Footer: layout.Footer})
	return err
}

// checkNativeUpdate 检查新版本（见 update_check.go）；有新版本时外壳打开发布页。
func checkNativeUpdate() (shell.UpdateInfo, error) {
	p, err := checkSoftwareUpdate()
	if err != nil {
		return shell.UpdateInfo{}, err
	}
	return shell.UpdateInfo{Supported: p.Supported, Available: p.Available, Installable: p.Available && updateInstallable(),
		Version: p.Version, Notes: p.Notes}, nil
}

// installNativeTray 为原生外壳创建托盘：单击恢复主窗口（Linux 只能显示菜单），菜单为“打开 Nomo / 退出”，
// 图标与文案沿用 WebView 版托盘（tray.go）。
func installNativeTray(show, quit func()) bool {
	tray, err := mygo.NewTray(mygo.TrayOptions{ID: "nomo-native-tray", Icon: trayIcon(), ToolTip: "Nomo"})
	if err != nil {
		applog.Warn("Tray", "创建托盘图标失败："+err.Error())
		return false
	}
	// 登记到 trayState，使 setDesktopIconTheme 切换明暗时一并替换托盘图标。
	trayState.mu.Lock()
	trayState.tray = tray
	trayState.mu.Unlock()
	menu := mygo.NewMenu([]*mygo.MenuItem{
		{ID: trayOpenID, Label: tr("tray_open"), Click: func(*mygo.MenuItem, *mygo.Window) { show() }},
		mygo.Separator(),
		{ID: trayExitID, Label: tr("tray_exit"), Click: func(*mygo.MenuItem, *mygo.Window) { quit() }},
	})
	if runtime.GOOS == "linux" {
		tray.SetMenu(menu)
	} else {
		tray.OnClick(show)
		tray.OnRightClick(func() {
			tray.SetMenu(menu)
			tray.PopUpMenu()
			tray.SetMenu(nil)
		})
	}
	return true
}

// sampleDocumentResource 是随安装包分发的示例文档在资源目录中的相对路径（mygo.config.ts 的 resources）。
const sampleDocumentResource = "samples/sample.md"

// resolveSampleDocument 依次在资源目录与可执行文件旁查找非空的示例文档；开发运行（mygo dev 不打包资源）时回退到
// 当前目录的 sample.md。直接打开安装目录中的文件，不复制到用户数据目录（与原版一致）。
func resolveSampleDocument() (string, error) {
	var candidates []string
	if resources, err := mygo.App.Path(mygo.PathResources); err == nil {
		candidates = append(candidates, filepath.Join(resources, sampleDocumentResource))
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(dir, sampleDocumentResource), filepath.Join(dir, "resources", sampleDocumentResource))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, filepath.Base(sampleDocumentResource)))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return candidate, nil
		}
	}
	return "", errors.New("未找到有效的 samples/sample.md")
}

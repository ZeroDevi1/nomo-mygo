package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/egoist/mygo"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/config"
	"nomo/desktop/internal/emoji"
	"nomo/desktop/ui/shell"
)

// 原生外壳入口（计划 P2：独立入口开发，现版本照常发布）。
// 以 `nomo --native` 或环境变量 NOMO_NATIVE=1 启动；默认仍走 WebView 外壳。

const nativeFlag = "--native"

func nativeShellRequested(args []string) bool {
	return os.Getenv("NOMO_NATIVE") == "1" || slices.Contains(args, nativeFlag)
}

// runNativeShell 打开原生外壳窗口并运行到退出。数据目录、配置、日志与单实例锁已由 main
// 初始化；外部打开（启动参数、第二实例、macOS 打开文件事件）直接交给外壳。
func runNativeShell(args []string) {
	// 渲染模式（原版设置 renderMode）：选择软件渲染时让 MyGo 在 CPU 上绘制，须在创建窗口前设置。
	if config.IsSoftwareRenderMode(app.config.DataDir()) {
		os.Setenv("MYGO_GPU", "0")
		applog.Info("Native", "已启用软件渲染模式")
	}
	targets := collectExternalOpenTargets(slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == nativeFlag }), mustGetwd())
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
			target.OpenFiles(append(t.folderPaths, t.markdownPaths...))
			w.Show()
			w.Focus()
		})
	}
	mygo.App.OnOpenFile(func(path string) { deliver(externalOpenTargets{markdownPaths: []string{path}}) })
	mygo.App.OnSecondInstance(func(args []string, workingDir string) {
		deliver(collectExternalOpenTargets(args, workingDir))
	})
	// 关闭最后一个窗口即退出；“关闭到托盘”时窗口只是隐藏，不触发此事件。
	mygo.App.OnWindowAllClosed(func() { mygo.App.Quit() })
	mygo.App.WhenReady(func() {
		system := ""
		if languages := mygo.App.PreferredLanguages(); len(languages) > 0 {
			system = languages[0]
		}
		sh = shell.New(shell.Options{Config: app.config, Locale: effectiveLocale(), SystemLocale: system, Version: appVersion(),
			Files: targets.markdownPaths, Folders: targets.folderPaths, IconLight: appIconLight, IconDark: appIconDark,
			ExportPDF: exportNativePDF, CheckUpdate: checkNativeUpdate,
			LoadEmoji:   func(retry bool) ([]emoji.Entry, error) { return emoji.Catalog(appIdentifier, retry) },
			InstallTray: installNativeTray})
		win = sh.Open()
		applyAppIcon(win)
		applog.Info("Native", "原生外壳已启动")
	})
	if err := mygo.App.Run(); err != nil {
		applog.Error("App", err.Error())
		os.Exit(1)
	}
}

// applyAppIcon 设置应用图标：Windows / Linux 设到窗口（标题栏与任务栏）；macOS 只在不是从
// .app 包运行（go run、开发构建）时替换 Dock 图标，打包后的应用保留 Assets.car 中支持深色与
// 着色变体的图标。
func applyAppIcon(win *mygo.Window) {
	switch runtime.GOOS {
	case "darwin":
		if exe, err := os.Executable(); err == nil && !strings.Contains(exe, ".app/Contents/MacOS/") {
			if err := mygo.App.Dock.SetIcon(appIconDock); err != nil {
				applog.Warn("Native", "设置 Dock 图标失败："+err.Error())
			}
		}
	default:
		if err := win.SetIcon(appIconLight); err != nil {
			applog.Warn("Native", "设置窗口图标失败："+err.Error())
		}
	}
}

// exportNativePDF 用 WebView 版的 PDF 管线（按需创建的隐藏窗口打印，含书签后处理）导出原生外壳生成的
// HTML；纸张、方向与边距同原版：A4、纵向、20mm。
func exportNativePDF(html, path string, outline []shell.PDFOutlineEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建导出目录失败：%w", err)
	}
	paper, orientation, background := "A4", "portrait", true
	entries := make([]pdfOutlineEntry, len(outline))
	for i, o := range outline {
		entries[i] = pdfOutlineEntry{MarkerURI: o.MarkerURI, Title: o.Title, Level: o.Level}
	}
	_, err := runPDFExport(exportPDFInput{HTMLContent: html, FilePath: path, PaperSize: &paper, Orientation: &orientation,
		Margins: &pdfMarginsInput{Top: 20, Right: 20, Bottom: 20, Left: 20}, PrintBackground: &background, Outline: entries})
	return err
}

// checkNativeUpdate 复用 WebView 版的更新检查（GitHub Release、代理回退与安装方式判断）。
func checkNativeUpdate() (shell.UpdateInfo, error) {
	p, err := checkSoftwareUpdate("native", false)
	if err != nil {
		return shell.UpdateInfo{}, err
	}
	info := shell.UpdateInfo{Supported: p.Supported && p.InstallationKind != installationStore, Available: p.Available}
	if p.Version != nil {
		info.Version = *p.Version
	}
	return info, nil
}

// installNativeTray 为原生外壳创建托盘：单击恢复主窗口（Linux 只能显示菜单），菜单为“打开 Nomo / 退出”，
// 图标与文案沿用 WebView 版托盘（tray.go）。
func installNativeTray(show, quit func()) bool {
	tray, err := mygo.NewTray(mygo.TrayOptions{ID: "nomo-native-tray", Icon: trayIcon(), ToolTip: "Nomo"})
	if err != nil {
		applog.Warn("Tray", "创建托盘图标失败："+err.Error())
		return false
	}
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

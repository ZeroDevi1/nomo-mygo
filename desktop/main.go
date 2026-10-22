// Command desktop 是 Nomo 的 MyGo 桌面外壳：承载 Svelte 前端页面，并以 Go 实现文件读写、
// 分段大文档引擎、多窗口、菜单托盘、导出与更新等原生能力。
package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/config"
)

func main() {
	started := time.Now()
	dataDir, err := config.ResolveAppDataDir(appIdentifier)
	if err != nil {
		fmt.Fprintln(os.Stderr, "定位应用数据目录失败：", err)
		os.Exit(1)
	}
	logsDir := filepath.Join(dataDir, "logs")
	if mygo.IsDev() {
		// 开发构建把日志写到项目根目录，便于和前端日志一起查看。
		if wd, err := os.Getwd(); err == nil {
			logsDir = filepath.Join(wd, "logs")
		}
	}
	applog.Init(logsDir, config.IsDeveloperMode(dataDir))

	// WebView2 渲染模式必须在环境初始化前通过环境变量设置。
	if runtime.GOOS == "windows" {
		if config.IsSoftwareRenderMode(dataDir) {
			os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", "--disable-gpu --disable-gpu-compositing")
			applog.Info("App", "已启用软件渲染模式")
		} else {
			applog.Info("App", "已启用硬件加速渲染模式")
		}
	}

	// 单实例：后启动的进程把参数交给已运行的实例后退出。
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}

	if app, err = newDesktopApp(dataDir); err != nil {
		fmt.Fprintln(os.Stderr, "初始化应用失败：", err)
		os.Exit(1)
	}
	if err := recoverTransferJournals(); err != nil {
		fmt.Fprintln(os.Stderr, "恢复标签移交记录失败：", err)
		os.Exit(1)
	}
	if err := initializeInstalledReleaseNotes(); err != nil {
		applog.Warn("Update", "初始化更新日志版本记录失败："+err.Error())
	}
	if nativeShellRequested(os.Args[1:]) {
		runNativeShell(os.Args[1:])
		return
	}

	mygo.Bind(Bridge{})
	if err := mygo.Protocol.Handle("asset", http.HandlerFunc(serveAssetFile)); err != nil {
		applog.Error("App", "注册 asset 协议失败："+err.Error())
	}

	startupTargets := collectExternalOpenTargets(os.Args[1:], mustGetwd())
	markArgsDelivered(startupTargets.markdownPaths)
	mygo.App.OnOpenFile(handleOpenFile)
	mygo.App.OnSecondInstance(func(args []string, workingDir string) {
		applog.Info("App", "收到单实例启动参数")
		targets := collectExternalOpenTargets(args, workingDir)
		markArgsDelivered(targets.markdownPaths)
		restore := len(targets.markdownPaths) == 0 && len(targets.folderPaths) == 0
		queueExternalOpenTargets(targets)
		if restore {
			go showMainWindow()
		}
	})
	mygo.App.OnActivate(func(hasVisibleWindows bool) {
		applog.Info("Window", fmt.Sprintf("收到 macOS Dock 重开事件：visible=%v", hasVisibleWindows))
		if !hasVisibleWindows {
			go showMainWindow()
		}
	})
	// 关闭最后一个窗口不退出：托盘与 Dock 仍可唤回窗口，退出只走工作区退出事务。
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) {
		if isExitCommitting() {
			return
		}
		// 系统发起的退出（Dock 菜单、注销）也先让每个窗口保存工作区。
		e.PreventDefault()
		go func() { _ = beginExit() }()
	})
	mygo.Theme.OnUpdated(broadcastSystemThemeChanged)

	mygo.App.WhenReady(func() {
		setupStarted := time.Now()
		applog.Info("App", "开始 MyGo setup")
		main, err := createDocumentWindow(mainWindowLabel, true, nil)
		if err != nil {
			applog.Error("App", err.Error())
			mygo.App.Exit(1)
			return
		}
		installWindowMenu(main)
		installAppTray()
		restoreWindowState(main)
		main.win.Show()

		targets := startupTargets
		for _, path := range takeEarlyOpenPaths() {
			if !containsString(targets.markdownPaths, path) {
				targets.markdownPaths = append(targets.markdownPaths, path)
			}
		}
		applog.Info("App", fmt.Sprintf("启动待打开目标：files=%d folders=%d", len(targets.markdownPaths), len(targets.folderPaths)))
		if err := persistPendingExternalOpen(mainWindowLabel, targets.markdownPaths); err != nil {
			applog.Error("ExternalOpen", err.Error())
		}
		if len(targets.folderPaths) > 0 {
			if err := persistPendingExternalFolderOpen(mainWindowLabel, targets.folderPaths[0]); err != nil {
				applog.Error("ExternalOpen", err.Error())
			}
		}
		applog.Perf("App", "MyGo setup", time.Since(setupStarted))
		applog.Perf("App", "软件打开", time.Since(started))
	})

	if err := mygo.App.Run(); err != nil {
		applog.Error("App", err.Error())
		os.Exit(1)
	}
	committedExit()
}

func mustGetwd() string {
	wd, _ := os.Getwd()
	return wd
}

// serveAssetFile 为页面提供本地图片等文件（asset://localhost/<编码路径>），对应前端 convertFileSrc。
// 与 Tauri asset 协议的作用域一致：允许读取任意本地文件，只接受 GET/HEAD。
func serveAssetFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path, err := url.PathUnescape(strings.TrimPrefix(r.URL.EscapedPath(), "/"))
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	if runtime.GOOS != "windows" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}

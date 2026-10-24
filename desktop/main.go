// Command desktop 是 Nomo 的桌面程序：MyGo 原生界面（desktop/ui/shell）与文件、托盘、导出、更新检查等原生能力。
// 没有 Web 前端；PDF 导出把 Go 生成的 HTML 交给按需创建的隐藏 WebView 打印。
package main

import (
	"fmt"
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
		// 开发构建把日志写到项目根目录。
		if wd, err := os.Getwd(); err == nil {
			logsDir = filepath.Join(wd, "logs")
		}
	}
	applog.Init(logsDir, config.IsDeveloperMode(dataDir))

	// PDF 导出用隐藏的 WebView2：浏览器参数须在环境初始化前通过环境变量设置（MyGo 不提供该选项）。
	if runtime.GOOS == "windows" {
		os.Setenv(webview2ArgsEnv, webview2BrowserArgs(os.Getenv(webview2ArgsEnv), config.IsSoftwareRenderMode(dataDir)))
	}

	// 单实例：后启动的进程把参数交给已运行的实例后退出。
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}
	if app, err = newDesktopApp(dataDir); err != nil {
		fmt.Fprintln(os.Stderr, "初始化应用失败：", err)
		os.Exit(1)
	}
	applog.Perf("App", "启动准备", time.Since(started))
	runNativeShell(os.Args[1:])
}

const webview2ArgsEnv = "WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"

// webview2DefaultArgs 同 Tauri（wry）在 Windows 上的默认参数：关闭选中文字时的“迷你菜单”、PDF 界面与 SmartScreen
// 网址信誉检查。SmartScreen 会为每次导航发起网络查询并常驻额外进程，本地页面不需要它。
const webview2DefaultArgs = "--disable-features=msWebOOUI,msPdfOOUI,msSmartScreenProtection"

// webview2BrowserArgs 返回 WebView2 附加浏览器参数：existing 为启动前已有的同名环境变量（用户或系统设置，原样保留且
// 不再追加 --disable-features，因为 Chromium 只认最后一个该参数）；software 为软件渲染模式。
func webview2BrowserArgs(existing string, software bool) string {
	args := strings.TrimSpace(existing)
	if args == "" {
		args = webview2DefaultArgs
	}
	if software {
		args += " --disable-gpu --disable-gpu-compositing"
	}
	return args
}

func mustGetwd() string {
	wd, _ := os.Getwd()
	return wd
}

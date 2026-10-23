package main

import (
	"embed"
	"encoding/json/v2"
	"runtime"
	"strings"
	"sync"

	"github.com/egoist/mygo"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/i18n"
)

// 托盘与 Dock 图标的深浅色、托盘的创建与删除，以及主程序界面文字的语言。托盘菜单见 native.go 的 installNativeTray。

//go:embed packaging/icons/nomo/tray/*-48.png packaging/icons/nomo/macos/nomo-app-light-1024.png packaging/icons/nomo/macos/nomo-app-dark-1024.png
var iconAssets embed.FS

const (
	trayOpenID = "tray-open-main-window"
	trayExitID = "tray-exit-app"
)

// trayState 保存托盘与当前图标深浅色；托盘在主线程创建，图标替换可能来自外壳的后台 goroutine，由 mu 保护。
var trayState struct {
	mu             sync.Mutex
	tray           *mygo.Tray
	dark           bool
	appliedIconSet *bool // nil 表示尚未应用过桌面图标主题
}

// setDesktopIconTheme 让托盘与 macOS Dock 图标跟随应用深浅色；主题未变化时跳过。
func setDesktopIconTheme(dark bool) {
	trayState.mu.Lock()
	trayState.dark = dark
	unchanged := trayState.appliedIconSet != nil && *trayState.appliedIconSet == dark
	if !unchanged {
		trayState.appliedIconSet = &dark
	}
	trayState.mu.Unlock()
	if unchanged {
		return
	}
	if runtime.GOOS == "darwin" {
		name := "packaging/icons/nomo/macos/nomo-app-light-1024.png"
		if dark {
			name = "packaging/icons/nomo/macos/nomo-app-dark-1024.png"
		}
		if icon, err := iconAssets.ReadFile(name); err == nil {
			mygo.App.Dock.SetIcon(icon)
		}
	}
	applyTrayIcon()
}

// destroyAppTray 删除托盘图标。MyGo 的 App.Exit 与 Quit 都不删除通知区域图标：Windows 上进程结束后图标会残留，
// 直到鼠标经过才消失。退出前调用；可重复调用。
func destroyAppTray() {
	trayState.mu.Lock()
	tray := trayState.tray
	trayState.tray = nil
	trayState.mu.Unlock()
	if tray != nil {
		tray.Destroy()
	}
}

// trayIcon 返回当前深浅色的托盘图标（48px PNG，高 DPI 下缩放）。
func trayIcon() []byte {
	trayState.mu.Lock()
	theme := "light"
	if trayState.dark {
		theme = "dark"
	}
	trayState.mu.Unlock()
	icon, _ := iconAssets.ReadFile("packaging/icons/nomo/tray/nomo-tray-" + theme + "-active-48.png")
	return icon
}

func applyTrayIcon() {
	trayState.mu.Lock()
	tray := trayState.tray
	trayState.mu.Unlock()
	if tray == nil {
		return
	}
	if err := tray.SetIcon(trayIcon(), false); err != nil {
		applog.Warn("Tray", "设置 Nomo 托盘图标失败："+err.Error())
	}
}

// effectiveLocale 返回界面语言：设置 interfaceLanguage，缺省跟随系统首选语言。
func effectiveLocale() i18n.Locale {
	preference := "system"
	if value, ok := app.config.Setting("interfaceLanguage"); ok {
		var decoded string
		if json.Unmarshal([]byte(value), &decoded) == nil {
			preference = decoded
		}
	}
	system := ""
	if languages := mygo.App.PreferredLanguages(); len(languages) > 0 {
		system = languages[0]
	}
	return i18n.ResolvePreference(preference, system)
}

// tr 返回主程序（托盘菜单）的界面文字；macOS 菜单不显示助记符，去掉单个 &，&& 还原为 &。
func tr(key string) string {
	text := i18n.Text(effectiveLocale(), key)
	if runtime.GOOS == "darwin" {
		text = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(text, "&&", "\x00"), "&", ""), "\x00", "&")
	}
	return text
}

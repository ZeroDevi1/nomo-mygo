package main

import (
	"embed"
	"encoding/json/v2"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/i18n"
)

// 托盘图标、窗口标题记录、桌面图标深浅色与界面语言。

//go:embed packaging/icons/nomo/tray/*-48.png packaging/icons/nomo/macos/nomo-app-light-1024.png packaging/icons/nomo/macos/nomo-app-dark-1024.png
var iconAssets embed.FS

const (
	trayOpenID         = "tray-open-main-window"
	trayExitID         = "tray-exit-app"
	trayWindowPrefix   = "tray-window:"
	systemThemeChanged = "nomo://system-theme-changed"
)

var trayState struct {
	mu             sync.Mutex
	tray           *mygo.Tray
	active         bool
	dark           bool
	appliedIconSet *bool // nil 表示尚未应用过桌面图标主题
	lastActive     string
	titles         map[string]string
	lastTheme      string
}

func init() {
	trayState.active = true
	trayState.titles = map[string]string{}
	register("set_desktop_icon_theme", func(_ *commandCall, args struct {
		Theme             string  `json:"theme"`
		CaptionBackground *string `json:"captionBackground"`
	}) (any, error) {
		applog.Info("Tray", "设置桌面图标主题："+args.Theme)
		var dark bool
		switch args.Theme {
		case "dark":
			dark = true
		case "light":
		default:
			return nil, fmt.Errorf("不支持的桌面主题：%s", args.Theme)
		}
		if runtime.GOOS == "windows" {
			// Windows 用原生背景色衬托页面标题栏，避免切换主题时露出旧颜色。
			background := "#f3f4f6"
			if dark {
				background = "#181d23"
			}
			if args.CaptionBackground != nil && parseCSSHexColor(*args.CaptionBackground) {
				background = *args.CaptionBackground
			}
			for _, window := range windows.all() {
				if err := window.win.SetBackgroundColor(background); err != nil {
					applog.Warn("Window", fmt.Sprintf("同步窗口原生背景色失败：label=%s error=%v", window.label, err))
				}
			}
		}
		// 图标替换不阻塞这次调用，避免拖慢设置窗口紧接着发出的外观广播。
		go setDesktopIconTheme(dark)
		return nil, nil
	})
	register("get_desktop_system_theme", func(_ *commandCall, _ noArgs) (string, error) {
		return systemTheme(), nil
	})
}

func systemTheme() string {
	if mygo.Theme.IsDark() {
		return "dark"
	}
	return "light"
}

// broadcastSystemThemeChanged 把系统深浅色广播给所有页面；与上次相同时跳过。
func broadcastSystemThemeChanged() {
	theme := systemTheme()
	trayState.mu.Lock()
	unchanged := trayState.lastTheme == theme
	trayState.lastTheme = theme
	trayState.mu.Unlock()
	if !unchanged {
		broadcast(systemThemeChanged, theme)
	}
}

// parseCSSHexColor 校验 #rgb / #rrggbb 形式的颜色。
func parseCSSHexColor(value string) bool {
	hex, ok := strings.CutPrefix(value, "#")
	if !ok || len(hex) != 3 && len(hex) != 6 {
		return false
	}
	_, err := strconv.ParseUint(hex, 16, 32)
	return err == nil
}

// installAppTray 创建托盘：左键打开最近活跃的文档窗口，右键弹出菜单（Linux 只能显示菜单）。
func installAppTray() {
	tray, err := mygo.NewTray(mygo.TrayOptions{ID: "nomo-main-tray", Icon: trayIcon(), ToolTip: "Nomo"})
	if err != nil {
		applog.Warn("Tray", "创建托盘图标失败："+err.Error())
		return
	}
	trayState.mu.Lock()
	trayState.tray = tray
	trayState.mu.Unlock()
	if runtime.GOOS == "linux" {
		tray.SetMenu(buildTrayMenu())
	} else {
		tray.OnClick(func() { go showMainWindow() })
		tray.OnRightClick(func() {
			tray.SetMenu(buildTrayMenu())
			tray.PopUpMenu()
			tray.SetMenu(nil)
		})
	}
	applog.Info("Tray", "托盘图标创建完成")
}

func buildTrayMenu() *mygo.Menu {
	items := []*mygo.MenuItem{{ID: trayOpenID, Label: tr("tray_open"), Click: onTrayMenu}}
	documents := windows.documentWindows()
	if len(documents) > 0 {
		items = append(items, mygo.Separator())
		for _, window := range documents {
			prefix := "○"
			if window.win.IsVisible() {
				prefix = "●"
			}
			items = append(items, &mygo.MenuItem{ID: trayWindowPrefix + window.label, Label: prefix + " " + windowDisplayTitle(window), Click: onTrayMenu})
		}
	}
	items = append(items, mygo.Separator(), &mygo.MenuItem{ID: trayExitID, Label: tr("tray_exit"), Click: onTrayMenu})
	return mygo.NewMenu(items)
}

func onTrayMenu(item *mygo.MenuItem, _ *mygo.Window) {
	switch id := item.ID; {
	case id == trayOpenID:
		go showMainWindow()
	case id == trayExitID:
		go func() { _ = beginExit() }()
	case strings.HasPrefix(id, trayWindowPrefix):
		if window := windows.get(strings.TrimPrefix(id, trayWindowPrefix)); window != nil && isDocumentWindowLabel(window.label) {
			go showDocumentWindow(window)
		}
	}
}

// refreshTrayMenu 只在 Linux 常驻菜单时需要重建；其他平台在右键时按需生成。
func refreshTrayMenu() {
	trayState.mu.Lock()
	tray := trayState.tray
	trayState.mu.Unlock()
	if tray != nil && runtime.GOOS == "linux" {
		tray.SetMenu(buildTrayMenu())
	}
}

// showMainWindow 打开最近活跃的文档窗口：有可见窗口时只在可见窗口中挑选。
func showMainWindow() {
	applog.Info("Tray", "从托盘打开文档窗口")
	documents := windows.documentWindows()
	var visible []*appWindow
	for _, window := range documents {
		if window.win.IsVisible() {
			visible = append(visible, window)
		}
	}
	candidates := documents
	if len(visible) > 0 {
		candidates = visible
	}
	if target := chooseLastActiveWindow(candidates); target != nil {
		showDocumentWindow(target)
	}
}

func chooseLastActiveWindow(candidates []*appWindow) *appWindow {
	last := lastActiveDocumentWindowLabel()
	for _, window := range candidates {
		if window.label == last {
			return window
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return nil
}

func recordLastActiveWindow(label string) {
	trayState.mu.Lock()
	trayState.lastActive = label
	trayState.mu.Unlock()
	refreshTrayMenu()
}

func lastActiveDocumentWindowLabel() string {
	trayState.mu.Lock()
	defer trayState.mu.Unlock()
	return trayState.lastActive
}

func recordWindowTitle(label, title string) {
	trayState.mu.Lock()
	trayState.titles[label] = title
	trayState.mu.Unlock()
	refreshTrayMenu()
}

func forgetTrayWindow(label string) {
	trayState.mu.Lock()
	delete(trayState.titles, label)
	if trayState.lastActive == label {
		trayState.lastActive = ""
	}
	trayState.mu.Unlock()
	refreshTrayMenu()
}

func windowDisplayTitle(window *appWindow) string {
	trayState.mu.Lock()
	title := trayState.titles[window.label]
	trayState.mu.Unlock()
	if strings.TrimSpace(title) != "" {
		return title
	}
	if title := window.win.Title(); strings.TrimSpace(title) != "" {
		return title
	}
	return window.label
}

func setTrayActive(active bool) {
	trayState.mu.Lock()
	trayState.active = active
	trayState.mu.Unlock()
	applyTrayIcon()
}

func syncTrayActiveWithWindowVisibility() {
	active := false
	for _, window := range windows.documentWindows() {
		active = active || window.win.IsVisible()
	}
	setTrayActive(active)
}

// setDesktopIconTheme 让托盘与 macOS Dock 图标跟随深浅色；主题未变化时跳过。
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

func trayIcon() []byte {
	trayState.mu.Lock()
	theme, state := "light", "active"
	if trayState.dark {
		theme = "dark"
	}
	if !trayState.active {
		state = "inactive"
	}
	trayState.mu.Unlock()
	icon, _ := iconAssets.ReadFile("packaging/icons/nomo/tray/nomo-tray-" + theme + "-" + state + "-48.png")
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

// effectiveLocale 返回原生外壳使用的语言：设置 interfaceLanguage，缺省跟随系统首选语言。
func effectiveLocale() i18n.Locale {
	preference := "system"
	if value, ok := settingValue("interfaceLanguage"); ok {
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

var errTrayUnavailable = errors.New("托盘尚未创建")

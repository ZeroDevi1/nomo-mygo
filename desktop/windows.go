package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"

	"nomo/desktop/internal/applog"
)

// 窗口注册表：为每个 MyGo 窗口维护与 Tauri 版本相同的标签（main、window-<ts>-<n>、window-settings、
// PDF 导出窗口），配置键与前端逻辑都以标签区分窗口。

const (
	mainWindowLabel         = "main"
	documentWindowWidth     = 1180
	documentWindowHeight    = 760
	documentWindowMinWidth  = 920
	documentWindowMinHeight = 640
	// 设置窗口默认小于文档窗口最小尺寸；宽度须大于前端 760px 断点，侧栏才显示文字标签。
	settingsWindowWidth  = 820
	settingsWindowHeight = 560
	// 最小宽度同样大于 760px 断点：侧栏收起为 64px 时放不下 macOS 红绿灯。
	settingsWindowMinWidth  = 780
	settingsWindowMinHeight = 480
	// windowBackground 与前端浅/深色主题的窗口底色一致，避免页面绘制前闪色。
	windowBackground = "light-dark(#f3f4f6, #181d23)"
)

// appWindow 绑定一个 MyGo 窗口与其标签；创建后不变。
type appWindow struct {
	label string
	win   *mygo.Window
}

type windowRegistry struct {
	mu      sync.RWMutex
	byLabel map[string]*appWindow
	byID    map[int]*appWindow
}

var windows = &windowRegistry{byLabel: map[string]*appWindow{}, byID: map[int]*appWindow{}}

func (r *windowRegistry) add(window *appWindow) {
	r.mu.Lock()
	r.byLabel[window.label] = window
	r.byID[window.win.ID()] = window
	r.mu.Unlock()
}

func (r *windowRegistry) remove(window *appWindow) {
	r.mu.Lock()
	if r.byLabel[window.label] == window {
		delete(r.byLabel, window.label)
	}
	delete(r.byID, window.win.ID())
	r.mu.Unlock()
}

func (r *windowRegistry) get(label string) *appWindow {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byLabel[label]
}

func (r *windowRegistry) byWindow(win *mygo.Window) *appWindow {
	if win == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byID[win.ID()]
}

// all 返回按标签排序的全部窗口。
func (r *windowRegistry) all() []*appWindow {
	r.mu.RLock()
	list := make([]*appWindow, 0, len(r.byLabel))
	for _, window := range r.byLabel {
		list = append(list, window)
	}
	r.mu.RUnlock()
	sort.Slice(list, func(i, j int) bool { return list[i].label < list[j].label })
	return list
}

func (r *windowRegistry) documentWindows() []*appWindow {
	var list []*appWindow
	for _, window := range r.all() {
		if isDocumentWindowLabel(window.label) {
			list = append(list, window)
		}
	}
	return list
}

func callerWindow(ctx context.Context) *appWindow {
	return windows.byWindow(mygo.CallerWindow(ctx))
}

// isDocumentWindowLabel 判断标签是否属于文档窗口：main 或 window-*（设置窗口除外）。
func isDocumentWindowLabel(label string) bool {
	return label == mainWindowLabel || strings.HasPrefix(label, "window-") && label != settingsWindowLabel
}

// preloadScript 在页面脚本之前注入窗口标签，前端 getCurrentWindow().label 由此读取。
func preloadScript(label string) string {
	encoded, _ := json.Marshal(label)
	return "window.__NOMO_WINDOW__ = Object.freeze({ label: " + string(encoded) + " });"
}

// documentWindowOptions 返回文档窗口的外观：macOS 隐藏标题栏保留红绿灯，
// Windows 隐藏系统按钮由页面绘制，Linux 使用系统标题栏。
func documentWindowOptions(label string, hidden bool) mygo.WindowOptions {
	options := mygo.WindowOptions{
		Title:           "Nomo",
		URL:             "/",
		Width:           documentWindowWidth,
		Height:          documentWindowHeight,
		MinWidth:        documentWindowMinWidth,
		MinHeight:       documentWindowMinHeight,
		Hidden:          hidden,
		BackgroundColor: windowBackground,
		Page:            mygo.PageOptions{PreloadScript: preloadScript(label)},
	}
	applyChromeOptions(&options)
	return options
}

func applyChromeOptions(options *mygo.WindowOptions) {
	switch runtime.GOOS {
	case "darwin":
		options.TitleBarStyle = mygo.TitleBarHidden
		// 关闭按钮左上角相对窗口的偏移：42px 标题栏中，14pt 高的按钮上沿取 14 使其与侧栏按钮垂直居中。
		options.TrafficLightPosition = &mygo.Point{X: 16, Y: 14}
	case "windows":
		options.TitleBarStyle = mygo.TitleBarHidden
		options.HideWindowButtons = true
	}
}

// createDocumentWindow 以给定标签创建文档窗口并接入事件；标签已存在时报错。
func createDocumentWindow(label string, hidden bool, position *mygo.Point) (*appWindow, error) {
	if windows.get(label) != nil {
		return nil, fmt.Errorf("窗口已存在：%s", label)
	}
	options := documentWindowOptions(label, hidden)
	if position != nil {
		options.X, options.Y = position.X, position.Y
	}
	window := &appWindow{label: label, win: mygo.NewWindow(options)}
	windows.add(window)
	attachWindowEvents(window)
	return window, nil
}

func createSettingsWindow(owner *appWindow) (*appWindow, error) {
	options := mygo.WindowOptions{
		Title:           settingsWindowTitle(),
		URL:             "/index.html?view=settings",
		Width:           settingsWindowWidth,
		Height:          settingsWindowHeight,
		MinWidth:        settingsWindowMinWidth,
		MinHeight:       settingsWindowMinHeight,
		Hidden:          true,
		SkipTaskbar:     true,
		BackgroundColor: windowBackground,
		Page:            mygo.PageOptions{PreloadScript: preloadScript(settingsWindowLabel)},
	}
	// macOS 隐藏标题栏、红绿灯落在侧栏顶部（同系统设置）；Windows 由页面绘制窗口按钮。
	applyChromeOptions(&options)
	// 有文档窗口时挂在其上并保持在其上方。Windows 为模态（禁用父窗口输入）；macOS 不用 sheet，
	// 保留红绿灯以便缩放、放大与关闭。
	if owner != nil {
		options.Parent = owner.win
		options.Modal = runtime.GOOS == "windows"
	}
	window := &appWindow{label: settingsWindowLabel, win: mygo.NewWindow(options)}
	windows.add(window)
	attachWindowEvents(window)
	return window, nil
}

// settingsOwnerWindow 返回设置窗口的模态父窗口：聚焦的文档窗口，其次最近活动的文档窗口，
// 最后主窗口；都不存在时为 nil（设置窗口退化为独立窗口）。
func settingsOwnerWindow() *appWindow {
	documents := windows.documentWindows()
	for _, window := range documents {
		if window.win.IsFocused() {
			return window
		}
	}
	last := lastActiveDocumentWindowLabel()
	for _, window := range documents {
		if window.label == last && window.win.IsVisible() {
			return window
		}
	}
	if main := windows.get(mainWindowLabel); main != nil && main.win.IsVisible() {
		return main
	}
	return nil
}

// attachWindowEvents 接入关闭确认、几何持久化、焦点记录与销毁清理。
func attachWindowEvents(window *appWindow) {
	label := window.label
	window.win.OnClose(func(e *mygo.CloseEvent) { handleCloseRequest(window, e) })
	window.win.OnMove(func() { persistWindowStateAfterGeometryChange(window) })
	window.win.OnResize(func() {
		persistWindowStateAfterGeometryChange(window)
		_ = emitToWindow(window, "nomo-window://resized", nil)
	})
	window.win.OnFocus(func() {
		if isDocumentWindowLabel(label) {
			recordLastActiveWindow(label)
		}
	})
	window.win.OnFileDrop(func(e *mygo.FileDropEvent) {
		_ = emitToWindow(window, "nomo-window://file-drop", map[string]any{"paths": e.Paths})
	})
	window.win.OnClosed(func() { handleWindowDestroyed(window) })
}

// handleCloseRequest 复刻原有关闭流程：已放行的关闭直接进行；设置窗口先请求页面保存；
// 文档窗口交给页面确认（nomo://request-close-window），由页面调用 close_window 再次关闭。
func handleCloseRequest(window *appWindow, e *mygo.CloseEvent) {
	label := window.label
	applog.Info("Window", "收到窗口关闭请求："+label)
	if isPDFExportWindowLabel(label) {
		return
	}
	returnsFromMini := isDocumentWindowLabel(label) && isMarkdownMiniModeWindow(label)
	if consumeNextClose(label) {
		if label == settingsWindowLabel {
			settingsState.handlerReady.Store(false)
			return
		}
		if returnsFromMini {
			return
		}
		deferred, err := requestSettingsCloseBeforeOwner(label)
		if err != nil {
			e.PreventDefault()
			applog.Warn("Settings", fmt.Sprintf("关闭 owner 前保存偏好设置失败，已取消关闭：label=%s error=%v", label, err))
			return
		}
		if deferred {
			e.PreventDefault()
		}
		return
	}
	if label == settingsWindowLabel {
		if !settingsState.handlerReady.Load() {
			return
		}
		requestID, started := beginSettingsCloseRequest()
		e.PreventDefault()
		if started {
			broadcastTo("nomo://settings-request-close", requestID, settingsWindowLabel)
			scheduleSettingsCloseFallback(window, requestID)
		}
		return
	}
	if !isDocumentWindowLabel(label) {
		return
	}
	e.PreventDefault()
	if returnsFromMini {
		_ = emitToWindow(window, "nomo://markdown-mini-request-return", nil)
		return
	}
	applog.Info("Window", "请求前端确认关闭："+label)
	broadcast("nomo://request-close-window", map[string]string{"windowLabel": label})
}

// handleWindowDestroyed 在窗口销毁后持久化状态并清理各子系统中与该窗口相关的记录。
func handleWindowDestroyed(window *appWindow) {
	label := window.label
	windows.remove(window)
	if isPDFExportWindowLabel(label) {
		return
	}
	releaseInstalledReleaseNotes(label)
	forgetTabDragWindow(label)
	forgetTabTransferWindow(label)
	windowDestroyed(label)
	applog.Debug("Window", "窗口销毁后持久化状态："+label)
	persistWindowStateOnDestroy(window)
	if label == settingsWindowLabel {
		settingsWindowDestroyed()
	}
	if isDocumentWindowLabel(label) {
		openTargets.forgetWindow(label)
		forgetMarkdownMiniModeWindow(label)
		forgetTrayWindow(label)
	}
}

// showDocumentWindow 显示、还原并聚焦文档窗口。
func showDocumentWindow(window *appWindow) {
	window.win.Show()
	window.win.Restore()
	window.win.Focus()
	setTrayActive(true)
	refreshTrayMenu()
}

func init() {
	register("create_new_window", func(_ *commandCall, args struct {
		PendingFolder *string `json:"pendingFolder"`
	}) (string, error) {
		waitUntilIdle()
		label, err := openTargets.nextWindowLabel()
		if err != nil {
			return "", err
		}
		// 新窗口加载前先写入待打开目录，避免前端初始化读取设置时发生竞态。
		if args.PendingFolder != nil {
			value, _ := json.Marshal(*args.PendingFolder)
			if err := updateSettings([]settingInput{{Key: "pendingFolder:" + label, ValueJSON: string(value)}}); err != nil {
				return "", err
			}
		}
		return label, nil
	})
	register("create_document_window", func(_ *commandCall, args struct {
		WindowLabel string `json:"windowLabel"`
	}) (any, error) {
		if !isDocumentWindowLabel(args.WindowLabel) || args.WindowLabel == mainWindowLabel {
			return nil, errors.New("无效的文档窗口标签")
		}
		_, err := createDocumentWindow(args.WindowLabel, runtime.GOOS == "windows", nil)
		return nil, err
	})
	register("refresh_window_menu", func(call *commandCall, _ noArgs) (any, error) {
		window, err := call.requireWindow()
		if err != nil {
			return nil, err
		}
		applog.Info("Window", "刷新窗口菜单："+window.label)
		installWindowMenu(window)
		restoreWindowState(window)
		if !window.win.IsVisible() && window.label != mainWindowLabel && isDocumentWindowLabel(window.label) {
			window.win.Show()
			window.win.Focus()
		}
		return nil, nil
	})
	register("activate_document_window", func(call *commandCall, _ noArgs) (any, error) {
		window, err := call.requireWindow()
		if err != nil {
			return nil, err
		}
		if !isDocumentWindowLabel(window.label) {
			return nil, errors.New("当前窗口不是文档窗口")
		}
		window.win.Show()
		window.win.Restore()
		window.win.Focus()
		syncTrayActiveWithWindowVisibility()
		return nil, nil
	})
	register("report_window_title", func(call *commandCall, args struct {
		Title string `json:"title"`
	}) (any, error) {
		if call.window == nil || !isDocumentWindowLabel(call.window.label) {
			return nil, nil
		}
		recordWindowTitle(call.window.label, args.Title)
		return nil, nil
	})
	register("refresh_interface_language_chrome", func(_ *commandCall, _ noArgs) (any, error) {
		applog.Info("Window", "刷新界面语言相关窗口菜单")
		refreshApplicationMenu()
		if settings := windows.get(settingsWindowLabel); settings != nil {
			settings.win.SetTitle(settingsWindowTitle())
		}
		refreshTrayMenu()
		return nil, nil
	})
	register("close_window", func(call *commandCall, args struct {
		Sessions []closingSession `json:"sessions"`
	}) (any, error) {
		window, err := call.requireWindow()
		if err != nil {
			return nil, err
		}
		if err := prepareClose(window.label, args.Sessions); err != nil {
			return nil, err
		}
		applog.Info("Window", "关闭窗口："+window.label)
		if window.label == settingsWindowLabel {
			clearPendingSettingsCloseRequest()
		}
		allowNextClose(window.label)
		window.win.Close()
		return nil, nil
	})
	register("hide_window_to_tray", func(call *commandCall, _ noArgs) (any, error) {
		window, err := call.requireWindow()
		if err != nil {
			return nil, err
		}
		applog.Info("Window", "隐藏窗口到托盘："+window.label)
		window.win.Hide()
		syncTrayActiveWithWindowVisibility()
		refreshTrayMenu()
		return nil, nil
	})
	register("exit_app", func(_ *commandCall, _ noArgs) (any, error) {
		_ = beginExit()
		return nil, nil
	})
	register("request_exit_app", func(_ *commandCall, _ noArgs) (any, error) {
		applog.Info("App", "请求退出应用")
		return nil, beginExit()
	})

	// 以下是页面对自身窗口的操作，对应前端 getCurrentWindow() 的方法。
	register("window_is_fullscreen", func(call *commandCall, _ noArgs) (bool, error) {
		window, err := call.requireWindow()
		if err != nil {
			return false, err
		}
		return window.win.IsFullScreen(), nil
	})
	register("window_is_maximized", func(call *commandCall, _ noArgs) (bool, error) {
		window, err := call.requireWindow()
		if err != nil {
			return false, err
		}
		return window.win.IsMaximized(), nil
	})
	register("window_minimize", func(call *commandCall, _ noArgs) (any, error) {
		window, err := call.requireWindow()
		if err == nil {
			window.win.Minimize()
		}
		return nil, err
	})
	register("window_toggle_maximize", func(call *commandCall, _ noArgs) (any, error) {
		window, err := call.requireWindow()
		if err == nil {
			window.win.ToggleMaximize()
		}
		return nil, err
	})
	register("window_set_title", func(call *commandCall, args struct {
		Title string `json:"title"`
	}) (any, error) {
		window, err := call.requireWindow()
		if err == nil {
			window.win.SetTitle(args.Title)
		}
		return nil, err
	})
	register("window_geometry", func(call *commandCall, _ noArgs) (windowGeometry, error) {
		window, err := call.requireWindow()
		if err != nil {
			return windowGeometry{}, err
		}
		return currentWindowGeometry(window), nil
	})
}

// windowGeometry 是窗口外框位置与内容区尺寸（DIP）。
type windowGeometry struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

func currentWindowGeometry(window *appWindow) windowGeometry {
	x, y := window.win.Position()
	width, height := window.win.ContentSize()
	return windowGeometry{X: x, Y: y, Width: width, Height: height}
}

// delayedRefocus 在 WebView 初始化或异步场景下补一次激活。
func delayedRefocus(window *appWindow) {
	go func() {
		time.Sleep(120 * time.Millisecond)
		if window.win.IsDestroyed() {
			return
		}
		window.win.Show()
		window.win.Restore()
		window.win.Focus()
	}()
}

package main

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/egoist/mygo"

	"nomo/desktop/internal/applog"
)

// 窗口几何的持久化与恢复，以及 Markdown 小窗模式。几何以 DIP 保存在设置 windowState:<label>，
// 移动/缩放时 250ms 合并一次写盘，窗口销毁时同步落盘。

const (
	miniStateLabel          = "window-markdown-mini"
	miniMinWidth            = 320
	miniMinHeight           = 240
	miniDefaultWidth        = 460
	miniDefaultHeight       = 560
	minVisibleSize          = 80
	windowStatePersistDelay = 250 * time.Millisecond
)

// windowStateInput 是保存的窗口几何；字段可缺省以兼容前端只写部分字段。
type windowStateInput struct {
	X         *int  `json:"x"`
	Y         *int  `json:"y"`
	Width     *int  `json:"width"`
	Height    *int  `json:"height"`
	Maximized *bool `json:"maximized"`
}


// miniRuntimeState 记录进入小窗前的正常几何，返回时恢复。同一时刻至多一个窗口处于小窗模式。
type miniRuntimeState struct {
	label         string
	normal        mygo.Rectangle
	wasMaximized  bool
	wasFullScreen bool
}

var windowStateRuntime struct {
	mu      sync.Mutex
	mini    *miniRuntimeState
	pending map[string]windowStateInput
	timer   *time.Timer
}

func init() {
	windowStateRuntime.pending = map[string]windowStateInput{}
	register("update_window_state", func(_ *commandCall, args struct {
		Key   string           `json:"key"`
		Input windowStateInput `json:"input"`
	}) (any, error) {
		return nil, writeWindowState(args.Key, args.Input)
	})
	register("enter_markdown_mini_mode", func(call *commandCall, args struct {
		Pinned bool `json:"pinned"`
	}) (any, error) {
		window, err := call.requireWindow()
		if err != nil {
			return nil, err
		}
		if !isDocumentWindowLabel(window.label) {
			return nil, errors.New("只有文档窗口可以进入 Markdown 小窗模式")
		}
		return nil, enterMarkdownMiniMode(window, args.Pinned)
	})
	register("exit_markdown_mini_mode", func(call *commandCall, _ noArgs) (any, error) {
		window, err := call.requireWindow()
		if err != nil {
			return nil, err
		}
		if !isDocumentWindowLabel(window.label) {
			return nil, errors.New("当前窗口不是文档窗口")
		}
		return nil, exitMarkdownMiniMode(window)
	})
	register("set_markdown_mini_mode_pinned", func(call *commandCall, args struct {
		Pinned bool `json:"pinned"`
	}) (any, error) {
		window, err := call.requireWindow()
		if err != nil {
			return nil, err
		}
		if !isMarkdownMiniModeWindow(window.label) {
			return nil, errors.New("当前窗口未处于 Markdown 小窗模式")
		}
		window.win.SetAlwaysOnTop(args.Pinned)
		return nil, nil
	})
}

func writeWindowState(key string, input windowStateInput) error {
	encoded, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("序列化窗口状态失败：%w", err)
	}
	return updateSettings([]settingInput{{Key: key, ValueJSON: string(encoded)}})
}

func readWindowState(label string) (windowStateInput, bool) {
	value, ok := settingValue("windowState:" + label)
	if !ok {
		return windowStateInput{}, false
	}
	var state windowStateInput
	if json.Unmarshal([]byte(value), &state) != nil {
		return windowStateInput{}, false
	}
	return state, true
}

func captureWindowState(window *appWindow, maximized bool) windowStateInput {
	x, y := window.win.Position()
	width, height := window.win.ContentSize()
	return windowStateInput{X: &x, Y: &y, Width: &width, Height: &height, Maximized: &maximized}
}

// persistWindowStateAfterGeometryChange 合并高频的移动/缩放写盘；小窗模式只在退出或销毁时保存。
func persistWindowStateAfterGeometryChange(window *appWindow) {
	// 设置窗口总以默认尺寸模态打开，不持久化几何。
	if window.label == settingsWindowLabel || isPDFExportWindowLabel(window.label) || isMarkdownMiniModeWindow(window.label) || window.win.IsMinimized() {
		return
	}
	// 最大化与全屏时不覆盖正常尺寸，只更新 maximized 标记。
	state := captureWindowState(window, window.win.IsMaximized())
	if window.win.IsMaximized() || window.win.IsFullScreen() {
		if previous, ok := readWindowState(window.label); ok {
			state.X, state.Y, state.Width, state.Height = previous.X, previous.Y, previous.Width, previous.Height
		}
	}
	windowStateRuntime.mu.Lock()
	windowStateRuntime.pending[window.label] = state
	if windowStateRuntime.timer == nil {
		windowStateRuntime.timer = time.AfterFunc(windowStatePersistDelay, flushPendingWindowStates)
	} else {
		windowStateRuntime.timer.Reset(windowStatePersistDelay)
	}
	windowStateRuntime.mu.Unlock()
}

func flushPendingWindowStates() {
	windowStateRuntime.mu.Lock()
	pending := windowStateRuntime.pending
	windowStateRuntime.pending = map[string]windowStateInput{}
	windowStateRuntime.mu.Unlock()
	for label, state := range pending {
		if err := writeWindowState("windowState:"+label, state); err != nil {
			applog.Warn("Window", fmt.Sprintf("持久化窗口状态失败：label=%s error=%v", label, err))
		}
	}
}

// persistWindowStateOnDestroy 在窗口销毁时同步落盘：小窗保存到小窗专用键，其余保存当前几何。
func persistWindowStateOnDestroy(window *appWindow) {
	windowStateRuntime.mu.Lock()
	state, pending := windowStateRuntime.pending[window.label]
	delete(windowStateRuntime.pending, window.label)
	windowStateRuntime.mu.Unlock()
	if isMarkdownMiniModeWindow(window.label) {
		persistMiniWindowState(window)
		return
	}
	if !pending {
		return
	}
	if err := writeWindowState("windowState:"+window.label, state); err != nil {
		applog.Warn("Window", fmt.Sprintf("持久化窗口状态失败：label=%s error=%v", window.label, err))
	}
}

func persistMiniWindowState(window *appWindow) {
	if err := writeWindowState("windowState:"+miniStateLabel, captureWindowState(window, false)); err != nil {
		applog.Warn("Window", "持久化 Markdown 小窗状态失败："+err.Error())
	}
}

// restoreWindowState 恢复保存的尺寸与位置：旧坐标已不在任何显示器上时居中到主显示器。
func restoreWindowState(window *appWindow) {
	state, ok := readWindowState(window.label)
	if !ok {
		return
	}
	width := max(valueOr(state.Width, documentWindowWidth), documentWindowMinWidth)
	height := max(valueOr(state.Height, documentWindowHeight), documentWindowMinHeight)
	// 旧版本以物理像素保存；尺寸不超过目标显示器工作区，避免高分屏上恢复成两倍大小。
	primary := mygo.Screen.PrimaryDisplay().WorkArea
	if primary.Width > 0 {
		width = min(width, max(primary.Width, documentWindowMinWidth))
		height = min(height, max(primary.Height, documentWindowMinHeight))
	}
	window.win.SetContentSize(width, height)
	if state.X != nil && state.Y != nil && displayFor(mygo.Rectangle{X: *state.X, Y: *state.Y, Width: width, Height: height}) != nil {
		window.win.SetPosition(*state.X, *state.Y)
	} else {
		window.win.Center()
	}
	if state.Maximized != nil && *state.Maximized {
		window.win.Maximize()
	}
}

func valueOr(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

// displayFor 返回与矩形可见交集至少 80×80（或矩形自身大小）的显示器；没有时返回 nil。
func displayFor(rect mygo.Rectangle) *mygo.Display {
	requiredWidth := min(rect.Width, minVisibleSize)
	requiredHeight := min(rect.Height, minVisibleSize)
	for _, display := range mygo.Screen.Displays() {
		bounds := display.Bounds
		visibleWidth := min(rect.X+rect.Width, bounds.X+bounds.Width) - max(rect.X, bounds.X)
		visibleHeight := min(rect.Y+rect.Height, bounds.Y+bounds.Height) - max(rect.Y, bounds.Y)
		if visibleWidth >= requiredWidth && visibleHeight >= requiredHeight {
			return &display
		}
	}
	return nil
}

func isMarkdownMiniModeWindow(label string) bool {
	windowStateRuntime.mu.Lock()
	defer windowStateRuntime.mu.Unlock()
	return windowStateRuntime.mini != nil && windowStateRuntime.mini.label == label
}

func forgetMarkdownMiniModeWindow(label string) {
	windowStateRuntime.mu.Lock()
	if windowStateRuntime.mini != nil && windowStateRuntime.mini.label == label {
		windowStateRuntime.mini = nil
	}
	windowStateRuntime.mu.Unlock()
}

// enterMarkdownMiniMode 把文档窗口缩为置顶小窗：先退出全屏/最大化记录正常边界，再套用小窗几何。
func enterMarkdownMiniMode(window *appWindow, pinned bool) error {
	if isMarkdownMiniModeWindow(window.label) {
		window.win.SetAlwaysOnTop(pinned)
		return nil
	}
	state := &miniRuntimeState{
		label:         window.label,
		wasMaximized:  window.win.IsMaximized(),
		wasFullScreen: window.win.IsFullScreen(),
	}
	windowStateRuntime.mu.Lock()
	if current := windowStateRuntime.mini; current != nil {
		windowStateRuntime.mu.Unlock()
		return fmt.Errorf("窗口 %s 已处于 Markdown 小窗模式", current.label)
	}
	windowStateRuntime.mini = state
	windowStateRuntime.mu.Unlock()

	if state.wasFullScreen {
		window.win.SetFullScreen(false)
	}
	if state.wasMaximized {
		window.win.Unmaximize()
	}
	// 取消最大化后再记录正常边界，返回时才能恢复真正的非最大化尺寸。
	x, y := window.win.Position()
	width, height := window.win.ContentSize()
	state.normal = mygo.Rectangle{X: x, Y: y, Width: width, Height: height}
	window.win.SetMinimumSize(miniMinWidth, miniMinHeight)
	restoreMiniGeometry(window)
	window.win.SetMaximizable(false)
	window.win.SetMinimizable(false)
	window.win.SetAlwaysOnTop(pinned)
	window.win.SetSkipTaskbar(true)
	return nil
}

// restoreMiniGeometry 套用上次的小窗几何；位置失效时居中到窗口当前所在显示器的工作区。
func restoreMiniGeometry(window *appWindow) {
	saved, ok := readWindowState(miniStateLabel)
	width := miniDefaultWidth
	height := miniDefaultHeight
	if ok {
		width = max(valueOr(saved.Width, width), miniMinWidth)
		height = max(valueOr(saved.Height, height), miniMinHeight)
	}
	var display *mygo.Display
	if ok && saved.X != nil && saved.Y != nil {
		display = displayFor(mygo.Rectangle{X: *saved.X, Y: *saved.Y, Width: width, Height: height})
	}
	if display == nil {
		current := mygo.Screen.DisplayMatching(window.win.Bounds())
		display = &current
	}
	work := display.WorkArea
	if work.Width > 0 {
		width = min(width, max(work.Width, miniMinWidth))
		height = min(height, max(work.Height, miniMinHeight))
	}
	x := work.X + (work.Width-width)/2
	y := work.Y + (work.Height-height)/2
	if ok && saved.X != nil && saved.Y != nil {
		x = clamp(*saved.X, work.X, work.X+work.Width-width)
		y = clamp(*saved.Y, work.Y, work.Y+work.Height-height)
	}
	window.win.SetContentSize(width, height)
	window.win.SetPosition(x, y)
}

func clamp(value, low, high int) int {
	if high < low {
		return low
	}
	return min(max(value, low), high)
}

// exitMarkdownMiniMode 保存小窗几何并恢复进入前的正常窗口状态。
func exitMarkdownMiniMode(window *appWindow) error {
	windowStateRuntime.mu.Lock()
	state := windowStateRuntime.mini
	windowStateRuntime.mu.Unlock()
	if state == nil {
		return nil
	}
	if state.label != window.label {
		return errors.New("当前窗口不是正在使用的 Markdown 小窗")
	}
	persistMiniWindowState(window)
	window.win.SetAlwaysOnTop(false)
	window.win.SetMaximizable(true)
	window.win.SetMinimizable(true)
	if window.win.IsFullScreen() {
		window.win.SetFullScreen(false)
	}
	if window.win.IsMaximized() {
		window.win.Unmaximize()
	}
	window.win.SetContentSize(state.normal.Width, state.normal.Height)
	window.win.SetPosition(state.normal.X, state.normal.Y)
	// 先恢复最终边界再提高最小尺寸，避免小窗原地先被撑大一次。
	window.win.SetMinimumSize(documentWindowMinWidth, documentWindowMinHeight)
	if state.wasMaximized {
		window.win.Maximize()
	}
	if state.wasFullScreen {
		window.win.SetFullScreen(true)
	}
	window.win.SetSkipTaskbar(false)
	forgetMarkdownMiniModeWindow(window.label)
	return nil
}

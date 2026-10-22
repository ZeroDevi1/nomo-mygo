package main

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/i18n"
)

// 偏好设置窗口的关闭握手：关闭前先请求页面保存（nomo://settings-request-close），页面确认后再关闭；
// 8 秒内无响应则回退为直接关闭。关闭其所属文档窗口或退出应用时，先等设置窗口保存完成。

const settingsWindowLabel = "window-settings"

// deferredSettingsAction 是设置窗口关闭后要继续执行的动作：关闭 owner 窗口或退出应用。
type deferredSettingsAction struct {
	closeOwner string
	exitApp    bool
}

var settingsState struct {
	handlerReady    atomic.Bool
	requestSequence atomic.Uint64
	pendingRequest  atomic.Uint64
	acknowledged    atomic.Uint64
	mu              sync.Mutex
	ownerLabel      string
	deferred        *deferredSettingsAction
}

// forceCloseLabels 记录下一次关闭请求应直接放行的窗口（页面已确认或由 Go 发起的关闭）。
var forceClose struct {
	mu     sync.Mutex
	labels map[string]bool
}

func init() {
	forceClose.labels = map[string]bool{}
	register("open_settings_window", func(_ *commandCall, _ noArgs) (any, error) {
		return nil, openSettingsWindow()
	})
	register("mark_settings_close_handler_ready", func(call *commandCall, _ noArgs) (any, error) {
		if call.window == nil || call.window.label != settingsWindowLabel {
			return nil, errors.New("只有偏好设置窗口可以注册关闭处理器")
		}
		settingsState.handlerReady.Store(true)
		return nil, nil
	})
	register("cancel_settings_close_request", func(call *commandCall, args struct {
		RequestID uint64 `json:"requestId"`
	}) (any, error) {
		if call.window == nil || call.window.label != settingsWindowLabel {
			return nil, errors.New("只有偏好设置窗口可以取消关闭请求")
		}
		if settingsState.pendingRequest.CompareAndSwap(args.RequestID, 0) {
			settingsState.acknowledged.Store(0)
			cancelExit("exitSaveFailed")
			if action := takeDeferredSettingsAction(); action != nil && action.closeOwner != "" {
				clearNextClose(action.closeOwner)
			}
		}
		return nil, nil
	})
	register("acknowledge_settings_close_request", func(call *commandCall, args struct {
		RequestID uint64 `json:"requestId"`
	}) (any, error) {
		if call.window == nil || call.window.label != settingsWindowLabel {
			return nil, errors.New("只有偏好设置窗口可以确认关闭请求")
		}
		if settingsState.pendingRequest.Load() == args.RequestID {
			settingsState.acknowledged.Store(args.RequestID)
		}
		return nil, nil
	})
}

// openSettingsWindow 打开或前置偏好设置窗口；Windows 上以当前聚焦的文档窗口为 owner。
func openSettingsWindow() error {
	if window := windows.get(settingsWindowLabel); window != nil {
		applog.Info("Settings", "设置窗口已存在，将其前置")
		bringSettingsToFront(window)
		return nil
	}
	settingsState.handlerReady.Store(false)
	clearPendingSettingsCloseRequest()
	rememberSettingsOwner("")
	owner := settingsOwnerWindow()
	window, err := createSettingsWindow(owner)
	if err != nil {
		return err
	}
	if owner != nil {
		rememberSettingsOwner(owner.label)
	}
	// 设置窗口总以默认小尺寸打开，不恢复历史几何。
	window.win.Show()
	window.win.Focus()
	return nil
}

func settingsWindowTitle() string { return i18n.Text(effectiveLocale(), "settings_window_title") }

// bringSettingsToFront 前置已打开的设置窗口。模态窗口不能再次 Show（macOS 会把 sheet
// 重新挂成普通子窗口），只恢复其父窗口并聚焦。
func bringSettingsToFront(window *appWindow) {
	if parent := window.win.Parent(); parent != nil {
		parent.Restore()
		parent.Focus()
	} else {
		window.win.Show()
		window.win.Restore()
	}
	window.win.Focus()
}

func rememberSettingsOwner(label string) {
	settingsState.mu.Lock()
	settingsState.ownerLabel = label
	settingsState.mu.Unlock()
}

func forgetSettingsOwner() { rememberSettingsOwner("") }

func beginSettingsCloseRequest() (uint64, bool) {
	requestID := max(settingsState.requestSequence.Add(1), 1)
	if !settingsState.pendingRequest.CompareAndSwap(0, requestID) {
		return 0, false
	}
	settingsState.acknowledged.Store(0)
	return requestID, true
}

func clearPendingSettingsCloseRequest() {
	settingsState.pendingRequest.Store(0)
	settingsState.acknowledged.Store(0)
}

// scheduleSettingsCloseFallback 在页面 8 秒内未确认保存时直接关闭设置窗口。
func scheduleSettingsCloseFallback(window *appWindow, requestID uint64) {
	go func() {
		time.Sleep(8 * time.Second)
		if isExiting() || settingsState.acknowledged.Load() == requestID {
			return
		}
		if !settingsState.pendingRequest.CompareAndSwap(requestID, 0) {
			return
		}
		settingsState.handlerReady.Store(false)
		applog.Warn("Settings", "前端未在限定时间内完成设置窗口关闭，回退为直接关闭")
		allowNextClose(window.label)
		window.win.Close()
	}()
}

// requestSettingsCloseBeforeOwner 在关闭设置窗口的 owner 前先关闭设置窗口；返回 true 表示已延后 owner 的关闭。
func requestSettingsCloseBeforeOwner(ownerLabel string) (bool, error) {
	settingsState.mu.Lock()
	isOwner := settingsState.ownerLabel != "" && settingsState.ownerLabel == ownerLabel
	settingsState.mu.Unlock()
	if !isOwner {
		return false, nil
	}
	settings := windows.get(settingsWindowLabel)
	if settings == nil {
		forgetSettingsOwner()
		return false, nil
	}
	if !settingsState.handlerReady.Load() {
		return false, errors.New("偏好设置窗口尚未准备好保存")
	}
	settingsState.mu.Lock()
	shouldRequest := settingsState.deferred == nil
	if shouldRequest {
		settingsState.deferred = &deferredSettingsAction{closeOwner: ownerLabel}
	}
	settingsState.mu.Unlock()
	if shouldRequest {
		settings.win.Close()
	}
	return true, nil
}

// requestAppExitAfterSettings 在退出前先让设置窗口保存；返回 true 表示退出将在设置窗口关闭后继续。
func requestAppExitAfterSettings() (bool, error) {
	settings := windows.get(settingsWindowLabel)
	if settings == nil {
		return false, nil
	}
	if !settingsState.handlerReady.Load() {
		return false, errors.New("偏好设置窗口尚未准备好保存")
	}
	settingsState.mu.Lock()
	shouldRequest := settingsState.deferred == nil
	settingsState.deferred = &deferredSettingsAction{exitApp: true}
	settingsState.mu.Unlock()
	if shouldRequest {
		settings.win.Close()
	}
	return true, nil
}

func takeDeferredSettingsAction() *deferredSettingsAction {
	settingsState.mu.Lock()
	defer settingsState.mu.Unlock()
	action := settingsState.deferred
	settingsState.deferred = nil
	return action
}

// settingsWindowDestroyed 在设置窗口销毁后复位握手状态，并继续执行被延后的 owner 关闭或退出。
func settingsWindowDestroyed() {
	settingsState.handlerReady.Store(false)
	clearPendingSettingsCloseRequest()
	forgetSettingsOwner()
	action := takeDeferredSettingsAction()
	switch {
	case action == nil:
	case action.closeOwner != "":
		if owner := windows.get(action.closeOwner); owner != nil {
			allowNextClose(owner.label)
			owner.win.Close()
		}
	case action.exitApp:
		if err := finishExit(); err != nil {
			applog.Error("Workspace", err.Error())
		}
	}
}

func allowNextClose(label string) {
	forceClose.mu.Lock()
	forceClose.labels[label] = true
	forceClose.mu.Unlock()
}

func consumeNextClose(label string) bool {
	forceClose.mu.Lock()
	defer forceClose.mu.Unlock()
	allowed := forceClose.labels[label]
	delete(forceClose.labels, label)
	return allowed
}

func clearNextClose(label string) {
	forgetClose(label)
	forceClose.mu.Lock()
	delete(forceClose.labels, label)
	forceClose.mu.Unlock()
}

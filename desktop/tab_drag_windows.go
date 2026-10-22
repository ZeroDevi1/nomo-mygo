//go:build windows

package main

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

const nativeTabDragSupported = true

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procGetCursorPos     = user32.NewProc("GetCursorPos")
	procGetAsyncKeyState = user32.NewProc("GetAsyncKeyState")
	procWindowFromPoint  = user32.NewProc("WindowFromPoint")
	procGetAncestor      = user32.NewProc("GetAncestor")
	procClientToScreen   = user32.NewProc("ClientToScreen")
	procGetDpiForWindow  = user32.NewProc("GetDpiForWindow")
	procIsWindowVisible  = user32.NewProc("IsWindowVisible")
	procIsIconic         = user32.NewProc("IsIconic")
)

const (
	vkLButton = 0x01
	vkEscape  = 0x1B
	gaRoot    = 2
)

type win32Point struct{ X, Y int32 }

func cursorPosition() (win32Point, bool) {
	var point win32Point
	ok, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&point)))
	return point, ok != 0
}

func keyPressed(key uintptr) bool {
	state, _, _ := procGetAsyncKeyState.Call(key)
	return int16(state) < 0
}

// rootWindowAt 返回屏幕点下的顶层窗口句柄，用于排除被其他程序遮挡的文档窗口。
func rootWindowAt(point win32Point) uintptr {
	packed := uintptr(uint32(point.X)) | uintptr(uint32(point.Y))<<32
	hwnd, _, _ := procWindowFromPoint.Call(packed)
	if hwnd == 0 {
		return 0
	}
	root, _, _ := procGetAncestor.Call(hwnd, gaRoot)
	return root
}

// clientOriginAndScale 返回窗口客户区原点（物理像素）与 DPI 缩放。
func clientOriginAndScale(hwnd uintptr) (win32Point, float64) {
	var origin win32Point
	procClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&origin)))
	dpi, _, _ := procGetDpiForWindow.Call(hwnd)
	scale := 1.0
	if dpi > 0 {
		scale = float64(dpi) / 96
	}
	return origin, scale
}

// runNativeTabDrag 以约 60Hz 轮询光标与按键，直到松开（drop）、Esc 或源窗口消失（cancel）。
func runNativeTabDrag(source string, input dragInput) {
	defer endTabDrag(source, input.DragID)
	var lastKey string
	for {
		point, cursorOK := cursorPosition()
		escape := keyPressed(vkEscape)
		pressed := keyPressed(vkLButton)
		sourceWindow := windows.get(source)
		cancelled := !tabDragAlive(source, input.DragID) || !cursorOK || escape || sourceWindow == nil
		var targetLabel *string
		var placement *dropPlacement
		if !cancelled {
			targetLabel, placement = hitTestTabDrag(input, point)
		}
		phase := "move"
		switch {
		case cancelled:
			phase = "cancel"
		case !pressed:
			phase = "drop"
		}
		key := phaseKey(point, targetLabel, placement)
		if phase != "move" || key != lastKey {
			event := nativeTabDragEvent{
				DragID: input.DragID, ItemID: input.ItemID, TabID: input.TabID, CanCombine: input.CanCombine,
				Phase: phase, SourceWindowLabel: source, TargetWindowLabel: targetLabel, Placement: placement,
				ScreenPosition: screenPoint{X: int(point.X), Y: int(point.Y)},
			}
			if sourceWindow != nil {
				// 拖影使用源页面的逻辑坐标，捕获丢失后也能隐藏或继续跟随。
				origin, scale := clientOriginAndScale(sourceWindow.win.NativeHandle())
				event.SourceClientPosition = &logicalPosition{X: float64(point.X-origin.X) / scale, Y: float64(point.Y-origin.Y) / scale}
			}
			for _, window := range windows.documentWindows() {
				broadcastTo("nomo://native-tab-drag", event, window.label)
			}
			lastKey = key
		}
		if phase != "move" {
			return
		}
		time.Sleep(16 * time.Millisecond)
	}
}

// phaseKey 是光标位置与命中结果的去重键；只有变化时才发送 move 事件。
func phaseKey(point win32Point, target *string, placement *dropPlacement) string {
	key := fmt.Sprintf("%d,%d", point.X, point.Y)
	if target != nil {
		key += "|" + *target
	}
	if placement != nil {
		key += fmt.Sprintf("|%s|%s", placement.Kind, placement.TargetItemID)
		if placement.InsertionIndex != nil {
			key += fmt.Sprintf("|%d", *placement.InsertionIndex)
		}
	}
	return key
}

func hitTestTabDrag(input dragInput, point win32Point) (*string, *dropPlacement) {
	top := rootWindowAt(point)
	if top == 0 {
		return nil, nil
	}
	for _, window := range windows.documentWindows() {
		if isMarkdownMiniModeWindow(window.label) {
			continue
		}
		hwnd := window.win.NativeHandle()
		if hwnd != top {
			continue
		}
		if visible, _, _ := procIsWindowVisible.Call(hwnd); visible == 0 {
			continue
		}
		if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
			continue
		}
		origin, scale := clientOriginAndScale(hwnd)
		placement, registered := hitZones(window.label, input, float64(point.X-origin.X)/scale, float64(point.Y-origin.Y)/scale)
		if !registered {
			continue
		}
		label := window.label
		return &label, placement
	}
	return nil, nil
}

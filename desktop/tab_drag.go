package main

import (
	"errors"
	"math"
	"sync"
)

// Windows 标签 Pointer 手势的屏幕命中：页面登记标签栏落点矩形，拖动开始后由 Go 轮询光标，
// 判断落在哪个窗口的哪个落点，并通过 nomo://native-tab-drag 通知所有文档窗口。

type dropZone struct {
	ID        string        `json:"id"`
	X         float64       `json:"x"`
	Y         float64       `json:"y"`
	Width     float64       `json:"width"`
	Height    float64       `json:"height"`
	Placement dropPlacement `json:"placement"`
}

type dragInput struct {
	DragID     string  `json:"dragId"`
	ItemID     string  `json:"itemId"`
	TabID      *string `json:"tabId"`
	CanCombine bool    `json:"canCombine"`
}

type logicalPosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// nativeTabDragEvent 展开 dragInput 的字段并附带拖动阶段与命中结果。
type nativeTabDragEvent struct {
	DragID               string           `json:"dragId"`
	ItemID               string           `json:"itemId"`
	TabID                *string          `json:"tabId"`
	CanCombine           bool             `json:"canCombine"`
	Phase                string           `json:"phase"`
	SourceWindowLabel    string           `json:"sourceWindowLabel"`
	TargetWindowLabel    *string          `json:"targetWindowLabel"`
	Placement            *dropPlacement   `json:"placement"`
	ScreenPosition       screenPoint      `json:"screenPosition"`
	SourceClientPosition *logicalPosition `json:"sourceClientPosition"`
}

type registeredZones struct {
	ready bool
	zones []dropZone
}

var tabDrag struct {
	mu       sync.Mutex
	windows  map[string]registeredZones
	active   bool
	activeOf [2]string // 源窗口标签与拖动 ID
}

func init() {
	tabDrag.windows = map[string]registeredZones{}
	register("register_tab_drop_zones", func(call *commandCall, args struct {
		Zones []dropZone `json:"zones"`
		Ready bool       `json:"ready"`
	}) (any, error) {
		window, err := call.requireWindow()
		if err != nil {
			return nil, err
		}
		if !isDocumentWindowLabel(window.label) {
			return nil, errors.New("非文档窗口不能注册标签落点")
		}
		if len(args.Zones) > 1000 {
			return nil, errors.New("标签落点矩形无效")
		}
		for _, zone := range args.Zones {
			finite := !math.IsNaN(zone.X+zone.Y+zone.Width+zone.Height) && !math.IsInf(zone.X+zone.Y+zone.Width+zone.Height, 0)
			if zone.ID == "" || !finite || zone.Width < 0 || zone.Height < 0 {
				return nil, errors.New("标签落点矩形无效")
			}
		}
		tabDrag.mu.Lock()
		tabDrag.windows[window.label] = registeredZones{ready: args.Ready, zones: args.Zones}
		tabDrag.mu.Unlock()
		return nil, nil
	})
	register("cancel_native_tab_drag", func(call *commandCall, args struct {
		DragID string `json:"dragId"`
	}) (any, error) {
		window, err := call.requireWindow()
		if err != nil {
			return nil, err
		}
		tabDrag.mu.Lock()
		if tabDrag.active && tabDrag.activeOf == [2]string{window.label, args.DragID} {
			tabDrag.active = false
		}
		tabDrag.mu.Unlock()
		return nil, nil
	})
	register("begin_native_tab_drag", func(call *commandCall, args struct {
		Input dragInput `json:"input"`
	}) (any, error) {
		window, err := call.requireWindow()
		if err != nil {
			return nil, err
		}
		if !nativeTabDragSupported {
			return nil, errors.New("跨窗口标签拖动仅支持 Windows")
		}
		if !isDocumentWindowLabel(window.label) || isMarkdownMiniModeWindow(window.label) {
			return nil, errors.New("当前窗口不支持标签拖动")
		}
		if args.Input.DragID == "" || args.Input.ItemID == "" {
			return nil, errors.New("拖动项无效")
		}
		tabDrag.mu.Lock()
		if tabDrag.active {
			tabDrag.mu.Unlock()
			return nil, errors.New("已有标签拖动正在进行")
		}
		tabDrag.active = true
		tabDrag.activeOf = [2]string{window.label, args.Input.DragID}
		tabDrag.mu.Unlock()
		go runNativeTabDrag(window.label, args.Input)
		return nil, nil
	})
}

func tabDragWindowReady(label string) bool {
	tabDrag.mu.Lock()
	defer tabDrag.mu.Unlock()
	return tabDrag.windows[label].ready
}

func tabDragAlive(source, dragID string) bool {
	tabDrag.mu.Lock()
	defer tabDrag.mu.Unlock()
	return tabDrag.active && tabDrag.activeOf == [2]string{source, dragID}
}

func endTabDrag(source, dragID string) {
	tabDrag.mu.Lock()
	if tabDrag.active && tabDrag.activeOf == [2]string{source, dragID} {
		tabDrag.active = false
	}
	tabDrag.mu.Unlock()
}

func forgetTabDragWindow(label string) {
	tabDrag.mu.Lock()
	delete(tabDrag.windows, label)
	if tabDrag.active && tabDrag.activeOf[0] == label {
		tabDrag.active = false
	}
	tabDrag.mu.Unlock()
}

// hitZones 在窗口客户区逻辑坐标中命中落点；未命中标签栏时返回 nil（正文区不当作新建窗口）。
func hitZones(label string, input dragInput, clientX, clientY float64) (*dropPlacement, bool) {
	tabDrag.mu.Lock()
	zones, ok := tabDrag.windows[label]
	tabDrag.mu.Unlock()
	if !ok || !zones.ready {
		return nil, false
	}
	for _, zone := range zones.zones {
		if zone.Placement.Kind == "combine" && !input.CanCombine {
			continue
		}
		if clientX >= zone.X && clientX <= zone.X+zone.Width && clientY >= zone.Y && clientY <= zone.Y+zone.Height {
			placement := zone.Placement
			return &placement, true
		}
	}
	return nil, true
}

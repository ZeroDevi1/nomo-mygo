package main

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"sync"

	"github.com/egoist/mygo"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/textdoc"
)

// Bridge 是前端 invoke 的唯一入口：按命令名分发到类型化的 Go 处理函数。
//
// 前端沿用原有的 snake_case 命令名与 camelCase 参数对象（Tauri invoke 约定），
// 因此调用点无需逐个改写；每个命令的参数与返回值仍由 Go 结构体定义。
type Bridge struct{}

// InvokeResult 是一次调用的结果信封：成功时带 value，失败时带 error。
// error 可能是字符串（普通错误）或对象（分段文档的结构化错误，含 code），
// 与 Tauri invoke 拒绝时的取值一致，前端按原逻辑区分。
type InvokeResult struct {
	OK    bool           `json:"ok"`
	Value jsontext.Value `json:"value,omitzero"`
	Error jsontext.Value `json:"error,omitzero"`
}

// Invoke 执行 command，args 为调用参数对象（可为空）。调用在独立 goroutine 上运行，可以阻塞。
func (Bridge) Invoke(ctx context.Context, command string, args jsontext.Value) InvokeResult {
	handler, ok := lookupCommand(command)
	if !ok {
		return errorResult(fmt.Errorf("未知命令：%s", command))
	}
	if len(args) == 0 || string(args) == "null" {
		args = jsontext.Value("{}")
	}
	call := &commandCall{ctx: ctx, window: callerWindow(ctx)}
	value, err := handler(call, args)
	if err != nil {
		return errorResult(err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return errorResult(fmt.Errorf("序列化 %s 结果失败：%w", command, err))
	}
	return InvokeResult{OK: true, Value: encoded}
}

// Emit 把页面发出的事件转发给所有页面；target 非空时附带目标窗口标签，由接收方按需过滤。
// 对应前端 emit / emitTo，与 Tauri v2 一致：全局 listen 收到所有事件。
func (Bridge) Emit(ctx context.Context, event string, payload jsontext.Value, target string) {
	source := ""
	if window := callerWindow(ctx); window != nil {
		source = window.label
	}
	broadcastRaw(event, payload, target, source)
}

func errorResult(err error) InvokeResult {
	var structured *textdoc.Error
	var value any = err.Error()
	if errors.As(err, &structured) {
		value = structured
	}
	encoded, marshalErr := json.Marshal(value)
	if marshalErr != nil {
		encoded, _ = json.Marshal(err.Error())
	}
	return InvokeResult{Error: encoded}
}

// commandCall 是一次命令调用的上下文；window 为发起调用的窗口，非应用窗口时为 nil。
type commandCall struct {
	ctx    context.Context
	window *appWindow
}

// requireWindow 返回调用窗口；从非应用页面调用时报错。
func (c *commandCall) requireWindow() (*appWindow, error) {
	if c.window == nil {
		return nil, errors.New("命令必须从应用窗口调用")
	}
	return c.window, nil
}

type commandHandler func(call *commandCall, args jsontext.Value) (any, error)

var (
	commandsMu sync.RWMutex
	commands   = map[string]commandHandler{}
)

// register 注册命令：args 解码为 A（缺失字段为零值、未知字段忽略），返回值 R 编码为 JSON。
func register[A any, R any](name string, fn func(call *commandCall, args A) (R, error)) {
	commandsMu.Lock()
	defer commandsMu.Unlock()
	if _, exists := commands[name]; exists {
		panic("重复注册命令：" + name)
	}
	commands[name] = func(call *commandCall, raw jsontext.Value) (any, error) {
		var args A
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("命令 %s 参数无效：%w", name, err)
		}
		return fn(call, args)
	}
}

func lookupCommand(name string) (commandHandler, bool) {
	commandsMu.RLock()
	defer commandsMu.RUnlock()
	handler, ok := commands[name]
	return handler, ok
}

// noArgs 用于不需要参数的命令。
type noArgs struct{}

// DesktopEvent 是推给页面的事件；Target 为空表示不限定窗口，Source 为发出事件的窗口标签。
type DesktopEvent struct {
	Event   string         `json:"event"`
	Payload jsontext.Value `json:"payload"`
	Target  string         `json:"target,omitzero"`
	Source  string         `json:"source,omitzero"`
}

// desktopEvent 是唯一的 Go→页面事件通道，前端 listen 按 Event 名分发。
var desktopEvent = mygo.NewEvent[DesktopEvent]("nomo:event")

// broadcast 编码 payload 并发给所有页面。
func broadcast(event string, payload any) {
	broadcastTo(event, payload, "")
}

// broadcastTo 发给所有页面并标注目标窗口（对应 Tauri emit_to：全局监听同样收到）。
func broadcastTo(event string, payload any, target string) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		applog.Warn("Event", fmt.Sprintf("序列化事件 %s 失败：%v", event, err))
		return
	}
	broadcastRaw(event, encoded, target, "")
}

func broadcastRaw(event string, payload jsontext.Value, target, source string) {
	if len(payload) == 0 {
		payload = jsontext.Value("null")
	}
	if err := desktopEvent.Broadcast(DesktopEvent{Event: event, Payload: payload, Target: target, Source: source}); err != nil {
		applog.Warn("Event", fmt.Sprintf("广播事件 %s 失败：%v", event, err))
	}
}

// emitToWindow 只把事件发给指定窗口的页面；用于明确属于单个窗口的事件（如菜单命令）。
func emitToWindow(window *appWindow, event string, payload any) error {
	if window == nil {
		return errors.New("目标窗口不存在")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return desktopEvent.Emit(window.win, DesktopEvent{Event: event, Payload: encoded, Target: window.label})
}

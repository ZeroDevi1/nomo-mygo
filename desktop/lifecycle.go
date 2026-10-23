package main

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/config"
	"nomo/desktop/internal/memtrim"
)

// 启动快照与多窗口退出事务。普通自动保存不改变下次启动目标；只有窗口真正关闭或退出提交时，
// 才把对应窗口的工作区写入 startupWorkspace。

const (
	startupWorkspaceKey = "startupWorkspace"
	exitRequestTimeout  = 30 * time.Second
)

// closingSession 是窗口关闭时需要释放的分段文档会话。
type closingSession struct {
	SessionID      string `json:"sessionId"`
	DiscardChanges bool   `json:"discardChanges"`
}

type closingWindow struct {
	snapshot string
	sessions []closingSession
}

// exitRequest 是一次进行中的退出：pending 为尚未确认的文档窗口，target 的快照成为下次启动工作区。
type exitRequest struct {
	id              uint64
	pending         map[string]bool
	participants    map[string]bool
	target          string
	snapshot        *string
	waitingSettings bool
	committing      bool
	sessions        []closingSession
}

var lifecycle struct {
	mu       sync.Mutex
	closing  map[string]closingWindow
	exit     *exitRequest
	sequence atomic.Uint64
}

func init() {
	lifecycle.closing = map[string]closingWindow{}
	register("acknowledge_workspace_exit", func(call *commandCall, args struct {
		RequestID uint64           `json:"requestId"`
		Approved  bool             `json:"approved"`
		Sessions  []closingSession `json:"sessions"`
	}) (any, error) {
		window, err := call.requireWindow()
		if err != nil {
			return nil, err
		}
		return nil, acknowledgeWorkspaceExit(window.label, args.RequestID, args.Approved, args.Sessions)
	})
}

// releaseSessions 只在窗口实际销毁或退出已提交时释放会话；取消时保留完整编辑能力。
func releaseSessions(sessions []closingSession) {
	for _, session := range sessions {
		if err := app.documents.CloseSession(session.SessionID, session.DiscardChanges); err != nil {
			applog.Error("Workspace", "关闭分段会话失败："+err.Error())
		}
	}
	// 分段会话只为大文档创建，关闭后归还其占用的堆内存。
	if len(sessions) > 0 {
		memtrim.Request()
	}
}

// committedExit 在进程退出前释放已提交退出事务中收集的会话。
func committedExit() {
	lifecycle.mu.Lock()
	var sessions []closingSession
	if lifecycle.exit != nil && lifecycle.exit.committing {
		sessions = lifecycle.exit.sessions
		lifecycle.exit.sessions = nil
	}
	lifecycle.mu.Unlock()
	releaseSessions(sessions)
}

func isExiting() bool {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	return lifecycle.exit != nil
}

func isExitCommitting() bool {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	return lifecycle.exit != nil && lifecycle.exit.committing
}

// waitUntilIdle 阻塞到当前退出事务结束（取消）；用于创建窗口、路由外部打开等不能与退出交错的操作。
func waitUntilIdle() {
	for isExiting() {
		time.Sleep(100 * time.Millisecond)
	}
}

func workspaceSnapshot(label string) (string, error) {
	value, ok := settingValue("workspaceTabs:" + label)
	if !ok {
		return "", fmt.Errorf("窗口工作区尚未保存：%s", label)
	}
	return value, nil
}

func saveStartupWorkspace(value string) error {
	return app.config.Update(func(c *config.AppConfig) {
		c.App.Settings[startupWorkspaceKey] = config.SettingRecord{Key: startupWorkspaceKey, ValueJSON: value, UpdatedAt: config.NowTS()}
	})
}

// prepareClose 记录文档窗口关闭时的工作区快照与待释放会话，真正销毁时再提交。
func prepareClose(label string, sessions []closingSession) error {
	if !isDocumentWindowLabel(label) {
		return nil
	}
	if isExiting() {
		return errors.New("正在等待所有窗口完成退出")
	}
	value, err := workspaceSnapshot(label)
	if err != nil {
		return err
	}
	lifecycle.mu.Lock()
	lifecycle.closing[label] = closingWindow{snapshot: value, sessions: sessions}
	lifecycle.mu.Unlock()
	return nil
}

func forgetClose(label string) {
	lifecycle.mu.Lock()
	delete(lifecycle.closing, label)
	lifecycle.mu.Unlock()
}

// windowDestroyed 在窗口销毁后提交其关闭快照；退出事务中意外丢失参与窗口时取消退出。
func windowDestroyed(label string) {
	lifecycle.mu.Lock()
	if lifecycle.exit != nil && lifecycle.exit.committing {
		lifecycle.mu.Unlock()
		return
	}
	unexpected := lifecycle.exit != nil && isDocumentWindowLabel(label) && lifecycle.exit.participants[label]
	closing, hadClosing := lifecycle.closing[label]
	delete(lifecycle.closing, label)
	lifecycle.mu.Unlock()
	if unexpected {
		cancelExit("exitWindowLost")
	}
	if hadClosing {
		releaseSessions(closing.sessions)
		if err := saveStartupWorkspace(closing.snapshot); err != nil {
			applog.Error("Workspace", err.Error())
			broadcast("nomo://workspace-error", err.Error())
		}
	}
}

// beginExit 向所有文档窗口发起退出确认；每个窗口保存后 acknowledge，全部确认后退出进程。
func beginExit() error {
	pending := map[string]bool{}
	for _, window := range windows.documentWindows() {
		pending[window.label] = true
	}
	target := lastActiveDocumentWindowLabel()
	if !pending[target] {
		labels := make([]string, 0, len(pending))
		for label := range pending {
			labels = append(labels, label)
		}
		sort.Strings(labels)
		target = ""
		if len(labels) > 0 {
			target = labels[0]
		}
	}
	id := lifecycle.sequence.Add(1)
	lifecycle.mu.Lock()
	if lifecycle.exit != nil {
		lifecycle.mu.Unlock()
		return nil
	}
	participants := map[string]bool{}
	recipients := make([]string, 0, len(pending))
	for label := range pending {
		participants[label] = true
		recipients = append(recipients, label)
	}
	lifecycle.exit = &exitRequest{id: id, pending: pending, participants: participants, target: target}
	lifecycle.mu.Unlock()
	for _, label := range recipients {
		broadcastTo("nomo://request-exit-app", id, label)
	}
	go func() {
		time.Sleep(exitRequestTimeout)
		cancelExitRequest(&id, "exitTimedOut")
	}()
	return completeExitIfReady()
}

func acknowledgeWorkspaceExit(label string, requestID uint64, approved bool, sessions []closingSession) error {
	lifecycle.mu.Lock()
	active := lifecycle.exit != nil && lifecycle.exit.id == requestID && lifecycle.exit.pending[label]
	lifecycle.mu.Unlock()
	if !active {
		return nil
	}
	if !approved {
		cancelExitRequest(&requestID, "exitCancelled")
		return nil
	}
	value, err := workspaceSnapshot(label)
	if err != nil {
		cancelExitRequest(&requestID, "exitSaveFailed")
		return err
	}
	lifecycle.mu.Lock()
	exit := lifecycle.exit
	if exit == nil || exit.id != requestID || !exit.pending[label] {
		lifecycle.mu.Unlock()
		return nil
	}
	delete(exit.pending, label)
	exit.sessions = append(exit.sessions, sessions...)
	if exit.target == label {
		exit.snapshot = &value
	}
	lifecycle.mu.Unlock()
	return completeExitIfReady()
}

func completeExitIfReady() error {
	lifecycle.mu.Lock()
	exit := lifecycle.exit
	if exit == nil || len(exit.pending) > 0 || exit.waitingSettings {
		lifecycle.mu.Unlock()
		return nil
	}
	exit.waitingSettings = true
	lifecycle.mu.Unlock()
	deferred, err := requestAppExitAfterSettings()
	if err != nil {
		cancelExit("exitSaveFailed")
		return err
	}
	if deferred {
		return nil
	}
	return finishExit()
}

// finishExit 在所有窗口确认、设置窗口也已保存后写入启动快照并退出进程。
func finishExit() error {
	lifecycle.mu.Lock()
	exit := lifecycle.exit
	if exit == nil || len(exit.pending) > 0 || !exit.waitingSettings {
		lifecycle.mu.Unlock()
		return nil
	}
	alive := map[string]bool{}
	for _, window := range windows.documentWindows() {
		alive[window.label] = true
	}
	if !sameLabelSet(alive, exit.participants) {
		lifecycle.mu.Unlock()
		cancelExit("exitWindowLost")
		return errors.New("退出期间窗口列表发生变化")
	}
	if exit.snapshot != nil {
		if err := saveStartupWorkspace(*exit.snapshot); err != nil {
			lifecycle.mu.Unlock()
			cancelExit("exitSaveFailed")
			return err
		}
	}
	// 保持退出状态直到进程结束，避免窗口销毁事件覆盖选定的启动快照。
	exit.committing = true
	lifecycle.mu.Unlock()
	go func() {
		committedExit()
		destroyAppTray()
		mygo.App.Exit(0)
	}()
	return nil
}

func cancelExit(reason string) { cancelExitRequest(nil, reason) }

func cancelExitRequest(id *uint64, reason string) {
	lifecycle.mu.Lock()
	var cancelled *uint64
	if exit := lifecycle.exit; exit != nil && !exit.committing && (id == nil || *id == exit.id) {
		value := exit.id
		cancelled = &value
		lifecycle.exit = nil
	}
	lifecycle.mu.Unlock()
	if cancelled != nil {
		takeDeferredSettingsAction()
		broadcast("nomo://exit-cancelled", map[string]any{"requestId": *cancelled, "reason": reason})
	}
}

func sameLabelSet(left, right map[string]bool) bool {
	if len(left) != len(right) {
		return false
	}
	for label := range left {
		if !right[label] {
			return false
		}
	}
	return true
}

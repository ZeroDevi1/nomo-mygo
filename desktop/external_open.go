package main

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/config"
	"nomo/desktop/internal/docfs"
)

// 外部打开：命令行参数、系统文件关联、单实例转发与 macOS 打开事件。请求先写入
// pendingExternalOpen:<label> / pendingFolder:<label>，再通知目标窗口；窗口启动时也会读取这些待办。

const (
	openFolderEvent              = "nomo://open-folder"
	pendingExternalOpenPrefix    = "pendingExternalOpen:"
	pendingExternalFolderPrefix  = "pendingFolder:"
	externalOpenBatchQuiet       = 150 * time.Millisecond
	externalOpenBatchMaxDuration = time.Second
)

type externalOpenTargets struct {
	markdownPaths []string
	folderPaths   []string
}

type externalFolderOpenPayload struct {
	WindowLabel string `json:"windowLabel"`
	FolderPath  string `json:"folder_path"`
}

// externalOpenBatch 合并短时间内的逐文件启动请求（Explorer 传统菜单每个文件启动一次进程）：
// 静默 150ms 后发送，连续请求最多等待 1s。
var externalOpenBatch struct {
	mu           sync.Mutex
	paths        []string
	started      time.Time
	lastReceived time.Time
	running      bool
}

// earlyOpenPaths 暂存应用就绪前收到的文件打开请求（macOS 启动时的打开事件）。
var earlyOpenPaths struct {
	mu    sync.Mutex
	ready bool
	paths []string
}

func init() {
	register("take_pending_external_open", func(call *commandCall, args struct {
		Paths []string `json:"paths"`
	}) ([]string, error) {
		window, err := call.requireWindow()
		if err != nil {
			return nil, err
		}
		key := pendingExternalOpenKey(window.label)
		var taken []string
		err = app.config.Update(func(c *config.AppConfig) { taken = updatePendingPaths(c, key, args.Paths, true) })
		if taken == nil {
			taken = []string{}
		}
		return taken, err
	})
}

func pendingExternalOpenKey(label string) string   { return pendingExternalOpenPrefix + label }
func pendingExternalFolderKey(label string) string { return pendingExternalFolderPrefix + label }

// collectExternalOpenTargets 从参数中识别受支持的文档与文件夹（去重、忽略选项与可执行文件自身）。
func collectExternalOpenTargets(args []string, workingDir string) externalOpenTargets {
	exe, _ := os.Executable()
	seen := map[string]bool{}
	var targets externalOpenTargets
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") || arg == "" {
			continue
		}
		absolute := arg
		if !filepath.IsAbs(absolute) && workingDir != "" {
			absolute = filepath.Join(workingDir, absolute)
		}
		if exe != "" && samePath(absolute, exe) {
			continue
		}
		info, err := os.Stat(absolute)
		if err != nil {
			continue
		}
		normalized := normalizeDisplayPath(absolute)
		if seen[normalized] {
			continue
		}
		switch {
		case info.Mode().IsRegular() && docfs.IsSupportedDocument(absolute):
			seen[normalized] = true
			targets.markdownPaths = append(targets.markdownPaths, normalized)
		case info.IsDir():
			seen[normalized] = true
			targets.folderPaths = append(targets.folderPaths, normalized)
		}
	}
	return targets
}

func samePath(left, right string) bool {
	return normalizeDisplayPath(left) == normalizeDisplayPath(right)
}

// normalizeDisplayPath 解析符号链接并去掉 Windows 扩展长度前缀，得到便于展示的绝对路径。
func normalizeDisplayPath(path string) string {
	resolved := path
	if absolute, err := filepath.Abs(path); err == nil {
		resolved = absolute
	}
	if real, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = real
	}
	if rest, ok := strings.CutPrefix(resolved, `\\?\UNC\`); ok {
		return `\\` + rest
	}
	return strings.TrimPrefix(resolved, `\\?\`)
}

// queueExternalOpenTargets 处理一组外部打开目标：文件夹立即路由，文档合并后批量路由。
func queueExternalOpenTargets(targets externalOpenTargets) {
	if len(targets.folderPaths) > 0 {
		routeExternalOpen(targets.markdownPaths)
		routeExternalFolderOpen(targets.folderPaths[0])
		return
	}
	if len(targets.markdownPaths) == 0 {
		return
	}
	now := time.Now()
	externalOpenBatch.mu.Lock()
	if externalOpenBatch.started.IsZero() {
		externalOpenBatch.started = now
	}
	externalOpenBatch.lastReceived = now
	for _, path := range targets.markdownPaths {
		if !containsString(externalOpenBatch.paths, path) {
			externalOpenBatch.paths = append(externalOpenBatch.paths, path)
		}
	}
	start := !externalOpenBatch.running
	externalOpenBatch.running = true
	externalOpenBatch.mu.Unlock()
	if !start {
		return
	}
	go func() {
		for {
			time.Sleep(externalOpenBatchQuiet)
			externalOpenBatch.mu.Lock()
			now := time.Now()
			quiet := now.Sub(externalOpenBatch.lastReceived) >= externalOpenBatchQuiet
			expired := now.Sub(externalOpenBatch.started) >= externalOpenBatchMaxDuration
			if !quiet && !expired {
				externalOpenBatch.mu.Unlock()
				continue
			}
			paths := externalOpenBatch.paths
			externalOpenBatch.paths = nil
			externalOpenBatch.started = time.Time{}
			externalOpenBatch.running = false
			externalOpenBatch.mu.Unlock()
			applog.Info("ExternalOpen", "合并外部打开批次")
			routeExternalOpen(paths)
			return
		}
	}()
}

// routeExternalOpen 把文档交给聚焦的文档窗口（没有时为主窗口）；只转交请求，由页面决定是否激活窗口。
func routeExternalOpen(paths []string) {
	if len(paths) == 0 {
		return
	}
	if isExiting() {
		go func() {
			waitUntilIdle()
			routeExternalOpen(paths)
		}()
		return
	}
	target := targetDocumentWindow()
	if target == nil {
		if err := persistPendingExternalOpen(mainWindowLabel, paths); err != nil {
			applog.Error("ExternalOpen", err.Error())
		}
		return
	}
	_ = persistPendingExternalOpen(target.label, paths)
	broadcastTo(openDocumentEvent, externalOpenPayload{WindowLabel: target.label, Paths: paths}, target.label)
}

// routeExternalFolderOpen 在目标窗口打开文件夹，并把窗口带到前台。
func routeExternalFolderOpen(folderPath string) {
	if folderPath == "" {
		return
	}
	if isExiting() {
		go func() {
			waitUntilIdle()
			routeExternalFolderOpen(folderPath)
		}()
		return
	}
	target := targetDocumentWindow()
	if target == nil {
		if err := persistPendingExternalFolderOpen(mainWindowLabel, folderPath); err != nil {
			applog.Error("ExternalOpen", err.Error())
		}
		return
	}
	_ = persistPendingExternalFolderOpen(target.label, folderPath)
	target.win.Show()
	target.win.Restore()
	target.win.Focus()
	delayedRefocus(target)
	broadcastTo(openFolderEvent, externalFolderOpenPayload{WindowLabel: target.label, FolderPath: folderPath}, target.label)
	setTrayActive(true)
}

func targetDocumentWindow() *appWindow {
	for _, window := range windows.documentWindows() {
		if window.win.IsFocused() {
			return window
		}
	}
	return windows.get(mainWindowLabel)
}

// persistPendingExternalOpen 把路径追加到窗口的待打开队列；读改写在配置写锁内完成，逐文件启动不会互相覆盖。
func persistPendingExternalOpen(label string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	key := pendingExternalOpenKey(label)
	return app.config.Update(func(c *config.AppConfig) { updatePendingPaths(c, key, paths, false) })
}

func persistPendingExternalFolderOpen(label, folderPath string) error {
	if folderPath == "" {
		return nil
	}
	encoded, _ := json.Marshal(folderPath)
	return updateSettings([]settingInput{{Key: pendingExternalFolderKey(label), ValueJSON: string(encoded)}})
}

// updatePendingPaths 追加（consume=false）或取走（consume=true，paths 为空时取走全部）待打开路径。
func updatePendingPaths(c *config.AppConfig, key string, paths []string, consume bool) []string {
	var pending []string
	if record, ok := c.App.Settings[key]; ok {
		_ = json.Unmarshal([]byte(record.ValueJSON), &pending)
	}
	var taken []string
	if consume {
		kept := pending[:0]
		for _, path := range pending {
			if len(paths) == 0 || containsString(paths, path) {
				taken = append(taken, path)
			} else {
				kept = append(kept, path)
			}
		}
		pending = kept
	} else {
		for _, path := range paths {
			if !containsString(pending, path) {
				pending = append(pending, path)
			}
		}
	}
	if pending == nil {
		pending = []string{}
	}
	encoded, _ := json.Marshal(pending)
	putSettingRecord(c, key, string(encoded))
	return taken
}

// argDelivered 记录已从命令行参数（启动或单实例转发）自行解析过的文件。MyGo 会把同一批参数里
// 关联类型的文件再交给 OnOpenFile 一次，这里据此去重；macOS Finder 打开等事件不在其中。
var argDelivered struct {
	mu    sync.Mutex
	paths map[string]int
}

func markArgsDelivered(paths []string) {
	argDelivered.mu.Lock()
	defer argDelivered.mu.Unlock()
	if argDelivered.paths == nil {
		argDelivered.paths = map[string]int{}
	}
	for _, path := range paths {
		argDelivered.paths[normalizeDisplayPath(path)]++
	}
}

func consumeArgDelivered(path string) bool {
	argDelivered.mu.Lock()
	defer argDelivered.mu.Unlock()
	key := normalizeDisplayPath(path)
	if argDelivered.paths[key] == 0 {
		return false
	}
	argDelivered.paths[key]--
	if argDelivered.paths[key] == 0 {
		delete(argDelivered.paths, key)
	}
	return true
}

// handleOpenFile 接收系统文件关联与 Dock 拖入的文件；应用就绪前先暂存。
func handleOpenFile(path string) {
	if consumeArgDelivered(path) {
		return
	}
	earlyOpenPaths.mu.Lock()
	if !earlyOpenPaths.ready {
		if !containsString(earlyOpenPaths.paths, path) {
			earlyOpenPaths.paths = append(earlyOpenPaths.paths, path)
		}
		earlyOpenPaths.mu.Unlock()
		return
	}
	earlyOpenPaths.mu.Unlock()
	queueExternalOpenTargets(collectExternalOpenTargets([]string{path}, ""))
}

// takeEarlyOpenPaths 标记已就绪并取走就绪前收到的文件。
func takeEarlyOpenPaths() []string {
	earlyOpenPaths.mu.Lock()
	defer earlyOpenPaths.mu.Unlock()
	earlyOpenPaths.ready = true
	paths := earlyOpenPaths.paths
	earlyOpenPaths.paths = nil
	return paths
}

func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

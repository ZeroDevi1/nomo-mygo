package main

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nomo/desktop/internal/config"
	"nomo/desktop/internal/docfs"
)

// 文件与文件夹在窗口间的归属登记：同一文件只在一个窗口打开；外部打开、拖放与“在文件夹窗口打开”
// 都先查询归属，再决定激活已有窗口、在当前窗口打开还是新建窗口。

const (
	openDocumentEvent  = "nomo://open-document"
	reservationTimeout = 30 * time.Second
)

// openTargetInput 是待打开的目标：一批文档或一个文件夹，按 kind 区分。
type openTargetInput struct {
	Kind  string   `json:"kind"`
	Paths []string `json:"paths,omitzero"`
	Path  string   `json:"path,omitzero"`
}

func documentsTarget(paths []string) *openTargetInput {
	return &openTargetInput{Kind: "documents", Paths: paths}
}

func folderTarget(path string) *openTargetInput {
	return &openTargetInput{Kind: "folder", Path: path}
}

// MarshalJSON 保证 documents 目标总带 paths 数组、folder 目标总带 path。
func (t openTargetInput) MarshalJSON() ([]byte, error) {
	if t.Kind == "folder" {
		return json.Marshal(map[string]any{"kind": "folder", "path": t.Path})
	}
	paths := t.Paths
	if paths == nil {
		paths = []string{}
	}
	return json.Marshal(map[string]any{"kind": "documents", "paths": paths})
}

type openDirectoryWindow struct {
	WindowLabel string   `json:"windowLabel"`
	FolderPath  string   `json:"folderPath"`
	Paths       []string `json:"paths"`
}

// openTargetRouteDecision 告诉页面如何处理一次打开请求，action 决定其余字段。
type openTargetRouteDecision struct {
	Action           string                `json:"action"`
	Target           *openTargetInput      `json:"target,omitzero"`
	RemainingTarget  *openTargetInput      `json:"remainingTarget,omitzero"`
	DirectoryWindows []openDirectoryWindow `json:"directoryWindows,omitzero"`
	WindowLabel      string                `json:"windowLabel,omitzero"`
}

type windowTargetSnapshot struct {
	folderKey string
	fileKeys  map[string]bool
}

type targetReservation struct {
	targetKeys map[string]bool
	createdAt  time.Time
}

// openTargetRegistry 记录每个文档窗口登记的文件夹与文件，以及正在创建中的窗口预留。
type openTargetRegistry struct {
	mu             sync.Mutex
	windows        map[string]windowTargetSnapshot
	reservations   map[string]targetReservation
	windowSequence atomic.Uint64
}

var openTargets = &openTargetRegistry{windows: map[string]windowTargetSnapshot{}, reservations: map[string]targetReservation{}}

type externalOpenPayload struct {
	WindowLabel string   `json:"windowLabel"`
	Paths       []string `json:"paths"`
}

func init() {
	register("resolve_dropped_open_target", func(_ *commandCall, args struct {
		Paths []string `json:"paths"`
	}) (*openTargetInput, error) {
		return resolveDroppedPaths(args.Paths)
	})
	register("sync_window_open_targets", func(call *commandCall, args struct {
		Input struct {
			FolderPath     *string  `json:"folderPath"`
			FilePaths      []string `json:"filePaths"`
			OwnershipEpoch uint64   `json:"ownershipEpoch"`
			Revision       uint64   `json:"revision"`
		} `json:"input"`
	}) (any, error) {
		window, err := call.requireWindow()
		if err != nil {
			return nil, err
		}
		if !isDocumentWindowLabel(window.label) {
			return nil, errors.New("只有文档窗口可以登记文件归属")
		}
		snapshot := windowTargetSnapshot{fileKeys: map[string]bool{}}
		if args.Input.FolderPath != nil {
			snapshot.folderKey, _ = normalizeTargetPath(*args.Input.FolderPath)
		}
		for _, path := range args.Input.FilePaths {
			if key, ok := normalizeTargetPath(path); ok {
				snapshot.fileKeys[key] = true
			}
		}
		return nil, withOpenTargetsGuard(window.label, args.Input.OwnershipEpoch, args.Input.Revision, func() error {
			openTargets.mu.Lock()
			delete(openTargets.reservations, window.label)
			openTargets.windows[window.label] = snapshot
			openTargets.mu.Unlock()
			return nil
		})
	})
	register("prepare_open_target_window", func(call *commandCall, args struct {
		Target               openTargetInput `json:"target"`
		CreateIfMissing      bool            `json:"createIfMissing"`
		ReuseDirectoryWindow *bool           `json:"reuseDirectoryWindow"`
	}) (*openTargetRouteDecision, error) {
		window, err := call.requireWindow()
		if err != nil {
			return nil, err
		}
		return prepareOpenTargetWindow(window.label, args.Target, args.CreateIfMissing, args.ReuseDirectoryWindow != nil && *args.ReuseDirectoryWindow)
	})
	register("release_open_target_reservation", func(_ *commandCall, args struct {
		WindowLabel string `json:"windowLabel"`
	}) (any, error) {
		openTargets.releaseReservation(args.WindowLabel)
		return nil, clearPendingTarget(args.WindowLabel)
	})
	register("open_documents_in_directory_window", func(_ *commandCall, args struct {
		WindowLabel string   `json:"windowLabel"`
		Paths       []string `json:"paths"`
	}) (any, error) {
		return nil, openDocumentsInDirectoryWindow(args.WindowLabel, args.Paths)
	})
}

// resolveDroppedPaths 识别拖放内容：首个有效目标为目录时打开该目录，否则收集整批受支持文档。
func resolveDroppedPaths(paths []string) (*openTargetInput, error) {
	var documents []string
	seen := map[string]bool{}
	for _, path := range paths {
		info, err := os.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("无法读取拖放路径 %s：%w", path, err)
		}
		if info.IsDir() && len(documents) == 0 {
			return folderTarget(path), nil
		}
		if info.Mode().IsRegular() && docfs.IsSupportedDocument(path) {
			if key, ok := normalizeTargetPath(path); ok && !seen[key] {
				seen[key] = true
				documents = append(documents, path)
			}
		}
	}
	if len(documents) == 0 {
		return nil, nil
	}
	return documentsTarget(documents), nil
}

func prepareOpenTargetWindow(currentLabel string, target openTargetInput, createIfMissing, reuseDirectoryWindow bool) (*openTargetRouteDecision, error) {
	waitUntilIdle()
	openTargets.mu.Lock()
	openTargets.prune()
	existing, current, remaining := openTargets.resolveExisting(currentLabel, target, reuseDirectoryWindow)
	var candidates []openDirectoryWindow
	if remaining != nil {
		candidates = openTargets.findDirectoryWindows(*remaining)
	}
	openTargets.mu.Unlock()

	for _, label := range sortedKeys(existing) {
		focusDocumentWindow(label)
		paths := existing[label]
		if len(paths) == 0 {
			continue
		}
		if windows.get(label) == nil {
			continue
		}
		broadcastTo(openDocumentEvent, externalOpenPayload{WindowLabel: label, Paths: paths}, label)
	}
	if current != nil {
		// 已打开的文件先定位当前标签，混合批次中的未打开文件继续按设置处理。
		return &openTargetRouteDecision{Action: "activate-current", Target: current, RemainingTarget: remaining}, nil
	}
	if remaining == nil {
		if target.Kind == "folder" {
			if err := rememberRecentEntry(recentEntryInput{Path: target.Path, EntryType: "folder"}); err != nil {
				return nil, err
			}
		}
		return &openTargetRouteDecision{Action: "handled"}, nil
	}
	if !createIfMissing {
		if candidates == nil {
			candidates = []openDirectoryWindow{}
		}
		return &openTargetRouteDecision{Action: "open-current", Target: remaining, DirectoryWindows: candidates}, nil
	}
	keys := targetKeys(*remaining)
	if len(keys) == 0 {
		return &openTargetRouteDecision{Action: "handled"}, nil
	}
	label, err := openTargets.nextWindowLabel()
	if err != nil {
		return nil, err
	}
	openTargets.mu.Lock()
	openTargets.reservations[label] = targetReservation{targetKeys: keys, createdAt: time.Now()}
	openTargets.mu.Unlock()
	if err := persistPendingTarget(label, *remaining); err != nil {
		openTargets.releaseReservation(label)
		return nil, err
	}
	return &openTargetRouteDecision{Action: "create-window", WindowLabel: label, Target: remaining}, nil
}

// openDocumentsInDirectoryWindow 把文档交给用户选择的目录窗口；重新检查归属，避免询问期间窗口变化。
func openDocumentsInDirectoryWindow(windowLabel string, paths []string) error {
	waitUntilIdle()
	target := *documentsTarget(paths)
	openTargets.mu.Lock()
	openTargets.prune()
	var candidate *openDirectoryWindow
	for _, item := range openTargets.findDirectoryWindows(target) {
		if item.WindowLabel == windowLabel {
			candidate = &item
			break
		}
	}
	if candidate == nil {
		openTargets.mu.Unlock()
		return errors.New("所选文件夹窗口已关闭或已切换目录，请重新打开文件")
	}
	if len(candidate.Paths) != len(paths) || len(paths) == 0 {
		openTargets.mu.Unlock()
		return errors.New("文件已不属于所选文件夹窗口")
	}
	existing, current, remaining := openTargets.resolveExisting(windowLabel, target, false)
	openTargets.mu.Unlock()
	for _, label := range sortedKeys(existing) {
		if windows.get(label) == nil {
			return errors.New("已有文件窗口已关闭，请重新打开文件")
		}
		broadcastTo(openDocumentEvent, externalOpenPayload{WindowLabel: label, Paths: existing[label]}, label)
		focusDocumentWindow(label)
	}
	var selected []string
	for _, item := range []*openTargetInput{current, remaining} {
		if item != nil && item.Kind == "documents" {
			selected = append(selected, item.Paths...)
		}
	}
	if len(selected) > 0 {
		if windows.get(windowLabel) == nil {
			return errors.New("所选文件夹窗口已关闭，请重新打开文件")
		}
		broadcastTo("nomo://open-in-directory-window", externalOpenPayload{WindowLabel: windowLabel, Paths: selected}, windowLabel)
		focusDocumentWindow(windowLabel)
	}
	return nil
}

// resolveExisting 把目标拆成：已由其他窗口打开的（按窗口分组）、当前窗口已打开的、尚无归属的剩余部分。
// 调用方持有 mu。
func (r *openTargetRegistry) resolveExisting(currentLabel string, target openTargetInput, reuseDirectoryWindow bool) (map[string][]string, *openTargetInput, *openTargetInput) {
	owners := map[string][]string{}
	if target.Kind == "folder" {
		key, ok := normalizeTargetPath(target.Path)
		if !ok {
			return owners, nil, nil
		}
		if owner := r.findOwner(currentLabel, key, true); owner != "" {
			if owner == currentLabel {
				return owners, folderTarget(target.Path), nil
			}
			owners[owner] = []string{}
			return owners, nil, nil
		}
		return owners, nil, folderTarget(target.Path)
	}
	var currentPaths, remaining []string
	seen := map[string]bool{}
	for _, path := range target.Paths {
		key, ok := normalizeTargetPath(path)
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		owner := r.findOwner(currentLabel, key, false)
		if owner == "" && reuseDirectoryWindow {
			// 精确文件（包括在建预留）优先；只复用已经登记的直接父目录窗口。
			if parent, ok := parentKey(key); ok {
				if candidate := r.findOwner(currentLabel, parent, true); candidate != "" {
					if _, registered := r.windows[candidate]; registered {
						owner = candidate
					}
				}
			}
		}
		switch owner {
		case "":
			remaining = append(remaining, path)
		case currentLabel:
			currentPaths = append(currentPaths, path)
		default:
			owners[owner] = append(owners[owner], path)
		}
	}
	var currentTarget, remainingTarget *openTargetInput
	if len(currentPaths) > 0 {
		currentTarget = documentsTarget(currentPaths)
	}
	if len(remaining) > 0 {
		remainingTarget = documentsTarget(remaining)
	}
	return owners, currentTarget, remainingTarget
}

// findOwner 依次查找当前窗口、其他窗口（按标签排序）与在建预留中持有该目标的窗口。
func (r *openTargetRegistry) findOwner(currentLabel, key string, folder bool) string {
	owns := func(snapshot windowTargetSnapshot, ok bool) bool {
		if !ok {
			return false
		}
		if folder {
			return snapshot.folderKey == key
		}
		return snapshot.fileKeys[key]
	}
	if snapshot, ok := r.windows[currentLabel]; owns(snapshot, ok) {
		return currentLabel
	}
	labels := make([]string, 0, len(r.windows))
	for label := range r.windows {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for _, label := range labels {
		if label != currentLabel && owns(r.windows[label], true) {
			return label
		}
	}
	reserved := make([]string, 0, len(r.reservations))
	for label := range r.reservations {
		reserved = append(reserved, label)
	}
	sort.Strings(reserved)
	for _, label := range reserved {
		if r.reservations[label].targetKeys[key] {
			return label
		}
	}
	return ""
}

// findDirectoryWindows 返回直接父目录与已登记目录相同的文档，按窗口分组；混合目录批次可继续选择其余文件去向。
func (r *openTargetRegistry) findDirectoryWindows(target openTargetInput) []openDirectoryWindow {
	if target.Kind != "documents" {
		return nil
	}
	var candidates []openDirectoryWindow
	for label, snapshot := range r.windows {
		if snapshot.folderKey == "" {
			continue
		}
		var matching []string
		for _, path := range target.Paths {
			if key, ok := normalizeTargetPath(path); ok {
				if parent, ok := parentKey(key); ok && parent == snapshot.folderKey {
					matching = append(matching, path)
				}
			}
		}
		if len(matching) > 0 {
			candidates = append(candidates, openDirectoryWindow{WindowLabel: label, FolderPath: snapshot.folderKey, Paths: matching})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].WindowLabel < candidates[j].WindowLabel })
	return candidates
}

// prune 清除已关闭窗口的登记与超时预留；调用方持有 mu。
func (r *openTargetRegistry) prune() {
	alive := map[string]bool{}
	for _, window := range windows.all() {
		alive[window.label] = true
	}
	for label := range r.windows {
		if !alive[label] {
			delete(r.windows, label)
		}
	}
	for label, reservation := range r.reservations {
		if time.Since(reservation.createdAt) > reservationTimeout {
			delete(r.reservations, label)
		}
	}
}

func (r *openTargetRegistry) forgetWindow(label string) {
	r.mu.Lock()
	delete(r.windows, label)
	delete(r.reservations, label)
	r.mu.Unlock()
}

func (r *openTargetRegistry) releaseReservation(label string) {
	r.mu.Lock()
	delete(r.reservations, label)
	r.mu.Unlock()
}

// nextWindowLabel 生成未被占用也未被预留的新窗口标签 window-<ts>-<seq>。
func (r *openTargetRegistry) nextWindowLabel() (string, error) {
	for range 32 {
		label := fmt.Sprintf("window-%d-%d", config.NowTS(), r.windowSequence.Add(1))
		r.mu.Lock()
		_, reserved := r.reservations[label]
		r.mu.Unlock()
		if !reserved && windows.get(label) == nil {
			return label, nil
		}
	}
	return "", errors.New("无法生成唯一窗口标识")
}

// commitTransfer 在锁内完成标签移交后的归属切换与磁盘提交，路由与同步不会看到移交一半的状态。
func (r *openTargetRegistry) commitTransfer(source, target string, sourceTargets, targetTargets transferOpenTargets, persist func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	toSnapshot := func(targets transferOpenTargets) windowTargetSnapshot {
		snapshot := windowTargetSnapshot{fileKeys: map[string]bool{}}
		if targets.FolderPath != nil {
			snapshot.folderKey, _ = normalizeTargetPath(*targets.FolderPath)
		}
		for _, path := range targets.FilePaths {
			if key, ok := normalizeTargetPath(path); ok {
				snapshot.fileKeys[key] = true
			}
		}
		return snapshot
	}
	sourceSnapshot, targetSnapshot := toSnapshot(sourceTargets), toSnapshot(targetTargets)
	for key := range targetSnapshot.fileKeys {
		if sourceSnapshot.fileKeys[key] {
			return errors.New("移交后的两个窗口包含重复文件")
		}
	}
	for label, snapshot := range r.windows {
		if label == source || label == target {
			continue
		}
		for key := range targetSnapshot.fileKeys {
			if snapshot.fileKeys[key] {
				return errors.New("移交文件已由其他窗口打开")
			}
		}
	}
	if err := persist(); err != nil {
		return err
	}
	r.windows[source] = sourceSnapshot
	r.windows[target] = targetSnapshot
	delete(r.reservations, target)
	return nil
}

func targetKeys(target openTargetInput) map[string]bool {
	keys := map[string]bool{}
	if target.Kind == "folder" {
		if key, ok := normalizeTargetPath(target.Path); ok {
			keys[key] = true
		}
		return keys
	}
	for _, path := range target.Paths {
		if key, ok := normalizeTargetPath(path); ok {
			keys[key] = true
		}
	}
	return keys
}

// normalizeTargetPath 生成路径比较键：绝对化、解析符号链接、统一为正斜杠并去掉尾部斜杠；
// Windows 上去掉扩展长度前缀并转小写。空路径返回 false。
func normalizeTargetPath(path string) (string, bool) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", false
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		absolute = trimmed
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = resolved
	}
	normalized := strings.ReplaceAll(absolute, `\`, "/")
	if rest, ok := strings.CutPrefix(normalized, "//?/UNC/"); ok {
		normalized = "//" + rest
	} else if rest, ok := strings.CutPrefix(normalized, "//?/"); ok {
		normalized = rest
	}
	for len(normalized) > 1 && strings.HasSuffix(normalized, "/") {
		normalized = normalized[:len(normalized)-1]
	}
	if runtime.GOOS == "windows" {
		normalized = strings.ToLower(normalized)
	}
	return normalized, true
}

// parentKey 返回比较键的直接父目录（根目录为 "/"）。
func parentKey(key string) (string, bool) {
	index := strings.LastIndexByte(key, '/')
	if index < 0 {
		return "", false
	}
	if index == 0 {
		return "/", true
	}
	return key[:index], true
}

func persistPendingTarget(label string, target openTargetInput) error {
	if target.Kind == "folder" {
		return persistPendingExternalFolderOpen(label, target.Path)
	}
	return persistPendingExternalOpen(label, target.Paths)
}

func clearPendingTarget(label string) error {
	empty := `""`
	return updateSettings([]settingInput{
		{Key: pendingExternalOpenKey(label), ValueJSON: empty},
		{Key: pendingExternalFolderKey(label), ValueJSON: empty},
	})
}

func focusDocumentWindow(label string) {
	if window := windows.get(label); window != nil {
		window.win.Show()
		window.win.Restore()
		window.win.Focus()
	}
}

func sortedKeys[V any](items map[string]V) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

var _ = jsontext.Value(nil)

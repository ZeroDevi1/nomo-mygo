package main

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/config"
)

// Markdown 项的跨窗口移交：正文先在目标窗口暂存，磁盘元数据与文件归属提交成功后才移除源项。
// 状态机 prepared → target-ready → committed，任何阶段可 cancelled；记录持久化在 tabTransfer:<token>，
// 重启时未完成的移交回滚到源窗口。

const (
	transferJournalPrefix = "tabTransfer:"
	transferTimeout       = 30 * time.Second
)

// windowStamp 是窗口工作区的归属代数与写入序号，防止移交前后的旧快照覆盖新状态。
type windowStamp struct {
	epoch             uint64
	workspaceRevision uint64
	targetsRevision   uint64
}

type workspaceWriteStamp struct {
	OwnershipEpoch uint64 `json:"ownershipEpoch"`
	Revision       uint64 `json:"revision"`
}

type dropPlacement struct {
	Kind           string `json:"kind"`
	InsertionIndex *int   `json:"insertionIndex,omitzero"`
	TargetItemID   string `json:"targetItemId,omitzero"`
}

type transferOpenTargets struct {
	FolderPath *string  `json:"folderPath"`
	FilePaths  []string `json:"filePaths"`
}

type screenPoint struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// transferSnapshot 是移交记录，同时作为事件负载与持久化格式；工作区与标签以 JSON 对象原样保存。
type transferSnapshot struct {
	Token                 string               `json:"token"`
	Status                string               `json:"status"`
	SourceWindowLabel     string               `json:"sourceWindowLabel"`
	TargetWindowLabel     string               `json:"targetWindowLabel"`
	SourceEpoch           uint64               `json:"sourceEpoch"`
	TargetEpoch           uint64               `json:"targetEpoch"`
	NewWindow             bool                 `json:"newWindow"`
	Item                  map[string]any       `json:"item"`
	Tabs                  []map[string]any     `json:"tabs"`
	Positions             any                  `json:"positions"`
	SourceWorkspace       map[string]any       `json:"sourceWorkspace"`
	SourceBeforeWorkspace map[string]any       `json:"sourceBeforeWorkspace"`
	SourceOpenTargets     transferOpenTargets  `json:"sourceOpenTargets"`
	Placement             dropPlacement        `json:"placement"`
	ScreenPosition        *screenPoint         `json:"screenPosition"`
	TargetWorkspace       map[string]any       `json:"targetWorkspace,format:emitnull"`
	TargetBeforeWorkspace map[string]any       `json:"targetBeforeWorkspace,format:emitnull"`
	TargetOpenTargets     *transferOpenTargets `json:"targetOpenTargets"`
	CreatedAt             int64                `json:"createdAt"`
}

var transferState struct {
	mu        sync.Mutex
	stamps    map[string]*windowStamp
	transfers map[string]*transferSnapshot
	sequence  atomic.Uint64
}

func init() {
	transferState.stamps = map[string]*windowStamp{}
	transferState.transfers = map[string]*transferSnapshot{}
	register("get_window_workspace_stamp", func(call *commandCall, _ noArgs) (*workspaceWriteStamp, error) {
		window, err := call.requireWindow()
		if err != nil {
			return nil, err
		}
		if !isDocumentWindowLabel(window.label) {
			return nil, errors.New("只有文档窗口可以读取工作区序号")
		}
		transferState.mu.Lock()
		defer transferState.mu.Unlock()
		stamp := stampFor(window.label)
		return &workspaceWriteStamp{OwnershipEpoch: stamp.epoch, Revision: max(stamp.workspaceRevision, stamp.targetsRevision)}, nil
	})
	register("prepare_tab_transfer", func(call *commandCall, args struct {
		Input transferInput `json:"input"`
	}) (*transferSnapshot, error) {
		window, err := call.requireWindow()
		if err != nil {
			return nil, err
		}
		return prepareTabTransfer(window.label, args.Input)
	})
	register("get_tab_transfer_bootstrap", func(call *commandCall, _ noArgs) (*transferSnapshot, error) {
		window, err := call.requireWindow()
		if err != nil {
			return nil, err
		}
		if err := requireTransferDocumentWindow(window.label); err != nil {
			return nil, err
		}
		transferState.mu.Lock()
		defer transferState.mu.Unlock()
		for _, transfer := range transferState.transfers {
			if transfer.TargetWindowLabel == window.label && transfer.NewWindow && (transfer.Status == "prepared" || transfer.Status == "target-ready") {
				clone := *transfer
				return &clone, nil
			}
		}
		return nil, nil
	})
	register("target_ready_tab_transfer", func(call *commandCall, args struct {
		Token             string              `json:"token"`
		TargetWorkspace   map[string]any      `json:"targetWorkspace"`
		TargetOpenTargets transferOpenTargets `json:"targetOpenTargets"`
	}) (*transferSnapshot, error) {
		window, err := call.requireWindow()
		if err != nil {
			return nil, err
		}
		return targetReadyTabTransfer(window.label, args.Token, args.TargetWorkspace, args.TargetOpenTargets)
	})
	register("commit_tab_transfer", func(call *commandCall, args struct {
		Token string `json:"token"`
	}) (*transferSnapshot, error) {
		window, err := call.requireWindow()
		if err != nil {
			return nil, err
		}
		return commitTabTransfer(window.label, args.Token)
	})
	register("cancel_tab_transfer", func(call *commandCall, args struct {
		Token string `json:"token"`
	}) (*transferSnapshot, error) {
		window, err := call.requireWindow()
		if err != nil {
			return nil, err
		}
		transferState.mu.Lock()
		_, err = transferForCaller(args.Token, window.label)
		transferState.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return cancelTransfer(args.Token)
	})
	register("query_tab_transfer", func(call *commandCall, args struct {
		Token string `json:"token"`
	}) (*transferSnapshot, error) {
		window, err := call.requireWindow()
		if err != nil {
			return nil, err
		}
		transferState.mu.Lock()
		defer transferState.mu.Unlock()
		return transferForCaller(args.Token, window.label)
	})
	register("close_empty_transferred_window", func(call *commandCall, args struct {
		Token string `json:"token"`
	}) (any, error) {
		window, err := call.requireWindow()
		if err != nil {
			return nil, err
		}
		return nil, closeEmptyTransferredWindow(window, args.Token)
	})
}

// stampFor 返回窗口的归属戳，不存在时创建；调用方持有 transferState.mu。
func stampFor(label string) *windowStamp {
	stamp, ok := transferState.stamps[label]
	if !ok {
		stamp = &windowStamp{}
		transferState.stamps[label] = stamp
	}
	return stamp
}

func requireTransferDocumentWindow(label string) error {
	if !isDocumentWindowLabel(label) || isMarkdownMiniModeWindow(label) {
		return errors.New("只有普通文档窗口支持标签移交")
	}
	return nil
}

// withGuardedSettings 在同一锁内筛选并写入设置：窗口与文件夹工作区快照必须属于当前归属代数且序号递增，
// 否则静默丢弃（移交已让该窗口的旧快照失效）。
func withGuardedSettings(inputs []settingInput, write func([]settingInput) error) error {
	transferState.mu.Lock()
	defer transferState.mu.Unlock()
	var accepted []settingInput
	nextRevisions := map[string]uint64{}
	for _, input := range inputs {
		workspaceKey, ok := strings.CutPrefix(input.Key, "workspaceTabs:")
		if !ok {
			accepted = append(accepted, input)
			continue
		}
		windowKey := isDocumentWindowLabel(workspaceKey)
		if !windowKey && !strings.HasPrefix(workspaceKey, "folder:") {
			accepted = append(accepted, input)
			continue
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(input.ValueJSON), &value); err != nil {
			if windowKey {
				return fmt.Errorf("工作区快照无效：%w", err)
			}
			value = nil
		}
		label := workspaceKey
		if !windowKey {
			writer, ok := value["writerWindowLabel"].(string)
			if !ok {
				// 旧迁移快照没有 writer，沿用原来的文件夹恢复行为。
				accepted = append(accepted, input)
				continue
			}
			if !isDocumentWindowLabel(writer) {
				return errors.New("文件夹工作区写入窗口无效")
			}
			label = writer
		}
		// 同一次批量写入的窗口项与文件夹项共享一个戳，均以提交前版本核对。
		current := windowStamp{}
		if stamp, ok := transferState.stamps[label]; ok {
			current = *stamp
		}
		epoch := jsonUint(value["ownershipEpoch"])
		revision := jsonUint(value["revision"])
		if epoch != current.epoch || current.workspaceRevision > 0 && revision <= current.workspaceRevision {
			continue
		}
		nextRevisions[label] = max(nextRevisions[label], revision)
		accepted = append(accepted, input)
	}
	if len(accepted) == 0 {
		return nil
	}
	if err := write(accepted); err != nil {
		return err
	}
	for label, revision := range nextRevisions {
		stamp := stampFor(label)
		stamp.workspaceRevision = max(stamp.workspaceRevision, revision)
	}
	return nil
}

// withOpenTargetsGuard 只接受当前归属代数且序号递增的文件归属登记。
func withOpenTargetsGuard(label string, epoch, revision uint64, write func() error) error {
	transferState.mu.Lock()
	defer transferState.mu.Unlock()
	stamp := stampFor(label)
	if epoch != stamp.epoch || stamp.targetsRevision > 0 && revision <= stamp.targetsRevision {
		return nil
	}
	if err := write(); err != nil {
		return err
	}
	stamp.targetsRevision = revision
	return nil
}

// jsonUint 读取 JSON 数字为非负整数；缺失或类型不符时为 0。
func jsonUint(value any) uint64 {
	if number, ok := value.(float64); ok && number >= 0 {
		return uint64(number)
	}
	return 0
}

func jsonString(object map[string]any, key string) (string, bool) {
	value, ok := object[key].(string)
	return value, ok
}

// itemMembers 返回工作区项包含的文档 ID：single 一个，comparison 左右两个且不同。
func itemMembers(item map[string]any) ([]string, error) {
	switch kind, _ := jsonString(item, "kind"); kind {
	case "single":
		id, ok := jsonString(item, "tabId")
		if !ok {
			return nil, errors.New("普通项缺少文档 ID")
		}
		return []string{id}, nil
	case "comparison":
		left, ok := jsonString(item, "leftTabId")
		if !ok {
			return nil, errors.New("组合缺少左文档")
		}
		right, ok := jsonString(item, "rightTabId")
		if !ok {
			return nil, errors.New("组合缺少右文档")
		}
		if left == right {
			return nil, errors.New("组合不能包含重复文档")
		}
		return []string{left, right}, nil
	}
	return nil, errors.New("无法识别工作区项")
}

func asObjects(value any) ([]map[string]any, bool) {
	array, ok := value.([]any)
	if !ok {
		return nil, false
	}
	objects := make([]map[string]any, 0, len(array))
	for _, element := range array {
		object, ok := element.(map[string]any)
		if !ok {
			return nil, false
		}
		objects = append(objects, object)
	}
	return objects, true
}

func workspaceTabs(workspace map[string]any) ([]map[string]any, error) {
	if jsonUint(workspace["version"]) != 4 {
		return nil, errors.New("移交需要 v4 工作区")
	}
	tabs, ok := asObjects(workspace["tabs"])
	if !ok {
		return nil, errors.New("工作区缺少文档列表")
	}
	return tabs, nil
}

func tabIDs(tabs []map[string]any) map[string]bool {
	ids := map[string]bool{}
	for _, tab := range tabs {
		if id, ok := jsonString(tab, "id"); ok {
			ids[id] = true
		}
	}
	return ids
}

// validateWorkspace 校验 v4 工作区：文档 ID 唯一，外层项 ID 唯一，每个文档恰属于一个项，组合只含 Markdown。
func validateWorkspace(workspace map[string]any) error {
	tabs, err := workspaceTabs(workspace)
	if err != nil {
		return err
	}
	ids := tabIDs(tabs)
	if len(ids) != len(tabs) {
		return errors.New("工作区文档 ID 无效或重复")
	}
	items, ok := asObjects(workspace["items"])
	if !ok {
		return errors.New("工作区缺少外层项")
	}
	assigned := map[string]bool{}
	itemIDs := map[string]bool{}
	for _, item := range items {
		id, ok := jsonString(item, "id")
		if !ok {
			return errors.New("工作区项缺少 ID")
		}
		if itemIDs[id] {
			return errors.New("工作区项 ID 重复")
		}
		itemIDs[id] = true
		members, err := itemMembers(item)
		if err != nil {
			return err
		}
		for _, member := range members {
			if !ids[member] || assigned[member] {
				return errors.New("文档归属无效或重复")
			}
			assigned[member] = true
			if kind, _ := jsonString(item, "kind"); kind == "comparison" {
				for _, tab := range tabs {
					if tabID, _ := jsonString(tab, "id"); tabID == member {
						if documentKind, _ := jsonString(tab, "documentKind"); documentKind != "markdown" {
							return errors.New("组合只允许 Markdown")
						}
					}
				}
			}
		}
	}
	if len(assigned) != len(ids) {
		return errors.New("工作区存在未分配文档")
	}
	return nil
}

func nativePathKey(tab map[string]any) (string, bool) {
	path, ok := jsonString(tab, "nativePath")
	if !ok {
		return "", false
	}
	return normalizeTargetPath(path)
}

func validateOpenTargets(workspace map[string]any, targets transferOpenTargets) error {
	tabs, err := workspaceTabs(workspace)
	if err != nil {
		return err
	}
	actual := map[string]bool{}
	for _, tab := range tabs {
		if key, ok := nativePathKey(tab); ok {
			actual[key] = true
		}
	}
	registered := map[string]bool{}
	for _, path := range targets.FilePaths {
		if key, ok := normalizeTargetPath(path); ok {
			registered[key] = true
		}
	}
	if !sameLabelSet(actual, registered) {
		return errors.New("工作区与文件归属快照不一致")
	}
	folder := ""
	if path, ok := jsonString(workspace, "currentFolderPath"); ok {
		folder, _ = normalizeTargetPath(path)
	}
	registeredFolder := ""
	if targets.FolderPath != nil {
		registeredFolder, _ = normalizeTargetPath(*targets.FolderPath)
	}
	if folder != registeredFolder {
		return errors.New("工作区与文件夹归属快照不一致")
	}
	return nil
}

func validateIncomingPaths(incoming, existing []map[string]any) error {
	existingPaths := map[string]bool{}
	for _, tab := range existing {
		if key, ok := nativePathKey(tab); ok {
			existingPaths[key] = true
		}
	}
	incomingPaths := map[string]bool{}
	for _, tab := range incoming {
		key, ok := nativePathKey(tab)
		if !ok {
			continue
		}
		if incomingPaths[key] {
			return errors.New("迁移成员指向同一个文件，不能重复接收")
		}
		incomingPaths[key] = true
		if existingPaths[key] {
			return errors.New("目标窗口已经打开迁移文件，不能创建重复编辑基线")
		}
	}
	return nil
}

func putJSONSetting(c *config.AppConfig, key string, value any) {
	encoded, err := json.Marshal(value, json.Deterministic(true))
	if err != nil {
		applog.Warn("TabTransfer", fmt.Sprintf("序列化设置 %s 失败：%v", key, err))
		return
	}
	putSettingRecord(c, key, string(encoded))
}

func putWorkspaceSnapshot(c *config.AppConfig, label string, workspace map[string]any) {
	if folder, ok := jsonString(workspace, "currentFolderPath"); ok && folder != "" {
		putJSONSetting(c, "workspaceTabs:folder:"+folder, workspace)
	}
	putJSONSetting(c, "workspaceTabs:"+label, workspace)
}

func persistTransfer(transfer *transferSnapshot) error {
	return app.config.Update(func(c *config.AppConfig) {
		putJSONSetting(c, transferJournalPrefix+transfer.Token, transfer)
	})
}

func emitTransferParticipants(event string, transfer *transferSnapshot) {
	for _, label := range []string{transfer.SourceWindowLabel, transfer.TargetWindowLabel} {
		broadcastTo(event, transfer, label)
	}
}

type transferInput struct {
	Item              map[string]any      `json:"item"`
	Tabs              []map[string]any    `json:"tabs"`
	Positions         any                 `json:"positions"`
	SourceWorkspace   map[string]any      `json:"sourceWorkspace"`
	SourceOpenTargets transferOpenTargets `json:"sourceOpenTargets"`
	TargetWindowLabel *string             `json:"targetWindowLabel"`
	Placement         dropPlacement       `json:"placement"`
	ScreenPosition    *screenPoint        `json:"screenPosition"`
}

func readWorkspaceSetting(label string) (map[string]any, bool, error) {
	value, ok := settingValue("workspaceTabs:" + label)
	if !ok {
		return nil, false, nil
	}
	var workspace map[string]any
	if err := json.Unmarshal([]byte(value), &workspace); err != nil {
		return nil, true, err
	}
	return workspace, true, nil
}

func prepareTabTransfer(sourceLabel string, input transferInput) (*transferSnapshot, error) {
	if err := requireTransferDocumentWindow(sourceLabel); err != nil {
		return nil, err
	}
	if isExiting() {
		return nil, errors.New("退出期间不能移交标签")
	}
	members, err := itemMembers(input.Item)
	if err != nil {
		return nil, err
	}
	incoming := tabIDs(input.Tabs)
	if len(input.Tabs) != len(members) {
		return nil, errors.New("移交快照成员不完整")
	}
	for _, member := range members {
		if !incoming[member] {
			return nil, errors.New("移交快照成员不完整")
		}
	}
	for _, tab := range input.Tabs {
		kind, _ := jsonString(tab, "documentKind")
		_, hasMarkdown := jsonString(tab, "markdown")
		_, hasSaved := jsonString(tab, "savedMarkdown")
		if kind != "markdown" || !hasMarkdown || !hasSaved {
			return nil, errors.New("移交仅支持完整 Markdown 快照")
		}
	}
	if err := validateIncomingPaths(input.Tabs, nil); err != nil {
		return nil, err
	}
	if err := validateWorkspace(input.SourceWorkspace); err != nil {
		return nil, err
	}
	if err := validateOpenTargets(input.SourceWorkspace, input.SourceOpenTargets); err != nil {
		return nil, err
	}
	sourceTabs, _ := workspaceTabs(input.SourceWorkspace)
	for id := range tabIDs(sourceTabs) {
		if incoming[id] {
			return nil, errors.New("源工作区仍然包含迁移文档")
		}
	}
	if input.Placement.Kind == "combine" && len(members) != 1 {
		return nil, errors.New("组合不可再次嵌套")
	}
	newWindow := input.TargetWindowLabel == nil
	before, ok, err := readWorkspaceSetting(sourceLabel)
	if !ok {
		return nil, errors.New("源工作区尚未保存")
	}
	if err != nil {
		return nil, fmt.Errorf("源工作区无效：%w", err)
	}
	if err := validateWorkspace(before); err != nil {
		return nil, err
	}
	beforeTabs, _ := workspaceTabs(before)
	beforeIDs := tabIDs(beforeTabs)
	afterIDs := tabIDs(sourceTabs)
	expectedAfter := map[string]bool{}
	for id := range beforeIDs {
		if !incoming[id] {
			expectedAfter[id] = true
		}
	}
	for id := range incoming {
		if !beforeIDs[id] {
			return nil, errors.New("源工作区变更超出移交文档")
		}
	}
	if !sameLabelSet(expectedAfter, afterIDs) {
		return nil, errors.New("源工作区变更超出移交文档")
	}
	var target string
	if input.TargetWindowLabel != nil {
		target = *input.TargetWindowLabel
		if err := requireTransferDocumentWindow(target); err != nil {
			return nil, err
		}
		if target == sourceLabel || windows.get(target) == nil {
			return nil, errors.New("目标窗口无效")
		}
		if !tabDragWindowReady(target) {
			return nil, errors.New("目标窗口尚未就绪")
		}
	} else if target, err = openTargets.nextWindowLabel(); err != nil {
		return nil, err
	}
	var targetBefore map[string]any
	if !newWindow {
		workspace, ok, err := readWorkspaceSetting(target)
		if err != nil {
			return nil, fmt.Errorf("目标工作区无效：%w", err)
		}
		if ok {
			if err := validateWorkspace(workspace); err != nil {
				return nil, err
			}
			targetTabs, _ := workspaceTabs(workspace)
			for id := range tabIDs(targetTabs) {
				if incoming[id] {
					return nil, errors.New("目标工作区已有同 ID 文档")
				}
			}
			targetBefore = workspace
		}
	}
	token := fmt.Sprintf("%d-%d", time.Now().UnixNano(), transferState.sequence.Add(1))
	transferState.mu.Lock()
	for _, transfer := range transferState.transfers {
		if transfer.Status != "prepared" && transfer.Status != "target-ready" {
			continue
		}
		for _, label := range []string{transfer.SourceWindowLabel, transfer.TargetWindowLabel} {
			if label == sourceLabel || label == target {
				transferState.mu.Unlock()
				return nil, errors.New("窗口已有进行中的标签移交")
			}
		}
	}
	sourceEpoch := stampFor(sourceLabel).epoch
	targetEpoch := stampFor(target).epoch
	if jsonUint(input.SourceWorkspace["ownershipEpoch"]) != sourceEpoch {
		transferState.mu.Unlock()
		return nil, errors.New("源工作区快照已过期")
	}
	snapshot := &transferSnapshot{
		Token: token, Status: "prepared", SourceWindowLabel: sourceLabel, TargetWindowLabel: target,
		SourceEpoch: sourceEpoch, TargetEpoch: targetEpoch, NewWindow: newWindow,
		Item: input.Item, Tabs: input.Tabs, Positions: input.Positions,
		SourceWorkspace: input.SourceWorkspace, SourceBeforeWorkspace: before, SourceOpenTargets: input.SourceOpenTargets,
		Placement: input.Placement, ScreenPosition: input.ScreenPosition, TargetBeforeWorkspace: targetBefore,
		CreatedAt: config.NowTS(),
	}
	if err := persistTransfer(snapshot); err != nil {
		transferState.mu.Unlock()
		return nil, err
	}
	transferState.transfers[token] = snapshot
	clone := *snapshot
	transferState.mu.Unlock()
	if newWindow {
		if err := createTransferWindow(&clone); err != nil {
			_, _ = cancelTransfer(token)
			return nil, err
		}
	} else {
		broadcastTo("nomo://tab-transfer-prepared", &clone, target)
	}
	go func() {
		time.Sleep(transferTimeout)
		if _, err := cancelTransfer(token); err != nil {
			applog.Warn("TabTransfer", "取消超时移交失败："+err.Error())
		}
	}()
	return &clone, nil
}

// createTransferWindow 在拖放位置创建接收标签的新窗口（隐藏，页面就绪后由 refresh_window_menu 显示）。
func createTransferWindow(snapshot *transferSnapshot) error {
	var position *mygo.Point
	if point := snapshot.ScreenPosition; point != nil {
		position = &mygo.Point{X: point.X - 80, Y: point.Y - 24}
		if scale := mygo.Screen.DisplayNearestPoint(mygo.Point{X: point.X, Y: point.Y}).ScaleFactor; scale > 0 {
			// 屏幕坐标来自原生光标位置（物理像素），窗口位置以 DIP 计。
			position = &mygo.Point{X: int(float64(point.X)/scale) - 80, Y: int(float64(point.Y)/scale) - 24}
		}
	}
	if _, err := createDocumentWindow(snapshot.TargetWindowLabel, true, position); err != nil {
		return fmt.Errorf("创建标签窗口失败：%w", err)
	}
	return nil
}

// transferForCaller 返回调用窗口参与的移交副本；调用方持有 transferState.mu。
func transferForCaller(token, caller string) (*transferSnapshot, error) {
	transfer, ok := transferState.transfers[token]
	if !ok {
		return nil, errors.New("标签移交记录不存在")
	}
	if caller != transfer.SourceWindowLabel && caller != transfer.TargetWindowLabel {
		return nil, errors.New("当前窗口无权访问该移交")
	}
	clone := *transfer
	return &clone, nil
}

func targetReadyTabTransfer(caller, token string, targetWorkspace map[string]any, targetOpenTargets transferOpenTargets) (*transferSnapshot, error) {
	if err := validateWorkspace(targetWorkspace); err != nil {
		return nil, err
	}
	if err := validateOpenTargets(targetWorkspace, targetOpenTargets); err != nil {
		return nil, err
	}
	transferState.mu.Lock()
	transfer, err := transferForCaller(token, caller)
	if err != nil {
		transferState.mu.Unlock()
		return nil, err
	}
	fail := func(message string) (*transferSnapshot, error) {
		transferState.mu.Unlock()
		return nil, errors.New(message)
	}
	if transfer.TargetWindowLabel != caller {
		return fail("只有目标窗口可以确认接收")
	}
	if transfer.Status != "prepared" {
		transferState.mu.Unlock()
		return transfer, nil
	}
	if !transfer.NewWindow {
		before, ok, err := readWorkspaceSetting(transfer.TargetWindowLabel)
		if !ok {
			return fail("目标原工作区尚未保存")
		}
		if err != nil {
			return fail("目标原工作区无效：" + err.Error())
		}
		if err := validateWorkspace(before); err != nil {
			return fail(err.Error())
		}
		transfer.TargetBeforeWorkspace = before
	}
	tabs, _ := workspaceTabs(targetWorkspace)
	expected := tabIDs(transfer.Tabs)
	if before := transfer.TargetBeforeWorkspace; before != nil {
		beforeTabs, err := workspaceTabs(before)
		if err != nil {
			return fail(err.Error())
		}
		for id := range tabIDs(beforeTabs) {
			expected[id] = true
		}
		beforeFolder, _ := json.Marshal(before["currentFolderPath"])
		targetFolder, _ := json.Marshal(targetWorkspace["currentFolderPath"])
		if string(beforeFolder) != string(targetFolder) {
			return fail("移交不能改变已有目标的文件夹上下文")
		}
	}
	if !sameLabelSet(expected, tabIDs(tabs)) {
		return fail("目标工作区包含额外或缺失的文档")
	}
	var migrated []map[string]any
	for _, tab := range transfer.Tabs {
		id, ok := jsonString(tab, "id")
		if !ok {
			return fail("移交文档 ID 无效")
		}
		var targetTab map[string]any
		for _, candidate := range tabs {
			candidateID, _ := jsonString(candidate, "id")
			kind, _ := jsonString(candidate, "documentKind")
			if candidateID == id && kind == "markdown" {
				targetTab = candidate
				break
			}
		}
		if targetTab == nil {
			return fail("目标工作区未完整接收移交文档")
		}
		sourceKey, sourceOK := nativePathKey(tab)
		targetKey, targetOK := nativePathKey(targetTab)
		if sourceOK != targetOK || sourceKey != targetKey {
			return fail("目标工作区改变了迁移文档的文件路径")
		}
		migrated = append(migrated, targetTab)
	}
	incomingIDs := tabIDs(transfer.Tabs)
	var existing []map[string]any
	for _, tab := range tabs {
		if id, _ := jsonString(tab, "id"); !incomingIDs[id] {
			existing = append(existing, tab)
		}
	}
	if err := validateIncomingPaths(migrated, existing); err != nil {
		return fail(err.Error())
	}
	if jsonUint(targetWorkspace["ownershipEpoch"]) != transfer.TargetEpoch {
		return fail("目标工作区快照已过期")
	}
	targetItems, ok := asObjects(targetWorkspace["items"])
	if !ok {
		return fail("目标工作区项缺失")
	}
	members, err := itemMembers(transfer.Item)
	if err != nil {
		return fail(err.Error())
	}
	switch transfer.Placement.Kind {
	case "combine":
		if transfer.TargetBeforeWorkspace == nil {
			return fail("新窗口不能与既有标签组合")
		}
		beforeItems, _ := asObjects(transfer.TargetBeforeWorkspace["items"])
		var beforeTarget map[string]any
		for _, item := range beforeItems {
			if id, _ := jsonString(item, "id"); id == transfer.Placement.TargetItemID {
				beforeTarget = item
				break
			}
		}
		if beforeTarget == nil {
			return fail("目标标签已不存在")
		}
		if kind, _ := jsonString(beforeTarget, "kind"); kind != "single" {
			return fail("不能嵌套组合")
		}
		beforeMembers, err := itemMembers(beforeTarget)
		if err != nil {
			return fail(err.Error())
		}
		combined := false
		for _, item := range targetItems {
			kind, _ := jsonString(item, "kind")
			left, _ := jsonString(item, "leftTabId")
			right, _ := jsonString(item, "rightTabId")
			combined = combined || kind == "comparison" && left == beforeMembers[0] && right == members[0]
		}
		if !combined {
			return fail("目标未按左右顺序组成双文档标签")
		}
	default:
		index := 0
		if transfer.Placement.InsertionIndex != nil {
			index = *transfer.Placement.InsertionIndex
		}
		index = min(index, max(len(targetItems)-1, 0))
		if index >= len(targetItems) {
			return fail("目标缺少迁入工作区项")
		}
		actual, err := itemMembers(targetItems[index])
		if err != nil {
			return fail(err.Error())
		}
		if strings.Join(actual, "\x00") != strings.Join(members, "\x00") {
			return fail("目标插入位置或组合成员错误")
		}
	}
	transfer.TargetWorkspace = targetWorkspace
	transfer.TargetOpenTargets = &targetOpenTargets
	transfer.Status = "target-ready"
	if err := persistTransfer(transfer); err != nil {
		transferState.mu.Unlock()
		return nil, err
	}
	transferState.transfers[token] = transfer
	clone := *transfer
	transferState.mu.Unlock()
	emitTransferParticipants("nomo://tab-transfer-ready", &clone)
	return &clone, nil
}

func commitTabTransfer(caller, token string) (*transferSnapshot, error) {
	transferState.mu.Lock()
	transfer, err := transferForCaller(token, caller)
	if err != nil {
		transferState.mu.Unlock()
		return nil, err
	}
	fail := func(message string) (*transferSnapshot, error) {
		transferState.mu.Unlock()
		return nil, errors.New(message)
	}
	if transfer.SourceWindowLabel != caller {
		return fail("只有源窗口可以提交移交")
	}
	if transfer.Status == "committed" {
		transferState.mu.Unlock()
		emitTransferParticipants("nomo://tab-transfer-committed", transfer)
		return transfer, nil
	}
	if transfer.Status != "target-ready" {
		return fail("目标尚未准备好接收")
	}
	if windows.get(transfer.SourceWindowLabel) == nil || windows.get(transfer.TargetWindowLabel) == nil {
		return fail("移交窗口已关闭")
	}
	for label, epoch := range map[string]uint64{transfer.SourceWindowLabel: transfer.SourceEpoch, transfer.TargetWindowLabel: transfer.TargetEpoch} {
		current := uint64(0)
		if stamp, ok := transferState.stamps[label]; ok {
			current = stamp.epoch
		}
		if current != epoch {
			return fail("移交工作区归属已变化")
		}
	}
	if transfer.TargetWorkspace == nil {
		return fail("目标快照缺失")
	}
	if transfer.TargetOpenTargets == nil {
		return fail("目标文件归属缺失")
	}
	transfer.SourceEpoch++
	transfer.TargetEpoch++
	sourceWorkspace := cloneObject(transfer.SourceWorkspace)
	sourceWorkspace["ownershipEpoch"] = float64(transfer.SourceEpoch)
	sourceWorkspace["revision"] = float64(0)
	sourceWorkspace["writerWindowLabel"] = transfer.SourceWindowLabel
	targetWorkspace := cloneObject(transfer.TargetWorkspace)
	targetWorkspace["ownershipEpoch"] = float64(transfer.TargetEpoch)
	targetWorkspace["revision"] = float64(0)
	targetWorkspace["writerWindowLabel"] = transfer.TargetWindowLabel
	transfer.SourceWorkspace = sourceWorkspace
	transfer.TargetWorkspace = targetWorkspace
	transfer.Status = "committed"
	sourceTargets := transfer.SourceOpenTargets
	if tabs, _ := workspaceTabs(sourceWorkspace); len(tabs) == 0 {
		// 空源窗口即将关闭，不再接收指向原文件夹的外部打开；恢复快照仍保留目录。
		sourceTargets.FolderPath = nil
	}
	err = openTargets.commitTransfer(transfer.SourceWindowLabel, transfer.TargetWindowLabel, sourceTargets, *transfer.TargetOpenTargets, func() error {
		return app.config.Update(func(c *config.AppConfig) {
			putWorkspaceSnapshot(c, transfer.SourceWindowLabel, sourceWorkspace)
			putWorkspaceSnapshot(c, transfer.TargetWindowLabel, targetWorkspace)
			putJSONSetting(c, transferJournalPrefix+transfer.Token, transfer)
			// 程序崩溃时仍可由一份启动工作区恢复完整的迁移文档。
			putJSONSetting(c, startupWorkspaceKey, targetWorkspace)
		})
	})
	if err != nil {
		transferState.mu.Unlock()
		return nil, err
	}
	transferState.stamps[transfer.SourceWindowLabel] = &windowStamp{epoch: transfer.SourceEpoch}
	transferState.stamps[transfer.TargetWindowLabel] = &windowStamp{epoch: transfer.TargetEpoch}
	transferState.transfers[token] = transfer
	clone := *transfer
	transferState.mu.Unlock()
	emitTransferParticipants("nomo://tab-transfer-committed", &clone)
	if target := windows.get(clone.TargetWindowLabel); target != nil {
		target.win.Focus()
	}
	return &clone, nil
}

func cloneObject(object map[string]any) map[string]any {
	clone := make(map[string]any, len(object))
	for key, value := range object {
		clone[key] = value
	}
	return clone
}

// cancelTransfer 取消未完成的移交；新窗口直接销毁（内容尚未提交，不触发文档确认）。
// 磁盘写入失败不能把源窗口永久锁在 pending：内存已回滚，旧记录留待重启恢复。
func cancelTransfer(token string) (*transferSnapshot, error) {
	transferState.mu.Lock()
	transfer, ok := transferState.transfers[token]
	if !ok {
		transferState.mu.Unlock()
		return nil, errors.New("标签移交记录不存在")
	}
	if transfer.Status == "committed" || transfer.Status == "cancelled" {
		clone := *transfer
		transferState.mu.Unlock()
		return &clone, nil
	}
	transfer.Status = "cancelled"
	persistErr := persistTransfer(transfer)
	clone := *transfer
	transferState.mu.Unlock()
	emitTransferParticipants("nomo://tab-transfer-cancelled", &clone)
	if clone.NewWindow {
		if target := windows.get(clone.TargetWindowLabel); target != nil {
			forgetClose(clone.TargetWindowLabel)
			target.win.Destroy()
		}
	}
	if persistErr != nil {
		broadcast("nomo://workspace-error", persistErr.Error())
		return nil, persistErr
	}
	return &clone, nil
}

// closeEmptyTransferredWindow 关闭移交后已空的源窗口：跳过文档确认，但先完成设置窗口 owner 握手。
func closeEmptyTransferredWindow(window *appWindow, token string) error {
	transferState.mu.Lock()
	transfer, err := transferForCaller(token, window.label)
	transferState.mu.Unlock()
	if err != nil {
		return err
	}
	tabs, err := workspaceTabs(transfer.SourceWorkspace)
	if err != nil {
		return err
	}
	if transfer.Status != "committed" || transfer.SourceWindowLabel != window.label || len(tabs) != 0 {
		return errors.New("仅允许关闭已移空的源窗口")
	}
	forgetClose(window.label)
	deferred, err := requestSettingsCloseBeforeOwner(window.label)
	if err != nil {
		return err
	}
	if !deferred {
		allowNextClose(window.label)
		window.win.Close()
	}
	deadline := time.Now().Add(30 * time.Second)
	for windows.get(window.label) != nil {
		if time.Now().After(deadline) {
			return errors.New("等待移空窗口关闭超时，请检查偏好设置保存状态")
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

// recoverTransferJournals 在启动时回滚未完成的移交：源快照仍包含迁移文档，选一份有效的作为启动工作区。
func recoverTransferJournals() error {
	var pending []*transferSnapshot
	app.config.Read(func(c *config.AppConfig) {
		for key, record := range c.App.Settings {
			if !strings.HasPrefix(key, transferJournalPrefix) {
				continue
			}
			var transfer transferSnapshot
			if json.Unmarshal([]byte(record.ValueJSON), &transfer) == nil && (transfer.Status == "prepared" || transfer.Status == "target-ready") {
				pending = append(pending, &transfer)
			}
		}
	})
	if len(pending) == 0 {
		return nil
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].CreatedAt != pending[j].CreatedAt {
			return pending[i].CreatedAt < pending[j].CreatedAt
		}
		return pending[i].Token < pending[j].Token
	})
	return app.config.Update(func(c *config.AppConfig) {
		for _, transfer := range pending {
			transfer.Status = "cancelled"
			// 最新源快照的归属代数已变化时不恢复；有效时采用它；否则回退到移交前快照。
			var recovered, latest map[string]any
			record, hasLatest := c.App.Settings["workspaceTabs:"+transfer.SourceWindowLabel]
			hasLatest = hasLatest && json.Unmarshal([]byte(record.ValueJSON), &latest) == nil
			switch {
			case hasLatest && jsonUint(latest["ownershipEpoch"]) != transfer.SourceEpoch:
			case hasLatest && validateWorkspace(latest) == nil:
				recovered = latest
			case validateWorkspace(transfer.SourceBeforeWorkspace) == nil:
				recovered = transfer.SourceBeforeWorkspace
			}
			if recovered != nil {
				recovered = cloneObject(recovered)
				recovered["ownershipEpoch"] = float64(0)
				recovered["revision"] = float64(0)
				putJSONSetting(c, startupWorkspaceKey, recovered)
			}
			putJSONSetting(c, transferJournalPrefix+transfer.Token, transfer)
		}
	})
}

// forgetTabTransferWindow 在窗口销毁时取消其参与的未完成移交。
func forgetTabTransferWindow(label string) {
	transferState.mu.Lock()
	var tokens []string
	for token, transfer := range transferState.transfers {
		if (transfer.Status == "prepared" || transfer.Status == "target-ready") && (transfer.SourceWindowLabel == label || transfer.TargetWindowLabel == label) {
			tokens = append(tokens, token)
		}
	}
	transferState.mu.Unlock()
	for _, token := range tokens {
		_, _ = cancelTransfer(token)
	}
}

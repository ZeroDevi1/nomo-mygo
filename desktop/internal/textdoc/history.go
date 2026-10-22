package textdoc

// historyCapacityBytes 是撤销历史的软上限（按编辑字节计权）：最新事务即便自身超限也保留，
// 保证 replace-all 仍可撤销；不以裁剪用户撤销历史替代缓存治理。
const historyCapacityBytes = 64 * 1024 * 1024

// historyOperation 是撤销或重做时要执行的操作：一组编辑，或整文替换为某个文件。
type historyOperation struct {
	edits       []Edit
	replacement *journalReplacementFile
}

type historyEntry struct {
	undo   historyOperation
	redo   historyOperation
	weight uint64
}

// historyState 是会话的撤销/重做栈；ownedAssets 记录曾由历史引用过的替换文件，
// 不再被历史与日志引用时才删除。由会话 state 锁保护。
type historyState struct {
	undo        []historyEntry
	redo        []historyEntry
	bytes       uint64
	ownedAssets map[string]struct{}
}

func (h *historyState) pushTransaction(undo, redo historyOperation, weight uint64) {
	h.clearRedo()
	if h.ownedAssets == nil {
		h.ownedAssets = map[string]struct{}{}
	}
	for _, operation := range []historyOperation{undo, redo} {
		if operation.replacement != nil {
			h.ownedAssets[operation.replacement.Path] = struct{}{}
		}
	}
	h.bytes += weight
	h.undo = append(h.undo, historyEntry{undo: undo, redo: redo, weight: weight})
	for h.bytes > historyCapacityBytes && len(h.undo) > 1 {
		h.bytes -= min(h.bytes, h.undo[0].weight)
		h.undo = h.undo[1:]
	}
}

func (h *historyState) clearRedo() {
	for _, entry := range h.redo {
		h.bytes -= min(h.bytes, entry.weight)
	}
	h.redo = nil
}

func (h *historyState) canUndo() bool { return len(h.undo) > 0 }
func (h *historyState) canRedo() bool { return len(h.redo) > 0 }

func (h *historyState) protectedAssets() map[string]struct{} {
	assets := map[string]struct{}{}
	for _, stack := range [][]historyEntry{h.undo, h.redo} {
		for _, entry := range stack {
			for _, operation := range []historyOperation{entry.undo, entry.redo} {
				if operation.replacement != nil {
					assets[operation.replacement.Path] = struct{}{}
				}
			}
		}
	}
	return assets
}

func (h *historyState) cleanupCandidates(journalAssets map[string]struct{}) []string {
	protected := h.protectedAssets()
	var candidates []string
	for path := range h.ownedAssets {
		_, inHistory := protected[path]
		_, inJournal := journalAssets[path]
		if !inHistory && !inJournal {
			candidates = append(candidates, path)
		}
	}
	return candidates
}

func (h *historyState) cleanupCandidatesAfterHistoryDrop(journalAssets map[string]struct{}) []string {
	var candidates []string
	for path := range h.ownedAssets {
		if _, inJournal := journalAssets[path]; !inJournal {
			candidates = append(candidates, path)
		}
	}
	return candidates
}

func (h *historyState) forgetAssets(assets []string) {
	for _, asset := range assets {
		delete(h.ownedAssets, asset)
	}
}

func (h *historyState) allAssets() map[string]struct{} {
	all := h.protectedAssets()
	for path := range h.ownedAssets {
		all[path] = struct{}{}
	}
	return all
}

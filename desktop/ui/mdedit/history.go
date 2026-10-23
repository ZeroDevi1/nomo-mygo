package mdedit

// editKind 决定相邻编辑能否合并为一步撤销。
type editKind uint8

const (
	editOther editKind = iota
	editTyping
	editDelete
	editCompose
)

// step 是一步撤销：原文 [from, from+len(new)) 当前为 new，撤销时换回 old。
type step struct {
	from     int
	old, new string
	before   Selection
	after    Selection
	kind     editKind
	linked   bool // 与栈中下一步（更早的一步）一起撤销、重做（一个命令改动两处原文时，见 linkLast）
}

// history 保存完整撤销栈，不做裁剪（见 AGENTS.md：不得以裁剪撤销历史替代缓存治理）。
// open 为 true 时栈顶一步仍可与后续同类编辑合并。
type history struct {
	undo, redo []step
	open       bool
}

// record 记录一次已应用的替换：原文 [from, from+len(old)) 已由 old 换成 text。
// 合并规则：连续键入（插入点紧接上一步末尾）、连续向后删除、输入法组字
// （替换上一步插入的全部内容）合并为一步。
func (h *history) record(kind editKind, from int, old, text string, before, after Selection) {
	h.redo = h.redo[:0]
	if h.open && len(h.undo) > 0 {
		top := &h.undo[len(h.undo)-1]
		switch {
		case kind == editTyping && top.kind == editTyping && old == "" && from == top.from+len(top.new):
			top.new += text
			top.after = after
			return
		case kind == editDelete && top.kind == editDelete && text == "" && top.new == "" && from+len(old) == top.from:
			top.from = from
			top.old = old + top.old
			top.after = after
			return
		case kind == editCompose && top.kind == editCompose && from == top.from && old == top.new:
			top.new = text
			top.after = after
			return
		}
	}
	h.undo = append(h.undo, step{from: from, old: old, new: text, before: before, after: after, kind: kind})
	h.open = kind != editOther
}

// seal 结束当前可合并的一步，使后续编辑另起一步。
func (h *history) seal() { h.open = false }

// linkLast 把栈顶一步与它下面的一步连成一次撤销（Editor.undo 连续撤销、重做相连的步骤）。
func (h *history) linkLast() {
	if n := len(h.undo); n >= 2 {
		h.undo[n-1].linked = true
	}
	h.open = false
}

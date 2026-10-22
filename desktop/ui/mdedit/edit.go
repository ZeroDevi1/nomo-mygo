package mdedit

// Edit 是一次已应用的原文替换，供视图按块失效布局。
type Edit struct {
	Change
	Applied bool
}

// Replace 把 [from, to) 替换为 text，光标置于插入内容之后，并记入撤销历史。
func (d *Document) Replace(from, to int, text string, kind editKind) Edit {
	src := d.Source()
	from, to = clamp(from, 0, len(src)), clamp(to, 0, len(src))
	if from > to {
		from, to = to, from
	}
	old := src[from:to]
	if old == text {
		return Edit{}
	}
	// old 是旧原文的子串；复制一份，避免撤销栈长期持有整篇旧原文。
	old = cloneString(old)
	before := d.Sel
	ch := d.replace(from, to, text)
	d.Sel = Selection{Anchor: from + len(text), Head: from + len(text)}
	d.hist.record(kind, from, old, text, before, d.Sel)
	return Edit{Change: ch, Applied: true}
}

// SetSelection 移动选区并结束可合并的撤销步。
func (d *Document) SetSelection(s Selection) {
	n := len(d.Source())
	s.Anchor, s.Head = clamp(s.Anchor, 0, n), clamp(s.Head, 0, n)
	if s != d.Sel {
		d.hist.seal()
	}
	d.Sel = s
}

// SetMarked 用 text 替换 [from, to) 并把它标记为组字区间；selected 为组字内的
// 字节区间。组字期间的替换合并为一步撤销，提交后与被替换内容一起撤销。
func (d *Document) SetMarked(from, to int, text string, selA, selB int) Edit {
	if !d.hasMarked {
		d.hist.seal()
	}
	e := d.Replace(from, to, text, editCompose)
	if !e.Applied && from == to && text == "" {
		d.hasMarked = false
		return e
	}
	d.marked = Selection{Anchor: from, Head: from + len(text)}
	d.hasMarked = text != ""
	d.Sel = Selection{Anchor: from + clamp(selA, 0, len(text)), Head: from + clamp(selB, 0, len(text))}
	if n := len(d.hist.undo); n > 0 {
		d.hist.undo[n-1].after = d.Sel
	}
	return e
}

// CommitMarked 用 text 替换组字区间（或 [from, to)）并结束组字。
func (d *Document) CommitMarked(from, to int, text string) Edit {
	kind := editTyping
	if d.hasMarked {
		kind = editCompose
	}
	e := d.Replace(from, to, text, kind)
	if kind == editCompose {
		d.hist.seal()
	}
	d.hasMarked = false
	return e
}

// Unmark 结束组字，保留已写入的文本。
func (d *Document) Unmark() {
	if d.hasMarked {
		d.hist.seal()
	}
	d.hasMarked = false
}

// Undo 撤销一步，返回是否有变化。组字中的撤销先结束组字。
func (d *Document) Undo() (Edit, bool) {
	d.hasMarked = false
	h := &d.hist
	if len(h.undo) == 0 {
		return Edit{}, false
	}
	s := h.undo[len(h.undo)-1]
	h.undo = h.undo[:len(h.undo)-1]
	ch := d.replace(s.from, s.from+len(s.new), s.old)
	d.Sel = s.before
	h.redo = append(h.redo, s)
	h.open = false
	return Edit{Change: ch, Applied: true}, true
}

// Redo 重做一步。
func (d *Document) Redo() (Edit, bool) {
	d.hasMarked = false
	h := &d.hist
	if len(h.redo) == 0 {
		return Edit{}, false
	}
	s := h.redo[len(h.redo)-1]
	h.redo = h.redo[:len(h.redo)-1]
	ch := d.replace(s.from, s.from+len(s.old), s.new)
	d.Sel = s.after
	h.undo = append(h.undo, s)
	h.open = false
	return Edit{Change: ch, Applied: true}, true
}

func cloneString(s string) string {
	if s == "" {
		return ""
	}
	return string(append([]byte(nil), s...))
}

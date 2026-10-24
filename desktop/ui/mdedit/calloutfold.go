package mdedit

import "github.com/egoist/mygo/ui"

// 提示块折叠（> [!TYPE]+ / > [!TYPE]-，同 Obsidian）：“-”默认收起、“+”默认展开，点标题行切换；切换是临时显示
// 状态，不写入原文（同折叠块）。收起时正文各行高度与间距为 0，只留标题行；光标进入提示块时自动展开，方向键与
// 查找不会落进看不见的行。状态以引用块起点的绝对偏移为键，随编辑平移（remapFolds）。

// calloutOpen 报告顶层块 top 中的提示块 f 当前是否展开。
func (e *Editor) calloutOpen(top Block, f *frame) bool {
	if f.kind != frameCallout || f.callout.fold == 0 {
		return true
	}
	a, z := top.Start+f.keyRel, top.Start+f.endRel
	if h := e.Doc.Sel.Head; h >= a && h <= z {
		return true
	}
	if v, ok := e.calloutFolds[a]; ok {
		return v
	}
	return f.callout.fold == '+'
}

// calloutRowHidden 报告块 i 的第 k 行是否因所在提示块收起而隐藏。
func (e *Editor) calloutRowHidden(i int, en *entry, k int) bool {
	top := e.Doc.blocks[i]
	for fi := range en.frames {
		f := &en.frames[fi]
		if f.kind == frameCallout && f.callout.fold != 0 && k >= f.first && k <= f.last && !e.calloutOpen(top, f) {
			return true
		}
	}
	return false
}

// calloutToggleHit 报告 pt 是否落在可折叠提示块的标题行上，返回块与框架下标。
func (e *Editor) calloutToggleHit(pt ui.Point) (int, int, bool) {
	e.sync()
	for _, v := range e.vblocks {
		en := &e.entries[v.i]
		for fi := range en.frames {
			f := &en.frames[fi]
			if f.kind != frameCallout || f.callout.fold == 0 {
				continue
			}
			titleY := v.top + en.rows[f.first].y - calloutTitleH - quotePad + 8
			x := e.left() + f.indent
			if pt.Y >= titleY && pt.Y < titleY+calloutTitleH && pt.X >= x && pt.X < x+e.width()-f.indent-f.rinset {
				return v.i, fi, true
			}
		}
	}
	return 0, 0, false
}

// toggleCallout 切换提示块的展开状态；收起时光标若在块内，移到块后，以免收起后立即因光标在内而展开。
func (e *Editor) toggleCallout(i, fi int) {
	en := &e.entries[i]
	f := &en.frames[fi]
	top := e.Doc.blocks[i]
	open := e.calloutOpen(top, f)
	if e.calloutFolds == nil {
		e.calloutFolds = map[int]bool{}
	}
	a, z := top.Start+f.keyRel, top.Start+f.endRel
	e.calloutFolds[a] = !open
	if open {
		if h := e.Doc.Sel.Head; h >= a && h <= z {
			after := min(z+1, len(e.Doc.Source()))
			e.Doc.Sel = Selection{Anchor: after, Head: after}
		}
	}
	for k := f.first; k <= f.last; k++ {
		en.rows[k].gen = 0 // 强制重建：隐藏状态参与签名
	}
	en.ydirty = true
	e.dirty = true
}

// remapCalloutFolds 按一次原文替换平移提示块的折叠状态；被替换区间内的状态丢弃。
func (e *Editor) remapCalloutFolds(ch Change) {
	if len(e.calloutFolds) == 0 {
		return
	}
	next := make(map[int]bool, len(e.calloutFolds))
	for k, v := range e.calloutFolds {
		switch {
		case k < ch.At:
			next[k] = v
		case k >= ch.At+ch.OldLen:
			next[k+ch.NewLen-ch.OldLen] = v
		}
	}
	e.calloutFolds = next
}

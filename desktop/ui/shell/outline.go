package shell

import "nomo/desktop/ui/mdedit"

// outlineExpanded 报告大纲中标题 i 是否展开：默认展开级别小于设置层级（outlineDefaultExpandLevel）的标题，
// 点击箭头后与默认相反。
func (s *Shell) outlineExpanded(tab *Tab, hs []mdedit.Heading, i int) bool {
	def := float64(hs[i].Level) < s.prefs.OutlineDefaultExpandLevel
	return def != tab.outFold[i]
}

// outlineRows 返回大纲中可见的标题序号：所有上级标题都展开时可见（原版大纲树按默认层级展开）。按大纲版本、
// 折叠状态与默认层级缓存，每帧 O(1)，变化后 O(标题数)。
func (s *Shell) outlineRows(tab *Tab, hs []mdedit.Heading) []int {
	if len(tab.outFold) > 0 && len(tab.outline) != tab.outFoldCount {
		tab.outFold, tab.outFoldGen = nil, tab.outFoldGen+1 // 标题增删后序号对不上，回到默认
	}
	tab.outFoldCount = len(hs)
	key := [3]uint64{tab.outVer, tab.outFoldGen, uint64(s.prefs.OutlineDefaultExpandLevel)}
	if tab.outRows != nil && tab.outRowsKey == key {
		return tab.outRows
	}
	rows := tab.outRows[:0]
	type anc struct {
		level int
		open  bool
	}
	var stack []anc
	for i, h := range hs {
		for len(stack) > 0 && stack[len(stack)-1].level >= h.Level {
			stack = stack[:len(stack)-1]
		}
		visible := true
		for _, a := range stack {
			visible = visible && a.open
		}
		if visible {
			rows = append(rows, i)
		}
		stack = append(stack, anc{h.Level, visible && s.outlineExpanded(tab, hs, i)})
	}
	if rows == nil {
		rows = []int{}
	}
	tab.outRows, tab.outRowsKey = rows, key
	return rows
}

// toggleOutline 切换大纲中标题 i 的展开状态。
func (s *Shell) toggleOutlineItem(tab *Tab, i int) {
	if tab.outFold == nil {
		tab.outFold = map[int]bool{}
	}
	tab.outFold[i] = !tab.outFold[i]
	tab.outFoldGen++
}

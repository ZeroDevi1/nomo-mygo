package shell

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// 组合标签（原版 workspaceItems 的 comparison）：两篇 Markdown 文档合为一个标签，左右并排对照。组合关系记在两个
// Tab 上（partner 为对方 ID，left 表示位于左侧，左侧成员的 comparePct 为左栏宽度百分比 20–80）；标签栏把一组
// 画成一个标签，内容区并排显示两篇文档，键盘焦点所在的一篇为活动文档（工具栏、大纲、状态栏、保存都作用于它）。
// 组合期间不使用双栏模式；关闭其中一篇时组合自动解散。原生版不持久化组合关系（重启后各自为独立标签）。

// comparisonOf 返回 tab 所在组合的左右成员；不在组合中时 ok 为 false。
func (s *Shell) comparisonOf(tab *Tab) (left, right *Tab, ok bool) {
	if tab == nil || tab.partner == 0 {
		return nil, nil, false
	}
	other := s.tabWithID(tab.partner)
	if other == nil || other.partner != tab.ID {
		tab.partner = 0
		return nil, nil, false
	}
	if tab.left {
		return tab, other, true
	}
	return other, tab, true
}

// combine 把 a 与 b 组合（a 在左），并激活 b（与原版 createComparisonWorkspaceItem 的 focusedTabId 一致）。
func (s *Shell) combine(a, b *Tab) {
	if a == nil || b == nil || a == b || a.partner != 0 || b.partner != 0 || a.Kind() != kindMarkdown || b.Kind() != kindMarkdown {
		return
	}
	a.partner, b.partner, a.left, b.left, a.comparePct = b.ID, a.ID, true, false, 50
	// 组合成员在标签栏中相邻：右侧成员移到左侧成员之后。
	ai := s.findTabIndex(a.ID)
	bi := s.findTabIndex(b.ID)
	moved := s.tabs[bi]
	s.tabs = append(s.tabs[:bi], s.tabs[bi+1:]...)
	if bi < ai {
		ai--
	}
	s.tabs = append(s.tabs[:ai+1], append([]*Tab{moved}, s.tabs[ai+1:]...)...)
	s.active = ai + 1
	s.focusEditor = true
	s.updateTitle()
}

// ungroup 解散 tab 所在的组合。
func (s *Shell) ungroup(tab *Tab) {
	if left, right, ok := s.comparisonOf(tab); ok {
		left.partner, right.partner, left.left, right.left = 0, 0, false, false
	}
}

// swapComparison 交换组合的左右两侧。
func (s *Shell) swapComparison(tab *Tab) {
	left, right, ok := s.comparisonOf(tab)
	if !ok {
		return
	}
	activeID := s.activeTabID() // 交换位置前记下，活动文档不随位置改变
	right.left, left.left = true, false
	right.comparePct = 100 - left.comparePct
	li, ri := s.findTabIndex(left.ID), s.findTabIndex(right.ID)
	s.tabs[li], s.tabs[ri] = s.tabs[ri], s.tabs[li]
	s.active = s.findTabIndex(activeID)
}

func fmtKey(parts ...any) string { return fmt.Sprint(parts...) }

func (s *Shell) findTabIndex(id int) int {
	for i, t := range s.tabs {
		if t.ID == id {
			return i
		}
	}
	return -1
}

func (s *Shell) activeTabID() int {
	if t := s.activeTab(); t != nil {
		return t.ID
	}
	return 0
}

// comparisonView 画组合标签的内容区：左右两篇文档各按当前模式（语义或源码）显示；返回活动文档的语义编辑区
// 元素（快捷插入、表情建议以它定位；源码模式时为零值）。
func (s *Shell) comparisonView(c *ui.Context, left, right *Tab) (active ui.Element) {
	pct := left.comparePct
	if pct == 0 {
		pct = 50
	}
	var els [2]ui.Element
	pane := func(k int, t *Tab) func() {
		return func() {
			ck := c.Key(fmtKey("cmp", t.ID, s.mode()))
			ui.Column(ck).Fill().Children(func() {
				// 标题行：哪一侧是活动文档一目了然。
				ui.Row(ck).FillWidth().Height(26).Padding(0, 12).AlignItems(ui.Center).Children(func() {
					txt := ui.Text(ck, t.Name(s.untitledName)).FontSize(11).SingleLine()
					if t != s.activeTab() {
						txt.TextColor(s.ch.muted)
					} else {
						txt.Bold()
					}
				})
				if s.mode() == modeSource {
					ui.Row(ck).Grow(1).FillWidth().Padding(8, 0, 0, 12).Children(func() {
						st := t.Source()
						if st.OnCompletion == nil {
							st.OnCompletion = func(req editor.CompletionRequest) { s.sourceEmojiCompletion(t, req) }
						}
						if st.Focused() {
							s.focusComparisonMember(t)
						}
						editor.View(ck, st).Fill()
					})
					return
				}
				els[k] = s.editorView(ck, t)
				if t.Ed.Focused() {
					s.focusComparisonMember(t)
				}
			})
		}
	}
	// 两侧原文都与各自的源码缓冲同步（源码模式下编辑的是缓冲）。
	for _, t := range []*Tab{left, right} {
		s.applyEditorStyle(t)
		if s.mode() == modeSource {
			t.syncToSource()
			t.syncFromSource()
		}
	}
	sp := ui.SplitPercent(c, &pct, pane(0, left), pane(1, right)).Fill()
	if sp.Changed() {
		left.comparePct = float32(clampF64(float64(pct), 20, 80))
	}
	if s.activeTab() == left {
		return els[0]
	}
	return els[1]
}

// focusComparisonMember 把焦点所在的组合成员设为活动文档；不释放另一篇的布局（两篇都在显示）。
func (s *Shell) focusComparisonMember(t *Tab) {
	if i := s.findTabIndex(t.ID); i >= 0 && i != s.active {
		s.active = i
		s.updateTitle()
	}
}

// comparisonMenu 在标签右键菜单中加入组合相关项（原版 getWorkspaceItemActions）。
func (s *Shell) comparisonMenu(m *ui.Menu, tab *Tab) {
	if tab.Kind() != kindMarkdown {
		return
	}
	if _, _, ok := s.comparisonOf(tab); ok {
		if m.Item(s.tr("swapComparisonSides")).Chosen() {
			s.swapComparison(tab)
		}
		if m.Item(s.tr("ungroupComparison")).Chosen() {
			s.ungroup(tab)
		}
		m.Separator()
		return
	}
	var candidates []*Tab
	for _, t := range s.tabs {
		if t != tab && t.partner == 0 && t.Kind() == kindMarkdown {
			candidates = append(candidates, t)
		}
	}
	m.Submenu(s.tr("combineWithTab"), func(sub *ui.Menu) {
		for _, t := range candidates {
			if sub.Item(t.Name(s.untitledName)).Chosen() {
				s.combine(tab, t)
			}
		}
	}).Disabled(len(candidates) == 0)
	m.Separator()
}

// tabLabel 返回标签栏中标签的文字：组合标签显示“左 | 右”。
func (s *Shell) tabLabel(tab *Tab) string {
	if left, right, ok := s.comparisonOf(tab); ok {
		return left.Name(s.untitledName) + " | " + right.Name(s.untitledName)
	}
	return tab.Name(s.untitledName)
}

// tabHidden 报告标签是否不单独画在标签栏中（组合的右侧成员与左侧成员画成一个标签）。
func (s *Shell) tabHidden(tab *Tab) bool {
	_, right, ok := s.comparisonOf(tab)
	return ok && right == tab
}

// tabSelected 报告标签项是否为当前标签（组合标签任一成员为活动文档时都算）。
func (s *Shell) tabSelected(tab *Tab) bool {
	a := s.activeTab()
	if a == tab {
		return true
	}
	if left, right, ok := s.comparisonOf(tab); ok {
		return a == left || a == right
	}
	return false
}

// tabIDsOf 返回关闭标签项时要关闭的文档：组合标签关闭两篇。
func (s *Shell) tabIDsOf(tab *Tab) []int {
	if left, right, ok := s.comparisonOf(tab); ok {
		return []int{left.ID, right.ID}
	}
	return []int{tab.ID}
}

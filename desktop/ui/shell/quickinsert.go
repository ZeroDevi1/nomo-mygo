package shell

import (
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/ui/icons"
)

// 快捷插入（原版 quickInsert）：语义模式下在行首或空格后输入触发符（默认 /）弹出菜单，
// 继续输入可筛选，↑↓ 选择、回车插入、Esc 关闭；插入时删除触发符与筛选文字后执行对应命令。

type quickItem struct {
	label, icon, command string
}

func (s *Shell) quickItems() []quickItem {
	return []quickItem{
		{s.tr("quickInsertHeading") + " 1", "heading-1", "heading1"},
		{s.tr("quickInsertHeading") + " 2", "heading-2", "heading2"},
		{s.tr("quickInsertHeading") + " 3", "heading-3", "heading3"},
		{s.tr("quickInsertBullet"), "list", "unorderedList"},
		{s.tr("quickInsertOrdered"), "list-ordered", "orderedList"},
		{s.tr("quickInsertTask"), "square-check", "taskList"},
		{s.tr("quickInsertQuote"), "quote", "quote"},
		{s.tr("quickInsertCallout"), "info", "callout"},
		{s.tr("codeBlock"), "code-xml", "codeBlock"},
		{s.tr("mathBlock"), "type", "mathBlock"},
		{s.tr("quickInsertTable"), "table-2", "table"},
		{s.tr("horizontalRule"), "minus", "horizontalRule"},
		{s.tr("image"), "image", "image"},
	}
}

// quickState 是快捷插入菜单的状态：at 为触发符的字节偏移；dismissedVer 记录被 Esc 关闭时的
// 原文版本，避免同一次输入立即再次弹出。
type quickState struct {
	open         bool
	tab          int
	at           int
	selected     int
	dismissedVer uint64
}

// quickInsert 在每帧检查是否应打开、更新或关闭快捷插入菜单，并在打开时画出菜单。
func (s *Shell) quickInsert(c *ui.Context, tab *Tab, editor ui.Element) {
	q := &s.quick
	if tab == nil || s.mode() != modeSemantic || !s.prefs.QuickInsertEnabled {
		s.closeQuick(tab)
		return
	}
	src := tab.Doc.Source()
	head := tab.Doc.Sel.Head
	if !q.open {
		trig := s.prefs.QuickInsertTrigger
		at := head - len(trig)
		if tab.Doc.Sel.Empty() && at >= 0 && src[at:head] == trig && tab.Doc.Version() != q.dismissedVer &&
			(at == 0 || src[at-1] == '\n' || src[at-1] == ' ') {
			*q = quickState{open: true, tab: tab.ID, at: at, dismissedVer: q.dismissedVer}
			tab.Ed.Intercept = s.quickKey
		} else {
			return
		}
	}
	// 光标离开触发符之后、换了标签或输入了空白：关闭。
	if q.tab != tab.ID || head < q.at+len(s.prefs.QuickInsertTrigger) || q.at+len(s.prefs.QuickInsertTrigger) > len(src) ||
		src[q.at:q.at+len(s.prefs.QuickInsertTrigger)] != s.prefs.QuickInsertTrigger {
		s.closeQuick(tab)
		return
	}
	query := src[q.at+len(s.prefs.QuickInsertTrigger) : head]
	if strings.ContainsAny(query, " \n\t") || utf8.RuneCountInString(query) > 12 {
		s.closeQuick(tab)
		return
	}
	items := s.filteredQuick(query)
	if len(items) == 0 {
		s.closeQuick(tab)
		return
	}
	q.selected = clampInt(q.selected, 0, len(items)-1)
	caret, ok := tab.Ed.CaretRect()
	if !ok {
		return
	}
	origin := editor.Bounds()
	x, y := origin.X+caret.X, origin.Y+caret.Y+caret.H+4
	ui.Overlay(c, func() {
		panel := ui.Column(c).Absolute().Left(x).Top(y).Width(220).Padding(4).Gap(1).Radius(c.Theme().Radius+2).
			Background(c.Theme().Background).Border(1, s.ch.separator).Shadow(0, 6, 18, 0, ui.RGBA(0, 0, 0, 0.18)).
			Role(ui.RoleMenu).Label(s.tr("quickInsert"))
		panel.Children(func() {
			for i, it := range items {
				ck := c.Key(it.command)
				row := ui.ButtonBase(ck).FillWidth().Padding(5, 8).Gap(8).Radius(c.Theme().Radius).Justify(ui.Start)
				if i == q.selected {
					row.Background(s.ch.pressed)
				} else if row.Hovered() {
					row.Background(s.ch.hover)
				}
				row.Children(func() {
					ui.Icon(ck, icons.Get(it.icon)).FontSize(15).TextColor(s.ch.muted)
					ui.Text(ck, it.label).FontSize(13)
				})
				if row.Clicked() {
					s.applyQuick(tab, it)
				}
			}
		})
		if panel.PressedOutside() {
			s.dismissQuick(tab)
		}
	})
}

func (s *Shell) filteredQuick(query string) []quickItem {
	all := s.quickItems()
	if query == "" {
		return all
	}
	var out []quickItem
	for _, it := range all {
		if strings.Contains(strings.ToLower(it.label+" "+it.command), strings.ToLower(query)) {
			out = append(out, it)
		}
	}
	return out
}

// quickKey 在菜单打开时拦截编辑器的按键：↑↓ 选择，回车插入，Esc 关闭；其余按键照常输入（用于筛选）。
func (s *Shell) quickKey(ev ui.InputEvent) bool {
	tab := s.activeTab()
	q := &s.quick
	if tab == nil || !q.open {
		return false
	}
	src := tab.Doc.Source()
	start := min(len(src), q.at+len(s.prefs.QuickInsertTrigger))
	items := s.filteredQuick(src[start:min(len(src), max(start, tab.Doc.Sel.Head))])
	switch ev.Key {
	case ui.KeyDown:
		q.selected = (q.selected + 1) % max(1, len(items))
	case ui.KeyUp:
		q.selected = (q.selected - 1 + max(1, len(items))) % max(1, len(items))
	case ui.KeyEnter:
		if len(items) > 0 {
			s.applyQuick(tab, items[clampInt(q.selected, 0, len(items)-1)])
		}
	case ui.KeyEscape:
		s.dismissQuick(tab)
	default:
		return false
	}
	return true
}

// applyQuick 删除触发符与筛选文字，再执行命令（标题、列表等作用于当前行）。
func (s *Shell) applyQuick(tab *Tab, it quickItem) {
	at, head := s.quick.at, tab.Doc.Sel.Head
	s.closeQuick(tab)
	if head > at {
		tab.Ed.Delete(at, head)
	}
	if it.command == "image" {
		s.pickImages()
	} else {
		tab.Ed.Run(it.command)
	}
	s.focusEditor = true
}

func (s *Shell) dismissQuick(tab *Tab) {
	if tab != nil {
		s.quick.dismissedVer = tab.Doc.Version()
	}
	s.closeQuick(tab)
}

func (s *Shell) closeQuick(tab *Tab) {
	if s.quick.open && tab != nil {
		tab.Ed.Intercept = nil
	}
	s.quick.open = false
}

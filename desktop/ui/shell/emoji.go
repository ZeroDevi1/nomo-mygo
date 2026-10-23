package shell

import (
	"fmt"
	"regexp"
	"time"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/emoji"
	"nomo/desktop/internal/i18n"
	"nomo/desktop/ui/mdedit"
)

// 表情（原版 emojiSuggestions / EmojiPicker）：语义模式下输入“:短代码”弹出建议，↑↓ 选择、回车或 Tab 插入、
// Esc 关闭；工具栏与快捷插入打开表情选择器，可按中英文搜索、按分类浏览。目录在首次需要时于后台加载
// （读缓存或下载，见 desktop/internal/emoji），加载后进程内共享；失败一分钟内不重复请求，可手动重试。

const (
	emojiSuggestLimit = 8 // 建议浮层最多条数，与原版一致
	emojiColumns      = 8
	emojiCell         = 40 // 选择器格子边长，与原版 rowHeight 一致
	emojiGridH        = 240
	emojiPickerW      = 352
)

type emojiStatus uint8

const (
	emojiIdle emojiStatus = iota
	emojiLoading
	emojiReady
	emojiFailed
)

// emojiState 是表情目录与两个浮层的状态。
type emojiState struct {
	status emojiStatus
	cat    []emoji.Entry

	// 建议浮层：q 为当前查询；dismissed 为 Esc 关闭的查询签名，同一查询不再弹出；seenTab/seenVer
	// 为上一帧的标签与原文版本，只有原文变化（键入、删除）才打开浮层，单纯移动光标不打开。
	open       bool
	tab        int
	q          emoji.Query
	results    []emoji.Entry
	resultsFor string // 计算 results 时的查询签名与目录状态，变化时重新搜索
	selected   int
	dismissed  string
	seenTab    int
	seenVer    uint64

	// 选择器：target* 为打开时的标签、选区与原文版本，插入时须仍然一致（原版 isEmojiTargetCurrent）。
	picker    bool
	targetTab int
	targetSel mdedit.Selection
	targetVer uint64
	anchor    ui.Point // 选择器左上角（窗口坐标）
	search    string
	group     int // -1 为全部
	gridSel   int
	focusGrid bool // 下一帧让选中的格子获得焦点（方向键在网格中移动后）
	// outsideAt 为点击外部关闭选择器的时间：同一次点击落在工具栏表情按钮上时不再重新打开（原版 data-emoji-trigger）。
	outsideAt     time.Time
	list          ui.ListState
	pickerResults []emoji.Entry
	pickerFor     string
}

// unsafeTail 对应原版：链接地址或 HTML 标签尚未闭合时不弹出建议。
var unsafeTail = regexp.MustCompile(`\]\([^)]*$|<[^>]*$`)

func (s *Shell) chinese() bool { return s.locale == i18n.ZhCN || s.locale == i18n.ZhTW }

// ensureEmoji 在后台加载目录（已加载或正在加载时不做任何事）；完成后经 post 回到 UI 线程。
func (s *Shell) ensureEmoji(retry bool) {
	st := &s.emo
	if s.loadEmoji == nil || st.status == emojiReady || st.status == emojiLoading {
		return
	}
	st.status = emojiLoading
	load := s.loadEmoji
	go func() {
		cat, err := load(retry)
		s.post(func() {
			if err != nil {
				s.emo.status = emojiFailed
				return
			}
			s.emo.cat, s.emo.status = cat, emojiReady
		})
	}()
}

// emojiSuggest 每帧检查光标前的冒号查询，决定打开、更新或关闭建议浮层，并在打开时画出浮层。
func (s *Shell) emojiSuggest(c *ui.Context, tab *Tab, editor ui.Element) {
	st := &s.emo
	if tab == nil || s.loadEmoji == nil || s.mode() != modeSemantic || s.quick.open || st.picker {
		s.closeEmojiSuggest(tab)
		return
	}
	ver := tab.Doc.Version()
	// 只有键盘输入（键入、删除）带来的原文变化才打开浮层；撤销、粘贴、命令与单纯移动光标不打开。
	typed := tab.ID == st.seenTab && ver != st.seenVer && tab.Doc.InputVersion() == ver
	st.seenTab, st.seenVer = tab.ID, ver
	before, literal, ok := tab.Ed.CaretContext(192)
	var q emoji.Query
	found := false
	if ok && !literal && !unsafeTail.MatchString(before) {
		q, found = emoji.FindQuery(before, tab.Doc.Sel.Head)
	}
	sig := fmt.Sprint(q.From, ":", q.To, ":", q.Text)
	switch {
	case !found || sig == st.dismissed:
		s.closeEmojiSuggest(tab)
		return
	case !st.open && !typed:
		return
	case st.open && (st.tab != tab.ID || q != st.q && !typed):
		s.closeEmojiSuggest(tab)
		return
	}
	if !st.open {
		st.open, st.tab, st.selected = true, tab.ID, 0
		tab.Ed.Intercept = s.emojiKey
		s.ensureEmoji(false)
	}
	if st.q != q {
		st.q, st.selected = q, 0
	}
	if key := fmt.Sprint(sig, st.status); key != st.resultsFor {
		st.resultsFor, st.results = key, nil
		if st.status == emojiReady {
			st.results = emoji.Search(st.cat, q.Text, -1, emojiSuggestLimit)
		}
		st.selected = clampInt(st.selected, 0, max(0, len(st.results)-1))
	}
	caret, ok := tab.Ed.CaretRect()
	if !ok {
		return
	}
	origin := editor.Bounds()
	x, y := origin.X+caret.X, origin.Y+caret.Y+caret.H+5
	th := c.Theme()
	ui.Overlay(c, func() {
		panel := ui.Column(c).Absolute().Left(x).Top(y).Width(300).Padding(4).Gap(1).Radius(th.Radius+2).
			Background(th.Background).Border(1, s.ch.separator).Shadow(0, 6, 18, 0, ui.RGBA(0, 0, 0, 0.18)).
			Role(ui.RoleList).Label(s.tr("emojiSuggestions"))
		panel.Children(func() {
			for i, e := range st.results {
				ck := c.Key(e.ID)
				row := ui.ButtonBase(ck).FillWidth().Padding(4, 8).Gap(8).Radius(th.Radius).Justify(ui.Start).AlignItems(ui.Center)
				if i == st.selected {
					row.Background(s.ch.pressed)
				} else if row.Hovered() {
					row.Background(s.ch.hover)
				}
				row.Children(func() {
					ui.Text(ck, e.Unicode).FontSize(18)
					ui.Text(ck, e.Label(s.chinese())).FontSize(13).Grow(1).MaxLines(1)
					ui.Text(ck, ":"+e.Shortcodes[0]+":").FontSize(11).TextColor(s.ch.muted).MaxLines(1)
				})
				if row.Clicked() {
					s.chooseEmojiSuggestion(tab, i)
				}
			}
			if len(st.results) == 0 {
				s.emojiStatusRow(c)
			}
		})
		if panel.PressedOutside() {
			s.dismissEmojiSuggest(tab)
		}
	})
}

// emojiStatusRow 显示加载中、加载失败（带重试）或无结果。
func (s *Shell) emojiStatusRow(c *ui.Context) {
	ui.Row(c).FillWidth().Padding(6, 8).Gap(8).AlignItems(ui.Center).Children(func() {
		switch s.emo.status {
		case emojiLoading, emojiIdle:
			ui.Text(c, s.tr("emojiLoading")).FontSize(12).TextColor(s.ch.muted)
		case emojiFailed:
			ui.Text(c, s.tr("emojiLoadFailed")).FontSize(12).TextColor(s.ch.muted).Grow(1)
			if ui.Button(c, s.tr("emojiRetry")).Clicked() {
				s.emo.status = emojiIdle
				s.ensureEmoji(true)
			}
		default:
			ui.Text(c, s.tr("emojiNoResults")).FontSize(12).TextColor(s.ch.muted)
		}
	})
}

// emojiKey 在建议浮层打开时拦截编辑器按键：Esc 关闭；有结果时 ↑↓ 选择，回车或 Tab 插入。带修饰键的
// 按键照常交给编辑器。
func (s *Shell) emojiKey(ev ui.InputEvent) bool {
	tab := s.activeTab()
	st := &s.emo
	if tab == nil || !st.open || ev.Mods != 0 {
		return false
	}
	if ev.Key == ui.KeyEscape {
		s.dismissEmojiSuggest(tab)
		return true
	}
	n := len(st.results)
	if n == 0 {
		return false
	}
	switch ev.Key {
	case ui.KeyDown:
		st.selected = (st.selected + 1) % n
	case ui.KeyUp:
		st.selected = (st.selected - 1 + n) % n
	case ui.KeyEnter, ui.KeyTab:
		s.chooseEmojiSuggestion(tab, st.selected)
	default:
		return false
	}
	return true
}

// chooseEmojiSuggestion 用第 i 个结果替换冒号查询；查询已变化（如另一处编辑）时只关闭。
func (s *Shell) chooseEmojiSuggestion(tab *Tab, i int) {
	st := &s.emo
	q, results := st.q, st.results
	before, literal, ok := tab.Ed.CaretContext(192)
	cur, found := emoji.FindQuery(before, tab.Doc.Sel.Head)
	s.closeEmojiSuggest(tab)
	if !ok || literal || !found || cur != q || i < 0 || i >= len(results) {
		return
	}
	tab.Ed.Replace(q.From, q.To, results[i].Unicode)
	s.focusEditor = true
}

func (s *Shell) dismissEmojiSuggest(tab *Tab) {
	st := &s.emo
	if st.open {
		st.dismissed = fmt.Sprint(st.q.From, ":", st.q.To, ":", st.q.Text)
	}
	s.closeEmojiSuggest(tab)
}

func (s *Shell) closeEmojiSuggest(tab *Tab) {
	st := &s.emo
	if st.open && tab != nil && tab.ID == st.tab {
		tab.Ed.Intercept = nil
	}
	st.open, st.q, st.results, st.resultsFor = false, emoji.Query{}, nil, ""
}

// openEmojiPicker 在 at（窗口坐标）处打开选择器，插入目标为当前标签的当前选区；已打开时关闭（与原版
// 工具栏按钮的切换行为一致）。
func (s *Shell) openEmojiPicker(at ui.Point) {
	st := &s.emo
	tab := s.activeTab()
	if st.picker {
		s.closeEmojiPicker(true)
		return
	}
	if time.Since(st.outsideAt) < 400*time.Millisecond {
		return
	}
	if tab == nil || s.loadEmoji == nil || s.mode() != modeSemantic {
		return
	}
	if _, marked := tab.Doc.Marked(); marked {
		return
	}
	s.closeEmojiSuggest(tab)
	st.picker, st.targetTab, st.targetSel, st.targetVer = true, tab.ID, tab.Doc.Sel, tab.Doc.Version()
	st.anchor, st.search, st.group, st.gridSel, st.pickerFor = at, "", -1, 0, ""
	st.list = ui.ListState{}
	s.ensureEmoji(false)
}

// closeEmojiPicker 关闭选择器；refocus 为 true 时让编辑区重新获得焦点。
func (s *Shell) closeEmojiPicker(refocus bool) {
	s.emo.picker, s.emo.pickerResults = false, nil
	if refocus {
		s.focusEditor = true
	}
}

// pickerTargetOK 报告插入目标是否仍然有效：同一标签、原文未变、仍在语义模式。
func (s *Shell) pickerTargetOK() (*Tab, bool) {
	st := &s.emo
	tab := s.activeTab()
	return tab, tab != nil && tab.ID == st.targetTab && tab.Doc.Version() == st.targetVer && s.mode() == modeSemantic
}

func (s *Shell) chooseEmoji(e emoji.Entry) {
	st := &s.emo
	tab, ok := s.pickerTargetOK()
	s.closeEmojiPicker(ok)
	if !ok {
		return
	}
	a, z := st.targetSel.Range()
	tab.Ed.Replace(a, z, e.Unicode)
}

// emojiPicker 画出选择器（打开时）：标题与关闭按钮、搜索框、分类、虚拟化的表情网格与底部说明。
func (s *Shell) emojiPicker(c *ui.Context, root ui.Rect) {
	st := &s.emo
	if !st.picker {
		return
	}
	if _, ok := s.pickerTargetOK(); !ok {
		// 切换了标签、模式或原文被其他途径修改：目标失效，关闭（原版同样在目标变化时关闭）。
		s.closeEmojiPicker(false)
		return
	}
	if key := fmt.Sprint(st.search, "|", st.group, "|", st.status); key != st.pickerFor {
		st.pickerFor, st.pickerResults = key, nil
		if st.status == emojiReady {
			st.pickerResults = emoji.Search(st.cat, st.search, st.group, 0)
		}
		st.gridSel = 0
		st.list.ScrollTo(0, ui.Start)
	}
	results := st.pickerResults
	th := c.Theme()
	// 尽量放在锚点下方，超出窗口时向左、向上收回（原版 position()）。
	const pickerH = 460
	x := max(root.X+8, min(st.anchor.X, root.X+root.W-emojiPickerW-8))
	y := max(root.Y+8, min(st.anchor.Y, root.Y+root.H-pickerH-8))
	ui.Overlay(c, func() {
		panel := ui.Column(c).Absolute().Left(x).Top(y).Width(emojiPickerW).Padding(8).Gap(6).Radius(th.Radius+4).
			Background(th.Background).Border(1, s.ch.separator).Shadow(0, 10, 28, 0, ui.RGBA(0, 0, 0, 0.22)).
			Role(ui.RoleDialog).Label(s.tr("emojiInsert"))
		panel.Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Text(c, s.tr("emojiInsert")).FontSize(13).Bold().Grow(1)
				if s.toolButton(c, "x", s.tr("emojiClose"), false).Clicked() {
					s.closeEmojiPicker(true)
				}
			})
			in := ui.TextInput(c, &st.search).FillWidth().Placeholder(s.tr("emojiSearch")).AutoFocus()
			if in.Submitted() && len(results) > 0 {
				s.chooseEmoji(results[clampInt(st.gridSel, 0, len(results)-1)])
			}
			ui.Row(c).FillWidth().Wrap().Gap(4).Children(func() {
				chip := func(label string, g int) {
					ck := c.Key(fmt.Sprint("group", g))
					b := ui.ButtonBase(ck).Padding(3, 8).Radius(th.Radius)
					if st.group == g {
						b.Background(s.ch.pressed)
					} else if b.Hovered() {
						b.Background(s.ch.hover)
					}
					b.Children(func() { ui.Text(ck, label).FontSize(12) })
					if b.Clicked() {
						st.group = g
					}
				}
				chip(s.tr("emojiAll"), -1)
				for _, g := range emoji.Groups {
					chip(s.tr(fmt.Sprint("emojiGroup", g)), g)
				}
			})
			if len(results) == 0 {
				ui.Box(c).FillWidth().Height(emojiGridH).Children(func() { s.emojiStatusRow(c) })
				return
			}
			rows := (len(results) + emojiColumns - 1) / emojiColumns
			ui.List(c, &st.list, rows, func(r int) {
				ui.Row(c).Height(emojiCell).FillWidth().Children(func() {
					for k := r * emojiColumns; k < min(len(results), (r+1)*emojiColumns); k++ {
						e := results[k]
						ck := c.Key(e.ID)
						b := ui.ButtonBase(ck).Size(emojiCell, emojiCell).Radius(th.Radius).Justify(ui.Center).AlignItems(ui.Center).
							Tooltip(e.Label(s.chinese()) + " · :" + e.Shortcodes[0] + ":").Label(e.Label(s.chinese()))
						if k == st.gridSel {
							b.Background(s.ch.pressed)
							if st.focusGrid {
								b.Focus()
								st.focusGrid = false
							}
						} else if b.Hovered() {
							b.Background(s.ch.hover)
						}
						if b.Focused() {
							st.gridSel = k
						}
						b.Children(func() { ui.Text(ck, e.Unicode).FontSize(22) })
						if b.Clicked() {
							s.chooseEmoji(e)
						}
					}
				})
			}).FillWidth().Height(emojiGridH)
			sel := results[clampInt(st.gridSel, 0, len(results)-1)]
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Padding(2, 4).Children(func() {
				ui.Text(c, sel.Label(s.chinese())).FontSize(12).Grow(1).MaxLines(1)
				ui.Text(c, ":"+sel.Shortcodes[0]+":").FontSize(11).TextColor(s.ch.muted).MaxLines(1)
			})
			// 键盘：↑↓ 在搜索框与网格中按行移动；←→ 只在焦点位于网格时按格移动（搜索框中移动光标）。
			move := func(d int) {
				st.gridSel = clampInt(st.gridSel+d, 0, len(results)-1)
				st.list.ScrollIntoView(st.gridSel / emojiColumns)
			}
			switch {
			case panel.OverlayShortcut(0, ui.KeyDown):
				move(emojiColumns)
			case panel.OverlayShortcut(0, ui.KeyUp):
				move(-emojiColumns)
			case panel.OverlayShortcut(0, ui.KeyRight):
				move(1)
				st.focusGrid = true
			case panel.OverlayShortcut(0, ui.KeyLeft):
				move(-1)
				st.focusGrid = true
			}
		})
		if panel.OverlayShortcut(0, ui.KeyEscape) {
			s.closeEmojiPicker(true)
		}
		if panel.PressedOutside() {
			st.outsideAt = time.Now()
			s.closeEmojiPicker(false)
		}
	})
}

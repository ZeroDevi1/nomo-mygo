package shell

import "github.com/egoist/mygo/ui"

// Markdown 小窗（原版 markdownMini）：把当前窗口缩成置顶的小窗，只显示当前文档的语义编辑区与一条精简标题栏
// （文档名、置顶切换、返回主窗口）；退出时恢复原来的位置、尺寸、最大化状态与编辑模式。组合标签与非 Markdown
// 文档不进入小窗。小窗尺寸在本次运行中记住（原生版不写入窗口状态设置）。

const (
	miniDefaultW, miniDefaultH = 460, 560 // 与 WebView 版 window_state.go 一致
	miniMinW, miniMinH         = 320, 360
	normalMinW, normalMinH     = 920, 640 // 主窗口的最小尺寸（Shell.Open）
)

// miniState 是小窗模式的状态：active 时 normal* 为进入前的窗口几何，prevMode 为进入前的编辑模式。
type miniState struct {
	active, pinned                     bool
	normalX, normalY, normalW, normalH int
	maximized                          bool
	prevMode                           string
	w, h                               int // 本次运行中小窗的尺寸
}

// toggleMini 进入或退出小窗（快捷键 toggle-markdown-mini）。
func (s *Shell) toggleMini() {
	if s.mini.active {
		s.exitMini()
		return
	}
	t := s.activeTab()
	if s.win == nil || t == nil || t.Kind() != kindMarkdown || t.partner != 0 {
		return
	}
	m := &s.mini
	if m.w == 0 {
		m.w, m.h, m.pinned = miniDefaultW, miniDefaultH, true
	}
	m.maximized = s.win.IsMaximized()
	if m.maximized {
		s.win.Unmaximize()
	}
	m.normalX, m.normalY = s.win.Position()
	m.normalW, m.normalH = s.win.ContentSize()
	m.prevMode = s.prefs.EditorMode
	if s.prefs.EditorMode != modeSemantic {
		t.syncFromSource()
		s.prefs.EditorMode = modeSemantic // 只改内存：退出时恢复，不写入设置
	}
	m.active = true
	s.finding = false
	s.win.SetMinimumSize(miniMinW, miniMinH)
	s.win.SetContentSize(m.w, m.h)
	// 右上角对齐原窗口，便于在原位置附近继续阅读。
	s.win.SetPosition(m.normalX+m.normalW-m.w, m.normalY)
	s.win.SetMaximizable(false)
	s.win.SetAlwaysOnTop(m.pinned)
	s.focusEditor = true
}

func (s *Shell) exitMini() {
	m := &s.mini
	if !m.active {
		return
	}
	m.w, m.h = s.win.ContentSize()
	m.active = false
	s.win.SetAlwaysOnTop(false)
	s.win.SetMaximizable(true)
	s.win.SetMinimumSize(normalMinW, normalMinH)
	s.win.SetContentSize(m.normalW, m.normalH)
	s.win.SetPosition(m.normalX, m.normalY)
	if m.maximized {
		s.win.Maximize()
	}
	if m.prevMode != "" && m.prevMode != s.prefs.EditorMode {
		s.prefs.EditorMode = m.prevMode
		if t := s.activeTab(); t != nil && m.prevMode != modeSemantic {
			t.syncToSource()
		}
	}
	s.focusEditor = true
}

// miniLayout 是小窗的界面：可拖动的标题栏 + 语义编辑区。
func (s *Shell) miniLayout(c *ui.Context) {
	tab := s.activeTab()
	if tab == nil {
		s.exitMini()
		return
	}
	bar := c.TitleBar()
	left := bar.Left + 10
	if s.pf.mac {
		left = max(left, 78) // macOS 隐藏标题栏时红绿灯按钮占据左上角，文档名让开它们
	}
	ui.Column(c).Fill().Background(s.ch.content).Children(func() {
		ui.Row(c).Height(max(bar.Height, 38)).FillWidth().Padding(0, bar.Right+8, 0, left).Gap(4).AlignItems(ui.Center).
			DragWindow().Children(func() {
			name := tab.Name(s.untitledName)
			if tab.Dirty() {
				name = "• " + name
			}
			ui.Text(c, name).FontSize(12).Bold().SingleLine().Grow(1)
			pinIcon, pinTip := "pin", s.tr("markdownMiniPin")
			if s.mini.pinned {
				pinIcon, pinTip = "pin-off", s.tr("markdownMiniUnpin")
			}
			if s.toolButton(c, pinIcon, pinTip, s.mini.pinned).Clicked() {
				s.mini.pinned = !s.mini.pinned
				s.win.SetAlwaysOnTop(s.mini.pinned)
			}
			if s.toolButton(c, "maximize-2", s.tr("markdownMiniReturn"), false).Clicked() {
				s.exitMini()
			}
		})
		ui.Box(c).Height(1).FillWidth().Background(s.ch.separator)
		ck := c.Key(tab.ID)
		ui.Column(ck).Grow(1).Fill().Children(func() {
			el := tab.Ed.View(ck)
			if r := el.Bounds(); r.W > 0 {
				s.editorOrigin = ui.Point{X: r.X, Y: r.Y}
			}
			s.quickInsert(c, tab, el)
			s.emojiSuggest(c, tab, el)
			if s.focusEditor {
				el.Focus()
				s.focusEditor = false
			}
		})
	})
}

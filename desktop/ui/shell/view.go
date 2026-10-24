package shell

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"nomo/desktop/internal/config"
	"nomo/desktop/ui/icons"
)

// 外壳版式。
const (
	tabBarH   = 34
	toolbarH  = 38
	statusH   = 26
	outlineW  = 240
	sidebarHd = 32 // 侧栏标题行
)

func (s *Shell) view(c *ui.Context) {
	s.applyTheme(c)
	t := c.Theme()
	s.ch = s.pf.chrome(t, c.Vibrancy())
	svc := c.Services()
	s.svc = &svc
	if s.ch.material {
		c.Root().Background(ui.Transparent)
	}
	tab := s.activeTab()
	if tab != nil && tab.preview && tab.Dirty() {
		tab.preview = false // 编辑后固定预览标签
	}
	if tab != nil {
		if s.mode() == modeSource {
			tab.syncFromSource()
		}
		s.applyEditorStyle(tab)
	}
	s.applyDocumentAppearance(c, tab)
	s.autoSave(c)
	s.trackWorkspace(c)
	s.scheduleLint(c, s.activeTab())
	root := ui.Column(c).Fill()
	s.shortcuts(root)
	// 编辑区之外（空白工作区、侧栏、标签栏等）拖入的文件在这里接收；编辑区自己接收（图片插入文档）。
	if files := root.DroppedFiles(); len(files) > 0 {
		s.openDropped(files)
	}
	dropping := root.FileDragOver()
	// 其他窗口拖来的标签落在标签栏之外（或本窗口标签栏未显示）时加到末尾。
	if d, ok := ui.Drop[tabDrag](root); ok && d.from != s {
		s.adoptTabs(d.from, d.id, 0)
	}
	root.Children(func() {
		if s.mini.active {
			s.miniLayout(c)
			return
		}
		if s.pf.mac {
			s.macLayout(c)
		} else {
			s.desktopLayout(c)
		}
	})
	s.dialogs(c)
	w, h := c.Size()
	s.emojiPicker(c, ui.Rect{W: w, H: h})
	s.imagePreviewView(c)
	if dropping {
		s.dropHint(c, w, h)
	}
	if s.status != "" {
		c.Toast(s.status)
		s.status = ""
	}
}

// applyTheme 设置当前平台、明暗下的控件主题；themeMode 为 system 时跟随系统明暗。
func (s *Shell) applyTheme(c *ui.Context) {
	dark := c.Theme().Dark
	switch s.prefs.ThemeMode {
	case "light":
		dark = false
	case "dark":
		dark = true
	}
	c.SetTheme(s.pf.theme(dark, c.Preferences().Accent))
}

// toolButton 是工具栏、标题栏的图标按钮：无底色，悬停时显示浅底，带提示。
func (s *Shell) toolButton(c *ui.Context, icon, tip string, on bool) ui.Element {
	b := ui.ButtonBase(c).Padding(5).Radius(c.Theme().Radius).Tooltip(tip)
	switch {
	case on:
		b.Background(s.ch.pressed)
	case b.Hovered():
		b.Background(s.ch.hover)
	}
	b.Children(func() {
		ic := ui.Icon(c, s.icon(icon)).FontSize(16)
		if on {
			ic.TextColor(s.ch.accent)
		} else {
			ic.TextColor(s.ch.muted)
		}
	})
	return b
}

// ---- macOS：全高 vibrancy 侧栏 + 统一标题栏工具区 ----

func (s *Shell) macLayout(c *ui.Context) {
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		if s.prefs.SidebarHidden {
			s.macContent(c)
			return
		}
		ui.Split(c, &s.sidebarW, func() { s.sidebar(c, true) }, func() { s.macContent(c) }).Grow(1)
	})
}

func (s *Shell) macContent(c *ui.Context) {
	bar := c.TitleBar()
	ui.Column(c).Grow(1).Fill().Background(s.ch.content).Children(func() {
		left := float32(12)
		if s.prefs.SidebarHidden {
			left = bar.Left + 8
		}
		ui.Row(c).Height(s.pf.titleH).FillWidth().Padding(0, 12, 0, left).Gap(4).AlignItems(ui.Center).DragWindow().Children(func() {
			if s.prefs.SidebarHidden && s.toolButton(c, "panel-left", s.tr("showExplorerSidebar"), false).Clicked() {
				s.toggleSidebar()
			}
			s.titleText(c)
			ui.Spacer(c)
			s.trailingTools(c)
		})
		ui.Box(c).Height(1).FillWidth().Background(s.ch.separator)
		if len(s.tabs) > 1 {
			s.macTabs(c)
		}
		s.document(c)
	})
}

// macTabs 是 Safari / Finder 风格的标签栏：等宽标签，悬停显示关闭按钮。
func (s *Shell) macTabs(c *ui.Context) {
	ui.Row(c).Height(tabBarH - 4).FillWidth().Background(s.ch.tabBar).AlignItems(ui.Stretch).Children(func() {
		for i, tab := range s.tabs {
			if s.tabHidden(tab) {
				continue
			}
			ck := c.Key(tab.ID)
			selected := s.tabSelected(tab)
			cell := ui.Row(ck).Grow(1).Width(0).Padding(0, 6).AlignItems(ui.Center).Gap(4)
			if selected {
				cell.Background(s.ch.tabActive)
			} else if cell.Hovered() {
				cell.Background(s.ch.hover)
			}
			if i > 0 {
				cell.Border(1, s.ch.separator).BorderWidth(0, 0, 0, 1)
			}
			s.tabDragTarget(cell, tab)
			hovered := cell.Hovered()
			cell.Children(func() {
				s.closeButton(ck, tab, hovered || selected)
				label := ui.ButtonBase(ck).Grow(1).Children(func() {
					txt := ui.Text(ck, s.tabLabel(tab)).FontSize(12).SingleLine()
					if !selected {
						txt.TextColor(s.ch.muted)
					}
					if tab.preview {
						txt.Italic() // 预览标签（原版 DocumentTabs 的 .preview）
					}
				})
				if tab.Path != "" {
					label.Tooltip(tab.Path)
				}
				if label.DoubleClicked() {
					tab.preview = false // 双击标题固定预览标签
				}
				if label.Clicked() && !selected {
					s.activate(i)
				}
				ui.Box(ck).Size(18, 18)
			})
			s.tabMenu(cell, tab)
		}
	})
	ui.Box(c).Height(1).FillWidth().Background(s.ch.separator)
}

// ---- Windows / Linux：标题栏菜单 + 内容卡片 ----

func (s *Shell) desktopLayout(c *ui.Context) {
	bar := c.TitleBar()
	ui.Column(c).Fill().Background(s.ch.paint(s.ch.titlebar, true)).Children(func() {
		// 标题栏同原版 AppTitleBar（Windows）：侧栏开关、应用名与菜单，右侧小窗、主题与设置；文档名在标签上，
		// 模式切换、查找与大纲在格式工具栏右侧（toolbarTrailing）。
		ui.Row(c).Height(max(bar.Height, s.pf.titleH)).FillWidth().Padding(0, bar.Right+8, 0, bar.Left+6).Gap(2).
			AlignItems(ui.Center).DragWindow().Children(func() {
			hide := "hideExplorerSidebar"
			if s.prefs.SidebarHidden {
				hide = "showExplorerSidebar"
			}
			if s.toolButton(c, "panel-left", s.tr(hide), false).Clicked() {
				s.toggleSidebar()
			}
			ui.Text(c, "Nomo").FontSize(12).Bold().Padding(0, 6, 0, 4)
			s.menus(c)
			ui.Spacer(c)
			if s.activeTab() != nil && s.toolButton(c, "picture-in-picture-2", s.tr("markdownMiniShortcut"), false).Clicked() {
				s.toggleMini()
			}
			dark := c.Theme().Dark
			icon := "moon"
			if dark {
				icon = "sun"
			}
			if s.toolButton(c, icon, s.tr("switchTheme"), false).Clicked() {
				s.toggleTheme(dark)
			}
			if s.toolButton(c, "settings", s.tr("settings"), s.settings != nil).Clicked() {
				s.openSettings()
			}
		})
		ui.Row(c).Grow(1).FillWidth().AlignItems(ui.Stretch).Children(func() {
			card := func() {
				box := ui.Column(c).Grow(1).Fill().Background(s.ch.content)
				if s.pf.windows {
					// WinUI NavigationView 的内容区：左上圆角 8，细边框。
					box.Radius(8, 0, 0, 0).Border(1, s.ch.separator).BorderWidth(1, 0, 0, 1).Clip()
				}
				box.Children(func() {
					if len(s.tabs) > 0 {
						s.desktopTabs(c)
					}
					s.document(c)
				})
			}
			if s.prefs.SidebarHidden {
				card()
				return
			}
			ui.Split(c, &s.sidebarW, func() { s.sidebar(c, false) }, card).Grow(1)
		})
	})
}

// desktopTabs 是 Windows 11 / Fluent 风格的标签：选中标签与内容区同色，其余透明。
func (s *Shell) desktopTabs(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Height(tabBarH).FillWidth().Padding(4, 8, 0, 8).Gap(2).AlignItems(ui.End).Children(func() {
		ui.Row(c).Grow(1).Gap(2).Clip().AlignItems(ui.End).Children(func() {
			for i, tab := range s.tabs {
				if s.tabHidden(tab) {
					continue
				}
				ck := c.Key(tab.ID)
				selected := s.tabSelected(tab)
				box := ui.Row(ck).Height(tabBarH-6).Padding(0, 4, 0, 12).Gap(6).AlignItems(ui.Center).Radius(t.Radius*2, t.Radius*2, 0, 0).MaxWidth(240)
				switch {
				case selected:
					box.Background(s.ch.tabActive)
				case box.Hovered():
					box.Background(s.ch.hover)
				}
				s.tabDragTarget(box, tab)
				hovered := box.Hovered()
				box.Children(func() {
					label := ui.ButtonBase(ck).Gap(6).Children(func() {
						ui.Icon(ck, icons.ForFile(tab.Name(s.untitledName))).FontSize(14).TextColor(s.ch.muted)
						txt := ui.Text(ck, s.tabLabel(tab)).FontSize(12).SingleLine()
						if !selected {
							txt.TextColor(s.ch.muted)
						}
						if tab.preview {
							txt.Italic() // 预览标签（原版 DocumentTabs 的 .preview）
						}
					})
					if tab.Path != "" {
						label.Tooltip(tab.Path)
					}
					if label.DoubleClicked() {
						tab.preview = false // 双击标题固定预览标签
					}
					if label.Clicked() && !selected {
						s.activate(i)
					}
					s.closeButton(ck, tab, hovered || selected)
				})
				s.tabMenu(box, tab)
			}
		})
		if s.toolButton(c, "plus", s.tr("newMarkdown"), false).Clicked() {
			s.NewDocument()
		}
	})
	ui.Box(c).Height(1).FillWidth().Background(s.ch.separator)
}

// tabDrag 是拖动中的标签：from 为来源窗口，id 为标签 ID。同一进程的其他窗口也能接收（DragData 携带同一值）。
type tabDrag struct {
	from *Shell
	id   int
}

// tabDragTarget 让标签可以拖动（并支持中键关闭）：在本窗口标签栏内拖到另一标签上排序（从左往右拖放在目标之后，反之放在之前，放下
// 一侧显示强调色竖线）；拖到其他窗口的标签上移入那个窗口；在所有窗口之外松开时移到新窗口（组合标签整组移动）。
func (s *Shell) tabDragTarget(el ui.Element, tab *Tab) {
	el.Drag(tabDrag{s, tab.ID}).DragData(transfer.TextData(tab.Name(s.untitledName)), transfer.DragOptions{
		Done: func(res transfer.Result) {
			if res.Canceled && !res.Escaped && res.Err == nil && !pointInWindows(res.Point) && s.win != nil {
				s.win.Update(func() {
					if t := s.tabWithID(tab.ID); t != nil && len(s.tabIDsOf(t)) < len(s.tabs) {
						s.moveToNewWindowAt(t, res.Point)
					}
				})
			}
		},
	})
	if el.Dragging() {
		el.Opacity(0.5)
	}
	// 中键单击关闭标签（组合标签关闭整组），未保存修改照常确认。
	el.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputPointerDown && ev.Button == 2 {
			s.requestClose(s.tabIDsOf(tab), false)
			return true
		}
		return false
	})
	if d, over := ui.DragOver[tabDrag](el); over && (d.from != s || d.id != tab.ID) {
		from, to := s.findTabIndex(d.id), s.findTabIndex(tab.ID)
		if d.from == s && from < to {
			el.Border(2, s.ch.accent).BorderWidth(0, 2, 0, 0)
		} else {
			el.Border(2, s.ch.accent).BorderWidth(0, 0, 0, 2)
		}
	}
	if d, ok := ui.Drop[tabDrag](el); ok {
		if d.from == s {
			s.moveTab(d.id, tab.ID)
		} else {
			s.adoptTabs(d.from, d.id, tab.ID)
		}
	}
}

// pointInWindows 报告屏幕点是否落在本进程的某个外壳窗口内。
func pointInWindows(pt image.Point) bool {
	for _, sh := range registry.shells {
		if sh.win == nil {
			continue
		}
		b := sh.win.Bounds()
		if pt.X >= b.X && pt.X < b.X+b.Width && pt.Y >= b.Y && pt.Y < b.Y+b.Height {
			return true
		}
	}
	return false
}

// moveTab 把标签 id 移到标签 target 的位置，活动标签保持不变。
func (s *Shell) moveTab(id, target int) {
	from, to := s.findTabIndex(id), s.findTabIndex(target)
	if from < 0 || to < 0 || from == to {
		return
	}
	active := s.activeTab()
	t := s.tabs[from]
	s.tabs = slices.Delete(s.tabs, from, from+1)
	s.tabs = slices.Insert(s.tabs, to, t)
	s.active = slices.Index(s.tabs, active)
}

// closeButton 画标签的关闭按钮；有未保存修改时平时显示圆点，悬停时显示 ×（与原版一致）。
func (s *Shell) closeButton(c *ui.Context, tab *Tab, visible bool) {
	x := ui.ButtonBase(c).Size(18, 18).Radius(4).Tooltip(s.tr("closeTab"))
	hovered := x.Hovered()
	if hovered {
		x.Background(s.ch.pressed)
	}
	x.Children(func() {
		switch {
		case tab.Dirty() && !hovered:
			ui.Box(c).Size(7, 7).Radius(4).Background(s.ch.muted)
		case visible || hovered:
			ui.Icon(c, s.icon("x")).FontSize(12).TextColor(s.ch.muted)
		}
	})
	if x.Clicked() {
		s.requestClose(s.tabIDsOf(tab), false)
	}
}

// tabMenu 是标签的右键菜单，对应原版 DocumentTabs 的关闭、关闭其他、关闭右侧、复制路径、在文件夹中显示。
func (s *Shell) tabMenu(el ui.Element, tab *Tab) {
	el.ContextMenu(func(m *ui.Menu) {
		s.comparisonMenu(m, tab)
		if m.Item(s.tr("closeTab")).Chosen() {
			s.requestClose([]int{tab.ID}, false)
		}
		if m.Item(s.tr("closeOtherTabs")).Chosen() {
			var ids []int
			for _, t := range s.tabs {
				if t.ID != tab.ID {
					ids = append(ids, t.ID)
				}
			}
			s.requestClose(ids, false)
		}
		if m.Item(s.tr("closeTabsToRight")).Chosen() {
			var ids []int
			seen := false
			for _, t := range s.tabs {
				if seen {
					ids = append(ids, t.ID)
				}
				seen = seen || t.ID == tab.ID
			}
			s.requestClose(ids, false)
		}
		if m.Item(s.tr("moveTabToNewWindow")).Disabled(len(s.tabIDsOf(tab)) == len(s.tabs)).Chosen() {
			s.moveToNewWindow(tab)
		}
		m.Separator()
		if m.Item(s.tr("copyPath")).Disabled(tab.Path == "").Chosen() {
			s.copyText(tab.Path)
		}
		if m.Item(s.tr("revealInFolder")).Disabled(tab.Path == "").Chosen() {
			revealInFolder(tab.Path)
		}
	})
}

// titleText 是 macOS 统一标题栏中的文档名（有未保存修改时附注）。
func (s *Shell) titleText(c *ui.Context) {
	title := "Nomo"
	if tab := s.activeTab(); tab != nil {
		title = tab.Name(s.untitledName)
		if tab.Dirty() {
			title += " — " + s.tr("unsavedChanges")
		}
	}
	ui.Text(c, title).FontSize(13).Bold().SingleLine().Shrink(1)
}

// trailingTools 是 macOS 统一标题栏右侧：文档工具与设置。
func (s *Shell) trailingTools(c *ui.Context) {
	s.documentTools(c)
	if s.toolButton(c, "settings", s.tr("settings"), s.settings != nil).Clicked() {
		s.openSettings()
	}
}

// documentTools 是模式切换、查找与大纲开关：macOS 在统一标题栏，Windows / Linux 在格式工具栏右侧（同原版 EditorToolbar）。
func (s *Shell) documentTools(c *ui.Context) {
	t := s.activeTab()
	if t == nil {
		return
	}
	modes := []string{modeSemantic, modeSource, modeSplit}
	mode := 0
	for k, m := range modes {
		if s.prefs.EditorMode == m {
			mode = k
		}
	}
	if t.Kind() == kindMarkdown && ui.Segmented(c, &mode, s.tr("semanticEditing"), s.tr("sourceMode"), s.tr("splitMode")).FontSize(12).Changed() {
		s.setMode(modes[mode])
	}
	ui.Box(c).Width(6)
	if s.toolButton(c, "search", s.tr("searchReplace"), s.finding).Clicked() {
		s.openFind(false)
	}
	if s.toolButton(c, "list-tree", s.tr("showOutline"), s.prefs.OutlineVisible).Clicked() {
		s.toggleOutline()
	}
}

// sidebar 是资源管理器；mac 为 true 时顶部留出交通灯位置，背景交给 vibrancy 材质。
func (s *Shell) sidebar(c *ui.Context, mac bool) {
	bar := c.TitleBar()
	ui.Column(c).Fill().Background(s.ch.paint(s.ch.sidebar, true)).Children(func() {
		if mac {
			ui.Row(c).Height(s.pf.titleH).FillWidth().Padding(0, 8, 0, bar.Left+8).AlignItems(ui.Center).DragWindow().Children(func() {
				ui.Spacer(c)
				if s.toolButton(c, "panel-left", s.tr("hideExplorerSidebar"), false).Clicked() {
					s.toggleSidebar()
				}
			})
		}
		ui.Row(c).Height(sidebarHd).FillWidth().Padding(0, 6, 0, 14).AlignItems(ui.Center).Children(func() {
			name := s.tr("explorer")
			if s.folder != nil {
				name = s.folder.name
			}
			ui.Text(c, name).FontSize(11).Bold().TextColor(s.ch.muted).SingleLine().Grow(1)
			if s.folder != nil && s.toolButton(c, "folder-open", s.tr("openFolderEllipsis"), false).Clicked() {
				s.openDialog(true)
			}
			if s.folder != nil && s.toolButton(c, "refresh-cw", s.tr("refresh"), false).Clicked() {
				s.folder.refresh()
			}
		})
		ui.Scroll(c).Grow(1).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Padding(0, 6, 8, 6).Children(func() {
				if s.folder != nil {
					s.tree(c, s.folder)
					if s.folder.loaded && len(s.folder.children) == 0 {
						ui.Text(c, s.tr("folderHasNoDocuments")).FontSize(12).TextColor(s.ch.muted).Wrap().Padding(8, 10)
					}
					return
				}
				ui.Column(c).FillWidth().Padding(20, 10, 12, 10).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, s.icon("folder-open")).FontSize(28).TextColor(s.ch.muted)
					ui.Text(c, s.tr("noFolderOpen")).FontSize(13).Bold()
					ui.Text(c, s.tr("noFolderOpenDescription")).FontSize(12).TextColor(s.ch.muted).Wrap().TextAlign(ui.Center)
					if ui.Button(c, s.tr("openFolderEllipsis")).Clicked() {
						s.openDialog(true)
					}
				})
				if s.hasRecent("folder") {
					s.sectionLabel(c, s.tr("recentFolders"))
					s.recentList(c, "folder", 6)
				}
			})
		})
	})
}

// document 是标签内容：格式工具栏、查找栏、编辑区与大纲、状态栏；没有文档时为空白工作区。
func (s *Shell) document(c *ui.Context) {
	tab := s.activeTab()
	if tab == nil {
		s.empty(c)
		return
	}
	if !s.prefs.ToolbarHidden {
		s.toolbar(c)
	}
	if s.mode() == modeSemantic {
		s.findBar(c, tab)
	}
	// MinHeight(0) 让这一行可以比内容矮：否则大纲的滚动内容会撑高整行，编辑区压到状态栏下面。
	ui.Row(c).Grow(1).FillWidth().MinHeight(0).AlignItems(ui.Stretch).Clip().Children(func() {
		// 组合标签以左侧成员为键：在两篇之间切换活动文档时不重建编辑区（保留焦点与滚动位置）。
		keyID := tab.ID
		if left, _, ok := s.comparisonOf(tab); ok {
			keyID = left.ID
		}
		// 双栏时 mode() 随活动一侧（焦点）变化，不能作为键：否则点进另一侧的下一帧整块重建，刚获得的焦点随之丢失。
		keyMode := s.mode()
		if s.split() {
			keyMode = modeSplit
		}
		ck := c.Key(fmt.Sprint(keyMode, s.split(), keyID))
		ui.Column(ck).Grow(1).Fill().Children(func() {
			var el ui.Element
			if left, right, ok := s.comparisonOf(tab); ok {
				el = s.comparisonView(c, left, right)
				if s.mode() == modeSemantic {
					if r := el.Bounds(); r.W > 0 {
						s.editorOrigin = ui.Point{X: r.X, Y: r.Y}
					}
					s.quickInsert(c, s.activeTab(), el)
					s.linkPopover(c, s.activeTab(), el)
					s.emojiSuggest(c, s.activeTab(), el)
				}
			} else if s.split() {
				el = s.splitView(c, tab, ck)
				if r := el.Bounds(); r.W > 0 {
					s.editorOrigin = ui.Point{X: r.X, Y: r.Y}
				}
				s.quickInsert(c, tab, el)
				s.linkPopover(c, tab, el)
				s.emojiSuggest(c, tab, el)
			} else if s.mode() == modeSource {
				// 与语义模式同宽居中（原版源码模式同样受内容宽度约束）。
				ui.Row(ck).Grow(1).Fill().Justify(ui.Center).Padding(16, 0, 0, 0).Children(func() {
					st := tab.Source()
					if st.OnCompletion == nil && tab.Kind() == kindMarkdown {
						st.OnCompletion = func(req editor.CompletionRequest) { s.sourceCompletion(tab, req) }
					}
					el = editor.View(ck, st).Fill().MaxWidth(820)
				})
			} else {
				el = s.editorView(ck, tab)
				if r := el.Bounds(); r.W > 0 {
					s.editorOrigin = ui.Point{X: r.X, Y: r.Y}
				}
				s.quickInsert(c, tab, el)
				s.linkPopover(c, tab, el)
				s.emojiSuggest(c, tab, el)
			}
			if files := el.DroppedFiles(); len(files) > 0 {
				s.dropFiles(tab, files)
			}
			if s.focusEditor {
				el.Focus()
				s.focusEditor = false
			}
		})
		if s.prefs.OutlineVisible {
			s.outline(c, tab)
		}
	})
	if s.prefs.WritingStatsVisible {
		s.statusBar(c, tab)
	}
}

// findBar 是语义模式的查找替换栏；匹配随原文版本、查询与选项变化重新计算。
func (s *Shell) findBar(c *ui.Context, tab *Tab) {
	if !s.finding {
		if s.foundTab != 0 {
			tab.Ed.SetHighlights(nil, -1)
			s.foundTab = 0
		}
		return
	}
	opts := [2]bool{s.matchCase, s.wholeWord}
	if v := tab.Doc.Version(); v != s.foundVer || s.query != s.foundQuery || tab.ID != s.foundTab || opts != s.foundOpts {
		s.matches = tab.Doc.Find(s.query, s.matchCase, s.wholeWord)
		s.foundVer, s.foundQuery, s.foundTab, s.foundOpts = v, s.query, tab.ID, opts
		s.match = clampInt(s.match, 0, max(0, len(s.matches)-1))
		tab.Ed.SetHighlights(s.matches, s.match)
	}
	fb := ui.FindBar(c, &s.finding, &s.query, len(s.matches), &s.match)
	if fb.Changed() && len(s.matches) > 0 {
		m := s.matches[s.match]
		tab.Ed.SetHighlights(s.matches, s.match)
		tab.Ed.Select(m[0], m[1])
	}
	ui.Row(c).FillWidth().Padding(4, 12).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Checkbox(c, &s.matchCase, s.tr("matchCase"))
		ui.Checkbox(c, &s.wholeWord, s.tr("wholeWord"))
		if s.replacing {
			ui.TextInput(c, &s.replaceWith).Width(220).Placeholder(s.tr("replaceWith"))
			if ui.Button(c, s.tr("replace")).Disabled(len(s.matches) == 0).Clicked() && len(s.matches) > 0 {
				tab.Ed.ReplaceRanges(s.matches[s.match:s.match+1], s.replaceWith)
			}
			if ui.Button(c, s.tr("replaceAll")).Disabled(len(s.matches) == 0).Clicked() {
				tab.Ed.ReplaceRanges(s.matches, s.replaceWith)
			}
		} else if ui.Button(c, s.tr("showReplace")).Clicked() {
			s.replacing = true
		}
	})
	ui.Box(c).Height(1).FillWidth().Background(s.ch.separator)
}

func (s *Shell) openFind(replace bool) {
	if s.mode() != modeSemantic {
		return
	}
	s.finding = true
	s.replacing = s.replacing || replace
	if t := s.activeTab(); t != nil {
		if a, z := t.Doc.Sel.Range(); a < z && z-a < 200 && !strings.Contains(t.Doc.Source()[a:z], "\n") {
			s.query = t.Doc.Source()[a:z]
		}
	}
}

func (s *Shell) outline(c *ui.Context, tab *Tab) {
	ui.Row(c).FillHeight().Children(func() {
		ui.Box(c).Width(1).FillHeight().Background(s.ch.separator)
		ui.Column(c).Width(outlineW).FillHeight().MinHeight(0).Children(func() {
			ui.Row(c).Height(sidebarHd).FillWidth().Padding(0, 14).AlignItems(ui.Center).Children(func() {
				ui.Text(c, s.tr("documentOutline")).FontSize(11).Bold().TextColor(s.ch.muted)
			})
			// 虚拟列表：只为可见的标题建元素，万级标题的文档每帧成本与标题数无关。
			hs := tab.Outline()
			rows := s.outlineRows(tab, hs)
			head := tab.Doc.Sel.Head
			current := sort.Search(len(hs), func(i int) bool { return hs[i].Pos > head }) - 1
			if len(hs) == 0 {
				ui.Text(c, s.tr("documentHasNoHeadings")).FontSize(12).TextColor(s.ch.muted).Wrap().Padding(4, 14)
				return
			}
			ui.List(c, &s.outlineList, len(rows), func(r int) {
				i := rows[r]
				h := hs[i]
				ck := c.Key(i)
				parent := i+1 < len(hs) && hs[i+1].Level > h.Level
				ui.Row(ck).FillWidth().Padding(0, 6).AlignItems(ui.Center).Children(func() {
					// 有下级标题时显示展开箭头（原版大纲树）；无下级时留出同宽空位，标题对齐。
					indent := float32(h.Level-1) * 12
					ui.Box(ck).Width(indent).Height(1)
					toggle := ui.ButtonBase(ck).Size(18, 18).Justify(ui.Center).AlignItems(ui.Center).Radius(4)
					toggle.Children(func() {
						arrow := ui.Icon(ck, s.icon("chevron-right")).FontSize(12).TextColor(s.ch.muted)
						if s.outlineExpanded(tab, hs, i) {
							arrow.Rotate(90)
						}
						if !parent {
							arrow.Opacity(0)
						}
					})
					if parent && toggle.Clicked() {
						s.toggleOutlineItem(tab, i)
					}
					b := ui.ButtonBase(ck).Grow(1).Padding(4, 6, 4, 4).Radius(c.Theme().Radius).Justify(ui.Start)
					if i == current {
						b.Background(s.ch.pressed)
					} else if b.Hovered() {
						b.Background(s.ch.hover)
					}
					b.Children(func() {
						txt := ui.Text(ck, h.Text).FontSize(12).SingleLine()
						if h.Level > 2 {
							txt.TextColor(s.ch.muted)
						}
					})
					if b.Clicked() {
						tab.Ed.JumpTo(h.Pos)
						s.focusEditor = true
					}
				})
			}).Grow(1).FillWidth().Padding(0, 0, 8, 0)
		})
	})
}

func (s *Shell) statusBar(c *ui.Context, tab *Tab) {
	ui.Box(c).Height(1).FillWidth().Background(s.ch.separator)
	ui.Row(c).Height(statusH).FillWidth().Padding(0, 12).Gap(14).AlignItems(ui.Center).Children(func() {
		s.lintButton(c, tab)
		st := tab.Stats()
		muted := func(text string) ui.Element {
			return ui.Text(c, text).FontSize(11).TextColor(s.ch.muted).SingleLine().Shrink(0)
		}
		muted(s.statsText(st.Words, st.Chars, st.Lines))
		if s.mode() == modeSemantic {
			line, col := tab.Doc.LineCol()
			muted(fmt.Sprintf("%d:%d", line, col))
		}
		// 路径最先让位：空间不够时由它截断。
		path := ui.ButtonBase(c).Shrink(1).Grow(1).Justify(ui.Start).Children(func() {
			ui.Text(c, displayPath(filepath.Dir(tab.Path))).FontSize(11).TextColor(s.ch.muted).SingleLine().Shrink(1)
		})
		if tab.Path == "" {
			path.Opacity(0)
		} else {
			path.Tooltip(tab.Path)
			if path.Clicked() {
				revealInFolder(tab.Path)
			}
		}
		if tab.saving {
			muted(s.tr("settingsSaving"))
		}
		s.zoomControl(c)
	})
}

// statsText 按原版 writingStatsMetric 把首选统计项放在最前。
func (s *Shell) statsText(words, chars, lines int) string {
	w := fmt.Sprintf("%d %s", words, s.tr("wordUnit"))
	ch := fmt.Sprintf("%d %s", chars, s.tr("charUnit"))
	l := fmt.Sprintf("%d %s", lines, s.tr("lineUnit"))
	var out string
	switch s.prefs.WritingStatsMetric {
	case "chars", "visibleChars":
		out = ch + " · " + w + " · " + l
	case "lines":
		out = l + " · " + w + " · " + ch
	default:
		out = w + " · " + ch + " · " + l
	}
	if s.prefs.ReadingTimeVisible {
		// 原版 writingStats：每分钟 280 词，至少 1 分钟。
		out += " · " + s.tr("estimatedReadingMinutes", "minutes", strconv.Itoa(max(1, (words+279)/280)))
	}
	return out
}

// empty 是没有打开文档时的空白工作区，对应原版 EmptyWorkspace。
func (s *Shell) empty(c *ui.Context) {
	ui.Column(c).Fill().Center().Gap(14).Children(func() {
		ui.Icon(c, s.icon("file-text")).FontSize(40).TextColor(s.ch.muted)
		ui.Text(c, s.tr("noOpenDocument")).FontSize(18).Bold()
		ui.Text(c, s.tr("noOpenDocumentDescription")).TextColor(s.ch.muted)
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, s.tr("newMarkdown")).Tooltip(s.shortcutText("new")).Clicked() {
				s.NewDocument()
			}
			if ui.Button(c, s.tr("openFileEllipsis")).Tooltip(s.shortcutText("open")).Clicked() {
				s.openDialog(false)
			}
			if ui.Button(c, s.tr("openFolderEllipsis")).Tooltip(s.shortcutText("openFolder")).Clicked() {
				s.openDialog(true)
			}
		})
		if s.hasRecent("file") {
			ui.Column(c).Width(440).Gap(2).Padding(14, 0, 0, 0).Children(func() {
				s.sectionLabel(c, s.tr("recentOpenLabel"))
				s.recentList(c, "file", 8)
			})
		}
	})
}

// recentList 列出最近打开的文件或文件夹，点击打开。
func (s *Shell) recentList(c *ui.Context, kind string, limit int) {
	n := 0
	for _, e := range s.recent {
		if e.EntryType != kind || n >= limit {
			continue
		}
		n++
		ck := c.Key(e.Path)
		b := ui.ButtonBase(ck).FillWidth().Padding(6, 10).Radius(c.Theme().Radius).Gap(8).Justify(ui.Start)
		if b.Hovered() {
			b.Background(s.ch.hover)
		}
		icon := icons.ForFile(e.Path)
		if kind == "folder" {
			icon = icons.Get("folder")
		}
		b.Children(func() {
			ui.Icon(ck, icon).FontSize(14).TextColor(s.ch.muted)
			ui.Text(ck, filepath.Base(e.Path)).FontSize(13).SingleLine().Shrink(0)
			ui.Text(ck, filepath.Dir(e.Path)).FontSize(12).TextColor(s.ch.muted).SingleLine().Shrink(1).Grow(1)
		})
		b.Tooltip(e.Path)
		if b.Clicked() {
			s.OpenFiles([]string{e.Path})
		}
		path := e.Path
		b.ContextMenu(func(m *ui.Menu) {
			if m.Item(s.tr("revealInFolder")).Chosen() {
				revealInFolder(path)
			}
			if m.Item(s.tr("removeRecentLabel")).Chosen() {
				s.forget(path)
			}
		})
	}
}

// hasRecent 报告是否有该类（file / folder）的最近记录。
func (s *Shell) hasRecent(kind string) bool {
	return slices.ContainsFunc(s.recent, func(e config.RecentEntry) bool { return e.EntryType == kind })
}

// sectionLabel 是侧栏与空白工作区中分组的小标题。
func (s *Shell) sectionLabel(c *ui.Context, text string) {
	ui.Text(c, text).FontSize(11).Bold().TextColor(s.ch.muted).Padding(10, 10, 4, 10).FillWidth()
}

// dropHint 是从其他应用拖入文件时覆盖窗口的提示（不拦截指针，拖放仍落到下面的接收元素）。
func (s *Shell) dropHint(c *ui.Context, w, h float32) {
	ui.Overlay(c, func() {
		ui.Column(c).Absolute().Left(0).Top(0).Size(w, h).Padding(10).PassThrough().Children(func() {
			ui.Column(c).Fill().Center().Gap(10).Radius(12).Border(2, s.ch.accent).
				Background(s.ch.accent.Alpha(0.08)).PassThrough().Children(func() {
				ui.Icon(c, s.icon("folder-open")).FontSize(32).TextColor(s.ch.accent)
				ui.Text(c, s.tr("dropFilesToOpen")).FontSize(15).Bold().TextColor(s.ch.accent)
			})
		})
	})
}

func (s *Shell) dialogs(c *ui.Context) {
	name := ""
	if t := s.activeTab(); t != nil {
		name = t.Name(s.untitledName)
	}
	switch ui.AlertDialog(c, &s.confirmOpen, s.tr("unsavedChanges"), name, s.tr("cancel"), s.tr("discardChanges"), s.tr("save")) {
	case 0, ui.AlertDismissed:
		s.resolveClose(0)
	case 1:
		s.resolveClose(1)
	case 2:
		s.resolveClose(2)
	}
	s.renameDialog(c)
	s.closeChoiceDialog(c)
	s.draftConflictDialog(c)
	s.snapshotsDialog(c)
	s.updateNoticeDialog(c)
	s.installedNotesDialog(c)
	s.legacyInstallerDialog(c)
}

func (s *Shell) toggleSidebar() {
	s.prefs.SidebarHidden = !s.prefs.SidebarHidden
	s.setPref("sidebarHidden")
}

func (s *Shell) toggleOutline() {
	s.prefs.OutlineVisible = !s.prefs.OutlineVisible
	s.setPref("outlineVisible")
}

// toggleFocus 切换专注模式（原版 toggle-focus）：隐藏侧栏、大纲与工具栏，再次切换时恢复原状。
func (s *Shell) toggleFocus() {
	if s.focusSaved != nil {
		s.prefs.SidebarHidden, s.prefs.OutlineVisible, s.prefs.ToolbarHidden = s.focusSaved[0], s.focusSaved[1], s.focusSaved[2]
		s.focusSaved = nil
		return
	}
	s.focusSaved = &[3]bool{s.prefs.SidebarHidden, s.prefs.OutlineVisible, s.prefs.ToolbarHidden}
	s.prefs.SidebarHidden, s.prefs.OutlineVisible, s.prefs.ToolbarHidden = true, false, true
}

func (s *Shell) toggleToolbar() {
	s.prefs.ToolbarHidden = !s.prefs.ToolbarHidden
	s.setPref("toolbarHidden")
}

// toggleTheme 在明暗之间切换（写入 themeMode，之后不再跟随系统，与原版一致）。
func (s *Shell) toggleTheme(dark bool) {
	s.prefs.ThemeMode = "dark"
	if dark {
		s.prefs.ThemeMode = "light"
	}
	s.setPref("themeMode")
}

// zoom 调整界面缩放（原版 zoomPercent，80–160，步长 10）；delta 为 0 时复位。
func (s *Shell) zoom(delta float64) {
	if delta == 0 {
		s.prefs.ZoomPercent = 100
	} else {
		s.prefs.ZoomPercent = max(80, min(160, s.prefs.ZoomPercent+delta))
	}
	s.setPref("zoomPercent")
}

func clampInt(v, lo, hi int) int { return max(lo, min(hi, v)) }

// displayPath 把用户目录开头的路径缩写为“~”，状态栏空间有限。
func displayPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if rel, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rel
	}
	return p
}

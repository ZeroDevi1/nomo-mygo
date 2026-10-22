package shell

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

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
	if tab != nil {
		if s.mode() == modeSource {
			tab.syncFromSource()
		}
		tab.Ed.SetStyle(float32(s.prefs.FontSize), float32(s.prefs.ZoomPercent), float32(s.prefs.ContentWidthPercent))
	}
	s.applyDocumentAppearance(c, tab)
	s.autoSave(c)
	s.scheduleLint(c, s.activeTab())
	root := ui.Column(c).Fill()
	s.shortcuts(root)
	root.Children(func() {
		if s.pf.mac {
			s.macLayout(c)
		} else {
			s.desktopLayout(c)
		}
	})
	s.dialogs(c)
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
			ck := c.Key(tab.ID)
			selected := i == s.active
			cell := ui.Row(ck).Grow(1).Width(0).Padding(0, 6).AlignItems(ui.Center).Gap(4)
			if selected {
				cell.Background(s.ch.tabActive)
			} else if cell.Hovered() {
				cell.Background(s.ch.hover)
			}
			if i > 0 {
				cell.Border(1, s.ch.separator).BorderWidth(0, 0, 0, 1)
			}
			hovered := cell.Hovered()
			cell.Children(func() {
				s.closeButton(ck, tab, hovered || selected)
				label := ui.ButtonBase(ck).Grow(1).Children(func() {
					txt := ui.Text(ck, tab.Name(s.untitledName)).FontSize(12).SingleLine()
					if !selected {
						txt.TextColor(s.ch.muted)
					}
				})
				if label.Clicked() {
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
		ui.Row(c).Height(max(bar.Height, s.pf.titleH)).FillWidth().Padding(0, bar.Right+8, 0, bar.Left+8).Gap(2).
			AlignItems(ui.Center).DragWindow().Children(func() {
			ui.Text(c, "Nomo").FontSize(12).Bold().Padding(0, 8, 0, 4)
			s.menus(c)
			ui.Spacer(c)
			s.titleText(c)
			ui.Spacer(c)
			s.trailingTools(c)
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
				ck := c.Key(tab.ID)
				selected := i == s.active
				box := ui.Row(ck).Height(tabBarH-6).Padding(0, 4, 0, 12).Gap(6).AlignItems(ui.Center).Radius(t.Radius*2, t.Radius*2, 0, 0).MaxWidth(240)
				switch {
				case selected:
					box.Background(s.ch.tabActive)
				case box.Hovered():
					box.Background(s.ch.hover)
				}
				hovered := box.Hovered()
				box.Children(func() {
					label := ui.ButtonBase(ck).Gap(6).Children(func() {
						ui.Icon(ck, icons.ForFile(tab.Name(s.untitledName))).FontSize(14).TextColor(s.ch.muted)
						txt := ui.Text(ck, tab.Name(s.untitledName)).FontSize(12).SingleLine()
						if !selected {
							txt.TextColor(s.ch.muted)
						}
					})
					if label.Clicked() {
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
		s.requestClose([]int{tab.ID}, false)
	}
}

// tabMenu 是标签的右键菜单，对应原版 DocumentTabs 的关闭、关闭其他、关闭右侧、复制路径、在文件夹中显示。
func (s *Shell) tabMenu(el ui.Element, tab *Tab) {
	el.ContextMenu(func(m *ui.Menu) {
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
		m.Separator()
		if m.Item(s.tr("copyPath")).Disabled(tab.Path == "").Chosen() {
			s.copyText(tab.Path)
		}
		if m.Item(s.tr("revealInFolder")).Disabled(tab.Path == "").Chosen() {
			revealInFolder(tab.Path)
		}
	})
}

func (s *Shell) titleText(c *ui.Context) {
	title := "Nomo"
	if tab := s.activeTab(); tab != nil {
		title = tab.Name(s.untitledName)
		if tab.Dirty() {
			title += " — " + s.tr("unsavedChanges")
		}
	}
	txt := ui.Text(c, title).FontSize(13).SingleLine().Shrink(1)
	if s.pf.mac {
		txt.Bold()
	} else {
		txt.TextColor(s.ch.muted).FontSize(12)
	}
}

// trailingTools 是标题栏右侧：模式切换、查找、大纲、侧栏（Windows / Linux）、设置。
func (s *Shell) trailingTools(c *ui.Context) {
	if t := s.activeTab(); t != nil {
		mode := 0
		if s.mode() == modeSource {
			mode = 1
		}
		if t.Kind() == kindMarkdown && ui.Segmented(c, &mode, s.tr("semanticEditing"), s.tr("sourceMode")).FontSize(12).Changed() {
			if mode == 1 {
				s.setMode(modeSource)
			} else {
				s.setMode(modeSemantic)
			}
		}
		ui.Box(c).Width(6)
		if s.toolButton(c, "search", s.tr("searchReplace"), s.finding).Clicked() {
			s.openFind(false)
		}
		if s.toolButton(c, "list-tree", s.tr("showOutline"), s.prefs.OutlineVisible).Clicked() {
			s.toggleOutline()
		}
	}
	if !s.pf.mac && s.toolButton(c, "panel-left", s.tr("showHideExplorer"), !s.prefs.SidebarHidden).Clicked() {
		s.toggleSidebar()
	}
	if s.toolButton(c, "settings", s.tr("settings"), s.settings != nil).Clicked() {
		s.openSettings()
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
			if s.toolButton(c, "folder-open", s.tr("openFolderEllipsis"), false).Clicked() {
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
					return
				}
				ui.Text(c, s.tr("noOpenDocumentDescription")).FontSize(12).TextColor(s.ch.muted).Wrap().Padding(6, 8)
				if ui.Button(c, s.tr("openFolderEllipsis")).Clicked() {
					s.openDialog(true)
				}
				s.recentList(c, "folder", 6)
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
		ck := c.Key(fmt.Sprint(s.mode(), tab.ID))
		ui.Column(ck).Grow(1).Fill().Children(func() {
			var el ui.Element
			if s.mode() == modeSource {
				// 与语义模式同宽居中（原版源码模式同样受内容宽度约束）。
				ui.Row(ck).Grow(1).Fill().Justify(ui.Center).Padding(16, 0, 0, 0).Children(func() {
					el = editor.View(ck, tab.Source()).Fill().MaxWidth(820)
				})
			} else {
				el = tab.Ed.View(ck)
				s.quickInsert(c, tab, el)
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

// toolbar 是格式工具栏，按钮与原版 EditorToolbar 对应；源码模式下禁用。
func (s *Shell) toolbar(c *ui.Context) {
	disabled := s.mode() != modeSemantic
	ui.Row(c).Height(toolbarH).FillWidth().Padding(0, 10).Gap(1).AlignItems(ui.Center).Children(func() {
		group := func(items ...[3]string) {
			for _, it := range items {
				b := s.toolButton(c, it[0], it[1], false).Disabled(disabled)
				if disabled {
					b.Opacity(0.4)
				}
				if b.Clicked() {
					s.run(it[2])
				}
			}
			ui.Box(c).Size(1, 16).Background(s.ch.separator).Margin(0, 6)
		}
		group([3]string{"heading-1", s.tr("setHeadingOne"), "heading1"}, [3]string{"heading-2", s.tr("heading") + " 2", "heading2"},
			[3]string{"heading-3", s.tr("heading") + " 3", "heading3"}, [3]string{"pilcrow", s.tr("paragraph"), "paragraph"})
		group([3]string{"bold", s.tr("toggleBold"), "bold"}, [3]string{"italic", s.tr("toggleItalic"), "italic"},
			[3]string{"strikethrough", s.tr("toggleStrikethrough"), "strikethrough"}, [3]string{"underline", s.tr("toggleUnderline"), "underline"},
			[3]string{"highlighter", s.tr("toggleHighlight"), "highlight"}, [3]string{"code", s.tr("inlineCode"), "inlineCode"},
			[3]string{"link", s.tr("link"), "link"})
		group([3]string{"list", s.tr("unorderedList"), "unorderedList"}, [3]string{"list-ordered", s.tr("orderedList"), "orderedList"},
			[3]string{"square-check", s.tr("toggleTaskList"), "taskList"}, [3]string{"quote", s.tr("toggleQuote"), "quote"},
			[3]string{"code-xml", s.tr("insertCodeBlock"), "codeBlock"}, [3]string{"table-2", s.tr("insertTable"), "table"},
			[3]string{"minus", s.tr("horizontalRule"), "horizontalRule"})
	})
	ui.Box(c).Height(1).FillWidth().Background(s.ch.separator)
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
			ui.Scroll(c).Grow(1).FillWidth().Children(func() {
				ui.Column(c).FillWidth().Padding(0, 6, 8, 6).Children(func() {
					head := tab.Doc.Sel.Head
					current := -1
					hs := tab.Outline()
					for i, h := range hs {
						if h.Pos <= head {
							current = i
						}
					}
					for i, h := range hs {
						ck := c.Key(i)
						b := ui.ButtonBase(ck).FillWidth().Padding(4, 8, 4, 8+float32(h.Level-1)*12).Radius(c.Theme().Radius).Justify(ui.Start)
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
					}
				})
			})
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
		path := ui.Text(c, filepath.Dir(tab.Path)).FontSize(11).TextColor(s.ch.muted).SingleLine().Shrink(1).Grow(1)
		if tab.Path == "" {
			path.Opacity(0)
		}
		if tab.saving {
			muted(s.tr("settingsSaving"))
		}
	})
}

// statsText 按原版 writingStatsMetric 把首选统计项放在最前。
func (s *Shell) statsText(words, chars, lines int) string {
	w := fmt.Sprintf("%d %s", words, s.tr("wordUnit"))
	ch := fmt.Sprintf("%d %s", chars, s.tr("charUnit"))
	l := fmt.Sprintf("%d %s", lines, s.tr("lineUnit"))
	switch s.prefs.WritingStatsMetric {
	case "chars", "visibleChars":
		return ch + " · " + w + " · " + l
	case "lines":
		return l + " · " + w + " · " + ch
	}
	return w + " · " + ch + " · " + l
}

// empty 是没有打开文档时的空白工作区，对应原版 EmptyWorkspace。
func (s *Shell) empty(c *ui.Context) {
	ui.Column(c).Fill().Center().Gap(14).Children(func() {
		ui.Icon(c, s.icon("file-text")).FontSize(40).TextColor(s.ch.muted)
		ui.Text(c, s.tr("noOpenDocument")).FontSize(18).Bold()
		ui.Text(c, s.tr("noOpenDocumentDescription")).TextColor(s.ch.muted)
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, s.tr("newMarkdown")).Clicked() {
				s.NewDocument()
			}
			if ui.Button(c, s.tr("openFileEllipsis")).Clicked() {
				s.openDialog(false)
			}
			if ui.Button(c, s.tr("openFolderEllipsis")).Clicked() {
				s.openDialog(true)
			}
		})
		ui.Column(c).Width(420).Gap(2).Children(func() { s.recentList(c, "file", 8) })
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
		if b.Clicked() {
			s.OpenFiles([]string{e.Path})
		}
	}
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

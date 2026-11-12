package shell

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const (
	scholarWindowsExpanded   = 1280
	scholarWindowsCompact    = 640
	scholarWindowsProjectMin = 1008
	scholarWindowsRail       = 48
	scholarWindowsTitleInset = 12
	scholarWindowsNavInsetX  = 4
	scholarWindowsNavInsetY  = 8
	scholarWindowsToggleSize = 40
)

func (p *scholarState) windowsRailWidth(width float32) float32 {
	if width < scholarWindowsCompact {
		return 0
	}
	if width < scholarWindowsExpanded || p.winRailCollapsed {
		return scholarWindowsRail
	}
	return 208
}

func (p *scholarState) windowsProjectWidth(width float32) float32 {
	if width >= scholarWindowsProjectMin && p.winProjectOpen && p.nav == scholarFiles &&
		!p.references.catalog && !p.accountOpen && p.winSettings == nil {
		return 216
	}
	return 0
}

func (s *Shell) desktopScholarLayout(c *ui.Context, p *scholarState) {
	width, _ := c.Size()
	if width >= scholarWindowsExpanded {
		p.winRailPopup = false
	}
	if width >= scholarWindowsProjectMin {
		p.winCompactProject = false
	}
	bar := c.TitleBar()
	rail := p.windowsRailWidth(width)
	ui.Column(c).Fill().Background(s.ch.paint(s.ch.titlebar, true)).Children(func() {
		ui.Row(c).Height(max(bar.Height, s.pf.titleH)).FillWidth().Padding(0, bar.Right+8, 0, bar.Left+scholarWindowsTitleInset).
			Gap(8).AlignItems(ui.Center).DragWindow().Children(func() {
			s.windowsAppTitle(c, p.project.Root)
			ui.Box(c).Width(8)
			s.menus(c.Key("menubar"))
			ui.Spacer(c)
			if p.winSettings != nil || p.accountOpen || p.winCompactProject {
				s.scholarAssistantButton(c, p, false, false)
			} else if s.editorGroupsVisible(p) {
				ui.Row(c.Key("workspace-tools")).FillHeight().Shrink(0).Gap(4).AlignItems(ui.Center).Children(func() {
					s.documentTools(c)
				})
			}
		})
		if rail == 0 {
			ui.Row(c.Key("compact-navigation")).FillWidth().
				Padding(scholarWindowsNavInsetY, scholarWindowsNavInsetX, 4, scholarWindowsNavInsetX).Children(func() {
				s.scholarWindowsPaneToggle(c.Key("navigation"), p, width)
			})
		}
		ui.Row(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
			if rail > 0 {
				ui.Column(c.Key("application-nav")).Width(rail).Shrink(0).FillHeight().MinHeight(0).
					Padding(scholarWindowsNavInsetY, scholarWindowsNavInsetX).Gap(4).Background(s.ch.paint(s.ch.sidebar, true)).
					Children(func() {
						s.scholarWindowsPaneToggle(c.Key("navigation"), p, width)
						s.scholarWindowsDestinations(c, p, width, rail > scholarWindowsRail)
						ui.Box(c).Grow(1)
						s.scholarWindowsFooter(c, p, rail > scholarWindowsRail)
					})
			}
			ui.Column(c.Key("workspace")).Grow(1).FillHeight().MinWidth(0).MinHeight(0).Radius(8, 0, 0, 0).
				Border(1, s.ch.separator).BorderWidth(1, 0, 0, 1).Clip().Background(s.ch.content).Children(func() {
				if p.winSettings != nil {
					p.references.releaseHidden()
					s.hideTypesetPreview(p, s.activeTab())
					s.scholarWindowsSettingsPage(c.Key("settings"), p)
					return
				}
				if p.accountOpen {
					p.references.releaseHidden()
					s.hideTypesetPreview(p, s.activeTab())
					ui.Column(c).FillWidth().MaxWidth(520).Padding(24).Gap(10).Children(func() {
						ui.Text(c, s.tr("scholarAccount")).FontSize(24).Bold()
						ui.Text(c, s.tr("scholarLocalWorkspace")).Wrap()
						if ui.Button(c, s.tr("scholarReturnManuscript")).AlignSelf(ui.Start).Clicked() {
							p.accountOpen = false
							s.focusEditor = true
						}
					})
					return
				}
				if p.winCompactProject {
					p.references.releaseHidden()
					s.hideTypesetPreview(p, s.activeTab())
					s.scholarProjectPane(c.Key("project"), p)
					return
				}
				ui.Row(c).Grow(1).MinHeight(0).FillWidth().Children(func() {
					if pane := p.windowsProjectWidth(width); pane > 0 {
						ui.Column(c.Key("project")).Width(pane).Shrink(0).FillHeight().MinHeight(0).Background(s.ch.tabBar).
							Border(1, s.ch.separator).BorderWidth(0, 1, 0, 0).Children(func() {
							s.scholarProjectPane(c, p)
						})
					}
					ui.Column(c.Key("document")).Grow(1).FillHeight().MinWidth(0).MinHeight(0).Children(func() {
						s.desktopTabs(c)
						s.document(c)
					})
				})
			})
		})
	})
}

func (s *Shell) scholarWindowsPaneToggle(c *ui.Context, p *scholarState, width float32) {
	compact := width < scholarWindowsExpanded
	label, expanded := "scholarOpenNavigation", p.winRailPopup
	if !compact {
		label, expanded = "scholarExpandNavigation", !p.winRailCollapsed
		if expanded {
			label = "scholarCollapseNavigation"
		}
	}
	button := s.toolButton(c, "panel-left", s.tr(label), p.winRailPopup).
		Size(scholarWindowsToggleSize, scholarWindowsToggleSize).AlignSelf(ui.Start).Shrink(0).Expanded(expanded)
	if compact {
		button.Role(ui.RoleMenuButton)
	}
	if button.Clicked() {
		s.scholarToggleSidebarAt(width)
	}
}

func (p *scholarState) windowsDestinationSelected(destination int, width float32) bool {
	if p.accountOpen || p.winSettings != nil {
		return false
	}
	if destination == scholarFiles {
		return p.winCompactProject || p.windowsProjectWidth(width) > 0
	}
	return p.references.catalog
}

func (p *scholarState) openWindowsAccount() {
	p.dismissWindowsPage()
	p.accountOpen = true
}

// 紧凑导航复用系统菜单的 Acrylic、键盘导航和外部点击关闭。
func (s *Shell) openScholarWindowsNavigation(p *scholarState) {
	win := s.win
	if win == nil || p.winRailPopup {
		return
	}
	p.winRailPopup = true
	// PopupAt 在菜单关闭后才返回。把它排到当前 UI 帧之后。
	win.Update(func() {
		defer func() { p.winRailPopup = false }()
		if !p.winRailPopup || win.IsDestroyed() || !win.IsVisible() || s.scholarForTab(s.activeTab()) != p {
			return
		}
		width, _ := win.ContentSize()
		if width >= scholarWindowsExpanded {
			return
		}
		entry := func(label string, selected bool, activate func()) *mygo.MenuItem {
			return &mygo.MenuItem{
				Label: s.tr(label), Type: mygo.MenuItemCheckbox, Checked: selected,
				Click: func(*mygo.MenuItem, *mygo.Window) { activate() },
			}
		}
		w := float32(width)
		items := make([]*mygo.MenuItem, 0, len(scholarDestinations)+3)
		for i, destination := range scholarDestinations {
			items = append(items, entry(destination.label, p.windowsDestinationSelected(i, w), func() {
				s.showScholarNavigation(p, i, w)
			}))
		}
		items = append(items,
			mygo.Separator(),
			entry("scholarAccount", p.accountOpen, p.openWindowsAccount),
			entry("settings", p.winSettings != nil, s.openSettings),
		)
		y := int(s.pf.titleH) + scholarWindowsNavInsetY + scholarWindowsToggleSize
		mygo.NewMenu(items).PopupAt(win, scholarWindowsNavInsetX, y)
	})
}

func (s *Shell) scholarWindowsNavButton(c *ui.Context, glyph, label string, active, expanded bool) ui.Element {
	return s.ch.windowsNavButton(c, s.icon(glyph), label, active, expanded)
}

func (s *Shell) scholarWindowsDestinations(c *ui.Context, p *scholarState, width float32, expanded bool) {
	for i, destination := range scholarDestinations {
		if s.scholarWindowsNavButton(c.Key(i), destination.icon, s.tr(destination.label), p.windowsDestinationSelected(i, width), expanded).Clicked() {
			s.showScholarNavigation(p, i, width)
		}
	}
}

func (s *Shell) scholarWindowsFooter(c *ui.Context, p *scholarState, expanded bool) {
	if s.scholarWindowsNavButton(c.Key("account"), "user-round", s.tr("scholarAccount"), p.accountOpen, expanded).Clicked() {
		p.openWindowsAccount()
	}
	if s.scholarWindowsNavButton(c.Key("settings"), "settings", s.tr("settings"), p.winSettings != nil, expanded).Clicked() {
		p.winRailPopup = false
		s.openSettings()
	}
}

func (p *scholarState) dismissWindowsPage() {
	p.closeWindowsSettings()
	p.accountOpen, p.winRailPopup, p.winCompactProject = false, false, false
}

func (p *scholarState) closeWindowsSettings() {
	if p.winSettings != nil {
		p.winSettings.flush()
		p.winSettings = nil
	}
}

func (s *Shell) scholarWindowsSettingsPage(c *ui.Context, p *scholarState) {
	sw := p.winSettings
	categories := s.settingsCategories()
	label := s.tr(categories[0].label)
	for _, category := range categories {
		if category.id == sw.cat {
			label = s.tr(category.label)
			break
		}
	}
	sw.windowsContent(c, func() {
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
			if s.toolButton(c.Key("back"), "arrow-left", s.tr("scholarReturnManuscript"), false).Size(32, 32).Clicked() {
				p.closeWindowsSettings()
				s.focusEditor = true
			}
			ui.Text(c, s.tr("settingsTitle")).FontSize(28).FontWeight(600).Grow(1).MinWidth(0).SingleLine()
			s.scholarMenuButton(c.Key("category"), "settings-2", label, true).MinHeight(32).Menu(func(m *ui.Menu) {
				for _, category := range categories {
					if m.Item(s.tr(category.label)).Radio(category.id == sw.cat).Chosen() {
						sw.cat = category.id
					}
				}
			})
		})
	})
	if !c.Root().Pressed() {
		sw.flush()
	}
}

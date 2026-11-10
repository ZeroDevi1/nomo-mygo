package shell

import (
	"path/filepath"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/config"
)

const (
	scholarWindowsExpanded = 1008
	scholarWindowsCompact  = 640
)

func (p *scholarState) windowsRailWidth(width float32) float32 {
	if width < scholarWindowsCompact {
		return 0
	}
	if width < scholarWindowsExpanded || p.winRailCollapsed {
		return 48
	}
	return 168
}

func (p *scholarState) windowsProjectWidth(width float32) float32 {
	if width >= scholarWindowsExpanded && p.winProjectOpen && p.nav == scholarFiles &&
		!p.references.catalog && !p.accountOpen && p.winSettings == nil {
		return 200
	}
	return 0
}

func (s *Shell) desktopScholarLayout(c *ui.Context, p *scholarState) {
	width, _ := c.Size()
	if width >= scholarWindowsExpanded {
		p.winCompactProject, p.winRailPopup = false, false
	}
	bar := c.TitleBar()
	ui.Column(c).Fill().Background(s.ch.titlebar).Children(func() {
		ui.Row(c).Height(max(bar.Height, s.pf.titleH)).FillWidth().Padding(0, bar.Right+8, 0, bar.Left+6).
			Gap(6).AlignItems(ui.Center).DragWindow().Children(func() {
			label := s.tr("showHideExplorer")
			nav := s.toolButton(c.Key("navigation"), "panel-left", label, p.winRailPopup).Label(label).FocusRing(true)
			if nav.Clicked() {
				s.scholarToggleSidebarAt(width)
			}
			ui.Popover(c, nav, &p.winRailPopup, func() {
				ui.Column(c).Width(184).Padding(6).Gap(2).Children(func() {
					s.scholarWindowsDestinations(c, p, width, true)
					s.scholarWindowsFooter(c, p, true)
				})
			})
			ui.Text(c, config.AppName+" · "+filepath.Base(p.project.Root)).FontSize(12).Bold().SingleLine().
				MaxWidth(220).MinWidth(0).Shrink(1).Tooltip(p.project.Root)
			s.menus(c.Key("menubar"))
			ui.Spacer(c)
		})
		ui.Row(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
			if rail := p.windowsRailWidth(width); rail > 0 {
				ui.Column(c.Key("application-nav")).Width(rail).Shrink(0).FillHeight().MinHeight(0).Padding(5).Gap(2).Background(s.ch.sidebar).Children(func() {
					s.scholarWindowsDestinations(c, p, width, rail > 48)
					ui.Box(c).Grow(1)
					ui.Box(c).FillWidth().Height(1).Background(s.ch.separator)
					s.scholarWindowsFooter(c, p, rail > 48)
				})
			}
			ui.Column(c.Key("workspace")).Grow(1).FillHeight().MinWidth(0).MinHeight(0).Radius(8, 0, 0, 0).
				Border(1, s.ch.separator).BorderWidth(1, 0, 0, 1).Clip().Background(s.ch.content).Children(func() {
				if p.winSettings != nil {
					p.references.visible(false, true)
					s.hideTypesetPreview(p, s.activeTab())
					s.scholarWindowsSettingsPage(c.Key("settings"), p)
					return
				}
				if p.accountOpen {
					p.references.visible(false, true)
					s.hideTypesetPreview(p, s.activeTab())
					ui.Column(c).Padding(22).Gap(10).Children(func() {
						ui.Text(c, s.tr("scholarAccount")).FontSize(24).Bold()
						ui.Text(c, s.tr("scholarLocalWorkspace")).Wrap()
						if ui.Button(c, s.tr("scholarReturnManuscript")).Clicked() {
							p.accountOpen = false
							s.focusEditor = true
						}
					})
					return
				}
				if p.winCompactProject {
					p.references.visible(false, true)
					s.hideTypesetPreview(p, s.activeTab())
					s.scholarProjectPane(c.Key("project"), p)
					return
				}
				s.desktopTabs(c)
				ui.Row(c).Grow(1).MinHeight(0).FillWidth().Children(func() {
					if pane := p.windowsProjectWidth(width); pane > 0 {
						ui.Column(c.Key("project")).Width(pane).Shrink(0).FillHeight().MinHeight(0).Background(s.ch.tabBar).
							Border(1, s.ch.separator).BorderWidth(0, 1, 0, 0).Children(func() {
							s.scholarProjectPane(c, p)
						})
					}
					ui.Column(c.Key("document")).Grow(1).FillHeight().MinWidth(0).MinHeight(0).Children(func() { s.document(c) })
				})
			})
		})
	})
}

func (s *Shell) scholarWindowsNavButton(c *ui.Context, glyph, label string, active, expanded bool) ui.Element {
	row := ui.ButtonBase(c).FillWidth().Height(33).Padding(0, 7).Gap(6).Radius(4).Label(label).Tooltip(label).FocusRing(true).Selected(active)
	if expanded {
		row.Justify(ui.Start)
	}
	if active {
		row.Background(s.ch.pressed).TextColor(s.ch.text)
	} else if row.Hovered() {
		row.Background(s.ch.hover)
	}
	row.Children(func() {
		if active {
			ui.Box(c).Absolute().Left(0).Width(3).Top(9).Height(15).Radius(2).Background(s.ch.accent)
		}
		ui.Icon(c, s.icon(glyph)).FontSize(15)
		if expanded {
			ui.Text(c, label).FontSize(12).SingleLine().MinWidth(0).Shrink(1)
		}
	})
	return row
}

func (s *Shell) scholarWindowsDestinations(c *ui.Context, p *scholarState, width float32, expanded bool) {
	for i, destination := range scholarDestinations {
		active := p.references.catalog
		if i == scholarFiles {
			active = p.winCompactProject || p.windowsProjectWidth(width) > 0
		}
		if s.scholarWindowsNavButton(c.Key(i), destination.icon, s.tr(destination.label), active && !p.accountOpen && p.winSettings == nil, expanded).Clicked() {
			s.showScholarNavigation(p, i, width)
		}
	}
	assistant := p.drawer == scholarCodex && !p.accountOpen && p.winSettings == nil && !p.winCompactProject
	if s.scholarWindowsNavButton(c.Key("assistant"), "message-square-text", s.tr("scholarWritingAssistant"), assistant, expanded).Clicked() {
		s.openScholarAgent(p)
	}
}

func (s *Shell) scholarWindowsFooter(c *ui.Context, p *scholarState, expanded bool) {
	if s.scholarWindowsNavButton(c.Key("account"), "user-round", s.tr("scholarAccount"), p.accountOpen, expanded).Clicked() {
		p.dismissWindowsPage()
		p.accountOpen = true
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
	labels := make([]string, 0, len(categories))
	selected := 0
	for i, category := range categories {
		labels = append(labels, s.tr(category.label))
		if category.id == sw.cat {
			selected = i
		}
	}
	ui.Row(c).FillWidth().Padding(8, 16).Gap(8).AlignItems(ui.Center).Children(func() {
		label := s.tr("scholarReturnManuscript")
		if s.toolButton(c.Key("back"), "arrow-left", label, false).Label(label).FocusRing(true).Clicked() {
			p.closeWindowsSettings()
			s.focusEditor = true
		}
		ui.Text(c, s.tr("settingsTitle")).FontSize(20).Bold().Grow(1).MinWidth(0).SingleLine()
		ui.Column(c.Key("category")).Width(220).MinWidth(0).Shrink(1).Children(func() {
			scholarSelectIndex(c, &selected, labels, false)
			sw.cat = categories[selected].id
		})
	})
	ui.Scroll(c.Key(sw.cat)).Grow(1).MinHeight(0).FillWidth().Children(func() {
		ui.Column(c).FillWidth().Padding(4, 16, 16, 16).Gap(16).Children(func() { sw.page(c) })
	})
	if !c.Root().Pressed() {
		sw.flush()
	}
}

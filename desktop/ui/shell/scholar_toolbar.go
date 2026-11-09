package shell

import "github.com/egoist/mygo/ui"

var scholarViewLabels = [...]string{"scholarSource", "scholarPreview", "scholarSplit"}

func (s *Shell) canPreviewTypeset(tab *Tab) bool {
	if !isManuscript(tab) || tab.partner != 0 || s.mini.active {
		return false
	}
	if provider := s.previewProvider(tab); provider == nil || provider.Open == nil {
		return false
	}
	if p := s.scholarForTab(tab); p != nil && !samePath(p.project.Main, tab.Path) {
		provider := s.previewProvider(&Tab{Path: p.project.Main})
		return provider != nil && provider.Open != nil
	}
	return true
}

func (s *Shell) setTypesetView(p *scholarState, tab *Tab, mode int) {
	if s.activeTab() != tab || s.scholarForTab(tab) != p || mode != typesetSource && !s.canPreviewTypeset(tab) {
		return
	}
	p.changes.compare = false
	s.closeScholarInspector(p)
	if tab.typesetMode != mode {
		tab.typesetPaused = false
	}
	tab.typesetMode, tab.typesetFocusPDF = mode, mode == typesetPreview
	s.focusEditor = true
}

func (s *Shell) scholarPreviewMode(c *ui.Context, p *scholarState, tab *Tab) int {
	if p.drawer != scholarClosed {
		return typesetSource
	}
	width, _ := c.Size()
	if tab.typesetMode == typesetSplit && s.scholarSinglePane(width) {
		if tab.typesetCompactPDF {
			return typesetPreview
		}
		return typesetSource
	}
	return tab.typesetMode
}

func (s *Shell) scholarPDFOnly(c *ui.Context, p *scholarState, tab *Tab) bool {
	mode := s.scholarPreviewMode(c, p, tab)
	return s.canPreviewTypeset(tab) && (mode == typesetPreview || mode != typesetSource && p.agent.trial.show && p.agent.trial.compare)
}

func (s *Shell) revealScholarSource(tab *Tab) {
	if p := s.scholar; p != nil {
		if s.win != nil {
			width, _ := s.win.ContentSize()
			if p.activeTabID != tab.ID || s.scholarSinglePane(float32(width)) {
				s.closeScholarInspector(p)
			}
		}
		p.changes.compare = false
		if p.agent.trial.show {
			p.agent.trial.compare = false
		}
	}
	if tab.typesetMode == typesetPreview {
		tab.typesetMode = typesetSplit
	}
	tab.typesetCompactPDF, tab.typesetFocusPDF = false, false
	s.focusEditor = true
}

func (s *Shell) rebuildScholarPreview(p *scholarState, tab *Tab) {
	mode := tab.typesetMode
	if mode == typesetSource {
		mode = typesetSplit
	}
	s.setTypesetView(p, tab, mode)
	p.agent.trial = scholarTrialView{}
	tab.typesetCompactPDF, tab.typesetPaused = true, false
	s.closeCompile()
}

func (s *Shell) scholarDocumentTools(c *ui.Context, p *scholarState, tab *Tab) {
	c = c.Key(tab.ID)
	if isManuscript(tab) {
		total, _ := c.Size()
		width := s.scholarWorkspaceWidthAt(total)
		preview := s.canPreviewTypeset(tab)
		mode := tab.typesetMode
		if !preview {
			mode = typesetSource
		}
		shown := s.scholarPreviewMode(c, p, tab)
		compact := s.scholarSinglePane(total)
		label := s.tr(scholarViewLabels[mode])
		icon := [...]string{"code-xml", "book-open-text", "columns-2"}[mode]
		compactLabels := [typesetSplit]string{"scholarCompactSource", "scholarCompactPDF"}
		if compact && mode == typesetSplit && p.drawer == scholarClosed {
			label = s.tr(compactLabels[shown])
			if shown == typesetPreview {
				icon = "book-open-text"
			} else {
				icon = "code-xml"
			}
		}
		s.scholarMenuButton(c.Key("layout"), icon, label, width >= 400).Menu(func(m *ui.Menu) {
			for i, key := range scholarViewLabels {
				if m.Item(s.tr(key)).Radio(i == mode).Disabled(!preview && i != typesetSource).Chosen() {
					s.setTypesetView(p, tab, i)
				}
			}
			if compact && mode == typesetSplit {
				m.Separator()
				for _, side := range []int{typesetSource, typesetPreview} {
					if m.Item(s.tr(compactLabels[side])).Radio(side == shown && p.drawer == scholarClosed).Chosen() {
						tab.typesetCompactPDF = side == typesetPreview
						s.setTypesetView(p, tab, typesetSplit)
					}
				}
			}
		})
	}
	tip := s.tr("scholarMoreCommands")
	s.toolButton(c.Key("more"), "ellipsis", tip, false).Label(tip).FocusRing(true).Menu(func(m *ui.Menu) {
		if m.Item(s.tr("searchReplace")).Shortcut(ui.Cmd, ui.KeyF).Chosen() {
			s.toggleDocumentSearch()
		}
		if isManuscript(tab) && m.Item(s.tr("scholarInsertLocalCitations")).Shortcut(scholarCiteMods, ui.KeyK).Chosen() {
			s.openScholarCitations()
		}
		m.Separator()
		if m.Item(s.tr("scholarAgentName")).Checked(p.drawer == scholarCodex).Shortcut(scholarAgentMods, ui.KeyL).Chosen() {
			s.openScholarAgent(p)
		}
		if m.Item(s.tr("scholarDiagnostics")).Checked(p.drawer == scholarDiagnostics).Chosen() {
			s.scholarToggleDrawer(p, scholarDiagnostics)
		}
		m.Separator()
		if m.Item(s.tr("scholarCommandPalette")).Shortcut(ui.Cmd, ui.KeyP).Chosen() {
			s.openScholarPalette(p)
		}
		m.Submenu(s.tr("scholarWorkspace"), func(nav *ui.Menu) {
			width, _ := c.Size()
			s.scholarNavigationMenu(nav, p, width, !s.scholarSidebarHidden(c))
		})
	})
}

func (s *Shell) scholarMenuButton(c *ui.Context, icon, label string, showText bool) ui.Element {
	b := ui.ButtonBase(c).Padding(4, 6).Radius(c.Theme().Radius).FocusRing(true).Label(label).Tooltip(label)
	if b.Hovered() {
		b.Background(s.ch.hover)
	}
	b.Children(func() {
		ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, s.icon(icon)).FontSize(14).TextColor(s.ch.muted)
			if showText {
				ui.Text(c, label).FontSize(12).SingleLine()
			}
			ui.Icon(c, s.icon("chevron-down")).FontSize(12).TextColor(s.ch.muted)
		})
	})
	return b
}

func (s *Shell) scholarCompileActions(m *ui.Menu, tab *Tab) {
	pane := s.compile
	current := s.activeTab() == tab
	busy := pane != nil && pane.tab == tab && pane.busy
	if m.Item(s.tr("scholarStopTypeset")).Disabled(!current || !busy).Chosen() {
		tab.typesetPaused = true
		if tab.typesetMode == typesetSplit {
			tab.typesetCompactPDF = true
		}
		s.closeCompile()
	}
	if m.Item(s.tr("scholarRebuildPreview")).Disabled(!current || busy).Chosen() {
		if p := s.scholarForTab(tab); p != nil {
			s.rebuildScholarPreview(p, tab)
		}
	}
}

func (s *Shell) openScholarCitations() {
	tab := s.activeTab()
	if p := s.scholarForTab(tab); p != nil && isManuscript(tab) && !p.literature.picker && !p.navigation.open {
		s.revealScholarSource(tab)
		p.literature.picker, p.literature.focusPicker = true, true
	}
}

func (s *Shell) scholarCommandTip(label string, mods ui.Modifiers, key ui.Key) string {
	return s.tr(label) + " (" + commandShortcutDisplay(mods, key) + ")"
}

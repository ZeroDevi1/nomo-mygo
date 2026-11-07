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
	if p != nil {
		p.changes.compare = false
	}
	tab.typesetMode, tab.typesetPaused = mode, false
	if mode == typesetSource {
		s.closeCompile()
		s.focusEditor = true
	}
}

func (s *Shell) scholarDocumentTools(c *ui.Context, p *scholarState, tab *Tab) {
	c = c.Key(tab.ID)
	total, _ := c.Size()
	inspectorVisible := s.scholarInspectorVisible(p, total)
	w := s.scholarWorkspaceWidth(c)
	compact := w < s.pf.scholarCommandThreshold()
	preview := s.canPreviewTypeset(tab)
	manuscript := isManuscript(tab)
	mode := tab.typesetMode
	if !preview {
		mode = typesetSource
	}
	labels := [len(scholarViewLabels)]string{}
	for i, key := range scholarViewLabels {
		labels[i] = s.tr(key)
	}
	if manuscript {
		if compact {
			b := ui.ButtonBase(c).Padding(4, 6).Radius(c.Theme().Radius).FocusRing(true).
				Label(labels[mode]).Tooltip(labels[mode])
			if b.Hovered() {
				b.Background(s.ch.hover)
			}
			b.Children(func() {
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					if w >= 340 {
						ui.Text(c, labels[mode]).FontSize(12).SingleLine()
					} else {
						icon := [...]string{"code-xml", "book-open-text", "columns-2"}[mode]
						ui.Icon(c, s.icon(icon)).FontSize(14).TextColor(s.ch.accent)
					}
					ui.Icon(c, s.icon("chevron-down")).FontSize(12).TextColor(s.ch.muted)
				})
			})
			b.Menu(func(m *ui.Menu) {
				for i, label := range labels {
					if m.Item(label).Radio(mode == i).Disabled(!preview && i != typesetSource).Chosen() {
						s.setTypesetView(p, tab, i)
					}
				}
			})
		} else if ui.Segmented(c, &mode, labels[:]...).FontSize(12).Disabled(!preview).Changed() {
			s.setTypesetView(p, tab, mode)
		}
	}

	if !compact {
		s.documentSearchButton(c)
		if manuscript {
			ui.Box(c).Size(1, 16).Margin(0, 3).Background(s.ch.separator)
			if s.toolButton(c, "quote", s.scholarCommandTip("scholarInsertLocalCitations", scholarCiteMods, ui.KeyK), false).Label(s.tr("scholarInsertLocalCitations")).FocusRing(true).Clicked() {
				s.openScholarCitations()
			}
		}
		ui.Box(c).Size(1, 16).Margin(0, 3).Background(s.ch.separator)
		if s.toolButton(c, "message-square-text", s.scholarCommandTip("scholarAgentName", scholarAgentMods, ui.KeyL), inspectorVisible && p.drawer == scholarCodex).Label(s.tr("scholarAgentName")).FocusRing(true).Clicked() {
			s.openScholarAgent(p)
		}
		if s.toolButton(c, "triangle-alert", s.tr("scholarDiagnostics"), inspectorVisible && p.drawer == scholarDiagnostics).Label(s.tr("scholarDiagnostics")).FocusRing(true).Clicked() {
			s.scholarToggleDrawer(p, scholarDiagnostics)
		}
	}

	// 紧凑双栏保留独立视图选择器；更多菜单收纳低频操作。
	tip := s.tr("scholarMoreCommands")
	compactSplit := preview && tab.typesetMode == typesetSplit && s.scholarCanvasWidth(c) < 620
	showingPDF := compactSplit && p.compactPDF
	if compactSplit {
		icon, label := "book-open-text", labels[typesetPreview]
		if showingPDF {
			icon, label = "code-xml", labels[typesetSource]
		}
		tip := s.tr("scholarCompactShow") + " · " + label
		if s.toolButton(c, icon, tip, false).Label(tip).FocusRing(true).Clicked() {
			p.compactPDF = !showingPDF
			if !p.compactPDF {
				s.closeCompile()
				s.focusEditor = true
			}
		}
	}
	s.toolButton(c, "ellipsis", tip, compact && inspectorVisible && (p.drawer == scholarCodex || p.drawer == scholarDiagnostics)).
		Label(tip).FocusRing(true).Menu(func(m *ui.Menu) {
		if compact {
			if m.Item(s.tr("searchReplace")).Shortcut(ui.Cmd, ui.KeyF).Chosen() {
				s.toggleDocumentSearch()
			}
			if manuscript && m.Item(s.tr("scholarInsertLocalCitations")).Shortcut(scholarCiteMods, ui.KeyK).Chosen() {
				s.openScholarCitations()
			}
			m.Separator()
			if m.Item(s.tr("scholarAgentName")).Checked(inspectorVisible && p.drawer == scholarCodex).Shortcut(scholarAgentMods, ui.KeyL).Chosen() {
				s.openScholarAgent(p)
			}
			if m.Item(s.tr("scholarDiagnostics")).Checked(inspectorVisible && p.drawer == scholarDiagnostics).Chosen() {
				s.scholarToggleDrawer(p, scholarDiagnostics)
			}
			m.Separator()
		}
		if m.Item(s.tr("scholarCommandPalette")).Shortcut(ui.Cmd, ui.KeyP).Chosen() {
			s.openScholarPalette(p)
		}
		m.Submenu(s.tr("scholarWorkspace"), func(nav *ui.Menu) {
			width, _ := c.Size()
			for i, destination := range scholarDestinations {
				if nav.Item(s.tr(destination.label)).Radio(p.nav == i && !s.scholarSidebarHidden(c)).Chosen() {
					s.showScholarNavigation(p, i, width)
				}
			}
		})
		if preview && !p.agent.trial.show && !p.changes.compare && tab.typesetMode != typesetSource && (!compactSplit || showingPDF) {
			m.Separator()
			s.scholarCompileActions(m, tab)
		}
	})
}

func (s *Shell) scholarCompileActions(m *ui.Menu, tab *Tab) {
	pane := s.compile
	current := s.activeTab() == tab
	busy := pane != nil && pane.tab == tab && pane.busy
	if m.Item(s.tr("scholarStopTypeset")).Disabled(!current || !busy).Chosen() {
		tab.typesetPaused = true
		if p := s.scholarForTab(tab); p != nil && tab.typesetMode == typesetSplit {
			p.compactPDF = true
		}
		s.closeCompile()
	}
	if m.Item(s.tr("scholarRebuildPreview")).Disabled(!current || busy).Chosen() {
		tab.typesetPaused = false
		s.closeCompile()
	}
}

func (s *Shell) openScholarCitations() {
	tab := s.activeTab()
	if p := s.scholarForTab(tab); p != nil && isManuscript(tab) && !p.literature.picker && !p.navigation.open {
		p.literature.picker, p.literature.focusPicker = true, true
	}
}

func (s *Shell) scholarCommandTip(label string, mods ui.Modifiers, key ui.Key) string {
	return s.tr(label) + " (" + commandShortcutDisplay(mods, key) + ")"
}

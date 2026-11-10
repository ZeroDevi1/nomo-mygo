package shell

import (
	"github.com/egoist/mygo/ui"
	"nomo/desktop/internal/scholar"
)

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
	p.agent.trial.close()
	p.agent.reviewHidden = true
	p.references.right = nil
	p.references.selectTab(nil)
	s.closeScholarInspector(p)
	if tab.typesetMode != mode {
		tab.typesetPaused = false
	}
	tab.typesetMode, tab.typesetFocusPDF = mode, mode == typesetPreview
	s.focusEditor = true
}

func (s *Shell) scholarPreviewMode(c *ui.Context, p *scholarState, tab *Tab) int {
	width, _ := c.Size()
	if v := &p.agent.trial; v.show && v.tabID == tab.ID {
		return typesetSplit
	}
	review := s.scholarReviewPatch(p, tab) != nil
	if p.references.right != nil && !p.references.catalog && !s.scholarSinglePane(width) {
		if review {
			return typesetSource
		}
		return typesetPreview
	}
	if p.drawer != scholarClosed && (p.drawer != scholarCodex || s.scholarWorkspaceWidthAt(width) < 930) {
		return typesetSource
	}
	mode := tab.typesetMode
	if review && mode == typesetPreview {
		mode = typesetSplit
	}
	if mode == typesetSplit && s.scholarSinglePane(width) {
		if tab.typesetCompactPDF && !review {
			return typesetPreview
		}
		return typesetSource
	}
	return mode
}

func (s *Shell) scholarPDFOnly(c *ui.Context, p *scholarState, tab *Tab) bool {
	return s.canPreviewTypeset(tab) && s.scholarPreviewMode(c, p, tab) == typesetPreview
}

func (s *Shell) revealScholarSource(tab *Tab) {
	if p := s.scholar; p != nil {
		p.references.catalog = false
		p.references.focused, p.references.left, p.references.right = nil, nil, nil
		if s.win != nil {
			width, _ := s.win.ContentSize()
			if p.activeTabID != tab.ID || s.scholarSinglePane(float32(width)) {
				s.closeScholarInspector(p)
			}
		}
		p.changes.compare = false
		p.agent.trial.close()
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
		if p.references.catalog || p.references.focused != nil || p.references.right != nil && !compact {
			label = s.tr("scholarManuscriptLayout")
		} else if compact && mode == typesetSplit && p.drawer == scholarClosed && !p.agent.trial.show {
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
						p.changes.compare = false
						p.agent.trial.close()
						p.agent.reviewHidden = true
						s.selectScholarPDF(p, nil)
						s.focusEditor = side == typesetSource
					}
				}
			}
		})
		label = s.tr("scholarOutline")
		open := &p.navigation.outlineOpen
		outline := s.toolButton(c.Key("outline"), "list-tree", label, *open).Label(label).FocusRing(true)
		if outline.Clicked() {
			*open = !*open
		}
		ui.Popover(c, outline, open, func() {
			_, height := c.Size()
			ui.Column(c).Width(300).Height(min(360, height*.65)).Padding(5).Role(ui.RoleDialog).Label(label).Children(func() {
				s.scholarOutlineView(c, p, tab)
			})
		})
	}
	label := s.tr("scholarToggleInspector")
	width, _ := c.Size()
	visible := (p.drawer == scholarReader || p.drawer == scholarDiagnostics) && s.scholarInspectorVisible(p, width)
	if s.toolButton(c.Key("inspector"), "panel-right", label, visible).Label(label).FocusRing(true).Clicked() {
		if visible {
			s.closeScholarInspector(p)
		} else if reading := p.references.focused; reading != nil && !p.references.catalog && !p.busy {
			if reading.key != "" {
				s.openScholarAnnotations(p, reading.entry, reading.zotero, reading.key, reading.title)
			} else {
				s.selectScholarEntry(p, reading.entry)
			}
		} else {
			drawer := p.lastInspector
			if drawer == scholarClosed || p.references.catalog && p.literature.selected != (scholar.Entry{}) {
				drawer = scholarDiagnostics
				if p.references.catalog && p.literature.selected != (scholar.Entry{}) {
					drawer = scholarReader
				}
			}
			s.showScholarInspector(p, drawer)
		}
	}
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

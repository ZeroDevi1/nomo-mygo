package shell

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/scholar"
	"nomo/desktop/typeset"
)

type scholarManuscriptReturn struct {
	tabID   int
	path    string
	version uint64
}

// The existing tab and editor group own their selection, divider, and PDF
// position. The return target carries only the identity of the live buffer.
func (s *Shell) rememberScholarManuscript(p *scholarState) {
	tab := s.activeTab()
	if !p.references.catalog && p.references.focused == nil && isManuscript(tab) {
		p.returnManuscript = scholarManuscriptReturn{tab.ID, tab.Path, scholarVersion(tab)}
	}
}

func (s *Shell) scholarReturnTab(p *scholarState) *Tab {
	target := p.returnManuscript
	tab := s.tabWithID(target.tabID)
	if !isManuscript(tab) || !samePath(tab.Path, target.path) || scholarVersion(tab) != target.version || s.tabHidden(tab) {
		return nil
	}
	if _, err := scholar.Relative(p.project.Root, tab.Path); err != nil {
		return nil
	}
	return tab
}

func (s *Shell) scholarReturnToManuscript(p *scholarState) {
	tab := s.scholarReturnTab(p)
	backToConversation := p.returnConversation
	s.selectScholarPDF(p, nil)
	p.returnConversation = backToConversation
	if tab == nil {
		return
	}
	if group := s.groups.root.containing(s.workspaceDocumentItem(tab)); group != nil {
		s.activateWorkspaceItem(group, s.workspaceDocumentItem(tab))
	}
	s.activate(s.findTabIndex(tab.ID))
	s.focusEditor = true
}

func (s *Shell) scholarReadingReturnControls(c *ui.Context, p *scholarState, iconOnly bool) {
	if (p.references.focused != nil || p.references.catalog) && p.returnManuscript.tabID != 0 {
		label := s.tr("scholarReturnManuscript")
		if tab := s.scholarReturnTab(p); tab != nil {
			label = fmt.Sprintf(s.tr("scholarReturnDocument"), filepath.Base(tab.Path))
		}
		var back ui.Element
		if iconOnly {
			back = s.toolButton(c.Key("return-document"), "chevron-left", label, false).Label(label).FocusRing(true)
		} else {
			back = s.scholarACPAction(c.Key("return-document"), "chevron-left", label, false).MaxWidth(220)
		}
		if back.Clicked() {
			s.scholarReturnToManuscript(p)
		}
	}
	if p.returnConversation && p.drawer != scholarCodex {
		label := s.tr("scholarACPBackToChat")
		if s.toolButton(c.Key("return-conversation"), "message-square-text", label, false).Label(label).FocusRing(true).Clicked() {
			p.returnConversation = false
			p.agent.acp.focusTranscript = true
			s.showScholarInspector(p, scholarCodex)
		}
	}
}

func (s *Shell) scholarInspectorBackLabel(p *scholarState) string {
	if p.references.focused != nil {
		return s.tr("scholarACPBackToReading")
	}
	if p.references.catalog {
		return s.tr("scholarCatalog")
	}
	if tab := s.activeTab(); isManuscript(tab) {
		return fmt.Sprintf(s.tr("scholarReturnDocument"), filepath.Base(tab.Path))
	}
	return s.tr("scholarCloseDrawer")
}

func (s *Shell) scholarReadingPanelTabs(c *ui.Context, p *scholarState, t *scholarReferenceTab) {
	ui.Row(c).FillWidth().Padding(3, 8).Gap(4).FocusGroup(ui.Horizontal).Role(ui.RoleTabList).Label(s.tr("scholarToggleInspector")).Children(func() {
		l := &p.literature
		type readingPanel struct {
			label, icon string
			selected    bool
			open        func()
		}
		tabs := []readingPanel{
			{"scholarReferenceDetails", "panel-right", p.drawer == scholarReader && l.annotations == nil, func() {
				if l.annotations != nil {
					s.returnScholarAnnotationEntry(p, l.annotations)
				}
				if !scholarSameCatalogItem(l.selected, t.entry) {
					s.setScholarEntry(p, t.entry)
				}
				s.showScholarInspector(p, scholarReader)
			}},
		}
		if t.key != "" {
			tabs = append(tabs, readingPanel{"scholarReadAnnotations", "highlighter", p.drawer == scholarReader && l.annotations != nil, func() {
				s.openScholarAnnotations(p, t.entry, t.zotero, t.key, t.title)
			}})
		}
		tabs = append(tabs, readingPanel{"scholarACPChat", "message-square-text", p.drawer == scholarCodex, func() { s.showScholarInspector(p, scholarCodex) }})
		for _, tab := range tabs {
			label := s.tr(tab.label)
			button := s.scholarACPAction(c.Key(tab.label), tab.icon, label, tab.selected).Grow(1).MinWidth(0).
				Role(ui.RoleTab).AccessSelected(tab.selected).Disabled(p.busy && tab.label != "scholarACPChat")
			if tab.selected {
				button.Background(s.ch.selected)
			}
			if button.Clicked() {
				tab.open()
			}
		}
	})
}

func (s *Shell) scholarCurrentDiagnostics(p *scholarState, tab *Tab) scholarCompileReport {
	if pane := s.compile; pane != nil && pane.tab == tab && !pane.busy {
		return pane.report()
	}
	return p.diagnostics
}

func (s *Shell) openScholarDiagnostics(p *scholarState, tab *Tab) {
	report := s.scholarCurrentDiagnostics(p, tab)
	p.diagnosticOrigin = 0
	if report.tabID == tab.ID && samePath(report.source, tab.Path) {
		if note := typeset.DiagnosticLocation(report.message); note.Line == 0 || !s.jumpScholarDiagnostic(p, report.path, note) {
			for _, note := range report.warnings {
				if note.Line > 0 && note.File != "" && s.jumpScholarDiagnostic(p, report.path, note) {
					break
				}
			}
		}
	}
	s.showScholarInspector(p, scholarDiagnostics)
	p.diagnosticScroll.X, p.diagnosticScroll.Y = 0, 0
	if report.tabID == tab.ID {
		p.diagnosticOrigin = tab.ID
	}
}

func (s *Shell) scholarACPStarter(c *ui.Context, p *scholarState) {
	a := &p.agent
	if p.busy || strings.TrimSpace(a.instruction) != "" {
		return
	}
	if t := p.references.focused; t != nil && !p.references.catalog {
		scope := scholarReferenceScope(t)
		if len(a.acp.quotes) != 0 && a.acp.quotes[len(a.acp.quotes)-1].scope == scope {
			s.scholarACPStarterAction(c, p, "scholarACPExplainSelection", "quote", "")
		} else if !a.hasDraft() && t.bitmap != nil && t.quote.cancel == nil && scholarValidPDFRegion(t.quote.region) {
			label := s.tr("scholarACPExplainSelection")
			known, available := scholarACPImageCapability()
			if s.scholarACPAction(c, "quote", label, false).Disabled(t.quote.picture && known && !available).Clicked() {
				ref := scholarACPQuote{scope: scope, label: t.title}
				s.captureScholarPDFQuote(p, t, s.scholarACPQuoteThread(p, ref, false), false, false, label)
			}
		}
		return
	}
	tab := s.activeTab()
	if p.references.catalog || a.hasDraft() || !isManuscript(tab) {
		return
	}
	s.scholarACPStarterAction(c.Key("argument"), p, "scholarACPCheckArgument", "file-text", "")
	report := s.scholarCurrentDiagnostics(p, tab)
	if report.tabID == tab.ID && samePath(report.source, tab.Path) && strings.TrimSpace(report.message) != "" {
		s.scholarACPStarterAction(c.Key("diagnostic"), p, "scholarACPExplainDiagnostic", "triangle-alert", report.message)
	}
}

func (s *Shell) scholarACPStarterAction(c *ui.Context, p *scholarState, key, glyph, diagnostics string) {
	label := s.tr(key)
	if !s.scholarACPAction(c, glyph, label, false).Clicked() {
		return
	}
	a := &p.agent
	a.instruction = label
	a.acp.focusComposer = true
	if diagnostics != "" {
		a.parts[3], a.include[3] = diagnostics, true
	} else if key == "scholarACPCheckArgument" {
		target := s.scholarSelection(p)
		if target.tabID == 0 {
			tab := s.activeTab()
			st := tab.Source()
			snap := st.Buffer.Snapshot()
			start := snap.LineStart(snap.LineOf(st.Selection().Head))
			end := min(snap.Len(), start+scholarSelectionLimit)
			text := snap.Slice(start, end)
			target = scholarTarget{tabID: tab.ID, path: tab.Path, line: snap.LineOf(start), before: strings.ToValidUTF8(text, "")}
		}
		if target.before != "" {
			a.acp.quotes = append(a.acp.quotes, s.scholarSourceQuote(p, target))
		}
	}
}

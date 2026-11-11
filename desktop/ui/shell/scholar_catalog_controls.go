package shell

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/scholar"
)

func (s *Shell) scholarLibraryToolbar(c *ui.Context, p *scholarState) {
	l := &p.literature
	ui.Row(c).FillWidth().Height(38).Padding(0, 7).Gap(5).AlignItems(ui.Center).Children(func() {
		titles := []string{s.tr("scholarProject"), "Zotero", s.tr("scholarOnline")}
		offline := l.zotero.ReadOnly()
		if offline {
			titles[1] = s.tr("scholarZoteroReadOnly")
		}
		glyph := [...]string{"folder", "book-open", "globe"}[l.mode]
		showSource := l.mode != 1 || offline || s.pf.windows && !l.connected
		s.scholarMenuButton(c.Key("source"), glyph, titles[l.mode], showSource).Disabled(p.busy).Menu(func(m *ui.Menu) {
			for i, title := range titles {
				if m.Item(title).Radio(l.mode == i).Chosen() {
					l.mode = i
				}
			}
		})
		if l.mode == 0 {
			s.scholarProjectCatalogToolbar(c.Key("project"), p)
			return
		}
		if l.mode != 1 || !l.connected || l.catalogForm() != nil {
			return
		}
		ui.Box(c).Width(1).Height(18).Background(s.ch.separator)
		s.scholarZoteroCatalogActions(c.Key("create"), p)
		if s.scholarCompactIcon(c.Key("lookup"), "wand-sparkles", s.tr("scholarOnline"), false).Disabled(p.busy).Clicked() {
			l.mode = 2
		}
		glyph, label := s.scholarReferenceOpenLabel(p, l.selected)
		if s.scholarCompactIcon(c.Key("pdf"), glyph, label, false).Disabled(p.busy || l.selected.RemoteID == "").Clicked() {
			s.openScholarReference(p, l.selected)
		}
		if !offline && s.scholarCompactIcon(c.Key("note"), "sticky-note", s.tr("scholarZoteroNewNote"), false).Disabled(p.busy || l.selected.Source != "Zotero" || l.selected.RemoteID == "" || l.detail.Deleted || l.detail.ItemType == "" || l.detail.ItemType == "note" || !l.zotero.CanWrite()).Clicked() {
			s.startScholarZoteroNote(p)
		}
		if s.scholarCompactIcon(c.Key("refresh"), "refresh-cw", s.tr("refresh"), false).Disabled(p.busy).Clicked() {
			s.loadZotero(p, false, l.collection, l.sentQuery, 0)
		}
		ui.Spacer(c)
		placeholder := s.tr("scholarSearchZotero")
		in := s.scholarReaderInput(c.Key("search"), &l.remoteQuery).Width(300).MinWidth(0).Shrink(1).
			Background(s.ch.content).Label(placeholder).Placeholder(placeholder)
		search := s.scholarCompactIcon(c.Key("submit"), "search", s.tr("scholarSearch"), false).Disabled(p.busy)
		if !p.busy && (in.Submitted() || search.Clicked()) {
			l.recentOpen = false
			s.loadZotero(p, false, l.collection, l.remoteQuery, 0)
		}
		if offline {
			if p.busy && l.zoteroBrowseJob == p.job &&
				s.scholarCompactIcon(c.Key("cancel"), "x", s.tr("scholarAgentCancelWork"), false).Clicked() {
				p.cancelCurrent()
			}
		} else {
			s.scholarCompactIcon(c.Key("options"), "list-filter", s.tr("scholarSearchOptions"), l.fulltext).Menu(func(m *ui.Menu) {
				if m.Item(s.tr("scholarFulltextPage")).Checked(l.fulltext).
					Disabled(p.busy || l.scope != scholar.ZoteroAll || l.tag != "").Chosen() {
					l.fulltext = !l.fulltext
				}
			})
		}
	})
}

func (s *Shell) scholarProjectCatalogToolbar(c *ui.Context, p *scholarState) {
	l := &p.literature
	ui.Box(c).Width(1).Height(18).Background(s.ch.separator)
	if s.scholarCompactIcon(c.Key("reload"), "refresh-cw", s.tr("scholarReloadReferences"), false).Disabled(p.busy).Clicked() {
		s.loadScholarLibrary(p, true)
	}
	ui.Spacer(c)

	fulltext := l.local.enabled
	placeholder := s.tr("scholarCitationSearch")
	if fulltext {
		placeholder = s.tr("scholarLocalFulltext")
	}
	in := ui.SearchField(c.Key("search"), &l.query).Width(290).MinWidth(0).Shrink(1).Height(27).
		FontSize(12).Padding(0, 8).Label(placeholder).Placeholder(placeholder)
	if fulltext {
		submit := s.scholarCompactIcon(c.Key("submit"), "search", s.tr("scholarSearch"), false).
			Disabled(p.busy || strings.TrimSpace(l.query) == "")
		if !p.busy && strings.TrimSpace(l.query) != "" && (in.Submitted() || submit.Clicked()) {
			s.searchScholarLocal(p, true)
		}
	} else if !p.busy && in.Submitted() && len(l.checked) != 0 {
		s.insertScholarProjectSelection(p)
	}
	s.scholarCompactIcon(c.Key("options"), "list-filter", s.tr("scholarSearchOptions"), fulltext).Disabled(p.busy).
		Menu(func(m *ui.Menu) {
			if m.Item(s.tr("scholarLocalFulltext")).Checked(fulltext).Chosen() {
				l.local.enabled = !fulltext
			}
		})
}

func (s *Shell) scholarZoteroPagination(c *ui.Context, p *scholarState) {
	l := &p.literature
	fulltext := l.fulltext && l.sentQuery != ""
	page, more := l.page/scholar.ZoteroPageSize, l.pageData.More
	if fulltext {
		page, more = l.fulltextPage, (l.fulltextPage+1)*scholar.ZoteroPageSize < len(l.fulltextEntries)
	}
	ui.Box(c).FillWidth().Height(1).Background(s.ch.separator)
	ui.Row(c).FillWidth().Height(30).Padding(0, 8).Gap(5).AlignItems(ui.Center).Background(s.ch.tabBar).Children(func() {
		ui.Text(c, fmt.Sprintf(s.tr("scholarCatalogPageCount"), len(l.pageData.Entries))).FontSize(11).TextColor(s.ch.muted).Grow(1).SingleLine().MinWidth(0)
		if s.toolButton(c.Key("previous"), "chevron-left", s.tr("scholarPreviousPage"), false).Disabled(p.busy || page == 0).Clicked() {
			if fulltext {
				l.fulltextPage--
			} else {
				s.loadZotero(p, false, l.collection, l.sentQuery, l.page-scholar.ZoteroPageSize)
			}
		}
		ui.Text(c, fmt.Sprint(page+1)).FontSize(11)
		if s.toolButton(c.Key("next"), "chevron-right", s.tr("scholarNextPage"), false).Disabled(p.busy || !more).Clicked() {
			if fulltext {
				l.fulltextPage++
			} else {
				s.loadZotero(p, false, l.collection, l.sentQuery, l.pageData.Next)
			}
		}
	})
}

func (s *Shell) scholarCatalogColumns(c *ui.Context, p *scholarState) (creator, year float32) {
	width := s.scholarWorkspaceWidth(c)
	if s.groups.paneWidth > 0 {
		width = s.groups.paneWidth
	}
	if s.groups.paneWidth == 0 && p.drawer != scholarClosed {
		width -= s.scholarInspectorWidth(p, width) + scholarInspectorGripWidth
	}
	if p.literature.mode == 1 && !s.scholarCatalogInSidebar(c, p) && width >= 600 {
		width -= 211
	}
	if width >= 380 {
		creator = 180
	}
	if p.literature.mode != 1 && width >= 560 {
		year = 52
	}
	return
}

const (
	scholarCatalogInset      = 7
	scholarCatalogDisclosure = 16
	scholarCatalogGlyphWidth = 14
	scholarCatalogAttachment = 18
	scholarCatalogCitation   = 26
)

func scholarCatalogLine(el ui.Element) ui.Element {
	return el.Grow(1).MinWidth(0).Padding(0, 4, 0, 1).Gap(8).AlignItems(ui.Center)
}

package shell

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/scholar"
)

type scholarCollectionBranch struct {
	page  scholar.ZoteroPage
	open  bool
	start int
}

type scholarTagPicker struct {
	open, more, tried bool
	query, sent       string
	start             int
	tags              []string
}

func (s *Shell) scholarCatalogInSidebar(c *ui.Context, p *scholarState) bool {
	return s.scholarUsesZoteroSidebar(p) && !s.scholarSidebarHidden(c)
}

func (s *Shell) revealScholarCatalog(p *scholarState) {
	if !p.references.catalog && (!s.editorGroupsVisible(p) || !s.editorGroupsShowCatalog()) {
		s.showScholarCatalog(p)
	}
}

func (s *Shell) scholarCatalogSidebar(c *ui.Context, p *scholarState) {
	l := &p.literature
	ui.Row(c).FillWidth().Height(36).Padding(0, 7).Gap(4).AlignItems(ui.Center).Children(func() {
		if s.toolButton(c.Key("collection"), "folder-plus", s.tr("scholarZoteroNewCollection"), false).
			Disabled(p.busy || !l.connected || l.zotero.ServerID == "").Clicked() {
			s.startScholarZoteroCollection(p, scholar.Collection{}, "", "scholarZoteroNewCollection")
		}
		ui.Spacer(c)
		if s.toolButton(c.Key("search"), "search", s.tr("scholarFilterCollections"), l.collectionSearch).Clicked() {
			l.collectionSearch = !l.collectionSearch
			if !l.collectionSearch {
				l.collectionFilter = ""
			}
		}
	})
	if l.collectionSearch {
		ui.TextInput(c.Key("filter"), &l.collectionFilter).FillWidth().Placeholder(s.tr("scholarFilterCollections"))
	}
	s.scholarCatalogScope(c.Key("scope"), p)
}

func (s *Shell) scholarCatalogButton(c *ui.Context, glyph, title string, selected bool) ui.Element {
	b := ui.ButtonBase(c).FillWidth().MinWidth(0).Height(28).Padding(0, 6).Gap(5).Radius(4).
		Justify(ui.Start).AlignItems(ui.Center).FocusRing(true).Selected(selected).TextColor(s.ch.text).Tooltip(title)
	if selected {
		b.Background(scholarZoteroSelection(c))
	} else if b.Hovered() {
		b.Background(s.ch.hover)
	}
	b.Children(func() {
		ui.Box(c).Size(16, 16).Shrink(0).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			ui.Icon(c, s.icon(glyph)).FontSize(15).TextColor(s.scholarZoteroIconColor(c, glyph))
		})
		ui.Text(c, title).FontSize(12).SingleLine().Grow(1).MinWidth(0).TextColor(s.ch.text)
	})
	return b
}

func (s *Shell) scholarZoteroCatalog(c *ui.Context, p *scholarState) {
	l := &p.literature
	if !l.connected {
		ui.Text(c, s.tr("scholarEnableZotero")).FontSize(12).Wrap()
		if ui.Button(c, s.tr("scholarConnectZotero")).Disabled(p.busy).Clicked() {
			s.loadZotero(p, true, "", "", 0)
		}
		if l.recentOpen {
			s.scholarCatalogRows(c, p, l.recent, false)
		}
		return
	}
	if form := l.catalogForm(); form != nil {
		ui.Column(c.Key("zotero-form")).FillWidth().Grow(1).MinHeight(0).Background(s.ch.sidebar).Children(func() {
			s.scholarReaderHeader(c.Key("title"), s.tr(form.label), "", nil, nil)
			s.scholarZoteroForm(c.Key(form), p, form, "")
		})
		return
	}
	if !p.busy {
		switch {
		case !l.loaded && p.err == "":
			s.loadZotero(p, false, l.collection, l.remoteQuery, 0)
		case l.libraries == nil:
			z := l.zotero
			s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
				ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				libraries, err := z.Libraries(ctx)
				return func() {
					if err != nil {
						libraries = []scholar.ZoteroLibrary{{}}
					}
					l.libraries = libraries
				}, err
			})
		case !l.treeTried:
			l.treeTried = true
			s.loadScholarCollectionBranch(p, "", 0)
		case !l.tagPicker.tried:
			l.tagPicker.tried = true
			s.loadScholarTags(p, "", 0)
		}
	}
	width := s.scholarWorkspaceWidth(c)
	if s.groups.paneWidth > 0 {
		width = s.groups.paneWidth
	}
	if s.groups.paneWidth == 0 && p.drawer == scholarReader {
		width -= s.scholarInspectorWidth(p, width)
	}
	if s.scholarCatalogInSidebar(c, p) {
		s.scholarCatalogResults(c, p)
		return
	}
	if width < 600 {
		if s.scholarTabButton(c.Key("collections"), "folder", s.tr("scholarCollections"), l.scopeOpen).Clicked() {
			l.scopeOpen = !l.scopeOpen
		}
		if l.scopeOpen {
			s.scholarCatalogScope(c, p)
		} else {
			s.scholarCatalogResults(c, p)
		}
		return
	}
	ui.Row(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
		ui.Column(c.Key("collections")).Width(210).Shrink(0).FillHeight().MinHeight(0).Background(s.ch.sidebar).Children(func() {
			s.scholarCatalogSidebar(c, p)
		})
		ui.Box(c).Width(1).FillHeight().Background(s.ch.separator)
		ui.Column(c.Key("catalog-results")).Grow(1).MinWidth(0).FillHeight().MinHeight(0).Children(func() {
			s.scholarCatalogResults(c, p)
		})
	})
}

func (s *Shell) scholarCatalogScope(c *ui.Context, p *scholarState) {
	l := &p.literature
	ui.Column(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Scroll(c.Key("collections")).Grow(1).FillWidth().MinHeight(0).Children(func() {
			ui.Column(c).FillWidth().Padding(0, 6).Gap(0).Children(func() {
				for _, library := range l.libraries {
					ck := c.Key(library.ID)
					name := library.Name
					if library.ID == 0 {
						name = s.tr("scholarPersonalLibrary")
					}
					active := l.zotero.Group == library.ID
					selected := active && l.scope == scholar.ZoteroAll && l.collection == "" && !l.recentOpen
					branch := l.tree[""]
					root := ui.Row(ck).FillWidth().Height(28).Radius(4).AlignItems(ui.Center)
					if selected {
						root.Background(scholarZoteroSelection(c))
					}
					root.Children(func() {
						glyph := "chevron-right"
						if active && branch != nil && branch.open {
							glyph = "chevron-down"
						}
						if s.toolButton(ck.Key("expand"), glyph, s.tr("scholarCollections"), false).Width(16).Padding(1).Disabled(!active || p.busy).Clicked() {
							if branch != nil {
								branch.open = !branch.open
							} else {
								s.loadScholarCollectionBranch(p, "", 0)
							}
						}
						if s.scholarCatalogButton(ck.Key("select"), "landmark", name, selected).Grow(1).MinWidth(0).Disabled(p.busy).Clicked() {
							if !active {
								l.tree, l.treeTried = nil, false
								l.tag = ""
								l.tagPicker = scholarTagPicker{}
							}
							l.zotero.Group = library.ID
							l.recentOpen, l.scope, l.scopeOpen = false, scholar.ZoteroAll, false
							s.revealScholarCatalog(p)
							s.loadZotero(p, false, "", l.remoteQuery, 0)
						}
					})
					if active && (branch == nil || branch.open) {
						if s.scholarCatalogButton(ck.Key("recent"), "clock", s.tr("scholarRecentReading"), l.recentOpen).Padding(0, 6, 0, 31).Clicked() {
							l.recentOpen, l.scopeOpen = true, false
							s.revealScholarCatalog(p)
						}
						s.scholarCollectionTree(ck, p, "", 1)
						for _, scope := range []struct {
							id           scholar.ZoteroScope
							label, glyph string
						}{
							{scholar.ZoteroPublications, "scholarPublications", "file-text"},
							{scholar.ZoteroUnfiled, "scholarUnfiled", "folder-open"},
							{scholar.ZoteroTrash, "scholarZoteroTrash", "trash"},
						} {
							if scope.id == scholar.ZoteroPublications && l.zotero.Group != 0 {
								continue
							}
							if s.scholarCatalogButton(ck.Key(scope.id), scope.glyph, s.tr(scope.label), !l.recentOpen && l.scope == scope.id).
								Padding(0, 6, 0, 31).Disabled(p.busy).Clicked() {
								l.recentOpen, l.scopeOpen, l.fulltext = false, false, false
								l.scope = scope.id
								s.revealScholarCatalog(p)
								s.loadZotero(p, false, "", l.remoteQuery, 0)
							}
						}
					}
				}
			})
		})
		ui.Box(c).Height(1).FillWidth().Background(s.ch.separator)
		s.scholarCatalogTags(c.Key("tags"), p)
	})
}

func (s *Shell) scholarCatalogTags(c *ui.Context, p *scholarState) {
	l := &p.literature
	picker := &l.tagPicker
	ui.Column(c).FillWidth().MinHeight(0).Children(func() {
		ui.Scroll(c.Key("cloud")).FillWidth().Height(154).Shrink(1).Children(func() {
			ui.Row(c).FillWidth().Wrap().Padding(6, 8).Gap(2).Children(func() {
				for i, tag := range picker.tags {
					b := ui.ButtonBase(c.Key(i)).Padding(2, 3).Radius(3).MaxWidth(190).Label(tag).Tooltip(tag).
						Selected(tag == l.tag).TextColor(s.ch.text).Disabled(p.busy)
					if tag == l.tag {
						b.Background(scholarZoteroSelection(c))
					} else if b.Hovered() {
						b.Background(s.ch.hover)
					}
					b.Children(func() { ui.Text(c, tag).FontSize(11).SingleLine().MinWidth(0) })
					if b.Clicked() {
						l.recentOpen, l.fulltext, l.scopeOpen = false, false, false
						if l.tag == tag {
							l.tag = ""
						} else {
							l.tag = tag
						}
						s.revealScholarCatalog(p)
						s.loadZotero(p, false, l.collection, l.remoteQuery, 0)
					}
				}
			})
		})
		ui.Box(c).Height(1).FillWidth().Background(s.ch.separator)
		ui.Row(c).FillWidth().Height(32).Padding(3, 8).Gap(4).AlignItems(ui.Center).Children(func() {
			in := ui.TextInput(c.Key("query"), &picker.query).Grow(1).MinWidth(0).Height(24).FontSize(11).
				Padding(3, 6).Radius(4).Background(s.ch.content).Placeholder(s.tr("scholarSearchTags"))
			if in.Submitted() && !p.busy {
				s.loadScholarTags(p, picker.query, 0)
			}
			s.scholarCompactIcon(c.Key("options"), "list-filter", s.tr("scholarSearchTags"), l.tag != "").Menu(func(m *ui.Menu) {
				if m.Item(s.tr("scholarSearch")).Disabled(p.busy).Chosen() {
					s.loadScholarTags(p, picker.query, 0)
				}
				if m.Item(s.tr("scholarPreviousPage")).Disabled(p.busy || picker.start == 0).Chosen() {
					s.loadScholarTags(p, picker.sent, picker.start-scholar.ZoteroPageSize)
				}
				if m.Item(s.tr("scholarNextPage")).Disabled(p.busy || !picker.more).Chosen() {
					s.loadScholarTags(p, picker.sent, picker.start+scholar.ZoteroPageSize)
				}
				if m.Item(s.tr("scholarClearTag")).Disabled(p.busy || l.tag == "").Chosen() {
					l.tag = ""
					s.revealScholarCatalog(p)
					s.loadZotero(p, false, l.collection, l.remoteQuery, 0)
				}
			})
		})
	})
}

func (s *Shell) loadScholarTags(p *scholarState, query string, start int) {
	l := &p.literature
	z := l.zotero
	query = strings.TrimSpace(query)
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		tags, more, err := z.Tags(ctx, query, start)
		return func() {
			if l.zotero == z && err == nil {
				picker := &l.tagPicker
				picker.tags, picker.more, picker.start, picker.sent = tags, more, start, query
			}
		}, err
	})
}

func (s *Shell) scholarCatalogResults(c *ui.Context, p *scholarState) {
	l := &p.literature
	if l.recentOpen {
		ui.Text(c, s.tr("scholarRecentReading")).Bold().FontSize(12)
		s.scholarCatalogRows(c, p, l.recent, false)
		return
	}
	if !l.loaded && !p.busy && ui.Button(c, s.tr("refresh")).Clicked() {
		s.loadZotero(p, false, l.collection, l.remoteQuery, 0)
	}
	s.scholarZoteroResults(c, p)
}

func scholarNoteTitle(s *Shell, note scholar.ZoteroNote) string {
	if note.Title != "" {
		return note.Title
	}
	return s.tr("scholarChildNote")
}

func scholarNoteContent(s *Shell, note scholar.ZoteroNote) (title, body string) {
	body = strings.TrimSpace(note.Text)
	first, rest, multiline := strings.Cut(body, "\n")
	if multiline && strings.TrimSpace(first) == note.Title {
		return scholarNoteTitle(s, note), strings.TrimSpace(rest)
	}
	if body == "" {
		return scholarNoteTitle(s, note), body
	}
	// A generated summary of a single paragraph is not a removable heading.
	return s.tr("scholarChildNote"), body
}

func (s *Shell) scholarNoteDetails(c *ui.Context, p *scholarState, note scholar.ZoteroNote) {
	l := &p.literature
	_, body := scholarNoteContent(s, note)
	if l.readerJump == 4 {
		ui.Box(c.Key("note-top")).Height(1).FillWidth().ScrollIntoView()
		l.readerJump = 0
	}
	if body != "" {
		scholarReaderText(c.Key("note-body"), body).Padding(10, 0, 12, 0).Selectable()
	}
	s.scholarZoteroMetadata(c, p, note.Tags, note.Related)
}

func (s *Shell) scholarZoteroMetadata(c *ui.Context, p *scholarState, tags, related []string) {
	l := &p.literature
	s.scholarReaderSection(c, p, 5, "tag", s.tr("scholarItemTags"), fmt.Sprint(len(tags)), func(c *ui.Context) {
		for i, tag := range tags {
			if s.scholarReaderLink(c.Key(i), "tag", tag).Disabled(!l.connected || p.busy).Clicked() {
				z := l.zotero
				z.Group = l.selected.Library
				if z.Group != l.zotero.Group {
					l.tree, l.treeTried = nil, false
					l.tagPicker = scholarTagPicker{}
				}
				l.zotero, l.mode, l.tag, l.recentOpen, l.fulltext = z, 1, tag, false, false
				l.scope = scholar.ZoteroAll
				s.showScholarCatalog(p)
				s.loadZotero(p, false, "", "", 0)
			}
		}
	})
	s.scholarReaderSection(c, p, 6, "link", s.tr("scholarItemRelated"), fmt.Sprint(len(related)), func(c *ui.Context) {
		for i, key := range related {
			label := key
			for _, e := range l.pageData.Entries {
				if e.RemoteID == key && e.Library == l.selected.Library {
					label = e.Title
					break
				}
			}
			if s.scholarReaderLink(c.Key(i), "link", label).Disabled(p.busy).Clicked() {
				z := l.zotero
				z.Group = l.selected.Library
				s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
					ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
					defer cancel()
					e, err := z.Entry(ctx, key)
					return func() {
						if err == nil {
							s.selectScholarEntry(p, e)
						}
					}, err
				})
			}
		}
	})
}

func scholarSameCatalogItem(a, b scholar.Entry) bool {
	if a.RemoteID != "" && b.RemoteID != "" {
		return a.Library == b.Library && a.RemoteID == b.RemoteID
	}
	return a.Key == b.Key && a.Source == b.Source && a.Title == b.Title
}

func (s *Shell) scholarCatalogRows(c *ui.Context, p *scholarState, entries []scholar.Entry, local bool) {
	l := &p.literature
	creator, year := s.scholarCatalogColumns(c, p)
	ui.Row(c.Key("columns")).FillWidth().Height(29).Padding(0, scholarCatalogInset).AlignItems(ui.Center).Background(s.ch.content).Children(func() {
		ui.Box(c).Width(scholarCatalogDisclosure).Shrink(0)
		scholarCatalogLine(ui.Row(c)).Children(func() {
			ui.Box(c).Width(scholarCatalogGlyphWidth).Shrink(0)
			ui.Text(c, s.tr("scholarItemTitle")).FontSize(11).TextColor(s.ch.muted).SingleLine().Grow(1).MinWidth(0)
			if creator > 0 {
				ui.Text(c, s.tr("scholarItemCreator")).FontSize(11).TextColor(s.ch.muted).Width(creator).Shrink(0).SingleLine()
			}
			if year > 0 {
				ui.Text(c, s.tr("scholarItemYear")).FontSize(11).TextColor(s.ch.muted).Width(year).Shrink(0).SingleLine()
			}
			ui.Row(c).Width(scholarCatalogAttachment).Shrink(0).Justify(ui.Center).Children(func() {
				ui.Icon(c, s.icon("paperclip")).FontSize(13).TextColor(s.ch.muted).Tooltip(s.tr("scholarItemAttachments"))
			})
		})
		if local {
			ui.Box(c).Width(scholarCatalogCitation).Shrink(0)
		}
	})
	ui.Box(c).Height(1).FillWidth().Background(s.ch.separator)
	ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Padding(3, scholarCatalogInset).Children(func() {
			if len(entries) == 0 && !p.busy {
				ui.Text(c, s.tr("scholarCatalogEmpty")).Padding(8).FontSize(12).TextColor(s.ch.muted).Wrap()
			}
			for i, e := range entries {
				ck := c.Key(i)
				selected := scholarSameCatalogItem(s.scholarLinkedEntry(p, e), l.selected)
				expanded := selected && l.expanded && !e.ZoteroNote
				row := ui.Row(ck).FillWidth().Radius(4).AlignItems(ui.Center)
				if selected && l.selectedNote == "" {
					row.Background(scholarZoteroSelection(c))
				} else if i%2 != 0 {
					row.Background(ui.RGBA(128, 128, 128, 0.08))
				}
				row.Children(func() {
					if e.RemoteID != "" && !e.ZoteroNote && (e.NumChildren > 0 || selected && len(l.detail.PDFs)+len(l.detail.Notes) > 0) {
						glyph := "chevron-right"
						if expanded {
							glyph = "chevron-down"
						}
						if s.toolButton(ck.Key("expand"), glyph, s.tr("scholarItemChildren"), false).
							Width(scholarCatalogDisclosure).Shrink(0).Padding(1).Disabled(p.busy).Clicked() {
							if selected {
								l.expanded = !l.expanded
							} else {
								s.setScholarEntry(p, e)
								l.expanded = true
							}
						}
					} else {
						ui.Box(ck).Width(scholarCatalogDisclosure).Shrink(0)
					}
					b := scholarCatalogLine(ui.ButtonBase(ck.Key("item"))).Height(27).Justify(ui.Start).
						Selected(selected && l.selectedNote == "").TextColor(s.ch.text).FocusRing(true).Tooltip(e.Title).Disabled(p.busy)
					if selected && l.selectedNote == "" {
						b.Background(scholarZoteroSelection(c))
					} else if b.Hovered() {
						b.Background(s.ch.hover)
					}
					b.Children(func() {
						glyph := scholarCatalogGlyph(e)
						ink := s.ch.muted
						if glyph != "file-text" && glyph != "file-pen-line" {
							ink = s.scholarZoteroIconColor(c, glyph)
						}
						ui.Icon(ck, s.icon(glyph)).FontSize(14).Width(scholarCatalogGlyphWidth).Shrink(0).TextColor(ink)
						ui.Text(ck, e.Title).FontSize(12).SingleLine().Grow(1).MinWidth(0).TextColor(s.ch.text)
						if creator > 0 {
							ui.Text(ck, s.scholarCreatorSummary(e)).FontSize(12).Width(creator).Shrink(0).SingleLine().TextColor(s.ch.text)
						}
						if year > 0 {
							ui.Text(ck, e.Year).FontSize(11).Width(year).Shrink(0).SingleLine().TextColor(s.ch.muted)
						}
						ui.Row(ck.Key("attachment-status")).Width(scholarCatalogAttachment).Shrink(0).Justify(ui.Center).Children(func() {
							hasPDF := local && !filepath.IsAbs(e.File) && strings.EqualFold(filepath.Ext(e.File), ".pdf")
							if hasPDF || selected && len(l.detail.PDFs) > 0 {
								ui.Icon(ck, s.icon("file-text")).FontSize(12).TextColor(s.ch.muted).Tooltip(s.tr("scholarItemAttachments"))
							}
						})
					})
					if b.Clicked() {
						if !selected || e.Source == "Zotero" && l.selected.Source != e.Source {
							s.selectScholarEntry(p, e)
						} else {
							l.selectedNote = ""
							l.annotations = nil
							s.showScholarInspector(p, scholarReader)
						}
					}
					if local {
						checked := slices.Contains(l.checked, e.Key)
						glyph := "plus"
						if checked {
							glyph = "check"
						}
						if s.scholarCompactIcon(ck.Key("cite"), glyph, s.tr("scholarMultiCite"), checked).
							Size(scholarCatalogCitation, 24).Shrink(0).Checked(checked).Disabled(p.busy).Clicked() {
							toggleScholarCitation(l, e.Key)
						}
					}
				})
				if expanded {
					s.scholarCatalogChildren(ck.Key("children"), p)
				}
			}
		})
	})
}

func (s *Shell) scholarCatalogChildren(c *ui.Context, p *scholarState) {
	l := &p.literature
	ui.Column(c).FillWidth().Padding(0, 6, 3, 32).Gap(2).Children(func() {
		for _, pdf := range l.detail.PDFs {
			title := pdf.Title
			if title == "" {
				title = s.tr("scholarOpenPDF")
			}
			icon := "file-text"
			if pdf.LinkOnly {
				icon = "external-link"
			}
			if s.scholarReaderCommand(c.Key(pdf.Key), icon, title, false).Disabled(pdf.LinkOnly && pdf.URL == "").Clicked() {
				s.openScholarZoteroPDF(p, l.selected, pdf)
			}
		}
		for _, note := range l.detail.Notes {
			b := s.scholarReaderLink(c.Key(note.Key), "sticky-note", scholarNoteTitle(s, note))
			if l.selectedNote == note.Key {
				b.Selected(true).Background(scholarZoteroSelection(c))
			}
			if b.Clicked() {
				l.selectedNote, l.readerActive, l.readerJump = note.Key, 3, 4
				l.annotations = nil
				s.showScholarInspector(p, scholarReader)
			}
		}
		if l.detail.More || l.attachmentPage > 0 {
			ui.Row(c).Gap(4).Children(func() {
				if ui.Button(c.Key("previous"), s.tr("scholarPreviousPage")).Disabled(p.busy || l.attachmentPage == 0).Clicked() {
					s.loadScholarDetail(p, l.selected, l.attachmentPage-scholar.ZoteroPageSize)
				}
				if ui.Button(c.Key("next"), s.tr("scholarNextPage")).Disabled(p.busy || !l.detail.More).Clicked() {
					s.loadScholarDetail(p, l.selected, l.attachmentPage+scholar.ZoteroPageSize)
				}
			})
		}
	})
}

func (s *Shell) loadScholarCollectionBranch(p *scholarState, parent string, start int) {
	l := &p.literature
	if p.busy {
		return
	}
	if l.tree == nil {
		l.tree = make(map[string]*scholarCollectionBranch)
	}
	if l.tree[parent] == nil && len(l.tree) >= 48 {
		p.err = s.tr("scholarCollectionLimit")
		return
	}
	z := l.zotero
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		page, err := z.CollectionsIn(ctx, parent, start)
		return func() {
			if l.zotero == z && err == nil {
				l.tree[parent] = &scholarCollectionBranch{page: page, open: true, start: start}
			}
		}, err
	})
}

func (s *Shell) scholarCollectionTree(c *ui.Context, p *scholarState, parent string, depth int) {
	l := &p.literature
	branch := l.tree[parent]
	if branch == nil || !branch.open || depth >= 32 {
		return
	}
	for _, collection := range branch.page.Collections {
		ck := c.Key(collection.Key)
		child := l.tree[collection.Key]
		if l.collectionFilter != "" && !strings.Contains(strings.ToLower(collection.Name), strings.ToLower(l.collectionFilter)) && child == nil {
			continue
		}
		selected := l.collection == collection.Key && !l.recentOpen
		row := ui.Row(ck).FillWidth().Height(28).Padding(0, 0, 0, float32(depth)*16).AlignItems(ui.Center).Radius(4)
		if selected {
			row.Background(scholarZoteroSelection(c))
		}
		row.Children(func() {
			glyph := "chevron-right"
			if child != nil && child.open {
				glyph = "chevron-down"
			}
			if s.toolButton(ck.Key("expand"), glyph, s.tr("scholarSubcollections"), false).Width(16).Padding(1).Disabled(p.busy).Clicked() {
				if child == nil {
					s.loadScholarCollectionBranch(p, collection.Key, 0)
				} else {
					child.open = !child.open
				}
			}
			b := ui.ButtonBase(ck.Key("select")).Grow(1).MinWidth(0).FillHeight().Gap(5).Justify(ui.Start).
				Selected(selected).TextColor(s.ch.text).Disabled(p.busy).Tooltip(collection.Name)
			if selected {
				b.Background(scholarZoteroSelection(c))
			} else if b.Hovered() {
				b.Background(s.ch.hover)
			}
			b.Children(func() {
				ui.Box(ck).Size(16, 16).Shrink(0).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
					ui.Icon(ck, s.icon("folder")).FontSize(15).TextColor(s.scholarZoteroIconColor(c, "folder"))
				})
				ui.Text(ck, collection.Name).FontSize(12).SingleLine().MinWidth(0).Shrink(1).TextColor(s.ch.text)
			})
			if l.zotero.ServerID != "" {
				b.ContextMenu(func(m *ui.Menu) {
					s.scholarZoteroCollectionActions(m, p, collection)
				})
				if l.collection == collection.Key && !l.recentOpen {
					label := s.tr("scholarZoteroManageCollection")
					s.toolButton(ck.Key("manage"), "ellipsis", label, false).Disabled(p.busy).Label(label).Menu(func(m *ui.Menu) {
						s.scholarZoteroCollectionActions(m, p, collection)
					})
				}
			}
			if b.Clicked() {
				l.recentOpen, l.scope = false, scholar.ZoteroAll
				l.scopeOpen = false
				s.revealScholarCatalog(p)
				s.loadZotero(p, false, collection.Key, l.remoteQuery, 0)
			}
		})
		s.scholarCollectionTree(ck.Key("children"), p, collection.Key, depth+1)
	}
	if branch.start > 0 || branch.page.More {
		ui.Row(c.Key("pagination")).Padding(0, 0, 0, float32(depth)*16).Gap(2).Children(func() {
			if s.toolButton(c.Key("prev"), "chevron-left", s.tr("scholarPreviousPage"), false).Disabled(p.busy || branch.start == 0).Clicked() {
				s.loadScholarCollectionBranch(p, parent, branch.start-scholar.ZoteroPageSize)
			}
			if s.toolButton(c.Key("next"), "chevron-right", s.tr("scholarNextPage"), false).Disabled(p.busy || !branch.page.More).Clicked() {
				s.loadScholarCollectionBranch(p, parent, branch.page.Next)
			}
		})
	}
}

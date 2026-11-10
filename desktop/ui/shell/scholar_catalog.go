package shell

import (
	"context"
	"fmt"
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
	open, more  bool
	query, sent string
	start       int
	tags        []string
}

func (s *Shell) scholarZoteroCatalog(c *ui.Context, p *scholarState) {
	l := &p.literature
	if !l.connected {
		ui.Text(c, s.tr("scholarEnableZotero")).FontSize(12).Wrap()
		if ui.Button(c, s.tr("scholarConnectZotero")).Disabled(p.busy).Clicked() {
			s.loadZotero(p, true, "", "", 0)
		}
		if l.recentOpen {
			s.scholarEntries(c, p, l.recent, false)
		}
		return
	}
	if form := l.zoteroForm; form != nil && form.catalog && form.z == l.zotero {
		ui.Scroll(c.Key("zotero-form")).Grow(1).FillWidth().MinHeight(0).Children(func() {
			ui.Column(c).FillWidth().Padding(0, 8, 8, 0).Gap(5).Children(func() {
				s.scholarZoteroForm(c, p, form)
			})
		})
		return
	}
	s.scholarZoteroCatalogActions(c.Key("zotero-actions"), p)
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
		}
	}
	width := s.scholarWorkspaceWidth(c)
	if p.drawer == scholarReader {
		width -= s.scholarInspectorWidth(p, width)
	}
	if width < 600 {
		if ui.Button(c.Key("collections"), s.tr("scholarCollections")).Clicked() {
			l.scopeOpen = !l.scopeOpen
		}
		if l.scopeOpen {
			s.scholarCatalogScope(c, p)
		} else {
			s.scholarCatalogResults(c, p)
		}
		return
	}
	ui.Row(c).FillWidth().Grow(1).MinHeight(0).Gap(7).Children(func() {
		ui.Column(c.Key("collections")).Width(180).FillHeight().MinHeight(0).Children(func() {
			s.scholarCatalogScope(c, p)
		})
		ui.Box(c).Width(1).FillHeight().Background(s.ch.separator)
		ui.Column(c.Key("catalog-results")).Grow(1).MinWidth(0).FillHeight().MinHeight(0).Gap(5).Children(func() {
			s.scholarCatalogResults(c, p)
		})
	})
}

func (s *Shell) scholarCatalogScope(c *ui.Context, p *scholarState) {
	l := &p.literature
	ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Gap(3).Children(func() {
			for _, library := range l.libraries {
				ck := c.Key(library.ID)
				name := library.Name
				if library.ID == 0 {
					name = s.tr("scholarPersonalLibrary")
				}
				if s.scholarTabButton(ck, "book-open", name, l.zotero.Group == library.ID && l.scope == scholar.ZoteroAll && l.collection == "" && !l.recentOpen).
					Disabled(p.busy).Clicked() {
					if l.zotero.Group != library.ID {
						l.tree, l.treeTried = nil, false
						l.tag = ""
						l.tagPicker = scholarTagPicker{}
					}
					l.zotero.Group = library.ID
					l.recentOpen, l.scope, l.scopeOpen = false, scholar.ZoteroAll, false
					s.loadZotero(p, false, "", l.remoteQuery, 0)
				}
				if l.zotero.Group == library.ID {
					s.scholarCollectionTree(ck, p, "", 1)
				}
			}
			if s.scholarTabButton(c.Key("recent"), "clock", s.tr("scholarRecentReading"), l.recentOpen).Clicked() {
				l.recentOpen, l.scopeOpen = true, false
			}
			for _, scope := range []struct {
				id           scholar.ZoteroScope
				label, glyph string
			}{
				{scholar.ZoteroUnfiled, "scholarUnfiled", "folder"},
				{scholar.ZoteroPublications, "scholarPublications", "file-text"},
				{scholar.ZoteroTrash, "scholarZoteroTrash", "trash"},
			} {
				if scope.id == scholar.ZoteroPublications && l.zotero.Group != 0 {
					continue
				}
				if s.scholarTabButton(c.Key(scope.id), scope.glyph, s.tr(scope.label), !l.recentOpen && l.scope == scope.id).
					Disabled(p.busy).Clicked() {
					l.recentOpen, l.scopeOpen, l.fulltext = false, false, false
					l.scope = scope.id
					s.loadZotero(p, false, "", l.remoteQuery, 0)
				}
			}
			if l.tag != "" {
				ui.Text(c, fmt.Sprintf(s.tr("scholarTagFilter"), l.tag)).FontSize(11).Wrap()
				if ui.Button(c, s.tr("scholarClearTag")).Disabled(p.busy).Clicked() {
					l.tag = ""
					s.loadZotero(p, false, l.collection, l.remoteQuery, 0)
				}
			}
			s.scholarCatalogTags(c.Key("tag-picker"), p)
		})
	})
}

func (s *Shell) scholarCatalogTags(c *ui.Context, p *scholarState) {
	l := &p.literature
	picker := &l.tagPicker
	label := s.tr("scholarSearchTags")
	button := s.scholarTabButton(c, "tag", label, picker.open).Disabled(p.busy)
	if button.Clicked() {
		picker.open = !picker.open
		if picker.open {
			s.loadScholarTags(p, picker.query, 0)
		}
	}
	ui.Popover(c, button, &picker.open, func() {
		ui.Column(c).Width(240).Padding(8).Gap(5).Children(func() {
			input := ui.TextInput(c, &picker.query).FillWidth().Placeholder(label)
			if (input.Submitted() || ui.Button(c, s.tr("scholarSearch")).Disabled(p.busy).Clicked()) && !p.busy {
				s.loadScholarTags(p, picker.query, 0)
			}
			ui.Scroll(c).Height(200).FillWidth().Children(func() {
				ui.Column(c).FillWidth().Gap(2).Children(func() {
					for i, tag := range picker.tags {
						if s.scholarTabButton(c.Key(i), "tag", tag, tag == l.tag).Disabled(p.busy).Clicked() {
							picker.open, l.recentOpen, l.fulltext, l.scopeOpen = false, false, false, false
							l.tag = tag
							s.loadZotero(p, false, l.collection, l.remoteQuery, 0)
						}
					}
				})
			})
			ui.Row(c).Gap(5).Children(func() {
				if s.toolButton(c.Key("prev"), "chevron-left", s.tr("scholarPreviousPage"), false).Disabled(p.busy || picker.start == 0).Clicked() {
					s.loadScholarTags(p, picker.sent, picker.start-scholar.ZoteroPageSize)
				}
				if s.toolButton(c.Key("next"), "chevron-right", s.tr("scholarNextPage"), false).Disabled(p.busy || !picker.more).Clicked() {
					s.loadScholarTags(p, picker.sent, picker.start+scholar.ZoteroPageSize)
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
		s.scholarEntries(c, p, l.recent, false)
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

func (s *Shell) scholarNoteDetails(c *ui.Context, p *scholarState, note scholar.ZoteroNote, standalone bool) {
	l := &p.literature
	if !standalone && ui.Button(c, s.tr("scholarParentItem")).Clicked() {
		l.selectedNote = ""
	}
	ui.Text(c, scholarNoteTitle(s, note)).Bold().Wrap()
	if body := strings.TrimSpace(strings.TrimPrefix(note.Text, note.Title)); body != "" {
		ui.Text(c, body).Wrap()
	}
	s.scholarZoteroMetadata(c, p, note.Tags, note.Related)
}

func (s *Shell) scholarZoteroMetadata(c *ui.Context, p *scholarState, tags, related []string) {
	l := &p.literature
	if len(tags) > 0 {
		ui.Text(c, fmt.Sprintf(s.tr("scholarTagsCount"), len(tags))).Bold().FontSize(11)
		for i, tag := range tags {
			if ui.Button(c.Key(i), tag).Disabled(!l.connected || p.busy).Clicked() {
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
	}
	if len(related) > 0 {
		ui.Text(c, fmt.Sprintf(s.tr("scholarRelatedCount"), len(related))).Bold().FontSize(11)
		for i, key := range related {
			label := key
			for _, e := range l.pageData.Entries {
				if e.RemoteID == key && e.Library == l.selected.Library {
					label = e.Title
					break
				}
			}
			if ui.Button(c.Key(i), label).Disabled(p.busy).Clicked() {
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
	}
}

func scholarSameCatalogItem(a, b scholar.Entry) bool {
	if a.RemoteID != "" && b.RemoteID != "" {
		return a.Library == b.Library && a.RemoteID == b.RemoteID
	}
	return a.Key == b.Key && a.Source == b.Source && a.Title == b.Title
}

func (s *Shell) scholarCatalogRows(c *ui.Context, p *scholarState, entries []scholar.Entry, local bool) {
	l := &p.literature
	ui.Row(c.Key("columns")).FillWidth().Height(25).Padding(0, 8, 0, 26).AlignItems(ui.Center).Children(func() {
		ui.Text(c, s.tr("scholarItemTitle")).FontSize(11).TextColor(s.ch.muted).Grow(1)
		ui.Text(c, s.tr("scholarItemCreator")).FontSize(11).TextColor(s.ch.muted).Width(104).SingleLine()
	})
	ui.Box(c).Height(1).FillWidth().Background(s.ch.separator)
	ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Gap(1).Children(func() {
			if len(entries) == 0 && !p.busy {
				ui.Text(c, s.tr("scholarCatalogEmpty")).Padding(8).Wrap()
			}
			for i, e := range entries {
				ck := c.Key(i)
				selected := scholarSameCatalogItem(s.scholarLinkedEntry(p, e), l.selected)
				expanded := selected && l.expanded && !e.ZoteroNote
				ui.Row(ck).FillWidth().AlignItems(ui.Center).Children(func() {
					if e.RemoteID != "" && !e.ZoteroNote {
						glyph := "chevron-right"
						if expanded {
							glyph = "chevron-down"
						}
						if s.toolButton(ck.Key("expand"), glyph, s.tr("scholarItemChildren"), false).Disabled(p.busy).Clicked() {
							if selected {
								l.expanded = !l.expanded
							} else {
								s.setScholarEntry(p, e)
								l.expanded = true
							}
						}
					} else {
						ui.Box(ck).Width(24)
					}
					b := ui.ButtonBase(ck.Key("item")).Grow(1).MinWidth(0).Height(28).Padding(0, 5).Gap(6).Justify(ui.Start).
						Selected(selected && l.selectedNote == "").FocusRing(true).Tooltip(e.Title).Disabled(p.busy)
					if selected && l.selectedNote == "" {
						b.Background(s.ch.pressed)
					} else if b.Hovered() {
						b.Background(s.ch.hover)
					}
					b.Children(func() {
						glyph := "file-text"
						if e.ZoteroNote {
							glyph = "notebook-pen"
						}
						ui.Icon(ck, s.icon(glyph)).FontSize(13).TextColor(s.ch.muted)
						ui.Text(ck, e.Title).FontSize(12).SingleLine().Grow(1).MinWidth(0)
						ui.Text(ck, strings.TrimSpace(e.Author+" "+e.Year)).FontSize(11).Width(104).SingleLine().TextColor(s.ch.muted)
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
						label := "+"
						if checked {
							label = "✓"
						}
						if ui.Button(ck.Key("cite"), label).Tooltip(s.tr("scholarMultiCite")).Clicked() {
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
			if s.scholarTabButton(c.Key(pdf.Key), icon, title, false).Disabled(pdf.LinkOnly && pdf.URL == "").Clicked() {
				s.openScholarZoteroPDF(p, l.selected, pdf, c.Services())
			}
		}
		for _, note := range l.detail.Notes {
			if s.scholarTabButton(c.Key(note.Key), "notebook-pen", scholarNoteTitle(s, note), l.selectedNote == note.Key).Clicked() {
				l.selectedNote = note.Key
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
		ui.Row(ck).FillWidth().Padding(0, 0, 0, float32(depth)*12).AlignItems(ui.Center).Children(func() {
			glyph := "chevron-right"
			if child != nil && child.open {
				glyph = "chevron-down"
			}
			if s.toolButton(ck.Key("expand"), glyph, s.tr("scholarSubcollections"), false).Disabled(p.busy).Clicked() {
				if child == nil {
					s.loadScholarCollectionBranch(p, collection.Key, 0)
				} else {
					child.open = !child.open
				}
			}
			b := ui.ButtonBase(ck.Key("select")).Grow(1).MinWidth(0).Height(26).Gap(4).Justify(ui.Start).
				Selected(l.collection == collection.Key && !l.recentOpen).Disabled(p.busy).Tooltip(collection.Name)
			if l.collection == collection.Key && !l.recentOpen {
				b.Background(s.ch.pressed)
			} else if b.Hovered() {
				b.Background(s.ch.hover)
			}
			b.Children(func() {
				ui.Icon(ck, s.icon("folder")).FontSize(13).TextColor(s.ch.accent)
				ui.Text(ck, collection.Name).FontSize(12).SingleLine().MinWidth(0).Shrink(1)
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
				s.loadZotero(p, false, collection.Key, l.remoteQuery, 0)
			}
		})
		s.scholarCollectionTree(ck.Key("children"), p, collection.Key, depth+1)
	}
	if branch.start > 0 || branch.page.More {
		ui.Row(c.Key("pagination")).Padding(0, 0, 0, float32(depth)*12).Gap(2).Children(func() {
			if s.toolButton(c.Key("prev"), "chevron-left", s.tr("scholarPreviousPage"), false).Disabled(p.busy || branch.start == 0).Clicked() {
				s.loadScholarCollectionBranch(p, parent, branch.start-scholar.ZoteroPageSize)
			}
			if s.toolButton(c.Key("next"), "chevron-right", s.tr("scholarNextPage"), false).Disabled(p.busy || !branch.page.More).Clicked() {
				s.loadScholarCollectionBranch(p, parent, branch.page.Next)
			}
		})
	}
}

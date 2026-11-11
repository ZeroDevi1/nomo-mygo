package shell

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"nomo/desktop/internal/scholar"
	"nomo/desktop/ui/mdedit"
)

type scholarLiteratureState struct {
	refreshPending                                     bool
	local                                              scholarLocalSearch
	picker, focusPicker                                bool
	inline                                             scholarInlineCitation
	mode                                               int
	fulltext                                           bool
	fulltextScanned, fulltextPDFs, fulltextUnavailable int
	fulltextEntries                                    []scholar.Entry
	fulltextNext, fulltextTotal, fulltextPage          int
	fulltextMore, fulltextPaused                       bool
	fulltextJob                                        uint64
	fulltextProgress                                   int
	libraries                                          []scholar.ZoteroLibrary
	query, remoteQuery, sentQuery                      string
	checked                                            []string
	pickerIndex                                        int
	pickerList                                         ui.ListState
	search                                             scholarQueryState
	visible                                            []scholar.Entry
	target                                             int
	zotero                                             scholar.Zotero
	zoteroConnectJob                                   uint64
	zoteroBrowseJob                                    uint64
	zoteroForm                                         *scholarZoteroForm
	connected                                          bool
	collection                                         string
	page                                               int
	pageData                                           scholar.ZoteroPage
	results                                            []scholar.Entry
	selected                                           scholar.Entry
	detail                                             scholar.ZoteroDetail
	annotations                                        *scholarAnnotationView
	attachmentPage                                     int
	selectedNote                                       string
	openNotice                                         string
	tag                                                string
	tagPicker                                          scholarTagPicker
	recent                                             []scholar.Entry
	recentOpen                                         bool
	expanded, scopeOpen                                bool
	readerCollapsed                                    uint16
	readerJump                                         int
	readerActive                                       uint
	readerAllCreators                                  bool
	catalogWidth                                       float32
	collectionFilter                                   string
	collectionSearch                                   bool
	loaded, treeTried                                  bool
	scope                                              scholar.ZoteroScope
	tree                                               map[string]*scholarCollectionBranch
}

func (l *scholarLiteratureState) resetZoteroSource() {
	l.loaded, l.refreshPending = false, false
	l.pageData, l.page = scholar.ZoteroPage{}, 0
	l.tree, l.treeTried, l.libraries = nil, false, nil
	l.tag, l.tagPicker = "", scholarTagPicker{}
	l.collection, l.remoteQuery, l.sentQuery = "", "", ""
	l.scope, l.recentOpen, l.fulltext = scholar.ZoteroAll, false, false
	l.fulltextEntries, l.recent = nil, nil
	l.zoteroForm, l.annotations = nil, nil
	l.inline.results.clear()
	if l.selected.RemoteID != "" {
		l.selected, l.detail = scholar.Entry{}, scholar.ZoteroDetail{}
		l.selectedNote, l.openNotice = "", ""
	}
}

func (l *scholarLiteratureState) rememberReading(entry scholar.Entry) {
	l.recent = slices.DeleteFunc(l.recent, func(candidate scholar.Entry) bool {
		return candidate.RemoteID == entry.RemoteID && candidate.Library == entry.Library
	})
	l.recent = append([]scholar.Entry{entry}, l.recent[:min(len(l.recent), 39)]...)
}

func (s *Shell) scholarCitationPicker(c *ui.Context, p *scholarState) {
	l := &p.literature
	ui.Modal(c, &l.picker, func() {
		panel := ui.Column(c).Width(460).Height(420).Padding(12).Gap(5)
		closePicker := func() { l.picker, s.focusEditor = false, true }
		panel.Children(func() {
			ui.Text(c, s.tr("scholarInsertLocalCitations")).Bold().TextColor(scholarInk(c, "citation"))
			input := ui.TextInput(c, &l.query).FillWidth().Placeholder(s.tr("scholarCitationSearch"))
			if l.focusPicker {
				input.Focus()
				l.focusPicker = false
			}
			items := s.scholarLiteratureResults(c, p)
			if input.Changed() {
				l.pickerIndex = 0
			}
			l.pickerIndex = min(l.pickerIndex, max(0, len(items)-1))
			if len(items) > 0 {
				switch {
				case panel.OverlayShortcut(0, ui.KeyDown):
					l.pickerIndex = (l.pickerIndex + 1) % len(items)
					l.pickerList.ScrollTo(l.pickerIndex, ui.Center)
				case panel.OverlayShortcut(0, ui.KeyUp):
					l.pickerIndex = (l.pickerIndex + len(items) - 1) % len(items)
					l.pickerList.ScrollTo(l.pickerIndex, ui.Center)
				case panel.OverlayShortcut(0, ui.KeyTab):
					toggleScholarCitation(l, items[l.pickerIndex].Key)
				}
			}
			if p.libraryError != "" {
				ui.Text(c, p.libraryError).Wrap()
			}
			ui.Row(c).Gap(4).Children(func() {
				add := ui.Button(c, fmt.Sprintf(s.tr("scholarInsertSelected"), len(l.checked))).Disabled(len(l.checked) == 0 && len(items) == 0)
				if add.Clicked() || input.Submitted() || panel.OverlayShortcut(0, ui.KeyEnter) {
					keys := l.checked
					if len(keys) == 0 && len(items) > 0 {
						keys = []string{items[l.pickerIndex].Key}
					}
					if len(keys) > 0 {
						s.insertScholarCitations(p, keys, "")
						l.checked = nil
						closePicker()
					}
				}
				if ui.Button(c, s.tr("scholarClose")).Clicked() {
					closePicker()
				}
			})
			ui.List(c, &l.pickerList, len(items), func(i int) {
				entry := items[i]
				ck := c.Key(entry.Key)
				b := ui.ButtonBase(ck).FillWidth().Justify(ui.Start).Padding(5).Tooltip(entry.Title)
				if i == l.pickerIndex {
					b.Background(s.ch.pressed)
				}
				b.Children(func() {
					prefix := ""
					if slices.Contains(l.checked, entry.Key) {
						prefix = "✓ "
					}
					ui.Column(ck).FillWidth().Children(func() {
						ui.Text(ck, prefix+entry.Key+" · "+entry.Title).SingleLine().FontSize(12)
						ui.Text(ck, entry.Author+" "+entry.Year).SingleLine().FontSize(11).TextColor(s.ch.muted)
					})
				})
				if b.Clicked() {
					l.pickerIndex = i
					toggleScholarCitation(l, entry.Key)
				}
			}).Grow(1).MinHeight(0).FillWidth()
			ui.Text(c, s.tr("scholarPickerKeys")).FontSize(11).TextColor(s.ch.muted)
		})
		if panel.OverlayShortcut(0, ui.KeyEscape) {
			closePicker()
		}
	})
}

func toggleScholarCitation(l *scholarLiteratureState, key string) {
	if slices.Contains(l.checked, key) {
		l.checked = slices.DeleteFunc(l.checked, func(k string) bool { return k == key })
	} else if len(l.checked) < 32 {
		l.checked = append(l.checked, key)
	}
}

func (s *Shell) scholarLiteratureResults(c *ui.Context, p *scholarState) []scholar.Entry {
	l := &p.literature
	l.visible = s.queryScholar(c, p, &l.search, l.query, 100)
	return l.visible
}

func (s *Shell) scholarLibraryView(c *ui.Context, p *scholarState) {
	l := &p.literature
	ui.Column(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
		s.scholarLibraryToolbar(c.Key("library-toolbar"), p)
		ui.Box(c).Height(1).FillWidth().Background(s.ch.separator)
		padding := float32(0)
		if l.mode == 2 {
			padding = 8
		}
		ui.Column(c.Key("source-content")).FillWidth().Grow(1).MinHeight(0).Padding(padding).Gap(5).Children(func() {
			width, _ := c.Size()
			if p.err != "" && !(p.drawer == scholarReader && s.scholarInspectorVisible(p, width)) {
				s.scholarReaderNotice(c.Key("error"), p.err, nil)
			}
			switch l.mode {
			case 0:
				s.scholarProjectCatalog(c, p)
			case 1:
				s.scholarZoteroCatalog(c, p)
			case 2:
				var submit bool
				ui.Column(c.Key("online-search")).FillWidth().MaxWidth(560).Padding(8).Gap(6).Children(func() {
					ui.Row(c).FillWidth().Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
						label := s.tr("scholarMetadataSearch")
						in := ui.TextInput(c.Key("query"), &l.remoteQuery).Width(260).MinWidth(0).Grow(1).
							Height(32).Label(label).Placeholder(label)
						search := ui.PrimaryButton(c.Key("submit"), s.tr("scholarOnlineSearch")).MinHeight(32).Shrink(0).Disabled(p.busy)
						submit = !p.busy && (search.Clicked() || in.Submitted())
					})
					ui.Text(c, s.tr("scholarOnlineDisclosure")).FontSize(12).TextColor(s.ch.muted).Wrap()
				})
				if submit {
					query := l.remoteQuery
					s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
						items, err := scholar.Lookup(ctx, query)
						return func() {
							l.results = items
							if err == nil && len(items) == 0 {
								p.err = s.tr("scholarNoMetadata")
							}
						}, err
					})
				}
				s.scholarCatalogRows(c, p, l.results, false)
			}
		})
	})
}

func (s *Shell) scholarProjectCatalog(c *ui.Context, p *scholarState) {
	l := &p.literature
	if p.libraryError != "" {
		s.scholarReaderNotice(c.Key("library-error"), p.libraryError, nil)
	} else if len(p.bibNames) == 0 && p.libraryReady {
		ui.Text(c, s.tr("scholarNoBibliography")).Margin(4, 8).FontSize(11).TextColor(s.ch.muted).Wrap()
	}
	var entries []scholar.Entry
	if l.local.enabled {
		entries = s.scholarLocalSearchResults(c.Key("fulltext"), p)
	} else {
		entries = s.scholarLiteratureResults(c, p)
	}
	s.scholarCatalogRows(c, p, entries, true)
	limited := !l.local.enabled && len(entries) == 100
	if len(l.checked) > 0 || limited {
		ui.Box(c).Height(1).FillWidth().Background(s.ch.separator)
		ui.Row(c.Key("project-footer")).FillWidth().MinHeight(32).Padding(3, 8).Gap(6).AlignItems(ui.Center).
			Background(s.ch.tabBar).Children(func() {
			if limited {
				ui.Text(c, s.tr("scholarFirstHundred")).FontSize(11).TextColor(s.ch.muted).Wrap().Grow(1).MinWidth(0)
			} else {
				ui.Spacer(c)
			}
			if len(l.checked) > 0 && ui.Button(c.Key("cite"), fmt.Sprintf(s.tr("scholarInsertCount"), len(l.checked))).
				Height(25).FontSize(12).Padding(0, 7).Shrink(0).Disabled(p.busy).Clicked() {
				s.insertScholarProjectSelection(p)
			}
		})
	}
}

func (s *Shell) insertScholarProjectSelection(p *scholarState) {
	s.insertScholarCitations(p, p.literature.checked, "")
	p.literature.checked = nil
}

func (s *Shell) scholarZoteroResults(c *ui.Context, p *scholarState) {
	l := &p.literature
	scanning := p.busy && l.fulltextJob != 0 && l.fulltextJob == p.job
	showProgress := l.fulltext && (scanning || l.sentQuery != "")
	if l.fulltext {
		ui.Column(c.Key("fulltext-status")).FillWidth().Padding(6, 8).Gap(4).Children(func() {
			ui.Text(c, fmt.Sprintf(s.tr("scholarFulltextScope"), l.fulltextScanned, l.fulltextPDFs, l.fulltextUnavailable)).
				FontSize(11).TextColor(s.ch.muted).Wrap()
			if !showProgress {
				return
			}
			ui.Text(c, fmt.Sprintf(s.tr("scholarFulltextProgress"), l.fulltextProgress, l.fulltextTotal, len(l.fulltextEntries))).
				FontSize(11).TextColor(s.ch.muted).Wrap()
			if l.fulltextPaused && !scanning {
				ui.Text(c, s.tr("scholarFulltextPaused")).FontSize(11).TextColor(s.ch.muted).Wrap()
			}
			ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
				if ui.Button(c.Key("continue"), s.tr("scholarContinueScan")).Disabled(p.busy || !l.fulltextMore).Clicked() {
					if len(l.fulltextEntries) >= 200 {
						l.fulltextEntries = nil
						l.fulltextPage = 0
					}
					s.loadZotero(p, l.collection, l.sentQuery, l.fulltextNext)
				}
				if scanning && ui.Button(c.Key("cancel"), s.tr("scholarAgentCancelWork")).Clicked() && p.cancelJob != nil {
					p.cancelJob()
				}
			})
		})
	}
	if showProgress {
		lo := min(l.fulltextPage*scholar.ZoteroPageSize, len(l.fulltextEntries))
		l.pageData.Entries = l.fulltextEntries[lo:min(lo+scholar.ZoteroPageSize, len(l.fulltextEntries))]
	}
	s.scholarCatalogRows(c, p, l.pageData.Entries, false)
	s.scholarZoteroPagination(c.Key("pagination"), p)
}

func (s *Shell) loadZotero(p *scholarState, collection, query string, start int) {
	if p.busy {
		return
	}
	l := &p.literature
	z := l.zotero
	offline := z.ReadOnly()
	tag, scope := l.tag, l.scope
	fulltext := !offline && l.fulltext && query != "" && scope == scholar.ZoteroAll && tag == ""
	if fulltext {
		l.fulltextProgress = start
		l.fulltextPaused = false
	}
	if fulltext && start == 0 {
		l.fulltextEntries = nil
		l.fulltextPage = 0
		l.fulltextNext, l.fulltextTotal = 0, 0
		l.fulltextMore = false
		l.fulltextScanned, l.fulltextPDFs, l.fulltextUnavailable = 0, 0, 0
	}
	s.scholarJob(p, func(ctx context.Context, job uint64) (func(), error) {
		ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		var err error
		var page scholar.ZoteroPage
		var full scholar.ZoteroFulltextResult
		if fulltext {
			full, err = z.SearchFulltext(ctx, query, collection, start, func(n int) {
				s.post(func() {
					if s.scholar == p && p.job == job {
						l.fulltextProgress = start + n
					}
				})
			})
			page.Entries, page.More = full.Entries, full.More
		} else {
			page, err = z.Browse(ctx, query, collection, tag, scope, start)
		}
		var connectionErr *url.Error
		if !offline && ctx.Err() == nil && errors.As(err, &connectionErr) {
			return func() {
				l.connected, l.zoteroConnectJob = false, 0
			}, nil
		}
		return func() {
			l.zoteroBrowseJob = 0
			if fulltext {
				l.fulltextJob = 0
				l.fulltextPaused = full.Interrupted
				l.fulltextProgress = full.Next
				l.fulltextScanned += full.Scanned
				l.fulltextPDFs += full.PDFs
				l.fulltextUnavailable += full.Unavailable
				l.fulltextNext, l.fulltextMore = full.Next, full.More
				if full.Total > 0 {
					l.fulltextTotal = full.Total
				}
				l.fulltextEntries = append(l.fulltextEntries, full.Entries...)
				if len(l.fulltextEntries) >= 200 {
					p.err = s.tr("scholarFulltextResultLimit")
				}
				lo := min(l.fulltextPage*scholar.ZoteroPageSize, len(l.fulltextEntries))
				page.Entries = l.fulltextEntries[lo:min(lo+scholar.ZoteroPageSize, len(l.fulltextEntries))]
			}
			if err != nil {
				return
			}
			l.pageData, l.collection, l.sentQuery, l.page = page, collection, query, start
			l.loaded = true
		}, err
	})
	if offline {
		l.zoteroBrowseJob = p.job
	}
	if fulltext {
		l.fulltextJob = p.job
	}
}

func (s *Shell) selectScholarEntry(p *scholarState, entry scholar.Entry) {
	s.showScholarInspector(p, scholarReader)
	s.setScholarEntry(p, entry)
}

func (s *Shell) setScholarEntry(p *scholarState, entry scholar.Entry) {
	l := &p.literature
	entry = s.scholarLinkedEntry(p, entry)
	if l.zoteroForm != nil && !l.zoteroForm.catalog {
		l.zoteroForm = nil
	}
	l.selected, l.detail, l.attachmentPage = entry, scholar.ZoteroDetail{}, 0
	l.selectedNote, l.openNotice = "", ""
	l.readerAllCreators, l.readerJump, l.readerActive = false, 1, 0
	l.expanded = true
	l.annotations = nil
	if entry.RemoteID != "" {
		s.loadScholarDetail(p, entry, 0)
	}
}

func (s *Shell) loadScholarDetail(p *scholarState, entry scholar.Entry, start int) {
	l := &p.literature
	z := l.zotero
	z.Group = entry.Library
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		detail, err := z.Detail(ctx, entry.RemoteID, start)
		return func() {
			if l.selected != entry {
				return
			}
			if err == nil {
				l.detail, l.attachmentPage = detail, start
				if !slices.ContainsFunc(detail.Notes, func(note scholar.ZoteroNote) bool { return note.Key == l.selectedNote }) {
					l.selectedNote = ""
				}
				if entry.Source == "Zotero" && detail.Entry.RemoteID == entry.RemoteID {
					if detail.Entry.Key == "" {
						detail.Entry.Key = entry.Key
					}
					detail.Entry.Ordinal = entry.Ordinal
					l.selected = detail.Entry
				}
			}
		}, err
	})
}

func (s *Shell) scholarReaderView(c *ui.Context, p *scholarState) {
	l := &p.literature
	if l.openNotice != "" && l.readerNote() == nil && l.annotations == nil {
		s.scholarReaderNotice(c.Key("open-notice"), l.openNotice, func() { l.openNotice = "" })
	}
	if view := l.annotations; view != nil {
		s.scholarAnnotations(c.Key(view), p, view)
		return
	}
	e := l.selected
	if e == (scholar.Entry{}) {
		ui.Column(c).FillWidth().Padding(20, 0).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, s.icon("book-open")).FontSize(28).TextColor(s.ch.muted)
			scholarReaderText(c, s.tr("scholarSelectPaper")).TextColor(s.ch.muted)
		})
		return
	}
	if l.detail.Deleted {
		return
	}
	if note := l.readerNote(); note != nil {
		s.scholarNoteDetails(c, p, *note)
		return
	}
	if e.ZoteroNote {
		return
	}
	s.scholarReaderSection(c, p, 0, "file-pen-line", s.tr("scholarItemInfo"), "", func(c *ui.Context) {
		if len(l.detail.Fields) > 0 {
			s.scholarZoteroFields(c, p)
		} else {
			s.scholarReaderField(c, s.tr("scholarItemCreator"), e.Author)
			s.scholarReaderField(c, s.tr("scholarItemYear"), e.Year)
			s.scholarReaderField(c, s.tr("scholarItemVenue"), e.Venue)
			s.scholarReaderField(c, "DOI", e.DOI)
			s.scholarReaderField(c, s.tr("scholarCitationKey"), e.Key)
			s.scholarReaderField(c, s.tr("scholarItemSource"), e.Source)
		}
	})
	abstract := l.detail.Abstract
	if abstract == "" {
		abstract = e.Abstract
	}
	if abstract != "" || p.references.catalog && l.mode == 1 {
		s.scholarReaderSection(c, p, 1, "notebook-tabs", s.tr("scholarItemAbstract"), "", func(c *ui.Context) {
			scholarReaderText(c, abstract).Selectable()
		})
	}
	childCount := func(count int) string {
		if l.detail.More || l.attachmentPage > 0 {
			return fmt.Sprintf(s.tr("scholarPageItemCount"), count)
		}
		return fmt.Sprint(count)
	}
	s.scholarReaderSection(c, p, 2, "paperclip", s.tr("scholarItemAttachments"), childCount(len(l.detail.PDFs)), func(c *ui.Context) {
		s.scholarReaderAttachments(c, p, e)
	})
	s.scholarReaderSection(c, p, 3, "sticky-note", s.tr("scholarItemNotes"), childCount(len(l.detail.Notes)), func(c *ui.Context) {
		for _, note := range l.detail.Notes {
			if s.scholarReaderLink(c.Key(note.Key), "sticky-note", scholarNoteTitle(s, note)).Disabled(p.busy).Clicked() {
				l.selectedNote, l.readerActive, l.readerJump = note.Key, 3, 4
			}
		}
	})
	if e.RemoteID != "" {
		s.scholarReaderCollections(c, p)
	}
	s.scholarZoteroMetadata(c, p, l.detail.Tags, l.detail.Related)
	s.scholarReaderSection(c, p, 4, "quote", s.tr("scholarCitationsWriting"), "", func(c *ui.Context) {
		s.scholarReaderCitations(c, p, e)
	})
}

func (s *Shell) scholarReaderCitations(c *ui.Context, p *scholarState, e scholar.Entry) {
	l := &p.literature
	offlineItem := l.zotero.ReadOnly() && e.RemoteID != "" && !slices.Contains(p.bibNames, e.Source)
	if !offlineItem && s.scholarReaderCommand(c.Key("scholarCopyBib"), "copy", s.tr("scholarCopyBib"), false).Disabled(p.busy).Clicked() {
		s.copyScholarBib(p, e, c.Services())
	}
	if slices.Contains(p.bibNames, e.Source) {
		if s.scholarReaderCommand(c.Key("scholarOpenBibliography"), "file-text", s.tr("scholarOpenBibliography"), false).Clicked() {
			s.OpenFiles([]string{filepath.Join(p.project.Root, e.Source)})
		}
		if s.scholarReaderCommand(c.Key("scholarInsertCitation"), "quote", s.tr("scholarInsertCitation"), false).Clicked() {
			s.insertScholarCitations(p, []string{e.Key}, "")
		}
	} else if !offlineItem && (e.RemoteID != "" || e.Source == "arXiv" || e.Source == "Crossref") {
		if len(p.bibNames) == 0 {
			ui.Text(c, s.tr("scholarDeclareTarget")).FontSize(11).Wrap()
		} else {
			if l.target < 0 || l.target >= len(p.bibNames) {
				l.target = 0
			}
			s.scholarReaderCommand(c.Key("bibliography"), "file-text", p.bibNames[l.target], true).Menu(func(m *ui.Menu) {
				for i, name := range p.bibNames {
					if m.Item(fmt.Sprintf("%d. %s", i+1, name)).Radio(l.target == i).Chosen() {
						l.target = i
					}
				}
			})
			if ext := strings.ToLower(filepath.Ext(p.bibNames[l.target])); ext == ".yml" || ext == ".yaml" {
				ui.Text(c, s.tr("scholarHayagrivaFields")).FontSize(11).Wrap()
			}
			if s.scholarReaderCommand(c.Key("scholarImportAndCite"), "quote", s.tr("scholarImportAndCite"), false).Disabled(p.busy || p.library == nil).Clicked() {
				s.importScholarEntry(p, p.bibNames[l.target], e)
			}
		}
		if e.RemoteID == "" {
			label := s.tr("scholarSaveToZotero")
			if !l.connected || !l.zotero.CanWrite() {
				label = s.tr("scholarCopyForZotero")
			}
			if s.scholarReaderCommand(c.Key("save-zotero"), "book-open", label, false).Disabled(p.busy).Clicked() {
				if !l.connected || !l.zotero.CanWrite() {
					value, err := scholar.ExportBibTeX(e)
					if err != nil {
						p.err = err.Error()
					} else {
						c.Services().WriteClipboard(value)
						s.status = s.tr("scholarCopiedBib")
					}
				} else {
					z := l.zotero
					s.scholarZoteroWrite(p, func(ctx context.Context) error {
						return z.Save(ctx, e)
					}, func() { s.scholarZoteroItemChanged(p, z, "", false) })
				}
			}
		} else if e.Library == 0 && e.Key != "" && s.scholarReaderCommand(c.Key("scholarCopyBBT"), "copy", s.tr("scholarCopyBBT"), false).Disabled(p.busy).Clicked() {
			z, svc := l.zotero, c.Services()
			s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
				ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				value, err := z.BibTeX(ctx, e.Key)
				return func() {
					if err == nil {
						svc.WriteClipboard(value)
					}
				}, err
			})
		}
	}
	if isManuscript(s.activeTab()) && s.scholarReaderCommand(c.Key("scholarDiscussPaper"), "message-square-text", s.tr("scholarDiscussPaper"), false).Disabled(p.busy || p.agent.patch != nil || p.agent.batch != nil).Clicked() {
		s.captureScholarTarget(p)
		s.showScholarInspector(p, scholarCodex)
		if p.agent.target.tabID == 0 {
			p.agent.multi = true
			p.err = ""
		}
		{
			p.agent.parts[2] = strings.Join([]string{e.Key, e.Title, e.Author, e.Year, e.DOI, l.detail.Abstract}, "\n")
			p.agent.include[2] = true
			p.agent.instruction = s.tr("scholarPaperInstruction")
		}
	}
}

func (s *Shell) scholarReaderAttachments(c *ui.Context, p *scholarState, e scholar.Entry) {
	l := &p.literature
	for _, pdf := range l.detail.PDFs {
		ck := c.Key(pdf.Key)
		title := pdf.Title
		if title == "" {
			title = s.tr("scholarOpenPDF")
		}
		ui.Row(ck).FillWidth().Gap(3).Children(func() {
			glyph := "file-text"
			if pdf.LinkOnly {
				glyph = "external-link"
			}
			if s.scholarReaderCommand(ck.Key("open"), glyph, title, false).Grow(1).Basis(0).Disabled(p.busy || pdf.LinkOnly && pdf.URL == "").Clicked() {
				s.openScholarZoteroPDF(p, e, pdf)
			}
			if pdf.LinkOnly {
				return
			}
			s.scholarCompactIcon(ck.Key("actions"), "ellipsis", s.tr("scholarAttachmentActions"), false).Disabled(p.busy).Menu(func(m *ui.Menu) {
				if s.pf.mac && m.Item("Quick Look · Space").Chosen() {
					s.quickLookScholarAttachment(p, pdf.Key)
				}
				if m.Item(s.tr("scholarReadAnnotations")).Chosen() {
					z := l.zotero
					z.Group = e.Library
					s.openScholarAnnotations(p, e, z, pdf.Key, pdf.Title)
				}
			})
		})
	}
	if l.detail.More || l.attachmentPage != 0 {
		ui.Row(c).FillWidth().Gap(4).Children(func() {
			if s.scholarCompactIcon(c.Key("previous"), "chevron-left", s.tr("scholarPreviousAttachments"), false).Disabled(p.busy || l.attachmentPage == 0).Clicked() {
				s.loadScholarDetail(p, e, l.attachmentPage-scholar.ZoteroPageSize)
			}
			if s.scholarCompactIcon(c.Key("next"), "chevron-right", s.tr("scholarNextAttachments"), false).Disabled(p.busy || !l.detail.More).Clicked() {
				s.loadScholarDetail(p, e, l.attachmentPage+scholar.ZoteroPageSize)
			}
		})
	}
}

func scholarReferenceKey(library *scholar.Library, entry scholar.Entry) string {
	if entry.Source != "Zotero" && entry.Source != "Crossref" && entry.Source != "arXiv" && entry.Key != "" {
		return entry.Key
	}
	if known, ok := library.Peek(entry.Key); ok &&
		(known.Title == entry.Title || entry.DOI != "" && scholar.NormalizeDOI(known.DOI) == scholar.NormalizeDOI(entry.DOI)) {
		return known.Key
	}
	if library != nil && entry.DOI != "" {
		for _, known := range library.Entries {
			if scholar.NormalizeDOI(known.DOI) == scholar.NormalizeDOI(entry.DOI) {
				return known.Key
			}
		}
	}
	return ""
}

func (s *Shell) insertScholarQuote(p *scholarState, key, page, quote string) {
	tab := s.activeTab()
	if !isManuscript(tab) {
		p.err = s.tr("scholarOpenQuoteTarget")
		return
	}
	quote = strings.Join(strings.Fields(quote), " ")
	var text string
	if strings.EqualFold(filepath.Ext(tab.Path), ".typ") {
		quote = strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(quote)
		text = fmt.Sprintf("\n#quote(block: true)[#text(\"%s\")] @%s[%s]\n", quote, key, scholarCitationLocator(page, true))
	} else {
		quote = strings.NewReplacer("\\", `\textbackslash{}`, "{", `\{`, "}", `\}`, "%", `\%`, "&", `\&`,
			"#", `\#`, "_", `\_`, "$", `\$`, "~", `\textasciitilde{}`, "^", `\textasciicircum{}`).Replace(quote)
		text = fmt.Sprintf("\n\\begin{quote}\n%s\n\\end{quote}\n\\cite[%s]{%s}\n", quote, scholarCitationLocator(page, false), key)
	}
	st := tab.Source()
	sel := st.Selection()
	offset := max(sel.Anchor, sel.Head)
	st.Buffer.Edit(editor.Edit{Start: offset, End: offset, Text: text})
	st.Select(offset+len(text), offset+len(text))
	if tab.typesetMode == typesetPreview {
		tab.typesetMode = typesetSplit
	}
	p.changes.compare, s.focusEditor = false, true
}

func (s *Shell) importScholarEntry(p *scholarState, target string, entry scholar.Entry) {
	if p.busy {
		return
	}
	if entry.RemoteID != "" && p.literature.zotero.ReadOnly() {
		p.err = s.tr("scholarZoteroOnlineExport")
		return
	}
	var paths []string
	for _, name := range p.bibNames {
		path := filepath.Join(p.project.Root, name)
		if scholarFileOpen(path) {
			p.err = s.tr("scholarCloseBibFirst")
			return
		}
		paths = append(paths, path)
	}
	tab := s.activeTab()
	if !isManuscript(tab) {
		p.err = s.tr("scholarOpenCitationTarget")
		return
	}
	st := tab.Source()
	version, selection := st.Buffer.Version(), st.Selection()
	project, names, zotero := p.project, slices.Clone(p.bibNames), p.literature.zotero
	zotero.Group = entry.Library
	release, ok := reserveScholarPaths(paths)
	if !ok {
		p.err = s.tr("scholarBibliographyBusy")
		return
	}
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		defer release()
		var export []byte
		if entry.RemoteID != "" {
			exportCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
			var err error
			export, err = zotero.ExportFor(exportCtx, entry, target)
			cancel()
			if err != nil {
				return nil, err
			}
		}
		key, err := project.Append(ctx, names, target, entry, export)
		return func() {
			if err != nil {
				return
			}
			p.libraryReady = false
			if entry.RemoteID != "" {
				if s.prefs.ScholarProjects == nil {
					s.prefs.ScholarProjects = make(map[string]scholarProjectPrefs)
				}
				pref := s.prefs.ScholarProjects[p.project.Root]
				if pref.Sources == nil {
					pref.Sources = make(map[string]scholarSourceLink)
				}
				pref.Sources[key] = scholarSourceLink{Key: entry.RemoteID, Group: entry.Library, DOI: scholar.NormalizeDOI(entry.DOI)}
				s.prefs.ScholarProjects[p.project.Root] = pref
				s.setPref("scholarProjects")
			}
			if s.activeTab() != tab || st.Buffer.Version() != version || st.Selection() != selection {
				s.status = s.tr("scholarImportedMoved")
				return
			}
			s.insertScholarCitations(p, []string{key}, "")
		}, err
	})
}

func (s *Shell) insertScholarCitations(p *scholarState, keys []string, page string) {
	tab := s.activeTab()
	if !isManuscript(tab) {
		p.err = s.tr("scholarOpenCitationTarget")
		return
	}
	typst := strings.EqualFold(filepath.Ext(tab.Path), ".typ")
	st := tab.Source()
	sel := st.Selection()
	snap := st.Buffer.Snapshot()
	start, end := min(sel.Anchor, sel.Head), max(sel.Anchor, sel.Head)
	at, inside := scholarCitationTail(snap, end, typst)
	var value string
	if inside && start == end && page == "" {
		start = at
		end += scholar.CitationKeyEnd(snap.Slice(end, min(snap.Len(), end+4096)), 0)
		if typst {
			value = strings.Join(keys, " @")
		} else {
			value = strings.Join(keys, ", ")
			tail := end
			for tail < snap.Len() {
				b := snap.Slice(tail, tail+1)[0]
				if b != ' ' && b != '\t' {
					break
				}
				tail++
			}
			if tail == snap.Len() || !strings.ContainsAny(snap.Slice(tail, tail+1), "},") {
				value += "}"
			}
		}
	} else if typst {
		value = "@" + strings.Join(keys, " @")
		if page != "" {
			value += "[" + scholarCitationLocator(page, true) + "]"
		}
	} else {
		value = `\cite{` + strings.Join(keys, ", ") + "}"
		if page != "" {
			value = `\autocite[` + scholarCitationLocator(page, false) + "]{" + strings.Join(keys, ", ") + "}"
		}
	}
	if tab.typesetMode == typesetPreview {
		tab.typesetMode = typesetSplit
	}
	p.changes.compare = false
	if !inside || sel.Anchor != sel.Head || page != "" {
		start = end
	}
	st.Buffer.Edit(editor.Edit{Start: start, End: end, Text: value})
	caret := start + len(value)
	st.Select(caret, caret)
	s.focusEditor = true
}

// Page labels are Zotero text, not manuscript markup. Keep printed labels
// separate from the physical page used to open the attachment.
func scholarCitationLocator(page string, typst bool) string {
	page = strings.Join(strings.Fields(page), " ")
	if typst {
		page = strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(page)
		return "p. #text(\"" + page + "\")"
	}
	page = strings.NewReplacer("\\", `\textbackslash{}`, "{", `\{`, "}", `\}`, "%", `\%`, "&", `\&`,
		"#", `\#`, "_", `\_`, "$", `\$`, "~", `\textasciitilde{}`, "^", `\textasciicircum{}`).Replace(page)
	return "p.~{" + page + "}"
}

func scholarCitationTail(snap editor.Snapshot, offset int, typst bool) (int, bool) {
	if offset <= 0 || offset > snap.Len() {
		return 0, false
	}
	begin := snap.LineStart(snap.LineOf(offset))
	if offset-begin > 2048 {
		return 0, false
	}
	if typst {
		line := snap.Slice(begin, offset)
		if !strings.Contains(line, "@") || offset > 128<<10 {
			return 0, false
		}
		begin = 0
	}
	at, ok := scholar.CitationTail(snap.Slice(begin, offset), typst)
	return begin + at, ok
}

func (s *Shell) bindScholarCitations(t *Tab) {
	if !isManuscript(t) || t.syntaxProvider == nil || t.scholarBound {
		return
	}
	t.scholarBound = true
	st := t.Source()
	typst := strings.EqualFold(filepath.Ext(t.Path), ".typ")
	priorCompletion, priorInput, after := st.OnCompletion, st.OnInput, t.afterSourceInput
	st.OnCompletion = func(req editor.CompletionRequest) {
		p := s.scholar
		if p != nil && p.library != nil && s.activeTab() == t && req.Version == st.Buffer.Version() {
			snap := st.Buffer.Snapshot()
			if at, ok := scholarCitationTail(snap, req.Offset, typst); ok {
				p.literature.inline.results.clear()
				generation := p.literature.inline.results.generation
				p.literature.inline = scholarInlineCitation{results: scholarQueryState{generation: generation}, open: true, focus: true, tabID: t.ID, version: req.Version, start: at, end: req.Offset, query: snap.Slice(at, req.Offset)}
				return
			}
		}
		if priorCompletion != nil {
			priorCompletion(req)
		}
	}
	pending := false
	st.OnInput = func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputPointerUp && ev.Button == 0 && ev.Clicks == 1 && ev.Mods == 0 {
			if pane := s.compile; pane != nil && pane.tab == t && pane.sync.linked {
				pane.sync.sourceClick = true
			}
		}
		if ev.Kind == ui.InputCommand && (ev.Text == "undo" || ev.Text == "redo") {
			s.tagScholarChange(t, ev.Text)
		}
		if ev.Kind == ui.InputKeyDown {
			switch {
			case ev.Mods == ui.Cmd && ev.Key == ui.KeyZ:
				s.tagScholarChange(t, "undo")
			case ev.Mods == ui.Cmd|ui.Shift && ev.Key == ui.KeyZ, ev.Mods == ui.Cmd && ev.Key == ui.KeyY:
				s.tagScholarChange(t, "redo")
			}
		}
		if ev.Kind == ui.InputText {
			pending = ev.Text == "@" && typst || !typst && (ev.Text == "{" || ev.Text == ",")
		}
		return priorInput != nil && priorInput(ev)
	}
	t.afterSourceInput = func() {
		if after != nil {
			after()
		}
		if pending {
			pending = false
			if st.Focused() {
				st.Run(editor.ShowCompletions)
			}
		}
	}
	st.OnDefinition = func(offset int) {
		p := s.scholar
		if p == nil || s.activeTab() != t {
			return
		}
		for _, ref := range p.citations.ranges {
			if offset >= ref.Start && offset <= ref.End {
				s.showScholarReferenceAt(st, p, ref, true)
				return
			}
		}
		if pane := s.compile; pane != nil && pane.tab == t && !pane.busy {
			line := st.Buffer.Snapshot().LineOf(offset)
			pane.requestSourcePosition(line)
		}
	}
	st.RenderHover = func(c *ui.Context, panel ui.Element, h *editor.HoverCard) {
		editor.DefaultHover(c, panel, h)
		panel.Width(360).MaxWidth(360)
	}
	st.OnHover = func(req editor.HoverRequest) {
		p := s.scholar
		if p == nil || p.library == nil || s.activeTab() != t {
			return
		}
		snap := st.Buffer.Snapshot()
		if req.Offset < 0 || req.Offset >= snap.Len() || req.Version != snap.Version() {
			return
		}
		state := &p.citations
		if state.tabID != t.ID || state.library != p.library || state.version != snap.Version() {
			return
		}
		i := sort.Search(len(state.ranges), func(i int) bool { return state.ranges[i].End > req.Offset })
		if i == len(state.ranges) {
			return
		}
		ref := state.ranges[i]
		if req.Offset < ref.Start {
			return
		}
		s.showScholarReferenceAt(st, p, ref, false)
	}
}

func (s *Shell) scholarCitationDiagnostics(c *ui.Context, p *scholarState, tab *Tab) {
	state := &p.citations
	if state.tabID != tab.ID {
		s.clearScholarEditor(p)
		*state = scholarCitations{tabID: tab.ID}
	}
	lib := p.library
	if state.pending {
		return
	}
	hasReferences := lib != nil && len(p.bibNames) > 0
	st := tab.Source()
	snap := st.Buffer.Snapshot()
	if state.ready && state.library == lib && state.version == snap.Version() {
		return
	}
	if state.version != snap.Version() {
		state.version = snap.Version()
		state.library, state.ready, state.counted = nil, false, false
		state.unknown, state.counts, state.ranges = 0, nil, nil
		state.retry = c.Now().Add(450 * time.Millisecond)
	}
	if c.Now().Before(state.retry) {
		c.After(state.retry.Sub(c.Now()))
		return
	}
	state.pending = true
	id, path := tab.ID, tab.Path
	typst := strings.EqualFold(filepath.Ext(path), ".typ")
	unknownLabel := s.tr("scholarUnknownKey")
	go func() {
		counts := make(map[string]int)
		var problems []editor.Diagnostic
		var ranges []scholar.Citation
		unknown := 0
		var stats mdedit.Stats
		counted := snap.Len() <= 256<<10
		var source string
		if p.ctx.Err() == nil && (counted || hasReferences && snap.Len() <= 1<<20) {
			source = snap.String()
		}
		if counted {
			stats = mdedit.TextStats(source)
		}
		if hasReferences && source != "" {
			scholar.Citations(source, typst, func(ref scholar.Citation) {
				if entry, ok := lib.Find(ref.Key); ok {
					counts[entry.Key]++
					if len(ranges) < 8192 {
						ref.Key = entry.Key // Keep only the indexed key, not a slice of the source.
						ranges = append(ranges, ref)
					}
					return
				}
				unknown++
				if len(problems) < 128 {
					problems = append(problems, editor.Diagnostic{Start: ref.Start, End: ref.End, Severity: editor.SeverityWarning,
						Message: unknownLabel + ref.Key, Source: "Nomo Scholar"})
				}
			})
		}
		s.post(func() {
			if s.scholar != p || state.tabID != id {
				return
			}
			state.pending = false
			if s.activeTab() != tab || st.Buffer.Version() != snap.Version() || p.library != lib {
				return
			}
			state.library, state.counts, state.unknown, state.ranges = lib, counts, unknown, ranges
			state.ready, state.counted, state.words, state.chars = true, counted, stats.Words, stats.Chars
			st.SetDiagnostics(snap.Version(), problems)
		})
	}()
}

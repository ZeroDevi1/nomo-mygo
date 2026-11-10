package shell

import (
	"context"
	"fmt"
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
	tag                                                string
	tagPicker                                          scholarTagPicker
	recent                                             []scholar.Entry
	recentOpen                                         bool
	expanded, scopeOpen                                bool
	loaded, treeTried                                  bool
	scope                                              scholar.ZoteroScope
	tree                                               map[string]*scholarCollectionBranch
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
	ui.Column(c).FillWidth().Grow(1).MinHeight(0).Padding(8).Gap(5).Children(func() {
		ui.Row(c).FillWidth().Gap(4).Children(func() {
			for i, title := range []string{s.tr("scholarProject"), "Zotero", s.tr("scholarOnline")} {
				if ui.Button(c.Key(i), title).Disabled(p.busy).Clicked() {
					l.mode = i
				}
			}
		})
		if p.err != "" {
			ui.Text(c, p.err).FontSize(11).Wrap().TextColor(scholarInk(c, "warning"))
		}
		switch l.mode {
		case 0:
			if s.scholarLocalSearchView(c, p) {
				return
			}
			input := ui.TextInput(c, &l.query).FillWidth().Placeholder(s.tr("scholarCitationSearch"))
			if len(l.checked) > 0 && (ui.Button(c, fmt.Sprintf(s.tr("scholarInsertCount"), len(l.checked))).Clicked() || input.Submitted()) {
				s.insertScholarCitations(p, l.checked, "")
				l.checked = nil
			}
			if ui.Button(c, s.tr("scholarReloadReferences")).Disabled(p.busy).Clicked() {
				s.loadScholarLibrary(p, true)
			}
			if p.libraryError != "" {
				ui.Text(c, p.libraryError).FontSize(11).Wrap()
			}
			if len(p.bibNames) == 0 && p.libraryReady {
				ui.Text(c, s.tr("scholarNoBibliography")).FontSize(11).Wrap()
			}
			s.scholarEntries(c, p, s.scholarLiteratureResults(c, p), true)
			if len(l.visible) == 100 {
				ui.Text(c, s.tr("scholarFirstHundred")).FontSize(11).Wrap()
			}
		case 1:
			s.scholarZoteroCatalog(c, p)
		case 2:
			ui.Text(c, s.tr("scholarOnlineDisclosure")).FontSize(11).Wrap()
			in := ui.TextInput(c, &l.remoteQuery).FillWidth().Placeholder(s.tr("scholarMetadataSearch"))
			if ui.Button(c, s.tr("scholarOnlineSearch")).Disabled(p.busy).Clicked() || in.Submitted() {
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
			s.scholarEntries(c, p, l.results, false)
		}
	})
}

func (s *Shell) scholarZoteroResults(c *ui.Context, p *scholarState) {
	l := &p.literature
	ui.Checkbox(c, &l.fulltext, s.tr("scholarFulltextPage")).Disabled(p.busy || l.scope != scholar.ZoteroAll || l.tag != "")
	if l.fulltext {
		ui.Text(c, fmt.Sprintf(s.tr("scholarFulltextScope"), l.fulltextScanned, l.fulltextPDFs, l.fulltextUnavailable)).FontSize(11).Wrap()
	}
	in := ui.TextInput(c, &l.remoteQuery).FillWidth().Placeholder(s.tr("scholarSearchZotero"))
	ui.Row(c).Gap(4).FillWidth().Children(func() {
		if ui.Button(c, s.tr("scholarSearch")).Disabled(p.busy).Clicked() || in.Submitted() {
			s.loadZotero(p, false, l.collection, l.remoteQuery, 0)
		}
	})
	scanning := p.busy && l.fulltextJob != 0 && l.fulltextJob == p.job
	if l.fulltext && (scanning || l.sentQuery != "") {
		ui.Text(c, fmt.Sprintf(s.tr("scholarFulltextProgress"), l.fulltextProgress, l.fulltextTotal, len(l.fulltextEntries))).FontSize(11).Wrap()
		if l.fulltextPaused && !scanning {
			ui.Text(c, s.tr("scholarFulltextPaused")).FontSize(11).Wrap()
		}
		if ui.Button(c, s.tr("scholarContinueScan")).Disabled(p.busy || !l.fulltextMore).Clicked() {
			if len(l.fulltextEntries) >= 200 {
				l.fulltextEntries = nil
				l.fulltextPage = 0
			}
			s.loadZotero(p, false, l.collection, l.sentQuery, l.fulltextNext)
		}
		if scanning && ui.Button(c, s.tr("scholarAgentCancelWork")).Clicked() && p.cancelJob != nil {
			p.cancelJob()
		}
		ui.Row(c).Gap(4).Children(func() {
			if ui.Button(c, s.tr("scholarPreviousPage")).Disabled(l.fulltextPage == 0).Clicked() {
				l.fulltextPage--
			}
			if ui.Button(c, s.tr("scholarNextPage")).Disabled((l.fulltextPage+1)*scholar.ZoteroPageSize >= len(l.fulltextEntries)).Clicked() {
				l.fulltextPage++
			}
		})
		lo := min(l.fulltextPage*scholar.ZoteroPageSize, len(l.fulltextEntries))
		l.pageData.Entries = l.fulltextEntries[lo:min(lo+scholar.ZoteroPageSize, len(l.fulltextEntries))]
	} else {
		ui.Row(c).FillWidth().Gap(4).Children(func() {
			if ui.Button(c, s.tr("scholarPreviousPage")).Disabled(p.busy || l.page == 0).Clicked() {
				s.loadZotero(p, false, l.collection, l.sentQuery, l.page-scholar.ZoteroPageSize)
			}
			if ui.Button(c, s.tr("scholarNextPage")).Disabled(p.busy || !l.pageData.More).Clicked() {
				s.loadZotero(p, false, l.collection, l.sentQuery, l.page+scholar.ZoteroPageSize)
			}
			ui.Text(c, fmt.Sprint(l.page/scholar.ZoteroPageSize+1)).FontSize(11)
		})
	}
	s.scholarEntries(c, p, l.pageData.Entries, false)
}

func (s *Shell) scholarEntries(c *ui.Context, p *scholarState, entries []scholar.Entry, local bool) {
	l := &p.literature
	if p.references.catalog {
		s.scholarCatalogRows(c, p, entries, local)
		return
	}
	ui.Scroll(c).Grow(1).MinHeight(0).FillWidth().Children(func() {
		ui.Column(c).FillWidth().Gap(2).Children(func() {
			for i, entry := range entries {
				ck := c.Key(i)
				ui.Row(ck).FillWidth().AlignItems(ui.Center).Gap(2).Children(func() {
					b := ui.ButtonBase(ck).Grow(1).Padding(4).Justify(ui.Start).Tooltip(entry.Title).Disabled(p.busy)
					if b.Hovered() {
						b.Background(s.ch.hover)
					}
					b.Children(func() {
						ui.Column(ck).FillWidth().Children(func() {
							key := entry.Key
							if key == "" {
								key = entry.Source
							}
							if local {
								if n := p.citations.counts[entry.Key]; n > 0 {
									key += fmt.Sprintf(s.tr("scholarCitedCount"), n)
								}
							}
							ui.Text(ck, key).FontSize(11).TextColor(scholarInk(c, "citation")).SingleLine()
							ui.Text(ck, entry.Title).FontSize(12).SingleLine()
							ui.Text(ck, entry.Author+" "+entry.Year).FontSize(11).TextColor(s.ch.muted).SingleLine()
						})
					})
					if b.Clicked() {
						if l.picker {
							l.picker = false
						}
						s.selectScholarEntry(p, entry)
					}
					if local {
						checked := slices.Contains(l.checked, entry.Key)
						label := "+"
						if checked {
							label = "✓"
						}
						if ui.Button(ck, label).Tooltip(s.tr("scholarMultiCite")).Clicked() {
							toggleScholarCitation(l, entry.Key)
						}
					}
				})
			}
		})
	})
}

func (s *Shell) loadZotero(p *scholarState, connect bool, collection, query string, start int) {
	if p.busy {
		return
	}
	l := &p.literature
	z := l.zotero
	tag, scope := l.tag, l.scope
	fulltext := l.fulltext && query != "" && scope == scholar.ZoteroAll && tag == ""
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
		if connect {
			z.Group = 0
			z, err = z.Reconnect(ctx)
		}
		var page scholar.ZoteroPage
		var full scholar.ZoteroFulltextResult
		if err == nil {
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
		}
		return func() {
			if connect {
				l.connected = err == nil
				l.annotations = nil
			}
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
			if connect && l.selected.RemoteID != "" {
				l.selected, l.detail = scholar.Entry{}, scholar.ZoteroDetail{}
			}
			if err != nil {
				if connect {
					l.zotero, l.pageData = scholar.Zotero{}, scholar.ZoteroPage{}
					l.loaded = false
				}
				return
			}
			l.zotero, l.pageData, l.collection, l.sentQuery, l.page = z, page, collection, query, start
			l.loaded = true
		}, err
	})
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
	l.selectedNote = ""
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
	if p.err != "" {
		ui.Text(c, p.err).Wrap().FontSize(11).TextColor(scholarInk(c, "warning"))
	}
	if view := l.annotations; view != nil {
		s.scholarAnnotations(c.Key(view), p, view)
		return
	}
	e := l.selected
	if e == (scholar.Entry{}) {
		ui.Text(c, s.tr("scholarSelectPaper")).Wrap()
		return
	}
	if form := l.zoteroForm; form != nil && !form.catalog && form.z.ServerID == l.zotero.ServerID &&
		form.z.Group == e.Library && (form.key == e.RemoteID || form.key == l.selectedNote) {
		s.scholarZoteroForm(c.Key("zotero-form"), p, form)
		return
	}
	if l.detail.Deleted {
		ui.Text(c, e.Title).Bold().Wrap()
		s.scholarZoteroReaderActions(c.Key("zotero-actions"), p, e, nil)
		return
	}
	for _, note := range l.detail.Notes {
		if e.ZoteroNote || note.Key == l.selectedNote {
			s.scholarZoteroReaderActions(c.Key("zotero-actions"), p, e, &note)
			s.scholarNoteDetails(c, p, note, e.ZoteroNote)
			return
		}
	}
	if e.ZoteroNote {
		ui.Text(c, e.Title).Bold().Wrap()
		return
	}
	ui.Text(c, e.Key).FontSize(12).TextColor(scholarInk(c, "citation"))
	ui.Text(c, e.Title).Bold().Wrap()
	ui.Text(c, strings.TrimSpace(e.Author+" "+e.Year+" "+e.Venue)).Wrap()
	ui.Text(c, e.Source+" "+e.DOI).FontSize(11).Wrap()
	s.scholarZoteroReaderActions(c.Key("zotero-actions"), p, e, nil)
	if ui.Button(c, s.tr("scholarOpenPDF")).Disabled(p.busy).Clicked() {
		s.openScholarPDF(p, e, c.Services())
	}
	if ui.Button(c, s.tr("scholarCopyBib")).Disabled(p.busy).Clicked() {
		s.copyScholarBib(p, e, c.Services())
	}

	if slices.Contains(p.bibNames, e.Source) {
		if ui.Button(c, s.tr("scholarOpenBibliography")).Clicked() {
			s.OpenFiles([]string{filepath.Join(p.project.Root, e.Source)})
		}
		if ui.Button(c, s.tr("scholarInsertCitation")).Clicked() {
			s.insertScholarCitations(p, []string{e.Key}, "")
		}
	} else if e.RemoteID != "" || e.Source == "arXiv" || e.Source == "Crossref" {
		if len(p.bibNames) == 0 {
			ui.Text(c, s.tr("scholarDeclareTarget")).FontSize(11).Wrap()
		} else {
			scholarSelectIndex(c, &l.target, p.bibNames, false)
			if ext := strings.ToLower(filepath.Ext(p.bibNames[l.target])); ext == ".yml" || ext == ".yaml" {
				ui.Text(c, s.tr("scholarHayagrivaFields")).FontSize(11).Wrap()
			}
			if ui.Button(c, s.tr("scholarImportAndCite")).Disabled(p.busy || p.library == nil).Clicked() {
				s.importScholarEntry(p, p.bibNames[l.target], e)
			}
		}
		if e.RemoteID == "" {
			label := s.tr("scholarSaveToZotero")
			if !l.connected || l.zotero.ServerID == "" {
				label = s.tr("scholarCopyForZotero")
			}
			if ui.Button(c, label).Disabled(p.busy).Clicked() {
				if !l.connected || l.zotero.ServerID == "" {
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
		} else if e.Library == 0 && e.Key != "" && ui.Button(c, s.tr("scholarCopyBBT")).Disabled(p.busy).Clicked() {
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
	abstract := l.detail.Abstract
	if abstract == "" {
		abstract = e.Abstract
	}
	if abstract != "" {
		ui.Text(c, abstract).FontSize(13).Wrap()
	}
	s.scholarZoteroMetadata(c, p, l.detail.Tags, l.detail.Related)
	if !p.references.catalog {
		for _, note := range l.detail.Notes {
			if ui.Button(c.Key(note.Key), scholarNoteTitle(s, note)).Clicked() {
				l.selectedNote = note.Key
			}
		}
	}
	if isManuscript(s.activeTab()) && ui.Button(c, s.tr("scholarDiscussPaper")).Disabled(p.busy || p.agent.patch != nil || p.agent.batch != nil).Clicked() {
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
	for _, pdf := range l.detail.PDFs {
		ck := c.Key(pdf.Key)
		ui.Text(ck, pdf.Title).Wrap()
		ui.Row(ck).Gap(4).Children(func() {
			if pdf.LinkOnly {
				if ui.Button(ck, s.tr("scholarOpenExternalPDF")).Disabled(pdf.URL == "").Clicked() {
					s.openScholarZoteroPDF(p, e, pdf, c.Services())
				}
				return
			}
			if ui.Button(ck, s.tr("scholarOpenPDF")).Clicked() {
				s.openScholarZoteroPDF(p, e, pdf, c.Services())
			}
			if s.pf.mac && ui.Button(ck, "Quick Look · Space").Disabled(p.busy).Clicked() {
				s.quickLookScholarAttachment(p, pdf.Key)
			}
			if ui.Button(ck, s.tr("scholarReadAnnotations")).Disabled(p.busy).Clicked() {
				z := l.zotero
				z.Group = e.Library
				s.openScholarAnnotations(p, e, z, pdf.Key, pdf.Title)
			}
		})
	}
	if l.detail.More || l.attachmentPage != 0 {
		ui.Row(c).Gap(4).Children(func() {
			if ui.Button(c, s.tr("scholarPreviousAttachments")).Disabled(p.busy || l.attachmentPage == 0).Clicked() {
				s.loadScholarDetail(p, e, l.attachmentPage-scholar.ZoteroPageSize)
			}
			if ui.Button(c, s.tr("scholarNextAttachments")).Disabled(p.busy || !l.detail.More).Clicked() {
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
		text = fmt.Sprintf("\n#quote(block: true)[#text(\"%s\")] @%s[p. %s]\n", quote, key, page)
	} else {
		quote = strings.NewReplacer("\\", `\textbackslash{}`, "{", `\{`, "}", `\}`, "%", `\%`, "&", `\&`,
			"#", `\#`, "_", `\_`, "$", `\$`, "~", `\textasciitilde{}`, "^", `\textasciicircum{}`).Replace(quote)
		text = fmt.Sprintf("\n\\begin{quote}\n%s\n\\end{quote}\n\\cite[p.~%s]{%s}\n", quote, page, key)
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
			value += "[p. " + page + "]"
		}
	} else {
		value = `\cite{` + strings.Join(keys, ", ") + "}"
		if page != "" {
			value = `\autocite[p.~` + page + "]{" + strings.Join(keys, ", ") + "}"
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

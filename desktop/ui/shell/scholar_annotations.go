package shell

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/scholar"
)

type scholarAnnotationView struct {
	entry      scholar.Entry
	zotero     scholar.Zotero
	key, title string
	items      []scholar.Annotation
	start      int
	more       bool
	loaded     bool
	scroll     ui.ScrollState
}

func (s *Shell) openScholarAnnotations(p *scholarState, entry scholar.Entry, z scholar.Zotero, key, title string) {
	if p.busy {
		return
	}
	if title == "" {
		title = entry.Title
	}
	view := p.references.annotation
	if view == nil || view.key != key || view.zotero.Group != z.Group || !view.zotero.SameSource(z) {
		view = &scholarAnnotationView{entry: entry, zotero: z, key: key, title: title}
		p.references.annotation = view
	}
	p.literature.annotations = view
	s.showScholarInspector(p, scholarReader)
	if !view.loaded {
		s.loadScholarAnnotations(p, view, view.start)
	}
}

func (view *scholarAnnotationView) matches(tab *scholarReferenceTab) bool {
	return view.key != "" && view.key == tab.key && view.zotero.Group == tab.zotero.Group && view.zotero.SameSource(tab.zotero)
}

func (s *Shell) loadScholarAnnotations(p *scholarState, view *scholarAnnotationView, start int) {
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		items, more, err := view.zotero.Annotations(ctx, view.key, start)
		return func() {
			if err == nil {
				view.items, view.more, view.start, view.loaded = items[:min(len(items), scholar.ZoteroPageSize)], more, start, true
				view.scroll.X, view.scroll.Y = 0, 0
			}
		}, err
	})
}

func (s *Shell) returnScholarAnnotationEntry(p *scholarState, view *scholarAnnotationView) {
	p.literature.annotations, p.literature.selectedNote = nil, ""
	p.literature.readerActive, p.literature.readerJump = 0, 1
	if !scholarSameCatalogItem(p.literature.selected, view.entry) {
		s.setScholarEntry(p, view.entry)
	}
}

func (s *Shell) scholarAnnotations(c *ui.Context, p *scholarState, view *scholarAnnotationView) {
	ui.Column(c).FillWidth().Padding(8, 0, 12, 0).Gap(6).Children(func() {
		currentLibrary := view.zotero.SameSource(p.literature.zotero)
		if !view.loaded {
			if p.busy {
				scholarReaderText(c, s.tr("scholarLoadingAnnotations")).TextColor(s.ch.muted)
			}
			return
		}
		if len(view.items) == 0 {
			scholarReaderText(c, s.tr("scholarNoAnnotations")).TextColor(s.ch.muted)
		}
		projectKey := ""
		if len(view.items) > 0 && isManuscript(s.activeTab()) {
			projectKey = scholarReferenceKey(p.library, view.entry)
		}
		for i, annotation := range view.items {
			ck := c
			if i > 0 {
				s.scholarReaderDivider(c)
			}
			ui.Column(c.Key(i)).FillWidth().Padding(4, 0).Gap(5).Children(func() {
				label := strings.TrimSpace(annotation.Label)
				if label == "" && annotation.Page > 0 {
					label = strconv.Itoa(annotation.Page)
				}
				ui.Row(ck).Gap(6).AlignItems(ui.Center).Children(func() {
					kind := "scholarReadAnnotations"
					switch annotation.Kind {
					case "highlight":
						kind = "scholarAnnotationHighlight"
					case "underline":
						kind = "scholarAnnotationUnderline"
					case "note", "text":
						kind = "scholarAnnotationComment"
					case "image", "ink":
						kind = "scholarAnnotationArea"
					}
					ui.Text(ck, s.tr(kind)).FontSize(11).TextColor(s.ch.muted)
					if s.scholarCompactIcon(ck.Key("zotero"), "external-link", s.tr("scholarOpenZoteroAnnotation"), false).
						Disabled(!currentLibrary).Clicked() {
						c.Services().OpenURL(view.zotero.AnnotationLink(view.key, annotation))
					}
					if annotation.Page <= 0 {
						return
					}
					if s.scholarReaderCommand(ck.Key("page"), "file-text", "PDF · "+label, false).Grow(1).Basis(0).Disabled(p.busy || !currentLibrary).Clicked() {
						t := s.openScholarAttachmentFrom(p, view.entry, view.zotero, view.key, "", view.title, annotation.Page)
						width, _ := c.Size()
						if t != nil {
							p.references.annotation = view
							if !s.scholarReadingInspectorPrimary(width) {
								p.literature.annotations = view
								s.showScholarInspector(p, scholarReader)
							}
						}
					}
				})
				if annotation.Quote != "" {
					scholarReaderText(ck, annotation.Quote).Selectable()
				}
				if annotation.Note != "" {
					scholarReaderText(ck, annotation.Note).Selectable().TextColor(s.ch.muted)
				}
				if projectKey != "" && label != "" {
					ui.Row(ck.Key("cite-actions")).Gap(4).Children(func() {
						if s.scholarReaderCommand(ck.Key("cite"), "quote", s.tr("scholarCitePage"), false).Grow(1).Basis(0).Disabled(p.busy).Clicked() {
							s.selectScholarPDF(p, nil)
							s.insertScholarCitations(p, []string{projectKey}, label)
						}
						if annotation.Quote != "" && s.scholarReaderCommand(ck.Key("quote"), "copy", s.tr("scholarInsertQuote"), false).Grow(1).Basis(0).Disabled(p.busy).Clicked() {
							s.selectScholarPDF(p, nil)
							s.insertScholarQuote(p, projectKey, label, annotation.Quote)
						}
					})
				}
			})
		}
		if view.start > 0 || view.more {
			s.scholarReaderDivider(c)
			ui.Row(c.Key("pagination")).FillWidth().Gap(4).Justify(ui.End).Children(func() {
				if s.scholarCompactIcon(c.Key("previous"), "chevron-left", s.tr("scholarPreviousAnnotations"), false).Disabled(p.busy || view.start == 0).Clicked() {
					s.loadScholarAnnotations(p, view, view.start-scholar.ZoteroPageSize)
				}
				if s.scholarCompactIcon(c.Key("next"), "chevron-right", s.tr("scholarNextAnnotations"), false).Disabled(p.busy || !view.more).Clicked() {
					s.loadScholarAnnotations(p, view, view.start+scholar.ZoteroPageSize)
				}
			})
		}
	})
}

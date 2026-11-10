package shell

import (
	"context"
	"strconv"
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
}

func (s *Shell) openScholarAnnotations(p *scholarState, entry scholar.Entry, z scholar.Zotero, key, title string) {
	if p.busy {
		return
	}
	if title == "" {
		title = entry.Title
	}
	view := &scholarAnnotationView{entry: entry, zotero: z, key: key, title: title}
	p.literature.annotations = view
	s.showScholarInspector(p, scholarReader)
	s.loadScholarAnnotations(p, view, 0)
}

func (s *Shell) loadScholarAnnotations(p *scholarState, view *scholarAnnotationView, start int) {
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		items, more, err := view.zotero.Annotations(ctx, view.key, start)
		return func() {
			if err == nil && p.literature.annotations == view {
				view.items, view.more, view.start, view.loaded = items, more, start, true
			}
		}, err
	})
}

func (s *Shell) scholarAnnotations(c *ui.Context, p *scholarState, view *scholarAnnotationView) {
	currentLibrary := view.zotero.ServerID == p.literature.zotero.ServerID
	if ui.Button(c.Key("details"), s.tr("scholarAnnotationEntry")).Disabled(p.busy || !currentLibrary).Clicked() {
		p.literature.annotations = nil
		p.literature.selectedNote = ""
		if !scholarSameCatalogItem(p.literature.selected, view.entry) {
			s.setScholarEntry(p, view.entry)
		}
		return
	}
	ui.Text(c, s.tr("scholarReadAnnotations")).Bold()
	ui.Text(c, view.title).FontSize(12).Wrap()
	if !view.loaded {
		if p.busy {
			ui.Text(c, s.tr("scholarLoadingAnnotations")).Wrap()
		} else if ui.Button(c.Key("retry"), s.tr("refresh")).Clicked() {
			s.loadScholarAnnotations(p, view, 0)
		}
		return
	}
	if len(view.items) == 0 {
		ui.Text(c, s.tr("scholarNoAnnotations")).FontSize(12).Wrap()
	}
	projectKey := ""
	if len(view.items) > 0 && isManuscript(s.activeTab()) {
		projectKey = scholarReferenceKey(p.library, view.entry)
	}
	for i, annotation := range view.items {
		ck := c.Key(i)
		ui.Box(ck).FillWidth().Height(1).Background(s.ch.separator)
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
			if annotation.Page <= 0 {
				return
			}
			label := annotation.Label
			if label == "" {
				label = strconv.Itoa(annotation.Page)
			}
			if ui.Button(ck.Key("page"), "PDF · "+label).Disabled(p.busy || !currentLibrary).Clicked() {
				s.openScholarAttachment(p, view.entry, view.key, "", view.title, annotation.Page)
				width, _ := c.Size()
				if p.err == "" && !s.scholarSinglePane(width) {
					s.showScholarInspector(p, scholarReader)
				}
			}
		})
		if annotation.Quote != "" {
			ui.Text(ck, annotation.Quote).Wrap()
		}
		if annotation.Note != "" {
			ui.Text(ck, annotation.Note).Wrap().TextColor(s.ch.muted)
		}
		if projectKey != "" && annotation.Page > 0 {
			page := strconv.Itoa(annotation.Page)
			ui.Row(ck.Key("cite-actions")).Gap(4).Children(func() {
				if ui.Button(ck.Key("cite"), s.tr("scholarCitePage")).Disabled(p.busy).Clicked() {
					s.selectScholarPDF(p, nil)
					s.insertScholarCitations(p, []string{projectKey}, page)
				}
				if annotation.Quote != "" && ui.Button(ck.Key("quote"), s.tr("scholarInsertQuote")).Disabled(p.busy).Clicked() {
					s.selectScholarPDF(p, nil)
					s.insertScholarQuote(p, projectKey, page, annotation.Quote)
				}
			})
		}
	}
	if view.start > 0 || view.more {
		ui.Row(c.Key("pagination")).Gap(4).Children(func() {
			if ui.Button(c.Key("previous"), s.tr("scholarPreviousAnnotations")).Disabled(p.busy || view.start == 0).Clicked() {
				s.loadScholarAnnotations(p, view, view.start-scholar.ZoteroPageSize)
			}
			if ui.Button(c.Key("next"), s.tr("scholarNextAnnotations")).Disabled(p.busy || !view.more).Clicked() {
				s.loadScholarAnnotations(p, view, view.start+scholar.ZoteroPageSize)
			}
		})
	}
}

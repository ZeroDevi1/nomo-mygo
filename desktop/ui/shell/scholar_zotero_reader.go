package shell

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/scholar"
)

func (s *Shell) scholarReaderPane(c *ui.Context, p *scholarState, width float32, singlePane bool) {
	l := &p.literature
	zoteroCatalog := p.references.catalog && l.mode == 1
	e, note, form, annotations := l.selected, l.readerNote(), l.readerForm(), l.annotations
	background := s.ch.content
	if zoteroCatalog || e.RemoteID != "" || annotations != nil {
		background = s.ch.sidebar
	}
	closePane := func() {
		label, icon := s.tr("scholarCloseDrawer"), "x"
		if singlePane {
			label, icon = s.scholarInspectorBackLabel(p), "chevron-left"
		}
		if (singlePane || !zoteroCatalog) && s.scholarCompactIcon(c.Key("close"), icon, label, false).Clicked() {
			s.closeScholarInspector(p)
		}
	}
	ui.Row(c).Width(width).FillHeight().MinHeight(0).Clip().Background(background).Children(func() {
		ui.Column(c.Key("content")).Grow(1).MinWidth(0).FillHeight().MinHeight(0).Children(func() {
			if form != nil && annotations == nil {
				subtitle := e.Title
				if form.label == "scholarZoteroMoveToTrash" {
					subtitle = ""
				}
				s.scholarReaderHeader(c.Key("title"), s.tr(form.label), subtitle, nil, closePane)
				s.scholarZoteroForm(c.Key(form), p, form, p.err)
				return
			}
			title, subtitle := e.Title, ""
			if t := p.references.focused; t != nil && !p.references.catalog && note == nil {
				title, subtitle = s.tr("scholarReferenceDetails"), t.title
			}
			var back func()
			switch {
			case annotations != nil:
				title, subtitle = s.tr("scholarReadAnnotations"), annotations.title
				back = func() {
					current := annotations.zotero.SameSource(l.zotero)
					if s.scholarCompactIcon(c.Key("back"), "arrow-left", s.tr("scholarAnnotationEntry"), false).Disabled(p.busy || !current).Clicked() {
						s.returnScholarAnnotationEntry(p, annotations)
					}
				}
			case note != nil && !l.detail.Deleted:
				title, _ = scholarNoteContent(s, *note)
				if !e.ZoteroNote {
					subtitle = e.Title
					back = func() {
						if s.scholarCompactIcon(c.Key("back"), "arrow-left", s.tr("scholarParentItem"), false).Clicked() {
							l.selectedNote, l.readerActive, l.readerJump = "", 0, 1
						}
					}
				}
			}
			if title == "" {
				title = s.tr("scholarReferenceDetails")
			}
			s.scholarReaderHeader(c.Key("title"), title, subtitle, back, func() {
				if annotations != nil {
					current := annotations.zotero.SameSource(l.zotero)
					canRefresh := !p.busy && (current || !annotations.loaded)
					if s.scholarCompactIcon(c.Key("refresh"), "refresh-cw", s.tr("refresh"), false).Disabled(!canRefresh).Clicked() {
						s.loadScholarAnnotations(p, annotations, annotations.start)
					}
				} else {
					if note == nil && !e.ZoteroNote && !l.detail.Deleted && e.Title != "" {
						glyph, label := s.scholarReferenceOpenLabel(p, e)
						if s.scholarCompactIcon(c.Key("open"), glyph, label, false).Disabled(p.busy).Clicked() {
							s.openScholarReference(p, e)
						}
					}
					s.scholarZoteroReaderActions(c.Key("zotero"), p, e, note)
				}
				closePane()
			})
			if p.err != "" {
				s.scholarReaderNotice(c.Key("error"), p.err, nil)
			}
			scroll := ui.Scroll(c.Key("details")).Grow(1).FillWidth().MinHeight(0)
			if annotations != nil {
				scroll.TrackScroll(&annotations.scroll)
			} else if t := p.references.focused; t != nil && !p.references.catalog {
				scroll.TrackScroll(&t.detailScroll)
			}
			scroll.Children(func() {
				ui.Column(c).FillWidth().Padding(0, scholarReaderInset).FontSize(12).LineHeight(1.5).Children(func() {
					s.scholarReaderView(c, p)
				})
			})
		})
		if zoteroCatalog && annotations == nil && form == nil && !l.detail.Deleted && e.Title != "" && (!e.ZoteroNote || note != nil) {
			s.scholarReaderSectionRail(c.Key("sections"), p, note != nil)
		}
	})
}

func (s *Shell) scholarReaderSectionRail(c *ui.Context, p *scholarState, note bool) {
	l := &p.literature
	ui.Column(c).Width(30).Shrink(0).FillHeight().Padding(6, 1).Gap(3).AlignItems(ui.Center).
		Border(1, s.ch.separator).BorderWidth(0, 0, 0, 1).Children(func() {
		for _, item := range []struct {
			section      uint
			glyph, label string
		}{
			{0, "file-pen-line", "scholarItemInfo"}, {1, "notebook-tabs", "scholarItemAbstract"},
			{2, "paperclip", "scholarItemAttachments"}, {3, "sticky-note", "scholarItemNotes"},
			{7, "library-big", "scholarLibrariesCollections"}, {5, "tag", "scholarItemTags"},
			{6, "link", "scholarItemRelated"}, {4, "quote", "scholarCitationsWriting"},
		} {
			if note && item.section != 5 && item.section != 6 || item.section == 7 && l.selected.RemoteID == "" {
				continue
			}
			b := s.scholarCompactIcon(c.Key(int(item.section)), item.glyph, s.tr(item.label), l.readerActive == item.section).
				Disabled(l.selected.RemoteID == "" || p.busy)
			if b.Clicked() {
				l.readerActive, l.readerJump = item.section, int(item.section)+1
				l.readerCollapsed &^= 1 << item.section
			}
		}
	})
}

func (s *Shell) scholarZoteroFields(c *ui.Context, p *scholarState) {
	l := &p.literature
	s.scholarReaderField(c.Key("type"), s.tr("scholarItemType"), l.detail.ItemTypeLabel)
	for _, field := range l.detail.Fields {
		ui.Row(c.Key(field.Name)).FillWidth().MinHeight(19).Gap(scholarReaderFieldGap).AlignItems(ui.Start).Children(func() {
			ui.Text(c, field.Label).Width(scholarReaderLabelWidth).FontSize(12).TextColor(s.ch.muted).TextAlign(ui.End).Wrap()
			ui.Text(c, field.Value).Grow(1).MinWidth(0).FontSize(12).Wrap().Selectable()
		})
		if field.Name == "title" {
			count := len(l.detail.Creators)
			if !l.readerAllCreators {
				count = min(count, 5)
			}
			for i, creator := range l.detail.Creators[:count] {
				name := creator.Name
				if name == "" {
					name = strings.TrimSpace(creator.FirstName + " " + creator.LastName)
				}
				s.scholarReaderField(c.Key(i), s.tr("scholarItemCreator"), name)
			}
			if count < len(l.detail.Creators) {
				if s.scholarReaderCommand(c.Key("all-creators"), "chevron-down", s.tr("scholarAllCreators"), false).
					Margin(0, 0, 0, scholarReaderLabelWidth+scholarReaderFieldGap).Clicked() {
					l.readerAllCreators = true
				}
			}
		}
	}
	for _, stamp := range []struct{ label, value string }{{"scholarDateAdded", l.detail.DateAdded}, {"scholarDateModified", l.detail.DateModified}} {
		value := stamp.value
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			value = parsed.Local().Format("2006/01/02 15:04:05")
		}
		s.scholarReaderField(c, s.tr(stamp.label), value)
	}
}

func (s *Shell) scholarReaderCollections(c *ui.Context, p *scholarState) {
	l := &p.literature
	s.scholarReaderSection(c, p, 7, "library-big", s.tr("scholarLibrariesCollections"), "", func(c *ui.Context) {
		name := s.tr("scholarPersonalLibrary")
		for _, library := range l.libraries {
			if library.ID == l.selected.Library && library.ID != 0 {
				name = library.Name
			}
		}
		if s.scholarReaderLink(c.Key("library"), "landmark", name).Disabled(p.busy).Clicked() {
			s.scholarReaderOpenCollection(p, "")
		}
		shownParents := make(map[string]bool)
		for _, key := range l.detail.Collections[:min(len(l.detail.Collections), 32)] {
			title, parent := key, ""
			for _, collection := range l.detail.CollectionInfo {
				if collection.Key == key {
					title, parent = collection.Name, collection.Parent
					break
				}
			}
			parentTitle := ""
			if l.selected.Library == l.zotero.Group {
				for _, branch := range l.tree {
					for _, collection := range branch.page.Collections {
						if collection.Key == key {
							title = collection.Name
						}
						if collection.Key == parent {
							parentTitle = collection.Name
						}
					}
				}
			}
			if parentTitle != "" && !shownParents[parent] {
				shownParents[parent] = true
				if s.scholarReaderLink(c.Key("parent-"+parent), "folder", parentTitle).Padding(2, 3, 2, 18).Disabled(p.busy).Clicked() {
					s.scholarReaderOpenCollection(p, parent)
				}
			}
			indent := float32(18)
			if parentTitle != "" {
				indent = 34
			}
			if s.scholarReaderLink(c.Key(key), "folder", title).Padding(2, 3, 2, indent).Disabled(p.busy).Clicked() {
				s.scholarReaderOpenCollection(p, key)
			}
		}
	})
}

func (s *Shell) scholarReaderOpenCollection(p *scholarState, key string) {
	l := &p.literature
	if l.zotero.Group != l.selected.Library {
		l.tree, l.treeTried = nil, false
		l.tagPicker = scholarTagPicker{}
	}
	l.zotero.Group, l.scope, l.tag = l.selected.Library, scholar.ZoteroAll, ""
	l.mode, l.recentOpen, l.fulltext = 1, false, false
	s.showScholarCatalog(p)
	s.loadZotero(p, key, "", 0)
}

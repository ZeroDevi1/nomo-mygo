package shell

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/scholar"
)

func (s *Shell) scholarZoteroInspector(c *ui.Context, p *scholarState, width float32, singlePane bool) {
	l := &p.literature
	ui.Row(c).Width(width).FillHeight().MinHeight(0).Clip().Background(s.ch.sidebar).Children(func() {
		ui.Column(c.Key("content")).Grow(1).MinWidth(0).FillHeight().MinHeight(0).Children(func() {
			note, form, annotations := l.readerNote(), l.readerForm(), l.annotations
			if annotations != nil {
				note, form = nil, nil
			}
			ui.Column(c.Key("title")).FillWidth().Padding(10, 10, 7, 10).Gap(5).Children(func() {
				title := l.selected.Title
				if note != nil {
					title, _ = scholarNoteContent(s, *note)
				}
				if annotations != nil {
					title = annotations.title
				}
				if form != nil {
					title = s.tr(form.label)
				}
				if title == "" {
					title = s.tr("scholarReferenceDetails")
				}
				ui.Row(c).FillWidth().Gap(4).AlignItems(ui.Start).Children(func() {
					ui.Text(c, title).FontSize(13).Bold().LineHeight(1.3).Wrap().MaxLines(4).Grow(1).MinWidth(0)
					if singlePane && s.scholarCompactIcon(c.Key("close"), "x", s.tr("scholarCloseDrawer"), false).Clicked() {
						s.closeScholarInspector(p)
					}
				})
				if note != nil && !l.selected.ZoteroNote || form != nil {
					ui.Text(c, l.selected.Title).FontSize(11).LineHeight(1.4).TextColor(s.ch.muted).Wrap().MaxLines(2)
				}
				if form != nil {
					return
				}
				ui.Row(c.Key("actions")).FillWidth().Gap(3).Children(func() {
					if annotations != nil {
						current := annotations.zotero.ServerID == l.zotero.ServerID
						if s.scholarCompactIcon(c.Key("back"), "arrow-left", s.tr("scholarAnnotationEntry"), false).Disabled(p.busy || !current).Clicked() {
							s.returnScholarAnnotationEntry(p, annotations)
						}
						if s.scholarCompactIcon(c.Key("refresh"), "refresh-cw", s.tr("refresh"), false).Disabled(p.busy || !current).Clicked() {
							s.loadScholarAnnotations(p, annotations, annotations.start)
						}
						return
					}
					if note != nil && !l.selected.ZoteroNote {
						if s.scholarCompactIcon(c.Key("back"), "arrow-left", s.tr("scholarParentItem"), false).Clicked() {
							l.selectedNote, l.readerActive, l.readerJump = "", 0, 1
						}
					} else if !l.selected.ZoteroNote && !l.detail.Deleted && l.selected.Title != "" {
						glyph, label := s.scholarReferenceOpenLabel(p, l.selected)
						if s.scholarCompactIcon(c.Key("pdf"), glyph, label, false).Disabled(p.busy).Clicked() {
							s.openScholarReference(p, l.selected)
						}
					}
					s.scholarZoteroReaderActions(c.Key("zotero"), p, l.selected, note)
				})
			})
			if note != nil || form != nil || annotations != nil {
				ui.Box(c).FillWidth().Height(1).Background(s.ch.separator)
			}
			ui.Scroll(c.Key("details")).Grow(1).FillWidth().MinHeight(0).Children(func() {
				ui.Column(c).FillWidth().Padding(0, 8).Gap(0).FontSize(12).LineHeight(1.5).Children(func() { s.scholarReaderView(c, p) })
			})
		})
		ui.Box(c).Width(1).FillHeight().Background(s.ch.separator)
		ui.Column(c.Key("sections")).Width(30).FillHeight().Padding(6, 2).Gap(3).AlignItems(ui.Center).Children(func() {
			for _, item := range []struct {
				section      uint
				glyph, label string
			}{
				{0, "file-pen-line", "scholarItemInfo"}, {1, "notebook-tabs", "scholarItemAbstract"},
				{2, "paperclip", "scholarItemAttachments"}, {3, "sticky-note", "scholarItemNotes"},
				{7, "library-big", "scholarLibrariesCollections"}, {5, "tag", "scholarItemTags"},
				{6, "link", "scholarItemRelated"}, {4, "quote", "scholarCitationsWriting"},
			} {
				if l.readerForm() != nil || l.annotations != nil || l.detail.Deleted || l.selected.Title == "" {
					continue
				}
				if l.readerNote() != nil && item.section != 5 && item.section != 6 {
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
	})
}

func (s *Shell) scholarZoteroFields(c *ui.Context, p *scholarState) {
	l := &p.literature
	s.scholarReaderField(c.Key("type"), s.tr("scholarItemType"), l.detail.ItemTypeLabel)
	for _, field := range l.detail.Fields {
		ui.Row(c.Key(field.Name)).FillWidth().MinHeight(19).Gap(9).AlignItems(ui.Start).Children(func() {
			ui.Text(c, field.Label).Width(76).FontSize(12).TextColor(s.ch.muted).TextAlign(ui.End).Wrap()
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
				if ui.ButtonBase(c.Key("all-creators")).FillWidth().Padding(2).Label(s.tr("scholarAllCreators")).Children(func() {
					ui.Text(c, s.tr("scholarAllCreators")).FontSize(11).TextColor(s.ch.accent)
				}).Clicked() {
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
	s.loadZotero(p, false, key, "", 0)
}

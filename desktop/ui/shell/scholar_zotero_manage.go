package shell

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/scholar"
)

type scholarZoteroForm struct {
	z                         scholar.Zotero
	catalog                   bool
	label, key, parent, token string
	item                      *scholar.ZoteroItemDraft
	itemTypes                 []scholar.ZoteroItemType
	itemType                  int
	collection                scholar.ZoteroCollectionDraft
	note, tag                 string
}

func (s *Shell) scholarZoteroCatalogActions(c *ui.Context, p *scholarState) {
	l := &p.literature
	s.scholarMenuButton(c, "file-plus", s.tr("scholarZoteroManage"), false).Disabled(p.busy).Menu(func(m *ui.Menu) {
		canWrite := l.zotero.ServerID != ""
		if m.Item(s.tr("scholarZoteroNewItem")).Disabled(!canWrite || p.busy).Chosen() {
			z, collection := l.zotero, l.collection
			s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
				ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
				defer cancel()
				types, err := z.ItemTypes(ctx)
				return func() {
					if err == nil && len(types) > 0 {
						l.zoteroForm = &scholarZoteroForm{z: z, catalog: true, parent: collection, token: rand.Text(), itemTypes: types, label: "scholarZoteroNewItem"}
					}
				}, err
			})
		}
		if m.Item(s.tr("scholarZoteroNewCollection")).Disabled(!canWrite || p.busy).Chosen() {
			s.startScholarZoteroCollection(p, scholar.Collection{}, "", "scholarZoteroNewCollection")
		}
		m.Separator()
		if m.Item(s.tr("refresh")).Disabled(p.busy).Chosen() {
			l.tree, l.treeTried = nil, false
			l.libraries = nil
			l.tagPicker = scholarTagPicker{}
			s.loadZotero(p, false, l.collection, l.sentQuery, 0)
		}
		if m.Item(s.tr("scholarConnectZotero")).Disabled(p.busy).Chosen() {
			l.zoteroForm = nil
			l.tree, l.treeTried, l.libraries = nil, false, nil
			l.tagPicker = scholarTagPicker{}
			l.tag, l.scope, l.recentOpen, l.fulltext = "", scholar.ZoteroAll, false, false
			s.loadZotero(p, true, "", "", 0)
		}
	})
}

func (s *Shell) scholarZoteroCollectionActions(m *ui.Menu, p *scholarState, collection scholar.Collection) {
	l := &p.literature
	if l.zotero.ServerID == "" || p.busy {
		return
	}
	if m.Item(s.tr("rename")).Chosen() {
		s.startScholarZoteroCollection(p, collection, collection.Parent, "rename")
	}
	if m.Item(s.tr("scholarZoteroNewSubcollection")).Chosen() {
		s.startScholarZoteroCollection(p, scholar.Collection{}, collection.Key, "scholarZoteroNewSubcollection")
	}
	e := l.selected
	if e.Source != "Zotero" || e.RemoteID == "" || e.Library != l.zotero.Group ||
		l.selectedNote != "" || l.detail.ItemType == "" || l.detail.Deleted {
		return
	}
	m.Separator()
	hasItem := slices.Contains(l.detail.Collections, collection.Key)
	label := "scholarZoteroAddToCollection"
	if hasItem {
		label = "scholarZoteroRemoveFromCollection"
	}
	if m.Item(s.tr(label)).Chosen() {
		s.revealScholarCatalog(p)
		z := l.zotero
		s.scholarZoteroWrite(p, func(ctx context.Context) error {
			return z.SetItemCollection(ctx, e.RemoteID, collection.Key, !hasItem)
		}, func() {
			s.scholarZoteroItemChanged(p, z, e.RemoteID, false)
		})
	}
}

func (s *Shell) startScholarZoteroCollection(p *scholarState, collection scholar.Collection, parent, label string) {
	s.revealScholarCatalog(p)
	l := &p.literature
	z := l.zotero
	form := &scholarZoteroForm{z: z, catalog: true, key: collection.Key, parent: parent, token: rand.Text(), label: label}
	if collection.Key == "" {
		l.zoteroForm = form
		return
	}
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		draft, err := z.CollectionDraft(ctx, collection.Key)
		return func() {
			if err == nil {
				form.collection = draft
				l.zoteroForm = form
			}
		}, err
	})
}

func (s *Shell) scholarZoteroReaderActions(c *ui.Context, p *scholarState, entry scholar.Entry, note *scholar.ZoteroNote) {
	l := &p.literature
	if entry.RemoteID == "" {
		return
	}
	z := l.zotero
	z.Group = entry.Library
	key, title := entry.RemoteID, entry.Title
	if note != nil {
		key, title = note.Key, scholarNoteTitle(s, *note)
	}
	link := z.ItemLink(key)
	s.scholarCompactIcon(c, "ellipsis", s.tr("scholarZoteroActions"), false).Menu(func(m *ui.Menu) {
		if address := scholarEntryWebURL(entry); note == nil && address != "" {
			if m.Item(s.tr("scholarOpenWeb")).Disabled(p.busy).Chosen() {
				s.openScholarWeb(p, entry, address)
			}
		}
		if m.Item(s.tr("scholarShowInZotero")).Disabled(link == "").Chosen() {
			c.Services().OpenURL(link)
		}
		if m.Item(s.tr("scholarCopyZoteroLink")).Disabled(link == "").Chosen() {
			c.Services().WriteClipboard(link)
		}
		if m.Item(s.tr("refresh")).Disabled(p.busy || !l.connected).Chosen() {
			s.loadScholarDetail(p, entry, l.attachmentPage)
		}
		if !l.connected || z.ServerID == "" || entry.Source != "Zotero" || l.detail.ItemType == "" {
			return
		}
		m.Separator()
		switch {
		case l.detail.Deleted:
			if m.Item(s.tr("scholarZoteroRestore")).Disabled(p.busy).Chosen() {
				s.scholarZoteroTrash(p, z, key, false)
			}
			return
		case note == nil && l.detail.ItemType != "note" && l.detail.ItemType != "attachment":
			if m.Item(s.tr("scholarZoteroEditItem")).Disabled(p.busy).Chosen() {
				form := &scholarZoteroForm{z: z, key: key, label: "scholarZoteroEditItem"}
				s.loadScholarZoteroItemDraft(p, form, "")
			}
			if m.Item(s.tr("scholarZoteroNewNote")).Disabled(p.busy).Chosen() {
				l.zoteroForm = &scholarZoteroForm{z: z, key: key, token: rand.Text(), label: "scholarZoteroNewNote"}
			}
			m.Separator()
		}
		if m.Item(s.tr("scholarZoteroMoveToTrash")).Disabled(p.busy).Chosen() {
			l.zoteroForm = &scholarZoteroForm{z: z, key: key, note: title, label: "scholarZoteroMoveToTrash"}
		}
	})
}

func (s *Shell) loadScholarZoteroItemDraft(p *scholarState, form *scholarZoteroForm, kind string) {
	z, key := form.z, form.key
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		draft, err := z.ItemDraft(ctx, key, kind)
		return func() {
			if err == nil {
				form.item = draft
				p.literature.zoteroForm = form
			}
		}, err
	})
}

const scholarZoteroFormWidth = 520

func (s *Shell) scholarZoteroForm(c *ui.Context, p *scholarState, form *scholarZoteroForm, message string) {
	ui.Column(c).FillWidth().Grow(1).MinHeight(0).AlignItems(ui.Center).Children(func() {
		if message != "" {
			s.scholarReaderNotice(c.Key("error"), message, nil)
		}
		var save func()
		label := s.tr("save")
		ui.Scroll(c.Key("fields")).FillWidth().Grow(1).MinHeight(0).AlignItems(ui.Center).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(scholarZoteroFormWidth).MinWidth(0).
				Padding(10, scholarReaderInset).Gap(8).FontSize(12).Children(func() {
				switch {
				case form.item != nil:
					s.scholarZoteroItemFields(c, p, form)
					save = func() {
						draft := *form.item
						draft.Fields = slices.Clone(draft.Fields)
						draft.Creators = slices.Clone(draft.Creators)
						draft.Tags = slices.Clone(draft.Tags)
						s.scholarZoteroWrite(p, func(ctx context.Context) error {
							return draft.Save(ctx, form.parent, form.token)
						}, func() { s.scholarZoteroItemChanged(p, form.z, form.key, false) })
					}
				case len(form.itemTypes) > 0:
					labels := make([]string, len(form.itemTypes))
					for i, kind := range form.itemTypes {
						labels[i] = kind.Localized
					}
					s.scholarZoteroFieldLabel(c, s.tr("scholarItemType"))
					s.scholarReaderChoice(c, "file-text", &form.itemType, labels, p.busy)
					label = s.tr("scholarZoteroNext")
					save = func() { s.loadScholarZoteroItemDraft(p, form, form.itemTypes[form.itemType].ItemType) }
				case form.label == "scholarZoteroNewNote":
					fieldLabel := s.tr("scholarZoteroNoteText")
					s.scholarZoteroFieldLabel(c, fieldLabel)
					s.scholarReaderTextArea(c.Key("note"), &form.note, 125).Label(fieldLabel).Disabled(p.busy)
					if strings.TrimSpace(form.note) != "" {
						body := form.note
						save = func() {
							s.scholarZoteroWrite(p, func(ctx context.Context) error {
								return form.z.CreateNote(ctx, form.key, body, form.token)
							}, func() { s.scholarZoteroItemChanged(p, form.z, form.key, false) })
						}
					}
				case form.label == "scholarZoteroMoveToTrash":
					scholarReaderText(c, form.note)
					label = s.tr("scholarZoteroMoveToTrash")
					save = func() { s.scholarZoteroTrash(p, form.z, form.key, true) }
				default:
					fieldLabel := s.tr("scholarZoteroCollectionName")
					s.scholarZoteroFieldLabel(c, fieldLabel)
					s.scholarReaderInput(c.Key("name"), &form.collection.Name).Label(fieldLabel).Disabled(p.busy)
					if strings.TrimSpace(form.collection.Name) != "" {
						draft := form.collection
						save = func() {
							s.scholarZoteroWrite(p, func(ctx context.Context) error {
								return form.z.SaveCollection(ctx, form.key, form.parent, draft, form.token)
							}, func() { s.loadScholarCollectionBranch(p, form.parent, 0) })
						}
					}
				}
			})
		})
		s.scholarReaderDivider(c)
		ui.Row(c.Key("form-actions")).FillWidth().MaxWidth(scholarZoteroFormWidth).Shrink(0).
			Padding(6, scholarReaderInset).Gap(6).Justify(ui.End).Children(func() {
			if ui.Button(c.Key("cancel"), s.tr("cancel")).Height(26).FontSize(12).Padding(3, 10).Radius(4).Disabled(p.busy).Clicked() {
				p.literature.zoteroForm, p.err = nil, ""
			}
			if ui.PrimaryButton(c.Key("apply"), label).Height(26).FontSize(12).Padding(3, 10).Radius(4).Disabled(p.busy || save == nil).Clicked() && save != nil {
				save()
			}
		})
	})
}

func (s *Shell) scholarZoteroFieldLabel(c *ui.Context, label string) {
	ui.Text(c, label).FontSize(11).TextColor(s.ch.muted).Wrap()
}

func (s *Shell) scholarZoteroItemFields(c *ui.Context, p *scholarState, form *scholarZoteroForm) {
	draft := form.item
	scholarReaderText(c, s.tr("scholarItemInfo")).Bold()
	for i := range draft.Fields {
		field := &draft.Fields[i]
		ui.Column(c.Key(i)).FillWidth().Gap(4).Children(func() {
			s.scholarZoteroFieldLabel(c, field.Label)
			if field.Multiline() {
				s.scholarReaderTextArea(c.Key("value"), &field.Value, 90).Label(field.Label).Disabled(p.busy)
			} else {
				s.scholarReaderInput(c.Key("value"), &field.Value).Label(field.Label).Disabled(p.busy)
			}
		})
	}
	labels := make([]string, 0, len(draft.CreatorTypes))
	roles := make([]string, 0, len(draft.CreatorTypes))
	for _, role := range draft.CreatorTypes {
		label := role.Localized
		if label == "" {
			switch role.CreatorType {
			case "author":
				label = s.tr("scholarCreatorAuthor")
			case "editor":
				label = s.tr("scholarCreatorEditor")
			case "contributor":
				label = s.tr("scholarCreatorContributor")
			case "reviewedAuthor":
				label = s.tr("scholarCreatorReviewedAuthor")
			case "translator":
				label = s.tr("scholarCreatorTranslator")
			default:
				label = role.CreatorType
			}
		}
		labels, roles = append(labels, label), append(roles, role.CreatorType)
	}
	if len(draft.CreatorTypes) > 0 || len(draft.Creators) > 0 {
		s.scholarReaderDivider(c)
		scholarReaderText(c, s.tr("scholarItemCreator")).Bold()
	}
	for i := range draft.Creators {
		creator := &draft.Creators[i]
		ck := c
		remove := false
		// Context.Key identifies only the next control, so each creator needs a parent.
		creatorRow := ui.Column(c.Key(fmt.Sprintf("creator-%d", i))).FillWidth().Gap(6)
		if i > 0 {
			creatorRow.Padding(8, 0, 0, 0).Border(1, s.ch.separator).BorderWidth(1, 0, 0, 0)
		}
		creatorRow.Children(func() {
			options := labels
			selected := slices.Index(roles, creator.Type)
			if selected < 0 {
				selected = len(roles)
				options = append(labels, creator.Type)
			}
			if s.scholarReaderChoice(ck.Key("role"), "user-round", &selected, options, p.busy) && selected < len(roles) {
				creator.Type = roles[selected]
			}
			ui.Checkbox(ck.Key("single"), &creator.SingleName, s.tr("scholarZoteroSingleName")).FontSize(12).Disabled(p.busy)
			if creator.SingleName {
				label := s.tr("scholarZoteroSingleName")
				s.scholarReaderInput(ck.Key("name"), &creator.Name).Label(label).Placeholder(label).Disabled(p.busy)
			} else {
				first, last := s.tr("scholarZoteroFirstName"), s.tr("scholarZoteroLastName")
				s.scholarReaderInput(ck.Key("first"), &creator.FirstName).Label(first).Placeholder(first).Disabled(p.busy)
				s.scholarReaderInput(ck.Key("last"), &creator.LastName).Label(last).Placeholder(last).Disabled(p.busy)
			}
			if s.scholarReaderCommand(ck.Key("remove"), "minus", s.tr("scholarZoteroRemoveCreator"), false).Disabled(p.busy).Clicked() {
				remove = true
			}
		})
		if remove {
			draft.Creators = slices.Delete(draft.Creators, i, i+1)
			break
		}
	}
	if len(draft.CreatorTypes) > 0 && s.scholarReaderCommand(c.Key("add-creator"), "plus", s.tr("scholarZoteroAddCreator"), false).Disabled(p.busy).Clicked() {
		draft.Creators = append(draft.Creators, scholar.ZoteroCreator{Type: draft.CreatorTypes[0].CreatorType})
	}
	s.scholarReaderDivider(c)
	scholarReaderText(c, s.tr("scholarItemTags")).Bold()
	for i, tag := range draft.Tags {
		ck := c.Key("tag").Key(tag)
		remove := false
		ui.Row(ck).Gap(4).AlignItems(ui.Center).Children(func() {
			ui.Text(ck, tag).FontSize(11).Grow(1).MinWidth(0).Wrap()
			remove = s.scholarCompactIcon(ck.Key("remove"), "x", s.tr("scholarZoteroRemoveTag"), false).Disabled(p.busy).Clicked()
		})
		if remove {
			draft.Tags = slices.Delete(draft.Tags, i, i+1)
			break
		}
	}
	tagLabel := s.tr("scholarZoteroAddTag")
	in := s.scholarReaderInput(c.Key("tag-input"), &form.tag).Label(tagLabel).Placeholder(tagLabel).Disabled(p.busy)
	tag := strings.TrimSpace(form.tag)
	if !p.busy && tag != "" && !slices.Contains(draft.Tags, tag) &&
		(s.scholarReaderCommand(c.Key("add-tag"), "plus", tagLabel, false).Clicked() || in.Submitted()) {
		draft.Tags = append(draft.Tags, tag)
		form.tag = ""
	}
}

func (s *Shell) scholarZoteroWrite(p *scholarState, write func(context.Context) error, applied func()) {
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		err := write(ctx)
		return func() {
			if err == nil {
				p.literature.zoteroForm = nil
				s.status = s.tr("scholarSavedZotero")
				applied()
			}
		}, err
	})
}

func (s *Shell) scholarZoteroTrash(p *scholarState, z scholar.Zotero, key string, deleted bool) {
	s.scholarZoteroWrite(p, func(ctx context.Context) error {
		return z.SetItemTrashed(ctx, key, deleted)
	}, func() { s.scholarZoteroItemChanged(p, z, key, true) })
}

func (s *Shell) scholarZoteroItemChanged(p *scholarState, z scholar.Zotero, key string, trashChanged bool) {
	l := &p.literature
	if l.zotero == z {
		l.loaded, l.fulltext = false, false
		l.fulltextEntries = nil
		l.tagPicker = scholarTagPicker{}
	}
	if key == "" || l.selected.RemoteID == "" || l.selected.Library != z.Group {
		return
	}
	if trashChanged && l.selected.RemoteID == key {
		l.recent = slices.DeleteFunc(l.recent, func(e scholar.Entry) bool { return e.RemoteID == key && e.Library == z.Group })
		l.selected, l.detail = scholar.Entry{}, scholar.ZoteroDetail{}
		l.selectedNote = ""
		l.annotations = nil
		return
	}
	if l.selectedNote == key && trashChanged {
		l.selectedNote = ""
	}
	s.loadScholarDetail(p, l.selected, 0)
}

func (s *Shell) startScholarZoteroNote(p *scholarState) {
	l := &p.literature
	z := l.zotero
	z.Group = l.selected.Library
	l.zoteroForm = &scholarZoteroForm{z: z, key: l.selected.RemoteID, token: rand.Text(), label: "scholarZoteroNewNote"}
	s.showScholarInspector(p, scholarReader)
}

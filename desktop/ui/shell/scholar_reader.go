package shell

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/scholar"
)

func (l *scholarLiteratureState) readerNote() *scholar.ZoteroNote {
	for i := range l.detail.Notes {
		if l.selected.ZoteroNote || l.detail.Notes[i].Key == l.selectedNote {
			return &l.detail.Notes[i]
		}
	}
	return nil
}

func (l *scholarLiteratureState) readerForm() *scholarZoteroForm {
	f := l.zoteroForm
	if f != nil && !f.catalog && f.z.ServerID == l.zotero.ServerID && f.z.Group == l.selected.Library &&
		(f.key == l.selected.RemoteID || f.key == l.selectedNote) {
		return f
	}
	return nil
}

func (l *scholarLiteratureState) catalogForm() *scholarZoteroForm {
	if f := l.zoteroForm; l.mode == 1 && f != nil && f.catalog && f.z == l.zotero {
		return f
	}
	return nil
}

func (s *Shell) scholarReaderChoice(c *ui.Context, glyph string, selected *int, labels []string, disabled bool) (changed bool) {
	if len(labels) == 0 {
		return false
	}
	if *selected < 0 || *selected >= len(labels) {
		*selected = 0
	}
	s.scholarReaderCommand(c, glyph, labels[*selected], true).Padding(0, 6).Radius(4).
		Background(s.ch.content).Border(1, s.ch.separator).Disabled(disabled).Menu(func(m *ui.Menu) {
		for i, label := range labels {
			if m.Item(fmt.Sprintf("%d. %s", i+1, label)).Radio(*selected == i).Chosen() {
				changed = *selected != i
				*selected = i
			}
		}
	})
	return changed
}

func (s *Shell) scholarReaderInput(c *ui.Context, value *string) ui.Element {
	return ui.TextInput(c, value).FillWidth().Height(26).FontSize(12).Padding(4, 6).Radius(4)
}

func (s *Shell) scholarReaderTextArea(c *ui.Context, value *string, height float32) ui.Element {
	return ui.TextArea(c, value).FillWidth().Height(height).FontSize(12).LineHeight(1.5).Padding(6).Radius(4)
}

func (s *Shell) scholarReaderCommand(c *ui.Context, glyph, label string, menu bool) ui.Element {
	b := ui.ButtonBase(c).FillWidth().MinWidth(0).Height(26).Padding(0, 3).Gap(7).Radius(3).
		Justify(ui.Start).AlignItems(ui.Center).FocusRing(true).Label(label).Tooltip(label).TextColor(s.ch.text)
	if b.Hovered() {
		b.Background(s.ch.hover)
	}
	b.Children(func() {
		ui.Icon(c, s.icon(glyph)).FontSize(13).TextColor(s.ch.muted).Shrink(0)
		ui.Text(c, label).FontSize(12).SingleLine().Grow(1).MinWidth(0).TextColor(s.ch.text)
		if menu {
			ui.Icon(c, s.icon("chevron-down")).FontSize(11).TextColor(s.ch.muted).Shrink(0)
		}
	})
	return b
}

func (s *Shell) scholarCompactIcon(c *ui.Context, glyph, label string, selected bool) ui.Element {
	b := ui.ButtonBase(c).Size(26, 26).Padding(5).Radius(4).Justify(ui.Center).AlignItems(ui.Center).
		FocusRing(true).Label(label).Tooltip(label)
	ink := s.ch.muted
	if selected {
		ink = s.ch.accent
		b.Background(s.ch.hover)
	} else if b.Hovered() {
		b.Background(s.ch.hover)
	}
	b.Children(func() { ui.Icon(c, s.icon(glyph)).FontSize(14).TextColor(ink) })
	return b
}

// Section preferences belong to the project view and keep no document or DOM copies.
func (s *Shell) scholarReaderSection(c *ui.Context, p *scholarState, section uint, glyph, label, count string, body func(*ui.Context)) {
	mask := uint16(1 << section)
	open := p.literature.readerCollapsed&mask == 0
	column := ui.Column(c.Key(int(section))).FillWidth()
	if section != 0 && p.literature.readerJump == int(section)+1 {
		column.ScrollIntoView()
		p.literature.readerJump = 0
	}
	column.Children(func() {
		ui.Box(c).FillWidth().Height(1).Background(s.ch.separator)
		header := ui.ButtonBase(c.Key("header")).FillWidth().Height(33).Padding(0, 2).Gap(5).
			Justify(ui.Start).AlignItems(ui.Center).FocusRing(true).Label(label)
		if section == 0 && p.literature.readerJump == 1 {
			header.ScrollIntoView()
			p.literature.readerJump = 0
		}
		if header.Hovered() {
			header.Background(s.ch.hover)
		}
		header.Children(func() {
			ui.Icon(c, s.icon(glyph)).FontSize(14).TextColor(s.scholarZoteroIconColor(c, glyph))
			title := label
			if count != "" {
				title = count + " " + title
			}
			ui.Text(c, title).FontSize(12).MinWidth(0).SingleLine().TextColor(s.ch.muted)
			ui.Spacer(c)
			chevron := "chevron-down"
			if open {
				chevron = "chevron-up"
			}
			ui.Icon(c, s.icon(chevron)).FontSize(12).TextColor(s.ch.muted)
		})
		if header.Clicked() {
			p.literature.readerCollapsed ^= mask
			open = !open
		}
		if open {
			ui.Column(c.Key("body")).FillWidth().Padding(0, 6, 8, 6).Gap(5).Children(func() { body(c) })
		}
	})
}

func (s *Shell) scholarReaderField(c *ui.Context, label, value string) {
	if value == "" {
		return
	}
	ui.Row(c).FillWidth().Gap(9).AlignItems(ui.Start).Children(func() {
		ui.Text(c, label).Width(76).FontSize(12).TextColor(s.ch.muted).TextAlign(ui.End).Wrap()
		ui.Text(c, value).Grow(1).MinWidth(0).FontSize(12).Wrap()
	})
}

func (s *Shell) scholarReaderNotice(c *ui.Context, message string, dismiss func()) {
	ui.Row(c).FillWidth().Margin(6, 8).Padding(5, 8).Gap(7).AlignItems(ui.Center).
		Radius(4).Background(s.ch.hover).Children(func() {
		ui.Icon(c, s.icon("info")).FontSize(14).TextColor(s.ch.muted).Shrink(0)
		ui.Text(c, message).FontSize(12).LineHeight(1.4).TextColor(s.ch.text).Wrap().Grow(1).MinWidth(0)
		if dismiss != nil && s.scholarCompactIcon(c.Key("dismiss"), "x", s.tr("scholarClose"), false).Clicked() {
			dismiss()
		}
	})
}

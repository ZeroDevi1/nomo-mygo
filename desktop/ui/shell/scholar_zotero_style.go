package shell

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/scholar"
)

func (s *Shell) scholarUsesZoteroSidebar(p *scholarState) bool {
	return !s.pf.windows && p.nav == scholarLiterature && p.literature.mode == 1
}

// The literature sidebar keeps its own width without changing the manuscript tree.
func (s *Shell) scholarSidebarWidth() *float32 {
	if p := s.scholarForTab(s.activeTab()); p != nil && s.scholarUsesZoteroSidebar(p) {
		if p.literature.catalogWidth == 0 {
			p.literature.catalogWidth = 200
		}
		return &p.literature.catalogWidth
	}
	return &s.sidebarW
}

func (s *Shell) scholarZoteroIconColor(c *ui.Context, glyph string) ui.Color {
	color := "#4778ff"
	switch glyph {
	case "clock":
		color = "#a27aee"
	case "paperclip", "file-text":
		color = "#38b96b"
	case "sticky-note", "notebook-pen":
		color = "#eaa000"
	case "folder-open", "tag", "link":
		color = "#ed8052"
	case "trash", "presentation":
		color = "#c67d52"
	case "library-big":
		color = "#50a9bf"
	}
	ink := ui.Hex(color)
	if c.Theme().Dark {
		return ui.RGBA(uint8(min(255, int(ink.R)+25)), uint8(min(255, int(ink.G)+25)), uint8(min(255, int(ink.B)+25)), 1)
	}
	return ink
}

func scholarCatalogGlyph(e scholar.Entry) string {
	switch e.ItemType {
	case "note":
		return "sticky-note"
	case "preprint", "presentation":
		return "presentation"
	case "computerProgram":
		return "code"
	case "book", "bookSection":
		return "book-open"
	case "journalArticle", "conferencePaper":
		return "file-pen-line"
	default:
		return "file-text"
	}
}

func (s *Shell) scholarReaderLink(c *ui.Context, glyph, title string) ui.Element {
	b := ui.ButtonBase(c).FillWidth().MinWidth(0).MinHeight(24).Padding(2, 3).Gap(6).
		Justify(ui.Start).AlignItems(ui.Center).TextColor(s.ch.text).Tooltip(title)
	if b.Hovered() {
		b.Background(s.ch.hover)
	}
	b.Children(func() {
		ui.Icon(c, s.icon(glyph)).FontSize(14).TextColor(s.scholarZoteroIconColor(c, glyph)).Shrink(0)
		ui.Text(c, title).FontSize(12).SingleLine().Grow(1).MinWidth(0).TextColor(s.ch.text)
	})
	return b
}

func (s *Shell) scholarCreatorSummary(e scholar.Entry) string {
	if e.CreatorSummary != "" {
		return e.CreatorSummary
	}
	first, rest, _ := strings.Cut(e.Author, " and ")
	if rest != "" {
		return first + " …"
	}
	return first
}

func scholarZoteroSelection(c *ui.Context) ui.Color {
	if c.Theme().Dark {
		return ui.Hex("#48484c")
	}
	return ui.Hex("#d8d8d8")
}

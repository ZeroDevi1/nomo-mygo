package shell

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/scholar"
	"nomo/desktop/typeset"
)

const (
	scholarPDFTabLimit        = 12
	scholarReferenceEdge      = 1600
	scholarReferenceFileLimit = 128 << 20
)

// A nil tab represents the active manuscript. Attachments never become editable
// documents or acquire entries in the regular save and workspace recovery paths.
type scholarReferenceTabs struct {
	tabs                 []*scholarPDFTab
	left, right, focused *scholarPDFTab
	catalog              bool
}

type scholarPDFTab struct {
	entry            scholar.Entry
	zotero           scholar.Zotero
	key, path, title string
	page, pages      int
	pageNavigation   scholarPageNavigation
	pinned           bool
	bitmap           *ui.Bitmap
	err              string
	cancel           context.CancelFunc
	generation       uint64
}

func (t *scholarPDFTab) releasePage() {
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	t.generation++
	t.bitmap = nil
}

func (r *scholarReferenceTabs) selectTab(t *scholarPDFTab) {
	r.catalog = false
	r.focused = t
	if r.right == nil || t != r.right {
		r.left = t
	}
}

func (r *scholarReferenceTabs) split(t *scholarPDFTab) {
	if r.left == t {
		r.left = r.right
		if r.left == t {
			r.left = nil
		}
	}
	r.right = t
	r.selectTab(t)
}

func (r *scholarReferenceTabs) close(t *scholarPDFTab) {
	t.releasePage()
	r.tabs = slices.DeleteFunc(r.tabs, func(candidate *scholarPDFTab) bool { return candidate == t })
	if r.left == t {
		r.left = nil
	}
	if r.right == t {
		r.right = nil
	}
	if r.focused == t {
		r.focused = r.left
	}
}

func (r *scholarReferenceTabs) visible(split, covered bool) (left, right *scholarPDFTab) {
	if !covered && !r.catalog {
		left = r.focused
		if split && r.right != nil {
			left, right = r.left, r.right
		}
	}
	for _, t := range r.tabs {
		if t != left && t != right {
			t.pageNavigation.open = false
			if t.bitmap != nil || t.cancel != nil {
				t.releasePage()
			}
		}
	}
	return left, right
}

func (s *Shell) showScholarCatalog(p *scholarState) {
	p.literature.annotations = nil
	s.closeScholarInspector(p)
	if p.nav != scholarLiterature {
		p.lastProjectNav = p.nav
	}
	p.references.catalog = true
	p.accountOpen, p.winRailPopup, p.winCompactProject = false, false, false
	p.nav, p.drawer, p.compactNav = scholarLiterature, scholarClosed, false
}

func (s *Shell) openScholarAttachment(p *scholarState, e scholar.Entry, key, path, title string, page int) {
	r := &p.references
	z := p.literature.zotero
	z.Group = e.Library
	for _, t := range r.tabs {
		if (key != "" && t.key == key && t.zotero.ServerID == z.ServerID && t.zotero.Group == z.Group) ||
			(key == "" && t.key == "" && samePath(t.path, path)) {
			s.selectScholarPDF(p, t)
			if key != "" {
				p.literature.rememberReading(t.entry)
			}
			if page > 0 {
				t.setPage(page)
			}
			return
		}
	}
	if len(r.tabs) >= scholarPDFTabLimit {
		p.err = s.tr("scholarPDFTabLimit")
		return
	}
	if title == "" {
		title = e.Title
	}
	e.Raw, e.Record = "", nil
	t := &scholarPDFTab{entry: e, zotero: z, key: key, path: path, title: title}
	t.setPage(max(page, 1))
	r.tabs = append(r.tabs, t)
	s.selectScholarPDF(p, t)
	if key != "" {
		p.literature.rememberReading(e)
	}
}

func (s *Shell) selectScholarPDF(p *scholarState, t *scholarPDFTab) {
	s.closeScholarInspector(p)
	if t == nil && p.nav == scholarLiterature {
		p.nav = p.lastProjectNav
	}
	p.references.selectTab(t)
	p.accountOpen, p.winRailPopup, p.winCompactProject = false, false, false
	p.drawer, p.compactNav = scholarClosed, false
	p.err = ""
}

func (t *scholarPDFTab) setPage(page int) {
	if t.pages > 0 {
		page = min(page, t.pages)
	}
	page = max(page, 1)
	if t.page != page || t.err != "" {
		t.releasePage()
		t.err = ""
	}
	t.page = page
}

func (s *Shell) loadScholarPDFPage(p *scholarState, t *scholarPDFTab) {
	if t.cancel != nil || t.bitmap != nil || t.err != "" {
		return
	}
	path, key, z, page := t.path, t.key, t.zotero, t.page
	ctx, cancel := context.WithTimeout(p.ctx, 20*time.Second)
	t.cancel = cancel
	t.generation++
	generation := t.generation
	go func() {
		defer cancel()
		pages, err := 0, error(nil)
		if path == "" {
			path, err = z.AttachmentPath(ctx, key)
		}
		if err == nil {
			info, statErr := os.Stat(path)
			if statErr != nil {
				err = statErr
			} else if !info.Mode().IsRegular() || info.Size() > scholarReferenceFileLimit {
				err = fmt.Errorf("PDF attachment exceeds the 128 MiB limit or is not a regular file")
			}
		}
		if err == nil {
			pages, err = typeset.PDFPages(ctx, path)
		}
		var bitmap *ui.Bitmap
		if err == nil {
			page = min(page, pages)
			if img, renderErr := typeset.ReadPDFPage(ctx, path, page, scholarReferenceEdge); renderErr != nil {
				err = renderErr
			} else {
				bitmap = ui.NewBitmap(img)
			}
		}
		s.post(func() {
			if s.scholar != p || p.ctx.Err() != nil || t.generation != generation {
				return
			}
			t.cancel = nil
			if err != nil {
				t.err = err.Error()
				return
			}
			t.path, t.page, t.pages, t.bitmap = path, page, pages, bitmap
		})
	}()
}

func (r *scholarReferenceTabs) hasTabs() bool {
	return r.catalog || len(r.tabs) > 0
}

func (s *Shell) scholarReferenceTabItems(c *ui.Context, p *scholarState, split bool) {
	r := &p.references
	if !r.hasTabs() {
		return
	}
	if s.scholarTabButton(c.Key("catalog"), "book-open", s.tr("scholarCatalog"), r.catalog).Clicked() {
		s.showScholarCatalog(p)
	}
	if len(r.tabs) == 0 {
		return
	}
	tabs := slices.Clone(r.tabs)
	slices.SortStableFunc(tabs, func(a, b *scholarPDFTab) int {
		switch {
		case a.pinned == b.pinned:
			return 0
		case a.pinned:
			return -1
		default:
			return 1
		}
	})
	label := s.tr("scholarMoreCommands")
	s.toolButton(c.Key("references"), "ellipsis", label, false).Label(label).Menu(func(m *ui.Menu) {
		for _, t := range tabs {
			if m.Item(t.title).Checked(r.focused == t && !r.catalog).Chosen() {
				s.selectScholarPDF(p, t)
			}
		}
		if r.focused != nil && !r.catalog {
			m.Separator()
			s.scholarReferenceActions(m, p, r.focused, split)
		}
	})
	ui.Row(c.Key("attachment-tabs")).Grow(1).MinWidth(0).Clip().Gap(4).AlignItems(ui.Center).Children(func() {
		for _, t := range tabs {
			ck := c.Key(t)
			glyph := "file-text"
			if t.pinned {
				glyph = "pin"
			}
			b := s.scholarTabButton(ck, glyph, t.title, !r.catalog && r.focused == t)
			if b.Clicked() {
				s.selectScholarPDF(p, t)
			}
			b.ContextMenu(func(m *ui.Menu) {
				s.scholarReferenceActions(m, p, t, split)
			})
		}
	})
}

func (s *Shell) scholarTabButton(c *ui.Context, glyph, title string, selected bool) ui.Element {
	b := ui.ButtonBase(c).Height(27).MaxWidth(190).Padding(0, 8).Gap(5).FocusRing(true).Selected(selected).Tooltip(title)
	if selected {
		b.Background(s.ch.tabActive)
	} else if b.Hovered() {
		b.Background(s.ch.hover)
	}
	b.Children(func() {
		ui.Icon(c, s.icon(glyph)).FontSize(13).TextColor(s.ch.muted)
		ui.Text(c, title).FontSize(12).SingleLine().MinWidth(0).Shrink(1)
	})
	return b
}

func (s *Shell) scholarReferenceActions(m *ui.Menu, p *scholarState, t *scholarPDFTab, split bool) {
	r := &p.references
	if m.Item(s.tr("scholarPinPDF")).Checked(t.pinned).Chosen() {
		t.pinned = !t.pinned
	}
	if r.right == t {
		if m.Item(s.tr("scholarEndPDFSplit")).Chosen() {
			r.right = nil
			r.selectTab(r.focused)
		}
	} else if m.Item(s.tr("scholarSplitPDF")).Disabled(!split).Chosen() {
		r.split(t)
		s.closeScholarInspector(p)
	}
	if m.Item(s.tr("scholarClose")).Chosen() {
		r.close(t)
		if r.focused == nil && r.right == nil {
			s.selectScholarPDF(p, nil)
		}
	}
}

func (s *Shell) scholarReferencePage(c *ui.Context, p *scholarState, t *scholarPDFTab, right bool) {
	c = c.Key(t)
	s.loadScholarPDFPage(p, t)
	ui.Column(c).Grow(1).Fill().MinWidth(0).Clip().Children(func() {
		ui.Row(c).Height(sidebarHd).FillWidth().Padding(0, 7).Gap(4).AlignItems(ui.Center).Background(s.ch.content).Children(func() {
			ui.Text(c, t.title).FontSize(12).SingleLine().Grow(1).MinWidth(0).Tooltip(t.title)
			if s.toolButton(c.Key("external"), "external-link", s.tr("scholarOpenExternalPDF"), false).Clicked() {
				if t.key != "" {
					c.Services().OpenURL(t.zotero.PDFLink(t.key, t.page))
				} else {
					c.Services().OpenURL((&url.URL{Scheme: "file", Path: t.path}).String())
				}
			}
			if t.key != "" && ui.Button(c.Key("annotations"), s.tr("scholarReadAnnotations")).Disabled(p.busy).Clicked() {
				s.openScholarAnnotations(p, t.entry, t.zotero, t.key, t.title)
			}
			if right && s.toolButton(c.Key("unsplit"), "x", s.tr("scholarEndPDFSplit"), false).Clicked() {
				p.references.right = nil
				p.references.selectTab(p.references.focused)
			}
		})
		ui.Box(c).Height(1).FillWidth().Background(s.ch.separator)
		if t.err != "" {
			ui.Text(c, t.err).Wrap()
			if ui.Button(c, s.tr("refresh")).Clicked() {
				t.err = ""
				t.pages = 0
				if t.key != "" {
					t.path = ""
				}
			}
		} else if t.bitmap != nil {
			bitmap := t.bitmap
			w, h := bitmap.Size()
			ui.Scroll(c).Grow(1).MinHeight(0).FillWidth().Background(s.ch.tabBar).Children(func() {
				ui.Column(c).FillWidth().AlignItems(ui.Center).Padding(10).Children(func() {
					ui.Image(c, bitmap).WidthPercent(float32(s.documentWidth(s.activeTab()))).AspectRatio(float32(w) / float32(h)).Label(t.title)
				})
			})
		} else {
			ui.Text(c, s.tr("scholarLoadingPDF")).Padding(12)
		}
	})
}

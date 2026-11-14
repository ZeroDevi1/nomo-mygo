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
	tabs        []*scholarReferenceTab
	focused     *scholarReferenceTab
	catalog     bool
	catalogOpen bool
	sourceJump  *scholarReferenceTab
	annotation  *scholarAnnotationView
}

type scholarReferenceTab struct {
	entry            scholar.Entry
	web              *scholarWebTab
	zotero           scholar.Zotero
	key, path, title string
	page, pages      int
	pageNavigation   scholarPageNavigation
	pinned           bool
	bitmap           *ui.Bitmap
	pageAspect       float32
	err              string
	cancel           context.CancelFunc
	generation       uint64
	quote            scholarPDFQuote
	fileInfo         os.FileInfo
	scroll           ui.ScrollState
	detailScroll     ui.ScrollState
	jump             *scholarPDFSource
	highlight        ui.Rect
	highlightUntil   time.Time
	sourceNotice     string
	revealRegion     bool
	focusPage        bool
}

func (t *scholarReferenceTab) releasePage() {
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	t.generation++
	t.bitmap, t.pageAspect, t.fileInfo = nil, 0, nil
	t.quote.cancelCapture()
	t.quote = scholarPDFQuote{}
	t.highlight, t.highlightUntil, t.revealRegion = ui.Rect{}, time.Time{}, false
}

func (r *scholarReferenceTabs) selectTab(t *scholarReferenceTab) {
	if r.focused != nil && r.focused != t {
		q := &r.focused.quote
		q.cancelCapture()
		q.popup, q.focusReader, q.focusActions = false, false, false
		r.focused.focusPage = false
	}
	if r.sourceJump != nil && r.sourceJump != t {
		r.sourceJump.clearSourceJump()
		r.sourceJump = nil
	}
	r.catalog, r.focused = false, t
}

func (t *scholarReferenceTab) releaseContent(remember bool) {
	if t.web != nil {
		t.web.stop(remember)
	} else if t.bitmap != nil || t.cancel != nil {
		t.releasePage()
	}
}

func (r *scholarReferenceTabs) close(t *scholarReferenceTab) {
	t.releaseContent(false)
	if r.sourceJump == t {
		r.sourceJump = nil
	}
	if r.annotation != nil && r.annotation.matches(t) {
		r.annotation = nil
	}
	r.tabs = slices.DeleteFunc(r.tabs, func(candidate *scholarReferenceTab) bool { return candidate == t })
	if r.focused == t {
		r.focused = nil
	}
}

func (r *scholarReferenceTabs) visible(covered bool) (shown *scholarReferenceTab) {
	if !covered && !r.catalog {
		shown = r.focused
	}
	r.releaseHidden(shown)
	return shown
}

func (r *scholarReferenceTabs) releaseHidden(shown ...*scholarReferenceTab) {
	for _, t := range r.tabs {
		if !slices.Contains(shown, t) {
			t.pageNavigation.open = false
			t.releaseContent(true)
		}
	}
}

func (s *Shell) showScholarCatalog(p *scholarState) {
	s.rememberScholarManuscript(p)
	p.references.selectTab(nil)
	p.literature.annotations = nil
	s.closeScholarInspector(p)
	if p.nav != scholarLiterature {
		p.lastProjectNav = p.nav
	}
	p.references.catalog, p.references.catalogOpen = true, true
	p.accountOpen, p.winRailPopup, p.winCompactProject = false, false, false
	p.nav, p.drawer, p.compactNav = scholarLiterature, scholarClosed, false
}

func (s *Shell) openScholarAttachment(p *scholarState, e scholar.Entry, key, path, title string, page int) {
	z := p.literature.zotero
	z.Group = e.Library
	s.openScholarAttachmentFrom(p, e, z, key, path, title, page)
}

func (s *Shell) openScholarAttachmentFrom(p *scholarState, e scholar.Entry, z scholar.Zotero, key, path, title string, page int) *scholarReferenceTab {
	r := &p.references
	for _, t := range r.tabs {
		if t.web != nil {
			continue
		}
		if (key != "" && t.key == key && t.zotero.SameSource(z) && t.zotero.Group == z.Group) ||
			(key == "" && t.key == "" && samePath(t.path, path)) {
			s.selectScholarPDF(p, t)
			if key != "" {
				p.literature.rememberReading(t.entry)
			}
			if page > 0 {
				t.setPage(page)
			}
			return t
		}
	}
	if len(r.tabs) >= scholarPDFTabLimit {
		p.err = s.tr("scholarPDFTabLimit")
		return nil
	}
	if title == "" {
		title = e.Title
	}
	e.Raw, e.Record = "", nil
	t := &scholarReferenceTab{entry: e, zotero: z, key: key, path: path, title: title}
	t.setPage(max(page, 1))
	r.tabs = append(r.tabs, t)
	s.selectScholarPDF(p, t)
	if key != "" {
		p.literature.rememberReading(e)
	}
	return t
}

func (s *Shell) selectScholarPDF(p *scholarState, t *scholarReferenceTab) {
	if t != nil {
		s.rememberScholarManuscript(p)
	} else {
		p.returnManuscript = scholarManuscriptReturn{}
		p.returnConversation = false
	}
	s.closeScholarInspector(p)
	if t == nil && p.nav == scholarLiterature {
		p.nav = p.lastProjectNav
	}
	p.references.selectTab(t)
	p.literature.annotations = nil
	p.accountOpen, p.winRailPopup, p.winCompactProject = false, false, false
	p.drawer, p.compactNav = scholarClosed, false
	p.err = ""
}

func (t *scholarReferenceTab) clearSourceJump() {
	if t.jump != nil && t.cancel != nil {
		t.releasePage()
	}
	t.jump, t.sourceNotice, t.highlight = nil, "", ui.Rect{}
	t.highlightUntil, t.revealRegion, t.focusPage = time.Time{}, false, false
}

func (t *scholarReferenceTab) setPage(page int) {
	t.clearSourceJump()
	if t.pages > 0 {
		page = min(page, t.pages)
	}
	page = max(page, 1)
	if t.page != page || t.err != "" {
		t.releasePage()
		t.err = ""
		t.scroll.X, t.scroll.Y = 0, 0
	}
	t.page = page
}

func (s *Shell) loadScholarPDFPage(p *scholarState, t *scholarReferenceTab) {
	if t.cancel != nil || t.bitmap != nil || t.err != "" {
		return
	}
	path, key, z, page := t.path, t.key, t.zotero, t.page
	jump := t.jump
	unavailable, stale := s.tr("scholarACPSourceUnavailable"), s.tr("scholarACPSourceStale")
	ctx, cancel := context.WithTimeout(p.ctx, 20*time.Second)
	t.cancel = cancel
	t.generation++
	generation := t.generation
	go func() {
		defer cancel()
		pages, err := 0, error(nil)
		var info os.FileInfo
		var message string
		if jump != nil && key == "" {
			path, err = p.project.Path(jump.Path)
		}
		if err == nil && (path == "" || jump != nil && key != "") {
			path, err = z.AttachmentPath(ctx, key)
		}
		if err == nil {
			info, err = os.Stat(path)
			if err == nil && (!info.Mode().IsRegular() || info.Size() > scholarReferenceFileLimit) {
				err = fmt.Errorf("PDF attachment exceeds the 128 MiB limit or is not a regular file")
			}
		}
		if err == nil {
			pages, err = typeset.PDFPages(ctx, path)
		}
		var bitmap *ui.Bitmap
		var region ui.Rect
		if err == nil && jump != nil {
			if page < 1 || page > pages {
				err = fmt.Errorf("%s", unavailable)
			} else {
				version, versionErr := scholarPDFVersion(ctx, path, info)
				if versionErr == nil && version == jump.Version && scholarValidPDFRegion(jump.Region) {
					region = jump.Region
				} else {
					message = stale
				}
			}
		}
		if err == nil {
			page = min(page, pages)
			if img, renderErr := typeset.ReadPDFPage(ctx, path, page, scholarReferenceEdge); renderErr != nil {
				err = renderErr
			} else if err = ctx.Err(); err == nil {
				bitmap = ui.NewBitmap(img)
				if after, statErr := os.Stat(path); statErr != nil || !scholarSamePDFVersion(info, after) {
					err = fmt.Errorf("%s", unavailable)
				}
			}
		}
		s.post(func() {
			if s.scholar != p || p.ctx.Err() != nil || t.generation != generation {
				return
			}
			t.cancel = nil
			if err != nil {
				t.err = err.Error()
				if jump != nil {
					t.err = unavailable
				}
				return
			}
			t.path, t.page, t.pages, t.bitmap, t.fileInfo = path, page, pages, bitmap, info
			if jump != nil {
				t.jump, t.sourceNotice, t.highlight = nil, message, region
				t.revealRegion = scholarValidPDFRegion(region)
				t.focusPage = true
			}
			w, h := bitmap.Size()
			t.pageAspect = float32(w) / float32(h)
		})
	}()
}

func (r *scholarReferenceTabs) hasTabs() bool {
	return r.catalogOpen || r.catalog || len(r.tabs) > 0
}

func (s *Shell) scholarReferenceTabItems(c *ui.Context, p *scholarState) {
	r := &p.references
	if !r.hasTabs() {
		return
	}
	if r.catalogOpen || r.catalog {
		b := s.scholarWorkspaceTab(c.Key("catalog"), "book-open", s.tr("scholarCatalog"), s.tr("scholarCloseCatalog"), r.catalog,
			func() { s.showScholarCatalog(p) }, func() {
				r.catalogOpen = false
				if r.catalog {
					s.selectScholarPDF(p, nil)
				}
			})
		b.ContextMenu(func(m *ui.Menu) { s.editorGroupMenu(m, workspaceItem{catalog: true}) })
	}
	if len(r.tabs) == 0 {
		return
	}
	tabs := slices.Clone(r.tabs)
	slices.SortStableFunc(tabs, func(a, b *scholarReferenceTab) int {
		switch {
		case a.pinned == b.pinned:
			return 0
		case a.pinned:
			return -1
		default:
			return 1
		}
	})
	ui.Row(c.Key("attachment-tabs")).FillHeight().MinWidth(0).Shrink(1).Clip().Gap(1).AlignItems(ui.Stretch).Children(func() {
		for _, t := range tabs {
			ck := c.Key(t)
			glyph := "file-text"
			if t.web != nil {
				glyph = "globe"
			}
			if t.pinned {
				glyph = "pin"
			}
			b := s.scholarWorkspaceTab(ck, glyph, t.title, s.tr("closeTab"), !r.catalog && r.focused == t,
				func() { s.selectScholarPDF(p, t) }, func() { s.closeScholarPDF(p, t) })
			b.ContextMenu(func(m *ui.Menu) {
				s.scholarReferenceActions(c, m, p, t)
			})
		}
	})
}

func (s *Shell) scholarWorkspaceTab(c *ui.Context, glyph, title, closeLabel string, selected bool, activate, close func()) ui.Element {
	cell := ui.Row(c).Width(140).MinWidth(94).Shrink(1).FillHeight().Padding(0, 6).Gap(4).AlignItems(ui.Center).
		Border(1, s.ch.separator).BorderWidth(0, 0, 0, 1)
	if s.pf.windows {
		cell.Width(184).MinWidth(96).Padding(0, 4, 0, 12).BorderWidth(0)
		s.ch.windowsTab(cell, selected)
	}
	if selected {
		cell.Background(s.ch.tabActive)
	} else if cell.Hovered() {
		cell.Background(s.ch.hover)
	}
	cell.Children(func() {
		label := ui.ButtonBase(c.Key("activate")).Grow(1).MinWidth(0).FillHeight().Gap(5).Justify(ui.Start).
			Selected(selected).TextColor(s.ch.text).FocusRing(true).Label(title).Tooltip(title)
		if selected {
			label.Background(s.ch.tabActive)
		}
		if s.pf.windows {
			label.Role(ui.RoleTab).Background(ui.Transparent)
		}
		label.Children(func() {
			ui.Icon(c, s.icon(glyph)).FontSize(13).TextColor(s.ch.muted)
			text := ui.Text(c, title).FontSize(12).SingleLine().Grow(1).MinWidth(0).TextColor(s.ch.text)
			if s.pf.windows {
				text.FontSize(c.Theme().FontSize)
				if selected {
					text.FontWeight(600)
				} else {
					text.TextColor(s.ch.muted)
				}
			}
		})
		if label.Clicked() {
			label.Focus()
			activate()
		}
		button := ui.ButtonBase(c.Key("close")).Size(18, 18).Radius(3).FocusRing(true).Label(closeLabel).Tooltip(closeLabel)
		if s.pf.windows {
			button.Size(24, 24).Radius(4)
		}
		if button.Hovered() {
			button.Background(s.ch.hover)
		}
		button.Children(func() { ui.Icon(c, s.icon("x")).FontSize(12).TextColor(s.ch.muted) })
		if button.Clicked() {
			close()
		}
	})
	return cell
}

func (s *Shell) closeScholarPDF(p *scholarState, t *scholarReferenceTab) {
	active := !p.references.catalog && p.references.focused == t
	p.references.close(t)
	if active {
		s.selectScholarPDF(p, nil)
	}
}

func (s *Shell) scholarTabButton(c *ui.Context, glyph, title string, selected bool) ui.Element {
	b := ui.ButtonBase(c).Height(27).MaxWidth(190).Padding(0, 8).Gap(5).Justify(ui.Start).FocusRing(true).
		Selected(selected).TextColor(s.ch.text).Tooltip(title)
	if selected {
		b.Background(s.ch.tabActive)
	} else if b.Hovered() {
		b.Background(s.ch.hover)
	}
	b.Children(func() {
		ui.Icon(c, s.icon(glyph)).FontSize(13).TextColor(s.ch.muted)
		ui.Text(c, title).FontSize(12).SingleLine().MinWidth(0).Shrink(1).TextColor(s.ch.text)
	})
	return b
}

func (s *Shell) scholarReferenceActions(c *ui.Context, m *ui.Menu, p *scholarState, t *scholarReferenceTab) {
	if t.web == nil {
		s.scholarPDFMenu(c, m, p, t)
		m.Separator()
	}
	pinLabel := "scholarPinPDF"
	if s.pf.windows && t.pinned {
		pinLabel = "scholarUnpinPDF"
	}
	pin := m.Item(s.tr(pinLabel))
	// Windows 原生菜单会为未勾选的复选项也预留一整列。
	if !s.pf.windows {
		pin.Checked(t.pinned)
	}
	if pin.Chosen() {
		t.pinned = !t.pinned
	}
	s.editorGroupMenu(m, workspaceItem{reference: t})
	if m.Item(s.tr("scholarClose")).Chosen() {
		s.closeScholarPDF(p, t)
	}
}

func (s *Shell) scholarPDFTabControls(c *ui.Context, p *scholarState, t *scholarReferenceTab) {
	if t.key != "" && s.scholarCompactIcon(c.Key("pdf-annotations"), "highlighter", s.tr("scholarReadAnnotations"), false).
		Disabled(p.busy).Clicked() {
		s.openScholarAnnotations(p, t.entry, t.zotero, t.key, t.title)
	}
	if s.scholarCompactIcon(c.Key("pdf-external"), "external-link", s.tr("scholarOpenExternalPDF"), false).Disabled(!t.hasExternalPDF()).Clicked() {
		t.openExternalPDF(c)
	}
}

func (s *Shell) scholarPDFMenu(c *ui.Context, m *ui.Menu, p *scholarState, t *scholarReferenceTab) {
	if t.key != "" && m.Item(s.tr("scholarReadAnnotations")).Disabled(p.busy).Chosen() {
		s.openScholarAnnotations(p, t.entry, t.zotero, t.key, t.title)
	}
	if m.Item(s.tr("scholarOpenExternalPDF")).Disabled(!t.hasExternalPDF()).Chosen() {
		t.openExternalPDF(c)
	}
}

func (t *scholarReferenceTab) hasExternalPDF() bool {
	return t.key != "" || t.path != ""
}

func (t *scholarReferenceTab) openExternalPDF(c *ui.Context) {
	if t.key != "" {
		c.Services().OpenURL(t.zotero.PDFLink(t.key, t.page))
	} else if t.path != "" {
		c.Services().OpenURL((&url.URL{Scheme: "file", Path: t.path}).String())
	}
}

func (s *Shell) scholarReferenceError(c *ui.Context, message string, actions func()) {
	ui.Column(c).FillWidth().MaxWidth(520).Children(func() {
		s.scholarReaderNotice(c.Key("message"), message, nil)
		ui.Column(c.Key("recovery")).FillWidth().Padding(0, scholarReaderInset).Gap(4).Children(actions)
	})
}

func (s *Shell) scholarReferencePage(c *ui.Context, p *scholarState, t *scholarReferenceTab) {
	c = c.Key(t)
	if t.web != nil {
		s.scholarWebPage(c, p, t)
		return
	}
	s.loadScholarPDFPage(p, t)
	ui.Column(c).Grow(1).Fill().MinWidth(0).Clip().Children(func() {
		if t.err != "" {
			s.scholarReferenceError(c, t.err, func() {
				if address := scholarEntryWebURL(t.entry); address != "" && s.scholarReaderCommand(c.Key("web-fallback"), "globe", s.tr("scholarOpenWeb"), false).Clicked() {
					s.openScholarWeb(p, t.entry, address)
				}
				if t.key != "" {
					scholarReaderText(c, s.tr("scholarPDFZoteroHelp")).TextColor(s.ch.muted)
					if s.scholarReaderCommand(c.Key("locate"), "external-link", s.tr("scholarShowInZotero"), false).Clicked() {
						c.Services().OpenURL(t.zotero.ItemLink(t.key))
					}
				}
				if s.scholarReaderCommand(c.Key("retry"), "refresh-cw", s.tr("scholarRetryPDF"), false).Clicked() {
					t.releasePage()
					t.err = ""
					t.pages = 0
					if t.key != "" {
						t.path = ""
					}
				}
			})
		} else if t.pageAspect > 0 {
			if t.sourceNotice != "" {
				scholarReaderText(c.Key("source-notice"), t.sourceNotice).Padding(4, 8).TextColor(scholarInk(c, "warning")).
					Role(ui.RoleStatus).Label(t.sourceNotice)
			}
			s.scholarPDFQuoteControls(c.Key("pdf-quote"), p, t)
			ui.Scroll(c.Key("pdf-page")).TrackScroll(&t.scroll).Grow(1).MinHeight(0).FillWidth().Background(s.ch.tabBar).Children(func() {
				ui.Column(c).FillWidth().AlignItems(ui.Center).Padding(10).Children(func() {
					width := float32(s.documentWidth(s.activeTab()))
					if t.bitmap != nil {
						s.scholarPDFQuotePage(c, p, t, width)
					} else {
						ui.Box(c).WidthPercent(width).AspectRatio(t.pageAspect)
					}
				})
			})
		} else {
			scholarReaderText(c, s.tr("scholarLoadingPDF")).Padding(scholarReaderInset).TextColor(s.ch.muted)
		}
	})
}

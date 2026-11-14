package shell

import (
	"github.com/egoist/mygo/ui"

	"nomo/desktop/typeset"
)

type pdfTextRequest struct {
	page            int
	version, serial uint64
}

const manuscriptPDFSelectionBytes = 2 << 20

// Only the active selection page holds a text layer. Coordinates are relative
// to the page so the range survives resizing and scrolling within that page.
type manuscriptPDFText struct {
	page           int
	words          []typeset.PDFTextWord
	down, dragging bool
	menu, word     bool
	start, end     ui.Point
	bounds         ui.Rect
	first, last    int
	text           string
	marks          []typeset.PDFTextBox
}

func (t *manuscriptPDFText) clear() { *t = manuscriptPDFText{first: -1, last: -1} }

func (p *compilePane) loadPDFText(page int) {
	if p.textRequests == nil || p.pdfText.page == page {
		return
	}
	p.pdfText.clear()
	p.pdfText.page = page
	select {
	case <-p.textRequests:
	default:
	}
	p.textRequests <- pdfTextRequest{page, p.fullVersion, p.fullSerial}
}

func (t *manuscriptPDFText) load(page int, words []typeset.PDFTextWord) {
	if t.page != page {
		return
	}
	t.words = words
	if t.dragging || t.word || t.menu {
		t.selectText()
	}
}

func (t *manuscriptPDFText) selectText() {
	a, distance := typeset.PDFWordAt(t.words, t.start.X, t.start.Y, t.bounds.W, t.bounds.H)
	b, _ := typeset.PDFWordAt(t.words, t.end.X, t.end.Y, t.bounds.W, t.bounds.H)
	if distance > 18 {
		a, b = -1, -1
	}
	if a != t.first || b != t.last {
		// The PDF backend bounds the page text layer. Copy can use that full
		// page budget; ACP keeps its smaller prompt selection limit.
		t.text, t.marks = typeset.PDFSelection(t.words, a, b, manuscriptPDFSelectionBytes)
		t.first, t.last = a, b
	}
}

func (s *Shell) manuscriptPDFTextPage(c *ui.Context, p *compilePane, el ui.Element, page int) {
	t := &p.pdfText
	box := el.Bounds()
	if t.page == page {
		t.bounds = box
		if len(t.marks) > 0 {
			ui.Box(c.Key("pdf-text-selection")).Absolute().Fill().PassThrough().Draw(func(painter *ui.Painter, r ui.Rect) {
				for _, m := range t.marks {
					mark := ui.Rect{X: r.X + m.X*r.W, Y: r.Y + m.Y*r.H, W: (m.Right - m.X) * r.W, H: (m.Bottom - m.Y) * r.H}
					painter.Fill(mark, s.ch.accent.Alpha(.24), 1)
				}
			})
		}
	}
	el.Focusable().Cursor(ui.CursorText)
	el.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputCommand {
			switch ev.Text {
			case "copy":
				return s.copyManuscriptPDFText(p)
			case "undo", "redo":
				return s.editCommand(ev.Text)
			}
		}
		if !p.canLocate() || p.pageEdit != nil || p.note != nil || box.W <= 0 || box.H <= 0 {
			return false
		}
		at := ui.Point{X: min(1, max(0, ev.X/box.W)), Y: min(1, max(0, ev.Y/box.H))}
		switch ev.Kind {
		case ui.InputPointerDown:
			if ev.Button != 0 && ev.Button != 1 {
				return false
			}
			p.sync.invalidate()
			p.loadPDFText(page)
			t.bounds = box
			if ev.Button == 1 {
				if t.text == "" || !t.contains(at) {
					t.start, t.end = at, at
					t.selectText()
				}
				t.menu = false
				t.down, t.dragging, t.word = false, false, false
				return true
			}
			t.first, t.last, t.text, t.marks = -1, -1, "", nil
			t.down, t.dragging, t.word, t.menu = true, false, ev.Clicks > 1, false
			t.start, t.end = at, at
			if t.word {
				t.selectText()
			}
			return true
		case ui.InputPointerMove, ui.InputPointerUp:
			if ev.Button == 1 && ev.Kind == ui.InputPointerUp && !ev.Canceled && t.page == page {
				t.menu = true
				if t.text == "" {
					t.selectText()
				}
				return true
			}
			if !t.down || t.page != page {
				return false
			}
			if ev.Canceled {
				t.down, t.dragging, t.menu = false, false, false
				return true
			}
			t.end = at
			if !t.dragging {
				dx, dy := (at.X-t.start.X)*box.W, (at.Y-t.start.Y)*box.H
				t.dragging = dx*dx+dy*dy >= 16
			}
			if t.word && !t.dragging {
				t.end = t.start
			}
			if t.dragging || t.word {
				t.selectText()
			}
			if ev.Kind == ui.InputPointerUp {
				t.down = false
				if t.dragging || t.word {
					t.menu = true
				} else {
					// Single-click synchronization may only move within the active source.
					p.requestPDFPosition(page, float64(at.X), float64(at.Y), ev.Mods&ui.Cmd != 0)
				}
			}
			return true
		}
		return false
	})
	if t.page == page && t.text != "" && (el.Shortcut(ui.Shift, ui.KeyF10) || el.Shortcut(0, ui.KeyContextMenu)) {
		t.menu = true
	}
}

func (t *manuscriptPDFText) contains(at ui.Point) bool {
	for _, r := range t.marks {
		if at.X >= r.X && at.X <= r.Right && at.Y >= r.Y && at.Y <= r.Bottom {
			return true
		}
	}
	return false
}

func (s *Shell) copyManuscriptPDFText(p *compilePane) bool {
	if p.pdfText.text == "" || s.svc == nil {
		return false
	}
	s.svc.WriteClipboard(p.pdfText.text)
	p.pdfText.menu = false
	return true
}

func (s *Shell) manuscriptPDFTextActions(c *ui.Context, p *scholarState, pane *compilePane) {
	t := &pane.pdfText
	if !t.menu || t.down || !pane.canLocate() {
		return
	}
	first, last := pane.list.Visible()
	if t.page-1 < first || t.page-1 > last {
		t.menu = false
		return
	}
	const size = 108
	spots := [...]ui.Point{{X: 39, Y: 7}, {X: 71, Y: 39}, {X: 39, Y: 71}, {X: 7, Y: 39}}
	view := pane.sync.pdfViewport
	if view.W < size || view.H < size {
		return
	}
	x := min(max(view.X, t.bounds.X+t.end.X*t.bounds.W-size/2), view.X+view.W-size)
	y := min(max(view.Y, t.bounds.Y+t.end.Y*t.bounds.H+6), view.Y+view.H-size)
	ui.Overlay(c, func() {
		panel := ui.Box(c.Key("pdf-actions")).Absolute().Left(x).Top(y).Size(size, size).Radius(size/2).
			Background(s.ch.content).Border(1, s.ch.separator).Shadow(0, 4, 12, 0, ui.RGBA(0, 0, 0, .16)).
			Role(ui.RoleToolbar).Label(s.tr("scholarPDFTextActions"))
		panel.Children(func() {
			action := func(i int, glyph, label string, disabled bool, run func()) {
				button := ui.ButtonBase(c.Key(i)).Absolute().Left(spots[i].X).Top(spots[i].Y).Size(30, 30).Radius(15).
					Label(s.tr(label)).Tooltip(s.tr(label)).AlignItems(ui.Center).Justify(ui.Center).FocusRing(true).Disabled(disabled)
				if button.Hovered() {
					button.Background(s.ch.hover)
				}
				button.Children(func() { ui.Icon(c, s.icon(glyph)).FontSize(14) })
				if button.Clicked() {
					t.menu = false
					run()
				}
			}
			blocked := p.busy || p.agent.pendingPatch() != nil
			at := t.start
			if t.first >= 0 && t.text != "" {
				word := t.words[t.first]
				at = ui.Point{X: (word.X + word.Right) / 2, Y: (word.Y + word.Bottom) / 2}
			}
			action(0, "copy", "copy", t.text == "", func() { s.copyManuscriptPDFText(pane) })
			action(1, "code-xml", "scholarPDFGoToSource", false, func() {
				pane.requestPDFPosition(t.page, float64(at.X), float64(at.Y), true)
			})
			action(2, "message-square", "scholarACPMarkManuscript", blocked || t.text == "" || len(t.text) > scholarSelectionLimit, func() {
				s.noteSelectedManuscriptText(pane)
			})
			action(3, "file-pen-line", "scholarPageEditText", blocked || !canEditManuscript(pane.tab), func() {
				pane.requestPDFEdit(t.page, float64(at.X), float64(at.Y))
			})
		})
		if panel.PressedOutside() || panel.OverlayShortcut(0, ui.KeyEscape) {
			t.menu = false
		}
	})
}

func (s *Shell) noteSelectedManuscriptText(p *compilePane) {
	t := &p.pdfText
	if len(t.marks) == 0 {
		return
	}
	rect := t.marks[0]
	for _, mark := range t.marks[1:] {
		rect.X, rect.Y = min(rect.X, mark.X), min(rect.Y, mark.Y)
		rect.Right, rect.Bottom = max(rect.Right, mark.Right), max(rect.Bottom, mark.Bottom)
	}
	r := ui.Rect{X: max(0, rect.X), Y: max(0, rect.Y)}
	r.W, r.H = min(1, rect.Right)-r.X, min(1, rect.Bottom)-r.Y
	anchor := t.words[t.first]
	p.note = &manuscriptPDFNote{page: t.page, excerpt: t.text, anchor: ui.Point{X: (anchor.X + anchor.Right) / 2, Y: (anchor.Y + anchor.Bottom) / 2},
		scholarPDFQuote: scholarPDFQuote{popup: true, region: r, bounds: t.bounds}}
	t.clear()
}

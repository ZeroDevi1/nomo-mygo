package shell

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"nomo/desktop/typeset"
)

// A draft belongs to one published page and one unchanged source buffer.
type manuscriptPageEdit struct {
	buffer     *editor.Buffer
	version    uint64
	serial     uint64
	page       int
	start, end int
	x, y, w    float32
	original   string
	text       string
}

func (s *Shell) beginManuscriptPageEdit(p *compilePane, hit typeset.PDFTextHit, page int) {
	if hit.Path == "" {
		s.status = s.tr("scholarPageEditUnavailable")
		return
	}
	t := p.tab
	if !samePath(t.Path, hit.Path) {
		s.status = s.tr("scholarPageEditOpenSource")
		return
	}
	if !canEditManuscript(t) || scholarPathReserved(t.Path) {
		s.status = s.tr("scholarPageEditUnavailable")
		return
	}
	buf := t.Source().Buffer
	snap := buf.Snapshot()
	if hit.Line < 0 || hit.Line >= snap.LineCount() || hit.Start < 0 || hit.End > len(hit.Text) || hit.Start >= hit.End {
		return
	}
	line := snap.Line(hit.Line)
	body := strings.TrimPrefix(line, "\ufeff")
	if body != hit.Text {
		s.status = s.tr("scholarPageEditStale")
		return
	}
	start := snap.LineStart(hit.Line) + len(line) - len(body)
	p.pdfText.clear()
	p.pageEdit = &manuscriptPageEdit{buffer: buf, version: snap.Version(), serial: p.fullSerial,
		page: page, start: start + hit.Start, end: start + hit.End, x: float32(hit.X), y: float32(hit.Y), w: float32(hit.W),
		original: hit.DisplayText, text: hit.DisplayText}
}

func (s *Shell) manuscriptPageEditor(c *ui.Context, p *compilePane, box ui.Rect) {
	edit := p.pageEdit
	if edit == nil {
		return
	}
	tab := p.tab
	if edit.serial != p.fullSerial || !p.canLocate() ||
		tab.Source().Buffer != edit.buffer || edit.buffer.Version() != edit.version ||
		tab.Source().ReadOnly || scholarPathReserved(tab.Path) {
		p.pageEdit = nil
		s.status = s.tr("scholarPageEditStale")
		return
	}
	view := p.sync.pdfViewport
	width := min(view.W, max(280, box.W*edit.w+16))
	if width <= 0 {
		return
	}
	x, y := view.X+(view.W-width)/2, view.Y+8
	if box.W > 0 && box.H > 0 {
		x = min(max(view.X, box.X+box.W*edit.x-6), view.X+view.W-width)
		y = max(view.Y, min(box.Y+box.H*edit.y-4, view.Y+view.H-76))
	}
	label := s.tr("scholarPageEditText")
	ui.Overlay(c, func() {
		panel := ui.Column(c.Key(edit)).Absolute().Left(x).Top(y).Width(width).Padding(4).Gap(4).
			Border(1, c.Theme().Focus).Radius(5).Background(c.Theme().Surface).Label(label)
		panel.Children(func() {
			field := ui.TextInput(c.Key("text"), &edit.text).FillWidth().AutoFocus().Label(label).MaxLength(4096)
			var save, cancel bool
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Spacer(c)
				cancel = ui.Button(c.Key("cancel"), s.tr("cancel")).Clicked()
				save = ui.PrimaryButton(c.Key("apply"), s.tr("author-apply")).Clicked()
			})
			if cancel || panel.OverlayShortcut(0, ui.KeyEscape) {
				p.pageEdit = nil
				c.Invalidate()
			} else if save || field.Submitted() {
				p.pageEdit = nil
				c.Invalidate()
				if !s.previewBuffersCurrent(p) {
					s.status = s.tr("scholarPageEditStale")
					return
				}
				if edit.text != edit.original {
					text := typeset.LiteralManuscriptText(edit.text, typeset.IsLaTeXPath(tab.Path))
					s.tagScholarChange(tab, "page-edit")
					edit.buffer.Edit(editor.Edit{Start: edit.start, End: edit.end, Text: text})
					tab.Source().Select(edit.start+len(text), edit.start+len(text))
				}
			}
		})
	})
}

package shell

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"nomo/desktop/provider"
	"nomo/desktop/typeset"
)

const typesetSyncDelay = 100 * time.Millisecond

type typesetPositionSync struct {
	pdfScroll          ui.ScrollState
	pdfViewport        ui.Rect
	pdfWidth           float64
	pdfReading         typeset.PDFPosition
	sourceLine         int
	sourceClick        bool
	linked             bool
	ignoreSource       int
	ignorePDF          int
	serial, fullSerial uint64
	due                time.Time
	source             sourcePageRequest
	pdf                pageClick
	target             typeset.PDFPosition
}

func (p *compilePane) canLocate() bool {
	return p.image != nil && !p.busy && !p.stopped && p.err == "" &&
		p.parkedPage == 0 && p.fullVersion == p.version
}

func (p *compilePane) observeSourcePosition(c *ui.Context, linked bool) {
	state, st := &p.sync, p.tab.Source()
	first, _ := st.VisibleLines()
	if linked != state.linked || state.fullSerial != p.fullSerial {
		state.linked, state.fullSerial = linked, p.fullSerial
		state.sourceClick, state.ignoreSource = false, 0
		state.sourceLine = first
		state.ignorePDF = 2
		state.invalidate()
		return
	}
	click := state.sourceClick
	moved := first != state.sourceLine
	state.sourceClick, state.sourceLine = false, first
	if state.ignoreSource > 0 {
		state.ignoreSource--
		if !click {
			return
		}
	}
	if !linked {
		return
	}
	if click {
		sel := st.Selection()
		if sel.Anchor == sel.Head {
			p.requestSourcePosition(st.Buffer.Snapshot().LineOf(sel.Head))
			return
		}
	}
	if moved {
		state.invalidate()
		state.source = sourcePageRequest{line: first, version: p.fullVersion, serial: state.serial}
		state.due = c.Now().Add(typesetSyncDelay)
	}
}

func (s *typesetPositionSync) invalidate() {
	s.serial++
	s.due = time.Time{}
	s.source, s.pdf = sourcePageRequest{}, pageClick{}
	s.target = typeset.PDFPosition{}
}

func (p *compilePane) requestSourcePosition(line int) {
	if !p.canLocate() {
		return
	}
	p.sync.invalidate()
	p.sendSourcePosition(sourcePageRequest{line: line, version: p.fullVersion, serial: p.sync.serial})
}

func (p *compilePane) requestPDFPosition(page int, x, y float64, openSource bool) {
	if !p.canLocate() {
		return
	}
	p.sync.invalidate()
	p.sendPDFPosition(pageClick{page: page, version: p.fullVersion, serial: p.sync.serial, x: x, y: y, openSource: openSource})
}

func (p *compilePane) requestPDFEdit(page int, x, y float64) {
	if p.pageEdit != nil || !p.canLocate() {
		return
	}
	p.sync.invalidate()
	p.sendPDFPosition(pageClick{page: page, version: p.fullVersion, serial: p.sync.serial, x: x, y: y, edit: true})
}

func (p *compilePane) sendSourcePosition(req sourcePageRequest) {
	select {
	case <-p.sections:
	default:
	}
	p.sections <- req
}

func (p *compilePane) sendPDFPosition(req pageClick) {
	select {
	case <-p.clicks:
	default:
	}
	p.clicks <- req
}

func (p *compilePane) flushPositionSync(c *ui.Context) {
	state := &p.sync
	if state.due.IsZero() {
		return
	}
	if wait := state.due.Sub(c.Now()); wait > 0 {
		c.After(wait)
		return
	}
	state.due = time.Time{}
	if !state.linked || !p.canLocate() {
		return
	}
	if state.source.serial == state.serial {
		p.sendSourcePosition(state.source)
	} else if state.pdf.serial == state.serial {
		p.sendPDFPosition(state.pdf)
	}
}

func (p *compilePane) observePDFLayout(viewport ui.Rect, width float64) bool {
	state := &p.sync
	geometryChanged := viewport.W != state.pdfViewport.W || viewport.H != state.pdfViewport.H || state.pdfWidth != width
	state.pdfViewport, state.pdfWidth = viewport, width
	if geometryChanged {
		state.ignorePDF = 2
	}
	if state.ignorePDF > 0 {
		state.ignorePDF--
		return false
	}
	return state.linked && state.target.Page == 0
}

func (p *compilePane) alignPDFPosition(pos typeset.PDFPosition) {
	if pos.Page <= 0 || pos.Page > p.pages {
		return
	}
	p.sync.target = pos
	p.sync.ignorePDF = 2
	first, last := p.list.Visible()
	if pos.Y < 0 || pos.Page-1 < first || pos.Page-1 > last {
		p.list.ScrollTo(pos.Page-1, ui.Start)
	}
	if pos.Y < 0 {
		p.sync.target = typeset.PDFPosition{}
	}
}

func (p *compilePane) syncPDFPage(c *ui.Context, page int, box ui.Rect, follow bool) bool {
	state := &p.sync
	view := state.pdfViewport
	if view.H <= 0 || box.H <= 0 {
		return false
	}
	guide := view.Y + min(64, view.H*.18)
	if target := state.target; target.Page == page {
		delta := box.Y + box.H*float32(target.Y) - guide
		if math.Abs(float64(delta)) >= 1 {
			state.pdfScroll.Y = max(0, state.pdfScroll.Y+delta)
		}
		state.target = typeset.PDFPosition{}
		state.ignorePDF = 2
		c.Invalidate()
		return true
	}
	if guide >= box.Y-8 && guide <= box.Y+box.H {
		y := float64(max(0, guide-box.Y) / box.H)
		previous := state.pdfReading
		state.pdfReading = typeset.PDFPosition{Page: page, Y: y}
		if follow && previous.Page > 0 && (previous.Page != page || math.Abs(previous.Y-y) >= .0005) {
			state.invalidate()
			state.pdf = pageClick{page: page, version: p.fullVersion, serial: state.serial, x: .35, y: y, scroll: true}
			state.due = c.Now().Add(typesetSyncDelay)
			c.Invalidate()
		}
		return true
	}
	return false
}

func (p *compilePane) gotoPDFPage(page int) {
	p.sync.invalidate()
	p.sync.ignorePDF = 0
	p.list.ScrollTo(max(0, min(p.pages-1, page-1)), ui.Start)
}

func (p *compilePane) sourcePDFPosition(ctx context.Context, session provider.Session, project *typeset.Session, source editor.Snapshot, line int) typeset.PDFPosition {
	locate := func(line int, text string) typeset.PDFPosition { return typeset.PDFPosition{} }
	switch {
	case project != nil:
		locate = func(line int, text string) typeset.PDFPosition {
			return project.PositionForProjectLine(ctx, p.sourcePath, line, text)
		}
	default:
		if locator, ok := session.(interface {
			PositionForLine(context.Context, int, string) typeset.PDFPosition
		}); ok {
			locate = func(line int, text string) typeset.PDFPosition {
				return locator.PositionForLine(ctx, line, text)
			}
		} else if locator, ok := session.(interface {
			PageForLine(context.Context, int) int
		}); ok {
			return typeset.PDFPosition{Page: locator.PageForLine(ctx, line), Y: -1}
		}
	}
	near := []int{0, 1, -1, 2, -2, 3, -3, 4, -4}
	if typeset.IsLaTeXPath(p.path) {
		near = near[:1]
	}
	for _, offset := range near {
		row := line + offset
		if row >= 0 && row < source.LineCount() && ctx.Err() == nil {
			text := strings.TrimSuffix(strings.TrimPrefix(source.Line(row), "\ufeff"), "\r")
			if pos := locate(row, text); pos.Page > 0 {
				return pos
			}
		}
	}
	return typeset.PDFPosition{}
}

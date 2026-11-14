package shell

import (
	"context"
	"fmt"
	"image"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"nomo/desktop/typeset"
)

type scholarTrialPage struct {
	bitmap                     *ui.Bitmap
	regions                    []image.Rectangle
	additions                  []image.Rectangle
	excerpts                   []scholarTrialExcerpt
	changed, dimensionsChanged bool
}

type scholarTrialExcerpt struct {
	bitmap *ui.Bitmap
	marks  []image.Rectangle
	at, x  int
}

func newScholarTrialPage(diff typeset.TrialPageDiff) scholarTrialPage {
	p := scholarTrialPage{bitmap: ui.NewBitmap(diff.Raster), regions: diff.Regions, additions: diff.Additions, changed: diff.Changed, dimensionsChanged: diff.DimensionsChanged}
	for _, e := range diff.Excerpts {
		p.excerpts = append(p.excerpts, scholarTrialExcerpt{bitmap: ui.NewBitmap(e.Raster), marks: e.Regions, at: e.At, x: e.X})
	}
	return p
}

func (p scholarTrialPage) bytes() int {
	if p.bitmap == nil {
		return 0
	}
	w, h := p.bitmap.Size()
	bytes := 4 * w * h
	for _, e := range p.excerpts {
		w, h := e.bitmap.Size()
		bytes += 4 * w * h
	}
	return bytes
}

const (
	scholarPDFSearchBatch    = 8
	scholarPDFDiffCacheBytes = 24 << 20
	scholarPDFDiffCachePages = 12
)

func (v *scholarTrialView) jumpPage(page int) {
	if v.searching && v.cancelPage != nil {
		v.cancelPage()
	}
	v.revealPDF = typeset.PDFPosition{}
	v.page = min(max(1, page), max(v.pagesBefore, v.pagesAfter))
	v.pageInput = strconv.Itoa(v.page)
	if first, last := v.pageList.Visible(); first <= last {
		v.firstVisible = first + 1
	}
	if v.failedPage != 0 && v.failedPage != v.page {
		v.failedPage, v.pageError = 0, ""
	}
	v.pageList.ScrollTo(v.page-1, ui.Start)
}

func (v *scholarTrialView) jumpPosition(pos typeset.PDFPosition) {
	if pos.Page > 0 {
		v.jumpPage(pos.Page)
		v.revealPDF = pos
	}
}

func (v *scholarTrialView) keepPage(page int, result scholarTrialPage) {
	if v.cached == nil {
		v.cached = make(map[int]scholarTrialPage)
	}
	v.cacheBytes += result.bytes() - v.cached[page].bytes()
	v.cached[page] = result
	if v.pageAspect == 0 {
		w, h := result.bitmap.Size()
		v.pageAspect = float32(w) / float32(h)
	}
	for len(v.cached) > scholarPDFDiffCachePages || v.cacheBytes > scholarPDFDiffCacheBytes {
		distance, evict := -1, 0
		for n := range v.cached {
			if n != page && max(n-v.page, v.page-n) > distance {
				distance, evict = max(n-v.page, v.page-n), n
			}
		}
		if evict == 0 {
			break
		}
		v.cacheBytes -= v.cached[evict].bytes()
		delete(v.cached, evict)
	}
}

// Scroll loads only visible pages. Explicit searches check a bounded batch
// and move the virtual list only when a page from that search is available.
func (s *Shell) requestScholarTrialPage(p *scholarState, start, step int) {
	v := &p.agent.trial
	limit := max(v.pagesBefore, v.pagesAfter)
	if v.pdfs == nil || v.cancelPage != nil || start < 1 || start > limit {
		return
	}
	pdfs := v.pdfs
	request, cancel := context.WithCancel(p.ctx)
	v.cancelPage, v.pageError, v.searched = cancel, "", false
	v.searching = step != 0
	go func() {
		ctx, done := context.WithTimeout(request, 40*time.Second)
		defer done()
		page := start
		var last typeset.TrialPageDiff
		lastPage := 0
		var err error
		for range scholarPDFSearchBatch {
			result, e := pdfs.DiffPage(ctx, page)
			if e != nil {
				err = e
				break
			}
			last, lastPage = result, page
			page += step
			if step == 0 || last.Changed || page < 1 || page > limit {
				break
			}
		}
		var result scholarTrialPage
		if lastPage != 0 && request.Err() == nil {
			result = newScholarTrialPage(last)
		}
		s.post(func() {
			defer cancel()
			if s.scholar != p || v.pdfs != pdfs {
				return
			}
			v.cancelPage, v.searching = nil, false
			if request.Err() != nil {
				return
			}
			if lastPage != 0 {
				if step != 0 {
					v.jumpPage(lastPage)
					v.searched = !result.changed
				}
				v.keepPage(lastPage, result)
			}
			if err != nil {
				v.pageError = err.Error()
				if step == 0 {
					v.failedPage = start
				}
			}
		})
	}()
}

func (s *Shell) scholarTrialWorkspace(c *ui.Context, p *scholarState, tab *Tab) ui.Element {
	v := &p.agent.trial
	el := ui.Column(c).Fill().MinHeight(0).Focusable().Label(s.tr("scholarComparePDFs"))
	if v.split == 0 {
		v.split = tab.typesetSplitPercent
		if v.split == 0 {
			v.split = 50
		}
	}
	el.Children(func() {
		s.scholarTrialHeader(c.Key("trial-header"), p)
		if !v.show {
			return
		}
		width, _ := c.Size()
		switch {
		case !v.showSource:
			s.scholarTrialPaper(c.Key("paper-diff"), p)
		case s.scholarSinglePane(width):
			s.scholarTrialSourcePane(c.Key("source-diff"), p)
		default:
			ui.SplitPercent(c, &v.split, func() {
				s.scholarTrialSourcePane(c.Key("source-diff"), p)
			}, func() {
				s.scholarTrialPaper(c.Key("paper-diff"), p)
			}).Fill().MinHeight(0)
		}
	})
	if v.pending && !p.busy {
		accept := el.Shortcut(ui.Cmd, ui.KeyEnter)
		reject := el.Shortcut(ui.Cmd, ui.KeyBackspace)
		s.decideScholarTrialPatch(p, p.agent.pendingPatch(), accept, reject)
	}
	return el
}

func (s *Shell) scholarTrialHeader(c *ui.Context, p *scholarState) {
	v := &p.agent.trial
	ui.Column(c).FillWidth().Shrink(0).Padding(6, 8).Gap(4).Background(c.Theme().SurfaceHover).Children(func() {
		ui.Row(c.Key("view")).Gap(4).FillWidth().Wrap().AlignItems(ui.Center).Children(func() {
			title := s.tr("scholarPDFDiff")
			if v.pagesBefore != v.pagesAfter {
				title += fmt.Sprintf(" · %d → %d", v.pagesBefore, v.pagesAfter)
			}
			ui.Text(c, title).FontSize(12).Bold().Grow(1).MinWidth(0)
			if v.pending && s.scholarACPAction(c.Key("chat"), "message-square", s.tr("scholarACPChat"), p.drawer == scholarCodex && p.agent.acp.reviewChat).
				Disabled(p.busy).Clicked() {
				if p.drawer == scholarCodex && p.agent.acp.reviewChat {
					s.closeScholarInspector(p)
				} else {
					p.agent.acp.reviewChat, p.agent.acp.historyOpen, p.agent.acp.scrollToEnd = true, false, true
					s.showScholarInspector(p, scholarCodex)
				}
			}
			if len(v.sources) > 0 && s.scholarACPAction(c.Key("source"), "code", s.tr("scholarSourceDiff"), v.showSource).
				Role(ui.RoleToggleButton).Checked(v.showSource).Clicked() {
				v.showSource = !v.showSource
				if v.showSource {
					index := scholarTrialInitialSource(v.sources, p.agent.pendingPatch())
					s.requestScholarTrialSource(p, index)
				} else {
					v.source.close()
				}
			}
			if s.scholarReviewIcon(c.Key("close"), "x", s.tr("scholarCloseTrial"), "", false).Clicked() {
				pending := v.pending
				v.close()
				if pending {
					s.openScholarReview(p)
				}
			}
		})
		if v.show && v.pending {
			s.scholarTrialReviewControls(c.Key("review"), p)
			if set := p.agent.batch; set != nil && set.summary != "" {
				ui.Text(c.Key("summary"), set.summary).TextColor(scholarInk(c, "warning")).FontSize(11).Wrap()
			}
		}
		if v.show && p.err != "" {
			ui.Text(c.Key("error"), p.err).TextColor(scholarInk(c, "warning")).FontSize(11).Wrap()
		}
	})
}

func (s *Shell) scholarTrialPaper(c *ui.Context, p *scholarState) {
	v := &p.agent.trial
	ui.Column(c).Fill().MinWidth(0).MinHeight(0).Background(c.Theme().SurfaceHover).Padding(8).Gap(6).Children(func() {
		limit := max(v.pagesBefore, v.pagesAfter)
		first, last := v.pageList.Visible()
		if first <= last && v.firstVisible != first+1 {
			v.firstVisible = first + 1
			v.page = min(first+1, limit)
			v.pageInput = strconv.Itoa(v.page)
		}
		s.scholarTrialPageControls(c, p)
		if v.searching {
			ui.Text(c, s.tr("scholarPDFCheckingPages")).FontSize(11).Wrap()
		} else if v.searched {
			ui.Text(c, s.tr("scholarPDFNoMoreChanges")).FontSize(11).Wrap()
		}
		if v.pageError != "" {
			ui.Row(c).FillWidth().Gap(4).AlignItems(ui.Center).Children(func() {
				ui.Text(c, v.pageError).FontSize(11).SingleLine().MinWidth(0).Grow(1).Tooltip(v.pageError).TextColor(scholarInk(c, "warning"))
				if v.failedPage != 0 && ui.Button(c, s.tr("scholarTrialRetry")).Clicked() {
					v.failedPage, v.pageError = 0, ""
				}
			})
		}
		list := ui.List(c.Key("pdf-pages"), &v.pageList, limit, func(row int) {
			s.scholarTrialPageGroup(c.Key(row+1), v, row+1)
		}).TrackScroll(&v.pageScroll).FillWidth().Grow(1).MinHeight(0)
		v.pageViewport = list.Bounds()
		if v.cancelPage == nil && v.failedPage == 0 {
			for i := first; i <= min(last, first+scholarPDFSearchBatch-1); i++ {
				n := i + 1
				if _, ok := v.cached[n]; !ok && n != v.failedPage {
					s.requestScholarTrialPage(p, n, 0)
					break
				}
			}
		}
	})
}

func (s *Shell) scholarTrialReviewControls(c *ui.Context, p *scholarState) {
	v := &p.agent.trial
	patch := p.agent.pendingPatch()
	if patch == nil {
		return
	}
	ui.Row(c).FillWidth().Wrap().AlignItems(ui.Center).Gap(4).Children(func() {
		ui.Text(c, s.scholarReviewLabel(patch)).FontSize(11).SingleLine().Grow(1).MinWidth(0)
		if set := p.agent.batch; v.allPending && set != nil && len(set.hunks) > 1 {
			for _, step := range []int{-1, 1} {
				icon, label := "chevron-left", "scholarPreviousChange"
				if step > 0 {
					icon, label = "chevron-right", "scholarNextChange"
				}
				if s.scholarReviewIcon(c.Key(step), icon, s.tr(label), "", false).Disabled(p.busy).Clicked() {
					s.navigateScholarHunk(p, step)
				}
			}
			ui.Text(c, fmt.Sprintf("%d / %d", set.selected+1, len(set.hunks))).FontSize(11)
		}
		position := v.anchors[p.agent.pendingPatch()]
		if position.Page > 0 {
			label := fmt.Sprintf(s.tr("scholarACPPage"), position.Page)
			locate := s.scholarReviewControl(c.Key("location"), label, false).Width(44)
			locate.Children(func() { ui.Text(c, fmt.Sprintf("p.%d", position.Page)).FontSize(11) })
			if locate.Clicked() {
				v.jumpPosition(position)
				width, _ := c.Size()
				if v.showSource && s.scholarSinglePane(width) {
					v.showSource = false
					v.source.close()
				}
			}
		}
		s.scholarTrialPatchActions(c, p, p.agent.pendingPatch())
	})
}

func (s *Shell) scholarTrialPageControls(c *ui.Context, p *scholarState) {
	v := &p.agent.trial
	limit := max(v.pagesBefore, v.pagesAfter)
	ui.Row(c.Key("trial-pages")).FillWidth().Gap(4).Wrap().AlignItems(ui.Center).Children(func() {
		outside := func(step int) bool { return v.page+step < 1 || v.page+step > limit }
		change := func(step int, symbol, key string) {
			if s.scholarReviewGlyph(c.Key(key), symbol, s.tr(key)).Disabled(v.cancelPage != nil || outside(step)).Clicked() {
				s.requestScholarTrialPage(p, v.page+step, step)
			}
		}
		turn := func(step int, symbol, key string) {
			label := s.tr(key)
			if s.scholarReviewGlyph(c.Key(key), symbol, label).Disabled(v.searching || outside(step)).Clicked() {
				v.jumpPage(v.page + step)
			}
		}
		change(-1, "«", "scholarPreviousChange")
		turn(-1, "‹", "scholarPreviousPage")
		input := ui.TextInput(c.Key("trial-page-input"), &v.pageInput).Width(48).Label(s.tr("scholarGoToPage")).Disabled(v.searching)
		if input.Submitted() {
			page, err := strconv.Atoi(strings.TrimSpace(v.pageInput))
			if err == nil && page >= 1 && page <= limit {
				v.jumpPage(page)
			} else {
				v.pageInput = strconv.Itoa(v.page)
			}
		}
		ui.Text(c, fmt.Sprintf("/ %d", limit)).FontSize(11)
		turn(1, "›", "scholarNextPage")
		change(1, "»", "scholarNextChange")
		if s.scholarReviewIcon(c.Key("marks"), "file-diff", s.tr("scholarPDFMarkChanges"), "", v.markChanges).Clicked() {
			v.markChanges = !v.markChanges
		}
		if v.searching && ui.Button(c.Key("cancel-pages"), s.tr("scholarAgentCancelWork")).Clicked() {
			v.cancelPage()
		}
	})
}

func (s *Shell) scholarTrialPageGroup(c *ui.Context, v *scholarTrialView, page int) {
	result, ready := v.cached[page]
	ui.Column(c).FillWidth().AlignItems(ui.Center).Padding(4, 2, 14, 2).Children(func() {
		ui.Column(c.Key("sheet")).FillWidth().MaxWidth(900).Gap(4).Children(func() {
			if !ready {
				ui.Text(c, fmt.Sprintf("p.%d", page)).FontSize(11).TextColor(s.ch.muted)
				aspect := v.pageAspect
				if aspect <= 0 {
					aspect = .72
				}
				placeholder := ui.Box(c).FillWidth().AspectRatio(aspect).MinHeight(140).
					Background(c.Theme().Surface).Border(1, s.ch.separator).AlignItems(ui.Center).Justify(ui.Center)
				placeholder.Children(func() {
					label := "scholarPDFCheckingPages"
					if page == v.failedPage {
						label = "scholarPDFDiffPageFailed"
					}
					ui.Text(c, s.tr(label)).FontSize(11).TextColor(s.ch.muted)
				})
				return
			}
			s.scholarTrialImage(c, v, page, result)
		})
	})
}

func (s *Shell) scholarTrialImage(c *ui.Context, v *scholarTrialView, page int, result scholarTrialPage) {
	added, removed := page > v.pagesBefore, page > v.pagesAfter
	ui.Column(c).FillWidth().Gap(4).Children(func() {
		label := fmt.Sprintf("p.%d", page)
		if added {
			label += " · " + s.tr("scholarPDFPageAdded")
		} else if removed {
			label += " · " + s.tr("scholarPDFPageRemoved")
		} else if v.markChanges && !result.changed {
			label += " · " + s.tr("scholarPDFPageUnchanged")
		}
		ui.Text(c, label).FontSize(11).TextColor(s.ch.muted)
		_, h := result.bitmap.Size()
		ui.Column(c.Key("paper")).FillWidth().Background(ui.Hex("#ffffff")).Children(func() {
			var border ui.Color
			if v.markChanges {
				switch {
				case added:
					border = ui.Hex("#228252")
				case removed:
					border = ui.Hex("#b0443f")
				case result.dimensionsChanged:
					border = ui.Hex("#c89127")
				}
			}
			start := 0
			y := min(h-1, int(v.revealPDF.Y*float64(h)))
			slice := func(end int) {
				frame := s.scholarTrialPaperSlice(c.Key(start), result, start, end, v.markChanges, border)
				if v.revealPDF.Page == page && page == v.page && v.revealPDF.Y >= 0 && y >= start && y < end {
					area := frame.Bounds()
					viewport := v.pageViewport
					if area.H > 0 && viewport.H > 0 {
						guide := viewport.Y + min(64, viewport.H*.18)
						delta := area.Y + area.H*float32(y-start)/float32(end-start) - guide
						v.pageScroll.Y = max(0, v.pageScroll.Y+delta)
						v.revealPDF = typeset.PDFPosition{}
						c.Invalidate()
					}
				}
				start = end
			}
			if v.markChanges {
				for i, excerpt := range result.excerpts {
					slice(excerpt.at)
					s.scholarTrialOldExcerpt(c.Key(i).Key("before"), result, excerpt)
				}
			}
			slice(h)
		})
	})
}

func (s *Shell) scholarTrialPaperSlice(c *ui.Context, page scholarTrialPage, start, end int, marked bool, border ui.Color) ui.Element {
	if end <= start {
		return ui.Element{}
	}
	w, _ := page.bitmap.Size()
	return ui.Box(c).FillWidth().AspectRatio(float32(w) / float32(end-start)).Role(ui.RoleImage).Label(s.tr("compiledPage")).Draw(func(painter *ui.Painter, r ui.Rect) {
		src := ui.Rect{Y: float32(start), W: float32(w), H: float32(end - start)}
		painter.ImageRegion(page.bitmap, src, r, ui.ImageOptions{})
		if !marked {
			return
		}
		bounds := image.Rect(0, start, w, end)
		scholarTrialPDFMarks(painter, r, bounds, page.regions, ui.RGBA(232, 171, 40, .23))
		scholarTrialPDFMarks(painter, r, bounds, page.additions, ui.RGBA(45, 167, 99, .24))
		if border.A > 0 {
			painter.Stroke(r, border, 0, 1)
		}
	})
}

func scholarTrialPDFMarks(painter *ui.Painter, r ui.Rect, bounds image.Rectangle, regions []image.Rectangle, color ui.Color) {
	sx, sy := r.W/float32(bounds.Dx()), r.H/float32(bounds.Dy())
	for _, region := range regions {
		region = region.Intersect(bounds).Sub(bounds.Min)
		if !region.Empty() {
			box := ui.Rect{X: r.X + float32(region.Min.X)*sx, Y: r.Y + float32(region.Min.Y)*sy, W: float32(region.Dx()) * sx, H: float32(region.Dy()) * sy}
			painter.Fill(box, color, 1)
		}
	}
}

func (s *Shell) scholarTrialOldExcerpt(c *ui.Context, page scholarTrialPage, excerpt scholarTrialExcerpt) {
	pageWidth, _ := page.bitmap.Size()
	w, h := excerpt.bitmap.Size()
	left := float32(excerpt.x) / float32(pageWidth) * 100
	ink := ui.Hex("#94332f")
	label := "− " + s.tr("scholarBeforeEdit")
	ui.Column(c).FillWidth().Padding(2, 0, 4, 0).Children(func() {
		ui.Box(c).FillWidth().Height(17).Children(func() {
			ui.Text(c, label).Absolute().LeftPercent(min(left, 75)).Top(0).FontSize(10).TextColor(ink)
		})
		ui.Box(c).FillWidth().AspectRatio(float32(pageWidth) / float32(h)).Role(ui.RoleImage).Label(label).Draw(func(painter *ui.Painter, r ui.Rect) {
			scale := r.W / float32(pageWidth)
			box := ui.Rect{X: r.X + float32(excerpt.x)*scale, Y: r.Y, W: float32(w) * scale, H: r.H}
			painter.ImageRegion(excerpt.bitmap, ui.Rect{}, box, ui.ImageOptions{})
			scholarTrialPDFMarks(painter, box, image.Rect(0, 0, w, h), excerpt.marks, ui.RGBA(211, 94, 87, .24))
			painter.Fill(ui.Rect{X: box.X, Y: box.Y, W: 2, H: box.H}, ink, 1)
		})
	})
}

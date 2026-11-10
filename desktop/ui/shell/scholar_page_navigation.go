package shell

import (
	"fmt"
	"math"
	"path/filepath"

	"github.com/egoist/mygo/ui"
)

type scholarPageNavigation struct {
	open     bool
	progress float64
}

func (s *Shell) scholarPageStatus(c *ui.Context, p *scholarState, tab *Tab) {
	if p.references.catalog || p.agent.trial.show {
		return
	}
	width, _ := c.Size()
	if s.scholarInspectorVisible(p, width) && s.scholarInspectorPrimary(p, width) {
		return
	}
	if reading := p.references.focused; reading != nil {
		if reading.pages > 0 {
			s.scholarPageControl(c.Key(reading), &reading.pageNavigation, reading.title, reading.page, reading.pages, reading.setPage)
		}
		return
	}
	pane := s.compile
	if pane == nil || pane.tab != tab || pane.parkedPage != 0 || pane.pages == 0 {
		return
	}
	page := pane.page
	if first, last := pane.list.Visible(); first <= last {
		page = first + 1
	}
	if pane.sync.pdfScroll.MaxY > 0 && pane.list.AtEnd() {
		page = pane.pages
	}
	s.scholarPageControl(c.Key(pane), &pane.pageNavigation, filepath.Base(pane.path), page, pane.pages, pane.gotoPDFPage)
}

func (s *Shell) scholarPageControl(c *ui.Context, nav *scholarPageNavigation, title string, page, pages int, goTo func(int)) {
	page = max(1, min(page, pages))
	count := fmt.Sprintf("%d / %d", page, pages)
	label := s.tr("scholarGoToPage")
	button := ui.ButtonBase(c).Padding(1, 5).Radius(c.Theme().Radius).Shrink(0).FocusRing(true).
		Label(label + " · " + count).Tooltip(label + " · " + title)
	if nav.open || button.Hovered() {
		button.Background(s.ch.hover)
	}
	button.Children(func() {
		ui.Text(c, count).FontSize(11).TextColor(s.ch.muted).SingleLine()
	})
	if button.Clicked() {
		nav.open = !nav.open
		nav.progress = float64(page)
	}
	ui.Popover(c, button, &nav.open, func() {
		ui.Column(c).Width(212).Padding(8).Gap(4).Role(ui.RoleDialog).Label(label).Children(func() {
			ui.Text(c, title).FillWidth().MinWidth(0).FontSize(11).TextColor(s.ch.muted).SingleLine().Tooltip(title)
			ui.Row(c).FillWidth().Gap(4).AlignItems(ui.Center).Children(func() {
				prev, next := s.tr("scholarPreviousPage"), s.tr("scholarNextPage")
				if s.toolButton(c.Key("previous"), "chevron-left", prev, false).Label(prev).Disabled(page <= 1).Clicked() {
					goTo(page - 1)
				}
				if pages > 1 {
					progress := ui.Slider(c.Key("progress"), &nav.progress, 1, float64(pages)).Grow(1).MinWidth(0).Label(label)
					if progress.Changed() {
						if dest := int(math.Round(nav.progress)); dest != page {
							goTo(dest)
						}
					} else if !progress.Pressed() && nav.progress != float64(page) {
						nav.progress = float64(page)
						c.Invalidate()
					}
				} else {
					ui.Box(c).Grow(1)
				}
				if s.toolButton(c.Key("next"), "chevron-right", next, false).Label(next).Disabled(page >= pages).Clicked() {
					goTo(page + 1)
				}
			})
		})
	})
}

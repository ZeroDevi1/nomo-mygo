package shell

import (
	"context"
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
	"nomo/desktop/internal/scholar"
)

type scholarLocalSearch struct {
	enabled                          bool
	entries                          []scholar.Entry
	query                            string
	next, scanned, pdfs, unavailable int
	more                             bool
	library                          *scholar.Library
}

func (s *Shell) scholarLocalSearchResults(c *ui.Context, p *scholarState) []scholar.Entry {
	l := &p.literature
	v := &l.local
	fresh := v.query != l.query || v.library != p.library
	if fresh {
		v.entries = nil
		v.more = false
	}
	ui.Row(c).FillWidth().MinHeight(30).Padding(3, 8).Gap(6).AlignItems(ui.Center).Children(func() {
		status := s.tr("scholarLocalFulltextScope")
		if !fresh {
			status = fmt.Sprintf(s.tr("scholarFulltextScope"), v.scanned, v.pdfs, v.unavailable)
		}
		ui.Text(c, status).FontSize(11).TextColor(s.ch.muted).Wrap().Grow(1).MinWidth(0)
		if p.busy {
			if s.scholarCompactIcon(c.Key("cancel"), "x", s.tr("scholarAgentCancelWork"), false).Clicked() {
				p.cancelCurrent()
			}
		} else if v.more && ui.Button(c.Key("continue"), s.tr("scholarContinueScan")).
			Height(25).FontSize(12).Padding(0, 7).Shrink(0).Clicked() {
			s.searchScholarLocal(p, false)
		}
	})
	return v.entries
}

func (s *Shell) searchScholarLocal(p *scholarState, fresh bool) {
	v := &p.literature.local
	if fresh || len(v.entries) >= 100 {
		v.entries = nil
	}
	if fresh {
		v.next, v.scanned, v.pdfs, v.unavailable = 0, 0, 0, 0
		v.query, v.library = p.literature.query, p.library
		v.more = false
	}
	start, query, library, project := v.next, v.query, v.library, p.project
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		result, err := project.SearchLocalFulltext(ctx, library, query, start)
		return func() {
			if p.library != library {
				return
			}
			v.entries = append(v.entries, result.Entries...)
			v.next, v.more = result.Next, result.More
			v.scanned += result.Scanned
			v.pdfs += result.PDFs
			v.unavailable += result.Unavailable
			if len(v.entries) >= 100 {
				p.err = s.tr("scholarLocalResultLimit")
			}
		}, err
	})
}

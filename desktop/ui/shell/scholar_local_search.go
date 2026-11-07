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

func (s *Shell) scholarLocalSearchView(c *ui.Context, p *scholarState) bool {
	l := &p.literature
	v := &l.local
	ui.Checkbox(c, &v.enabled, s.tr("scholarLocalFulltext")).Disabled(p.busy)
	if !v.enabled {
		return false
	}
	ui.Text(c, s.tr("scholarLocalFulltextScope")).FontSize(11).Wrap()
	ui.TextInput(c, &l.query).FillWidth().Placeholder(s.tr("scholarCitationSearch"))
	fresh := v.query != l.query || v.library != p.library
	if fresh {
		v.entries = nil
		v.more = false
	}
	if ui.Button(c, s.tr("scholarSearch")).Disabled(p.busy || l.query == "").Clicked() {
		s.searchScholarLocal(p, true)
	}
	if !fresh {
		ui.Text(c, fmt.Sprintf(s.tr("scholarFulltextScope"), v.scanned, v.pdfs, v.unavailable)).FontSize(11).Wrap()
		if ui.Button(c, s.tr("scholarContinueScan")).Disabled(p.busy || !v.more).Clicked() {
			s.searchScholarLocal(p, false)
		}
	}
	if p.busy && ui.Button(c, s.tr("scholarAgentCancelWork")).Clicked() {
		p.cancelCurrent()
	}
	s.scholarEntries(c, p, v.entries, true)
	return true
}
func (s *Shell) searchScholarLocal(p *scholarState, fresh bool) {
	v := &p.literature.local
	if fresh || len(v.entries) >= 100 {
		v.entries = nil
	}
	if fresh {
		v.next, v.scanned, v.pdfs, v.unavailable = 0, 0, 0, 0
		v.query, v.library = p.literature.query, p.library
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

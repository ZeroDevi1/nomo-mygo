package shell

import (
	"context"
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
	"nomo/desktop/internal/scholar"
)

type scholarQueryState struct {
	library    *scholar.Library
	query      string
	entries    []scholar.Entry
	pending    bool
	generation uint64
	cancel     context.CancelFunc
	err        string
}

func (q *scholarQueryState) clear() {
	if q.cancel != nil {
		q.cancel()
	}
	*q = scholarQueryState{generation: q.generation + 1}
}

func (s *Shell) queryScholar(c *ui.Context, p *scholarState, q *scholarQueryState, query string, limit int) []scholar.Entry {
	library := p.library
	return s.queryScholarWith(p, q, query, func(ctx context.Context) ([]scholar.Entry, error) { return library.Query(ctx, query, limit) })
}

func (s *Shell) queryScholarZotero(p *scholarState, q *scholarQueryState, query string) []scholar.Entry {
	z := p.literature.zotero
	return s.queryScholarWith(p, q, fmt.Sprintf("zotero:%d:%s", z.Group, query), func(ctx context.Context) ([]scholar.Entry, error) {
		delay := time.NewTimer(250 * time.Millisecond)
		defer delay.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-delay.C:
		}
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		page, err := z.Search(ctx, query, "", 0)
		return page.Entries, err
	})
}

func (s *Shell) queryScholarWith(p *scholarState, q *scholarQueryState, query string, load func(context.Context) ([]scholar.Entry, error)) []scholar.Entry {
	if q.library == p.library && q.query == query {
		return q.entries
	}
	q.clear()
	q.library, q.query = p.library, query
	if p.library == nil {
		return nil
	}
	q.pending = true
	generation, library := q.generation, p.library
	ctx, cancel := context.WithCancel(p.ctx)
	q.cancel = cancel
	go func() {
		defer cancel()
		entries, err := load(ctx)
		s.post(func() {
			if s.scholar != p || q.generation != generation || p.library != library {
				return
			}
			q.pending, q.entries, q.cancel = false, entries, nil
			if err != nil {
				q.err = err.Error()
				p.err = q.err
			}
		})
	}()
	return nil
}

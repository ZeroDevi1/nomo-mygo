package shell

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"nomo/desktop/internal/scholar"
	"nomo/desktop/typeset"
)

type scholarTrialView struct {
	pending                       bool
	ownerPatch                    *scholarPatch
	ownerBatch                    *scholarPatchSet
	compare                       bool
	identities                    map[int]*Tab
	paths                         map[int]string
	paragraph                     *ui.Bitmap
	anchor                        typeset.PageAnchor
	before, after                 *ui.Bitmap
	pagesBefore, pagesAfter, page int
	pageBefore                    int
	split                         float32
	elapsed                       time.Duration
	show                          bool
	versions                      map[int]uint64
}

func (s *Shell) trialScholarPatchSet(p *scholarState, set *scholarPatchSet) {
	s.trialScholarHunks(p, append([]*scholarPatch(nil), set.hunks...))
}

func (s *Shell) trialScholarEdit(p *scholarState, revision *scholarChangeBatch) {
	s.trialScholarRevisions(p, nil, revision)
}
func (s *Shell) trialScholarHunks(p *scholarState, hunks []*scholarPatch) {
	s.trialScholarRevisions(p, hunks, nil)
}
func (s *Shell) trialScholarRevisions(p *scholarState, hunks []*scholarPatch, revision *scholarChangeBatch) {
	if p.busy || len(hunks) == 0 && revision == nil {
		return
	}
	if s.scholarTab(p.project.Main) == nil {
		prior := s.activeTab()
		s.OpenFiles([]string{p.project.Main})
		if s.scholarTab(p.project.Main) == nil {
			p.err = s.tr("scholarMainUnavailable")
			return
		}
		if isManuscript(prior) {
			s.activate(s.findTabIndex(prior.ID))
		}
	}
	snapshots := make(map[string]editor.Snapshot)
	versions := make(map[int]uint64)
	identities := make(map[int]*Tab)
	paths := make(map[int]string)
	total := 0
	for _, t := range s.tabs {
		if t.Path == "" {
			continue
		}
		name, err := scholar.Relative(p.project.Root, t.Path)
		if err != nil {
			continue
		}
		if !t.plain {
			t.syncFromSource()
			t.syncToSource()
		}
		snap := t.Source().Buffer.Snapshot()
		total += snap.Len()
		if total > 16<<20 {
			p.err = s.tr("scholarTrialBufferLimit")
			return
		}
		snapshots[name] = snap
		versions[t.ID] = snap.Version()
		identities[t.ID], paths[t.ID] = t, t.Path
	}
	for _, h := range hunks {
		if !s.scholarTargetValid(h.target) {
			p.err = s.tr("scholarStaleBaseline")
			return
		}
	}
	if revision != nil {
		t := s.tabWithID(revision.change.tabID)
		if t == nil || !samePath(t.Path, revision.change.path) || t.Source().Buffer.Version() != revision.after.Version() {
			p.err = s.tr("scholarStaleBaseline")
			return
		}
	}
	project := p.project
	ownerPatch, ownerBatch := p.agent.patch, p.agent.batch
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		ctx, cancel := context.WithTimeout(ctx, 150*time.Second)
		defer cancel()
		inputs := func(after bool) map[string]io.Reader {
			out := make(map[string]io.Reader)
			for name, snap := range snapshots {
				if revision != nil {
					n, _ := scholar.Relative(project.Root, revision.change.path)
					if name == n {
						chosen := revision.before
						if after {
							chosen = revision.after
						}
						out[name] = &scholarSnapshotReader{snap: chosen}
						continue
					}
				}
				if !after {
					out[name] = &scholarSnapshotReader{snap: snap}
					continue
				}
				var edits []editor.Edit
				for _, h := range hunks {
					n, _ := scholar.Relative(project.Root, h.target.path)
					if n == name {
						edits = append(edits, editor.Edit{Start: h.target.start, End: h.target.end, Text: h.next})
					}
				}
				if len(edits) == 0 {
					out[name] = &scholarSnapshotReader{snap: snap}
					continue
				}
				// The source budget is checked before making the temporary edited rope.
				buf := editor.NewBuffer(snap.String())
				buf.Edit(edits...)
				out[name] = &scholarSnapshotReader{snap: buf.Snapshot()}
			}
			return out
		}
		started := time.Now()
		before, err := typeset.OpenProjectTrial(ctx, project.Root, project.Main, inputs(false))
		if err != nil {
			return nil, err
		}
		defer before.Close()
		nBefore, err := before.CompileProjectTrial(ctx)
		if err != nil {
			return nil, err
		}
		mainName, _ := scholar.Relative(project.Root, project.Main)
		after, err := typeset.OpenProjectTrial(ctx, before.TrialRoot(), filepath.Join(before.TrialRoot(), mainName), inputs(true))
		if err != nil {
			return nil, err
		}
		defer after.Close()
		nAfter, err := after.CompileProjectTrial(ctx)
		if err != nil {
			return nil, err
		}
		text := ""
		if len(hunks) > 0 {
			text = hunks[0].next
		} else {
			text = revision.change.after
		}
		anchor := after.Anchor(ctx, strings.TrimSpace(text))
		page := max(1, anchor.Page)
		var paragraph *ui.Bitmap
		if len(hunks) == 1 {
			mainName, _ := scholar.Relative(project.Root, project.Main)
			if main, ok := snapshots[mainName]; ok {
				header := main.Slice(0, min(main.Len(), 8<<10))
				if img := after.Paragraph(ctx, header, text); img != nil {
					paragraph = ui.NewBitmap(img)
				}
			}
		}
		page = min(max(1, page), nAfter)
		b, err := before.PageAtSize(ctx, min(page, nBefore), 1200)
		if err != nil {
			return nil, err
		}
		a, err := after.PageAtSize(ctx, page, 1200)
		if err != nil {
			return nil, err
		}
		elapsed := time.Since(started)
		return func() {
			if revision == nil {
				if ownerPatch != nil && p.agent.patch != ownerPatch {
					return
				}
				if ownerBatch != nil && p.agent.batch != ownerBatch {
					return
				}
				if ownerPatch == nil && ownerBatch == nil {
					return
				}
				if ownerBatch != nil {
					for _, h := range hunks {
						found := false
						for _, current := range ownerBatch.hunks {
							if current == h {
								found = true
								break
							}
						}
						if !found {
							return
						}
					}
				}
			}
			for id, v := range versions {
				t := s.tabWithID(id)
				if t == nil || t != identities[id] || !samePath(t.Path, paths[id]) || t.Source().Buffer.Version() != v {
					p.err = s.tr("scholarStaleBaseline")
					return
				}
			}
			for _, h := range hunks {
				if !s.scholarTargetValid(h.target) {
					return
				}
			}
			p.agent.trial = scholarTrialView{before: ui.NewBitmap(b), after: ui.NewBitmap(a), pagesBefore: nBefore, pagesAfter: nAfter, page: page, pageBefore: min(page, nBefore), elapsed: elapsed, show: true, versions: versions, identities: identities, paths: paths, pending: revision == nil, ownerPatch: ownerPatch, ownerBatch: ownerBatch, compare: revision != nil, paragraph: paragraph, anchor: anchor}
			p.changes.compare = false
			p.compactPDF = true
			if !isManuscript(s.activeTab()) {
				if t := s.scholarTab(project.Main); t != nil {
					s.activate(s.findTabIndex(t.ID))
				}
			}
			if t := s.activeTab(); t != nil {
				t.typesetMode = typesetSplit
			}
		}, nil
	})
}

func (s *Shell) scholarTrialPaper(c *ui.Context, p *scholarState) bool {
	v := &p.agent.trial
	if !v.show || v.after == nil {
		return false
	}
	ui.Column(c).Fill().MinWidth(0).Background(c.Theme().SurfaceHover).Padding(8).Gap(6).Children(func() {
		ui.Row(c).Gap(4).FillWidth().Children(func() {
			ui.Text(c, fmt.Sprintf("%s · %d → %d · %d ms", s.tr("scholarTrialComparison"), v.pagesBefore, v.pagesAfter, v.elapsed.Milliseconds())).FontSize(11).Grow(1)
			if ui.Button(c, s.tr("scholarComparePDFs")).Clicked() {
				v.compare = !v.compare
			}
			if ui.Button(c, s.tr("scholarCloseTrial")).Clicked() {
				*v = scholarTrialView{}
			}
		})
		ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
			ui.Row(c).FillWidth().Gap(8).Children(func() {
				for i, img := range []*ui.Bitmap{v.before, v.after} {
					if i == 0 && !v.compare {
						continue
					}
					ck := c.Key(i)
					ui.Column(ck).Grow(1).Width(0).MinWidth(0).Gap(4).Children(func() {
						label := s.tr("scholarBeforeEdit")
						if i == 1 {
							label = s.tr("scholarAfterEdit")
							if v.pending {
								label = s.tr("scholarPendingProposal")
							}
						}
						pageNo := v.page
						if i == 0 {
							pageNo = v.pageBefore
						}
						ui.Text(ck, label+fmt.Sprintf(" · p.%d", pageNo)).FontSize(11)
						w, h := img.Size()
						page := ui.Box(ck).FillWidth().AspectRatio(float32(w) / float32(h))
						page.Children(func() {
							ui.Image(ck, img).Fill()
							if i == 1 && v.anchor.Page == v.page && v.anchor.PageW > 0 {
								scale := page.Bounds().W / float32(v.anchor.PageW)
								a := v.anchor
								ui.Box(ck).Absolute().Left(float32(a.X)*scale).Top(float32(a.Y)*scale).Width(float32(a.W)*scale).Height(float32(a.H)*scale).Border(2, scholarInk(c, "diff"))
							}
						})
					})
				}
			})
		})
		if v.paragraph != nil {
			ui.Text(c, s.tr("liveParagraph")).FontSize(11)
			w, h := v.paragraph.Size()
			ui.Image(c, v.paragraph).FillWidth().AspectRatio(float32(w) / float32(h))
		}
		if v.anchor.Page == 0 {
			ui.Text(c, s.tr("scholarPDFLocationUnknown")).FontSize(11).Wrap()
		}
	})
	return true
}

// Read bounded rope slices without materializing another full source string.
type scholarSnapshotReader struct {
	snap editor.Snapshot
	at   int
}

func (r *scholarSnapshotReader) Read(p []byte) (int, error) {
	if r.at >= r.snap.Len() {
		return 0, io.EOF
	}
	n := min(len(p), r.snap.Len()-r.at, 32<<10)
	copy(p, r.snap.Slice(r.at, r.at+n))
	r.at += n
	return n, nil
}

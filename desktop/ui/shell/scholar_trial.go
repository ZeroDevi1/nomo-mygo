package shell

import (
	"context"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"nomo/desktop/internal/scholar"
	"nomo/desktop/typeset"
)

type scholarTrialView struct {
	pending                       bool
	allPending                    bool
	ownerPatch                    *scholarPatch
	ownerBatch                    *scholarPatchSet
	identities                    map[int]*Tab
	paths                         map[int]string
	anchors                       map[*scholarPatch]typeset.PageAnchor
	pdfs                          *typeset.TrialPDFPair
	sources                       []scholarTrialSource
	source                        scholarTrialSourceState
	pageList                      ui.ListState
	cached                        map[int]scholarTrialPage
	cacheBytes                    int
	firstVisible, failedPage      int
	pageAspect                    float32
	pagesBefore, pagesAfter, page int
	tabID                         int
	pageInput, pageError          string
	cancelPage                    context.CancelFunc
	markChanges, searched         bool
	searching                     bool
	split                         float32
	show                          bool
	versions                      map[int]uint64
}

func (v *scholarTrialView) close() {
	v.source.close()
	if v.cancelPage != nil {
		v.cancelPage()
	}
	if v.pdfs != nil {
		v.pdfs.Close()
	}
	*v = scholarTrialView{}
}

func (s *Shell) trialScholarPatchSet(p *scholarState, set *scholarPatchSet) {
	s.trialScholarHunks(p, append([]*scholarPatch(nil), set.hunks...))
}

func (s *Shell) resumeScholarTrial(p *scholarState, all bool) {
	if set := p.agent.batch; set != nil && all {
		s.trialScholarPatchSet(p, set)
	} else if patch := p.agent.pendingPatch(); patch != nil {
		s.trialScholarPatch(p, patch)
	}
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
	allPending := revision == nil && ownerBatch != nil && len(hunks) == len(ownerBatch.hunks)
	reviewTab := s.activeTab()
	selectedPatch := p.agent.pendingPatch()
	p.agent.trial.close()
	p.agent.trialPreparing = true
	s.scholarOwnedJob(p, func(ctx context.Context, _ uint64) (func(), func(), error) {
		ctx, cancel := context.WithTimeout(ctx, 150*time.Second)
		defer cancel()
		var sources []scholarTrialSource
		inputs := func(after bool) map[string]io.Reader {
			out := make(map[string]io.Reader)
			for name, snap := range snapshots {
				if revision != nil {
					n, _ := scholar.Relative(project.Root, revision.change.path)
					if name == n {
						chosen := revision.before
						if after {
							chosen = revision.after
							sources = append(sources, scholarTrialSource{path: name, before: revision.before, after: chosen})
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
				var patches []*scholarPatch
				for _, h := range hunks {
					n, _ := scholar.Relative(project.Root, h.target.path)
					if n == name {
						edits = append(edits, editor.Edit{Start: h.target.start, End: h.target.end, Text: h.next})
						patches = append(patches, h)
					}
				}
				if len(edits) == 0 {
					out[name] = &scholarSnapshotReader{snap: snap}
					continue
				}
				// The source budget is checked before making the temporary edited rope.
				buf := editor.NewBuffer(snap.String())
				buf.Edit(edits...)
				proposed := buf.Snapshot()
				sources = append(sources, scholarTrialSource{path: name, before: snap, after: proposed, patches: patches})
				out[name] = &scholarSnapshotReader{snap: proposed}
			}
			return out
		}
		before, err := typeset.OpenProjectTrial(ctx, project.Root, project.Main, inputs(false))
		if err != nil {
			return nil, nil, err
		}
		defer before.Close()
		nBefore, err := before.CompileProjectTrial(ctx)
		if err != nil {
			return nil, nil, err
		}
		mainName, _ := scholar.Relative(project.Root, project.Main)
		after, err := typeset.OpenProjectTrial(ctx, before.TrialRoot(), filepath.Join(before.TrialRoot(), mainName), inputs(true))
		if err != nil {
			return nil, nil, err
		}
		defer after.Close()
		nAfter, err := after.CompileProjectTrial(ctx)
		if err != nil {
			return nil, nil, err
		}
		var anchor typeset.PageAnchor
		anchors := make(map[*scholarPatch]typeset.PageAnchor, len(hunks))
		if len(hunks) == 0 {
			anchor = after.Anchor(ctx, strings.TrimSpace(revision.change.after))
		} else {
			for _, h := range hunks {
				a := after.Anchor(ctx, strings.TrimSpace(h.next))
				if a.Page == 0 && strings.TrimSpace(h.next) == "" {
					a = before.Anchor(ctx, strings.TrimSpace(h.target.before))
				}
				anchors[h] = a
			}
			initial := selectedPatch
			if _, ok := anchors[initial]; !ok {
				initial = hunks[0]
			}
			selectedPatch, anchor = initial, anchors[initial]
		}
		page := min(max(1, anchor.Page), nAfter)
		pdfs, err := typeset.KeepTrialPDFs(before, after)
		if err != nil {
			return nil, nil, err
		}
		diff, err := pdfs.DiffPage(ctx, page)
		if err != nil {
			pdfs.Close()
			return nil, nil, err
		}
		result := newScholarTrialPage(diff)
		sourceIndex := scholarTrialInitialSource(sources, selectedPatch)
		var sourceView *editor.DiffState
		var sourceHunks []editor.DiffHunk
		var sourceTargets []int
		if sourceIndex >= 0 {
			sourceView, sourceHunks, sourceTargets = sources[sourceIndex].diff()
		}
		return func() {
			installed := false
			defer func() {
				if !installed {
					pdfs.Close()
				}
			}()
			if s.activeTab() != reviewTab || p.references.catalog || p.references.focused != nil || p.accountOpen || p.winSettings != nil {
				return
			}
			if revision == nil {
				if len(hunks) == 1 && p.agent.pendingPatch() != hunks[0] {
					return
				}
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
			p.agent.reviewHidden = true
			p.changes.compare = false
			s.closeScholarInspector(p)
			p.compactNav, p.winCompactProject = false, false
			if !isManuscript(s.activeTab()) {
				if t := s.scholarTab(project.Main); t != nil {
					s.activate(s.findTabIndex(t.ID))
				}
			}
			if t := s.activeTab(); isManuscript(t) {
				p.agent.trial = scholarTrialView{
					pdfs: pdfs, pagesBefore: nBefore, pagesAfter: nAfter,
					page: page, pageInput: strconv.Itoa(page), tabID: t.ID,
					versions: versions, identities: identities, paths: paths, anchors: anchors,
					show: true, markChanges: true, pending: revision == nil, allPending: allPending,
					ownerPatch: ownerPatch, ownerBatch: ownerBatch,
					sources: sources, source: scholarTrialSourceState{index: sourceIndex, diff: sourceView, hunks: sourceHunks, targets: sourceTargets},
					firstVisible: -1,
				}
				p.agent.trial.keepPage(page, result)
				p.agent.trial.jumpPage(page)
				p.agent.trial.source.reveal(selectedPatch)
				installed = true
				s.focusEditor = true
			}
		}, pdfs.Close, nil
	})
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

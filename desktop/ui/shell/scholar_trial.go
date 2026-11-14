package shell

import (
	"context"
	"io"
	"path/filepath"
	"slices"
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
	anchors                       map[*scholarPatch]typeset.PDFPosition
	revealPDF                     typeset.PDFPosition
	pdfs                          *typeset.TrialPDFPair
	sources                       []scholarTrialSource
	source                        scholarTrialSourceState
	pageList                      ui.ListState
	pageScroll                    ui.ScrollState
	pageViewport                  ui.Rect
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
	show, showSource              bool
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
	s.trialScholarHunks(p, slices.Clone(set.hunks))
}

func (s *Shell) resumeScholarTrial(p *scholarState, all, showSource bool) {
	if set := p.agent.batch; set != nil && all {
		s.trialScholarRevisions(p, slices.Clone(set.hunks), nil, showSource)
	} else if patch := p.agent.pendingPatch(); patch != nil {
		s.trialScholarRevisions(p, []*scholarPatch{patch}, nil, showSource)
	}
}

func (s *Shell) trialScholarEdit(p *scholarState, revision *scholarChangeBatch) {
	s.trialScholarRevisions(p, nil, revision, true)
}
func (s *Shell) trialScholarHunks(p *scholarState, hunks []*scholarPatch) {
	s.trialScholarRevisions(p, hunks, nil, false)
}
func (s *Shell) trialScholarRevisions(p *scholarState, hunks []*scholarPatch, revision *scholarChangeBatch, showSource bool) {
	if p.busy || len(hunks) == 0 && revision == nil || !s.canPreviewTypeset(s.activeTab()) {
		return
	}
	reviewTab := s.activeTab()
	project := p.project
	if typeset.IsLaTeXPath(reviewTab.Path) != typeset.IsLaTeXPath(project.Main) {
		project.Main = reviewTab.Path
	}
	if s.scholarTab(project.Main) == nil {
		s.OpenFiles([]string{project.Main})
		if s.scholarTab(project.Main) == nil {
			p.err = s.tr("scholarMainUnavailable")
			return
		}
		s.activate(s.findTabIndex(reviewTab.ID))
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
	patchesByFile := make(map[string][]scholarTrialPatch)
	for _, h := range hunks {
		if !s.scholarTargetValid(h.target) {
			p.err = s.tr("scholarStaleBaseline")
			return
		}
		name, err := scholar.Relative(project.Root, h.target.path)
		if err != nil {
			p.err = err.Error()
			return
		}
		if _, ok := snapshots[name]; !ok {
			p.err = s.tr("scholarStaleBaseline")
			return
		}
		patchesByFile[name] = append(patchesByFile[name], scholarTrialPatch{patch: h, target: h.target, next: h.next})
	}
	if revision != nil {
		t := s.tabWithID(revision.change.tabID)
		if t == nil || !samePath(t.Path, revision.change.path) || t.Source().Buffer.Version() != revision.after.Version() {
			p.err = s.tr("scholarStaleBaseline")
			return
		}
	}
	ownerPatch, ownerBatch := p.agent.patch, p.agent.batch
	allPending := revision == nil && ownerBatch != nil && len(hunks) == len(ownerBatch.hunks)
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
				patches := patchesByFile[name]
				if len(patches) == 0 {
					out[name] = &scholarSnapshotReader{snap: snap}
					continue
				}
				edits := make([]editor.Edit, 0, len(patches))
				for _, h := range patches {
					edits = append(edits, editor.Edit{Start: h.target.start, End: h.target.end, Text: h.next})
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
		position := func(session *typeset.Session, path string, snap editor.Snapshot, offset int, text string) typeset.PDFPosition {
			if a := session.Anchor(ctx, strings.TrimSpace(text)); a.Page > 0 {
				return typeset.PDFPosition{Page: a.Page, Y: a.Y / a.PageH}
			}
			if typeset.IsLaTeXPath(project.Main) && ctx.Err() == nil {
				line := snap.LineOf(offset)
				return session.PositionForProjectLine(ctx, path, line, strings.TrimSuffix(strings.TrimPrefix(snap.Line(line), "\ufeff"), "\r"))
			}
			return typeset.PDFPosition{}
		}
		var initial typeset.PDFPosition
		anchors := make(map[*scholarPatch]typeset.PDFPosition, len(hunks))
		if len(hunks) == 0 {
			if a := after.Anchor(ctx, strings.TrimSpace(revision.change.after)); a.Page > 0 {
				initial = typeset.PDFPosition{Page: a.Page, Y: a.Y / a.PageH}
			}
		} else {
			for _, source := range sources {
				shift := 0
				for _, h := range source.patches {
					if strings.TrimSpace(h.next) == "" {
						anchors[h.patch] = position(before, h.target.path, source.before, h.target.start, h.target.before)
					} else {
						path := filepath.Join(before.TrialRoot(), source.path)
						anchors[h.patch] = position(after, path, source.after, h.target.start+shift, h.next)
					}
					shift += len(h.next) - (h.target.end - h.target.start)
				}
			}
			if _, ok := anchors[selectedPatch]; !ok {
				selectedPatch = hunks[0]
			}
			initial = anchors[selectedPatch]
		}
		page := min(max(1, initial.Page), max(nBefore, nAfter))
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
		slices.SortFunc(sources, func(a, b scholarTrialSource) int { return strings.Compare(a.path, b.path) })
		sourceIndex := -1
		var sourceView *editor.DiffState
		var sourceHunks []editor.DiffHunk
		var sourceTargets []int
		if showSource && len(sources) > 0 {
			sourceIndex = scholarTrialInitialSource(sources, selectedPatch)
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
					show: true, showSource: showSource, markChanges: true, pending: revision == nil, allPending: allPending,
					ownerPatch: ownerPatch, ownerBatch: ownerBatch,
					sources: sources, source: scholarTrialSourceState{index: sourceIndex, diff: sourceView, hunks: sourceHunks, targets: sourceTargets},
					firstVisible: -1,
				}
				p.agent.trial.keepPage(page, result)
				p.agent.trial.jumpPage(page)
				p.agent.trial.revealPDF = initial
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

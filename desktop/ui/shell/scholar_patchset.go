package shell

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"nomo/desktop/internal/scholar"
)

type scholarPatchSet struct {
	summary  string
	hunks    []*scholarPatch
	selected int
}

type scholarAppliedFile struct {
	id      int
	path    string
	version uint64
}

type scholarContextFile struct {
	target scholarTarget
}

func (s *Shell) scholarFileContexts(c *ui.Context, p *scholarState) {
	a := &p.agent
	for _, t := range s.tabs {
		if !isManuscript(t) {
			continue
		}
		if _, err := scholar.Relative(p.project.Root, t.Path); err != nil {
			continue
		}
		exists := false
		for _, f := range a.files {
			if f.target.tabID == t.ID {
				exists = true
				break
			}
		}
		if (!exists || !a.multi) && ui.Button(c.Key(t.ID), "+ "+t.Name(s.untitledName)).Disabled(p.busy).Clicked() {
			if exists {
				a.multi, a.attachments = true, false
				a.resetSession()
				continue
			}
			snap := t.Source().Buffer.Snapshot()
			total := snap.Len()
			for _, f := range a.files {
				total += len(f.target.before)
			}
			if total > 32<<10 {
				p.err = s.tr("scholarFileContextLimit")
				continue
			}
			text := snap.String()
			a.files = append(a.files, scholarContextFile{target: scholarTarget{tabID: t.ID, path: t.Path, start: 0, end: snap.Len(), version: snap.Version(), before: text, digest: sha256.Sum256([]byte(text))}})
			a.multi, a.attachments = true, false
			p.err = ""
			a.resetSession()
		}
	}
}

func (s *Shell) scholarAttachedFiles(c *ui.Context, p *scholarState) {
	a := &p.agent
	if !a.multi {
		return
	}
	remove := -1
	for i := range a.files {
		f := &a.files[i]
		ck := c.Key("context-" + fmt.Sprint(f.target.tabID))
		ui.Row(ck).FillWidth().Gap(4).Children(func() {
			name, _ := scholar.Relative(p.project.Root, f.target.path)
			ui.Text(ck, fmt.Sprintf("%s · %d B", name, len(f.target.before))).SingleLine().MinWidth(0).Shrink(1).FontSize(12)
			if s.toolButton(ck.Key("remove"), "x", s.tr("scholarRemoveAttachment"), false).Disabled(p.busy).Clicked() {
				remove = i
			}
		})
	}
	if remove >= 0 {
		a.resetSession()
		a.files = slices.Delete(a.files, remove, remove+1)
	}
}

func (s *Shell) sendScholarPatchSet(p *scholarState) {
	a := &p.agent
	files := make(map[string]string)
	bases := make(map[string]scholarTarget)
	for _, f := range a.files {
		if !s.scholarTargetValid(f.target) {
			p.err = s.tr("scholarStaleBaseline")
			return
		}
		name, err := scholar.Relative(p.project.Root, f.target.path)
		if err == nil {
			_, err = p.project.Path(name)
		}
		if err != nil {
			p.err = err.Error()
			return
		}
		files[name], bases[name] = f.target.before, f.target
	}
	if len(files) == 0 {
		p.err = s.tr("scholarChooseFiles")
		return
	}
	contextParts := make(map[string]string)
	for i, part := range a.parts {
		if i > 0 && a.include[i] {
			contextParts[scholarContextNames[i]] = part
		}
	}
	payload, _ := json.Marshal(map[string]any{"instruction": a.instruction, "files": files, "context": contextParts, "previous": a.conversation})
	model := ""
	if a.model > 0 && a.model <= len(a.info.Models) {
		model = a.info.Models[a.model-1].Model
		if model == "" {
			model = a.info.Models[a.model-1].ID
		}
	}
	instruction := a.instruction
	if a.session == nil {
		a.session = scholar.NewCodexConversation(p.ctx)
	}
	session := a.session
	a.running, a.stream = true, ""
	s.scholarJob(p, func(ctx context.Context, job uint64) (func(), error) {
		proposal, err := session.ProposePatch(ctx, model, string(payload), func(delta string) {
			s.post(func() {
				if s.scholar == p && p.job == job && a.running {
					a.stream = delta
				}
			})
		})
		set := &scholarPatchSet{summary: proposal.Summary}
		if err == nil {
			extra := scholarReviewMaxFragments - len(proposal.Edits)
			grouped := make(map[string][]scholar.PatchEdit)
			for _, e := range proposal.Edits {
				if _, ok := bases[e.Path]; !ok {
					err = fmt.Errorf("proposal references an unattached file %q", e.Path)
					break
				}
				grouped[e.Path] = append(grouped[e.Path], e)
			}
			for path, edits := range grouped {
				if err != nil {
					break
				}
				base := bases[path]
				spans, e := scholar.LocateEdits(base.before, edits)
				if e != nil {
					err = e
					break
				}
				for i, span := range spans {
					target := base
					target.start, target.end = span.Start, span.End
					target.before = strings.Clone(base.before[span.Start:span.End])
					target.digest = sha256.Sum256([]byte(target.before))
					target.line = strings.Count(base.before[:span.Start], "\n")
					nextStart := -1
					if i+1 < len(spans) {
						nextStart = spans[i+1].Start
					}
					parts := splitScholarReview(target, span.After, extra+1, nextStart)
					extra -= len(parts) - 1
					set.hunks = append(set.hunks, parts...)
				}
			}
			sort.Slice(set.hunks, func(i, j int) bool {
				a, b := set.hunks[i].target, set.hunks[j].target
				if a.path == b.path {
					return a.start < b.start
				}
				return a.path < b.path
			})
		}
		return func() {
			a.running, a.stream = false, ""
			if err != nil {
				return
			}
			for _, base := range bases {
				if !s.scholarTargetValid(base) {
					p.err = s.tr("scholarStaleBaseline")
					return
				}
			}
			a.batch = set
			// Retain a bounded, visible conversation summary across isolated turns.
			a.conversation = trimScholarConversation(a.conversation + "\nUser: " + instruction + "\nProposal: " + proposal.Summary)
			s.presentScholarReview(p)
		}, err
	})
}

func trimScholarConversation(text string) string {
	if len(text) > 8<<10 {
		text = text[len(text)-(8<<10):]
		for len(text) > 0 && text[0]&0xc0 == 0x80 {
			text = text[1:]
		}
	}
	return strings.Clone(text)
}

func (s *Shell) scholarPatchSetView(c *ui.Context, p *scholarState, set *scholarPatchSet) {
	if len(set.hunks) == 0 {
		p.agent.batch = nil
		return
	}
	set.selected = min(set.selected, len(set.hunks)-1)
	if set.summary != "" {
		ui.Text(c, set.summary).Wrap().FontSize(12)
	}
	ui.Row(c).Gap(4).Children(func() {
		if ui.Button(c, "↑").Disabled(p.busy).Clicked() {
			s.navigateScholarHunk(p, -1)
		}
		ui.Text(c, fmt.Sprintf("%d / %d", set.selected+1, len(set.hunks))).FontSize(11)
		if ui.Button(c, "↓").Disabled(p.busy).Clicked() {
			s.navigateScholarHunk(p, 1)
		}
	})
	patch := p.agent.pendingPatch()
	stale := !s.scholarTargetCurrent(patch.target)
	ui.Text(c, s.scholarReviewLabel(patch)).Wrap().TextColor(scholarInk(c, "agent")).FontSize(11)
	if stale {
		ui.Text(c, s.tr("scholarBaselineChanged")).FontSize(11).TextColor(scholarInk(c, "warning"))
		if ui.Button(c, s.tr("scholarAgentReject")).Disabled(p.busy).Clicked() {
			s.rejectScholarHunk(p)
			return
		}
	} else if ui.Button(c, s.tr("scholarShowReview")).Clicked() {
		s.openScholarReview(p)
	}
	ui.Row(c).Gap(4).Children(func() {
		if ui.Button(c, s.tr("scholarAcceptFile")).Disabled(p.busy || stale).Clicked() {
			var chosen []*scholarPatch
			for _, h := range set.hunks {
				if h.target.path == patch.target.path {
					chosen = append(chosen, h)
				}
			}
			s.acceptScholarHunks(p, chosen)
		}
		if ui.Button(c, s.tr("scholarAcceptAll")).Disabled(p.busy).Clicked() {
			s.acceptScholarHunks(p, append([]*scholarPatch(nil), set.hunks...))
		}
	})
	if ui.Button(c, s.tr("scholarRejectAll")).Disabled(p.busy).Clicked() {
		p.agent.batch = nil
		if p.agent.trial.pending {
			p.agent.trial.close()
		}
	}
	if ui.Button(c, s.tr("scholarPDFDiffAll")).Disabled(p.busy).Clicked() {
		s.trialScholarPatchSet(p, set)
	}
	if p.agent.trialPreparing && ui.Button(c, s.tr("scholarAgentCancelWork")).Clicked() {
		p.cancelCurrent()
	}
}

func (s *Shell) rejectScholarHunk(p *scholarState) {
	set := p.agent.batch
	if set == nil || p.busy || len(set.hunks) == 0 {
		return
	}
	i := min(set.selected, len(set.hunks)-1)
	resume, all := p.agent.trial.show && p.agent.trial.pending, p.agent.trial.allPending
	advance := resume || !p.agent.reviewHidden && s.activeTab() != nil && s.activeTab().ID == set.hunks[i].target.tabID
	set.hunks = slices.Delete(set.hunks, i, i+1)
	if p.agent.trial.pending {
		p.agent.trial.close()
	}
	if len(set.hunks) == 0 {
		p.agent.batch = nil
	} else {
		set.selected = min(i, len(set.hunks)-1)
		if advance {
			s.openScholarReview(p)
			if resume {
				s.resumeScholarTrial(p, all)
			}
		}
	}
}

func (s *Shell) navigateScholarHunk(p *scholarState, delta int) {
	if p.agent.pendingPatch() != nil {
		if p.busy {
			return
		}
		if set := p.agent.batch; set != nil {
			set.selected = (set.selected + len(set.hunks) + delta) % len(set.hunks)
		}
		s.openScholarReview(p)
		return
	}
	s.navigateScholarChange(p, delta)
}

// All selected files pass preflight before any buffer is changed. Buffer.Edit
// records each file's hunks as one undo step. Cross-file rollback checks every
// resulting version before touching any file.
func (s *Shell) acceptScholarHunks(p *scholarState, chosen []*scholarPatch) {
	if p.busy || p.agent.batch == nil || len(chosen) == 0 {
		return
	}
	set, library := p.agent.batch, p.library
	type fileEdit struct {
		tab   *Tab
		path  string
		snap  editor.Snapshot
		edits []editor.Edit
	}
	files := make(map[int]*fileEdit)
	for _, h := range chosen {
		if !s.scholarTargetValid(h.target) {
			p.err = s.tr("scholarStaleBaseline")
			return
		}
		t := s.tabWithID(h.target.tabID)
		f := files[t.ID]
		if f == nil {
			f = &fileEdit{tab: t, path: t.Path, snap: t.Source().Buffer.Snapshot()}
			files[t.ID] = f
		}
		f.edits = append(f.edits, editor.Edit{Start: h.target.start, End: h.target.end, Text: h.next})
	}
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		for _, h := range chosen {
			if err := validateScholarPatchReferences(ctx, library, files[h.target.tabID].snap, h.target, h.next); err != nil {
				return nil, err
			}
		}
		return func() {
			if p.agent.batch != set || p.library != library {
				return
			}
			for _, f := range files {
				current := s.tabWithID(f.tab.ID)
				if current != f.tab || !samePath(current.Path, f.path) || current.Source().Buffer.Version() != f.snap.Version() {
					p.err = s.tr("scholarStaleBaseline")
					return
				}
			}
			if t := s.activeTab(); t != nil {
				s.captureScholarChangeSnapshot(p, t.Source().Buffer.Snapshot(), time.Now())
				s.flushScholarChanges(p)
			}
			var applied []scholarAppliedFile
			for _, f := range files {
				sort.Slice(f.edits, func(i, j int) bool { return f.edits[i].Start < f.edits[j].Start })
				f.tab.Source().Buffer.Edit(f.edits...)
				after := f.tab.Source().Buffer.Snapshot()
				applied = append(applied, scholarAppliedFile{f.tab.ID, f.tab.Path, after.Version()})
				d := &p.changes
				d.nextID++
				d.waiting = append(d.waiting, scholarChangeBatch{change: &scholarChange{id: d.nextID, tabID: f.tab.ID, path: f.tab.Path, origin: "codex"}, before: f.snap, after: after})
				if d.tabID == f.tab.ID {
					d.baseline, d.observed, d.due = after, after, time.Time{}
				}
			}
			p.agent.applied = applied
			resume, all := p.agent.trial.show && p.agent.trial.pending, p.agent.trial.allPending
			p.agent.trial.close()
			selected := p.agent.pendingPatch()
			advance := resume || !p.agent.reviewHidden && slices.Contains(chosen, selected) && s.activeTab() != nil && s.activeTab().ID == selected.target.tabID
			remaining := set.hunks[:0]
			nextIndex := 0
			for i, h := range set.hunks {
				accepted := false
				for _, c := range chosen {
					if c == h {
						accepted = true
						break
					}
				}
				if accepted {
					continue
				}
				if i < set.selected {
					nextIndex++
				}
				if f := files[h.target.tabID]; f != nil {
					shift := 0
					for _, e := range f.edits {
						if e.End <= h.target.start {
							shift += len(e.Text) - (e.End - e.Start)
						}
					}
					h.target.start += shift
					h.target.end += shift
					snap := f.tab.Source().Buffer.Snapshot()
					h.target.version = snap.Version()
					h.target.line = snap.LineOf(h.target.start)
				}
				remaining = append(remaining, h)
			}
			clear(set.hunks[len(remaining):])
			set.hunks = remaining
			if len(remaining) == 0 {
				p.agent.resetSession()
				p.agent.batch = nil
				p.agent.files = nil
				if !p.agent.multi {
					p.agent.target = scholarTarget{}
					p.agent.parts[0], p.agent.include[0] = "", false
				}
			} else {
				set.selected = min(nextIndex, len(remaining)-1)
				if advance {
					s.openScholarReview(p)
					if resume {
						s.resumeScholarTrial(p, all)
					}
				}
			}
			s.startScholarChangeWorker(p)
			s.focusEditor = true
		}, nil
	})
}

func (s *Shell) rollbackScholarFiles(p *scholarState) {
	if p.busy {
		return
	}
	for _, f := range p.agent.applied {
		t := s.tabWithID(f.id)
		if t == nil || !samePath(t.Path, f.path) || t.Source().Buffer.Version() != f.version || !t.Source().Buffer.CanUndo() {
			p.err = s.tr("scholarRollbackConflict")
			return
		}
	}
	for _, f := range p.agent.applied {
		t := s.tabWithID(f.id)
		if p.changes.tabID == t.ID {
			s.tagScholarChange(t, "undo")
		}
		before := t.Source().Buffer.Snapshot()
		t.Source().Buffer.Undo()
		after := t.Source().Buffer.Snapshot()
		if p.changes.tabID == t.ID {
			s.captureScholarChangeSnapshot(p, after, time.Now())
			s.flushScholarChanges(p)
		} else {
			d := &p.changes
			d.nextID++
			d.waiting = append(d.waiting, scholarChangeBatch{change: &scholarChange{id: d.nextID, tabID: t.ID, path: t.Path, origin: "undo"}, before: before, after: after})
		}
	}
	s.startScholarChangeWorker(p)
	p.agent.applied = nil
	p.agent.batch = nil
}

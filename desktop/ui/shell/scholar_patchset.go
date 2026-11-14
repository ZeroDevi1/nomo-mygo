package shell

import (
	"context"
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
	target   scholarTarget
	fromNote bool
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
		existing := slices.IndexFunc(a.files, func(f scholarContextFile) bool { return f.target.tabID == t.ID })
		if (existing < 0 || !a.multi || a.files[existing].fromNote) && ui.Button(c.Key(t.ID), "+ "+t.Name(s.untitledName)).Disabled(p.busy).Clicked() {
			if existing >= 0 {
				a.files[existing].fromNote = false
				a.multi, a.attachments = true, false
				continue
			}
			snap := t.Source().Buffer.Snapshot()
			total := snap.Len()
			for _, f := range a.files {
				total += len(f.target.before)
			}
			budget, label := scholarACPFileBudget(a.acp.hasManuscriptNotes())
			if total > budget || snap.Len() > scholar.ACPFileLimit {
				p.err = s.tr(label)
				continue
			}
			a.files = append(a.files, scholarContextFile{target: fullManuscriptTarget(t, snap)})
			a.multi, a.attachments = true, false
			p.err = ""
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
			label := fmt.Sprintf("%s · %d B", name, len(f.target.before))
			ui.Text(ck, label).SingleLine().MinWidth(0).Shrink(1).FontSize(12).Tooltip(label)
			if s.toolButton(ck.Key("remove"), "x", s.tr("scholarRemoveAttachment"), false).Disabled(p.busy).Clicked() {
				remove = i
			}
		})
	}
	if remove >= 0 {
		id := a.files[remove].target.tabID
		a.files = slices.Delete(a.files, remove, remove+1)
		if slices.ContainsFunc(a.acp.quotes, func(ref scholarACPQuote) bool { return ref.manuscriptTarget == id }) {
			a.acp.quotes = slices.DeleteFunc(a.acp.quotes, func(ref scholarACPQuote) bool { return ref.manuscriptTarget == id })
			a.acp.previewQuote, a.acp.previewImage = 0, nil
		}
	}
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
	patch := p.agent.pendingPatch()
	stale := !s.scholarTargetCurrent(patch.target)
	if set.summary != "" {
		ui.Text(c.Key("summary"), set.summary).Wrap().FontSize(11).TextColor(scholarInk(c, "warning"))
	}
	ui.Row(c.Key("heading")).FillWidth().AlignItems(ui.Center).Gap(3).Children(func() {
		ui.Text(c.Key("file"), s.scholarReviewLabel(patch)).FontSize(12).SingleLine().MinWidth(0).Grow(1)
		if len(set.hunks) > 1 {
			if s.scholarCompactIcon(c.Key("previous"), "chevron-left", s.tr("scholarPreviousChange"), false).Disabled(p.busy).Clicked() {
				s.navigateScholarHunk(p, -1)
			}
			ui.Text(c.Key("counter"), fmt.Sprintf("%d / %d", set.selected+1, len(set.hunks))).FontSize(11).TextColor(s.ch.muted)
			if s.scholarCompactIcon(c.Key("next"), "chevron-right", s.tr("scholarNextChange"), false).Disabled(p.busy).Clicked() {
				s.navigateScholarHunk(p, 1)
			}
		}
	})
	if stale {
		ui.Text(c.Key("stale"), s.tr("scholarBaselineChanged")).FontSize(11).TextColor(scholarInk(c, "warning"))
	}
	ui.Row(c.Key("actions")).FillWidth().Wrap().AlignItems(ui.Center).Gap(4).Children(func() {
		if stale {
			if s.scholarACPAction(c.Key("reject"), "undo-2", s.tr("scholarAgentReject"), false).Disabled(p.busy).Clicked() {
				s.rejectScholarHunk(p)
			}
		} else if s.scholarACPAction(c.Key("review"), "file-diff", s.tr("scholarShowReview"), true).Disabled(p.busy).Clicked() {
			s.openScholarReview(p)
		}
		if p.agent.trialPreparing {
			if s.scholarACPAction(c.Key("cancel"), "x", s.tr("scholarAgentCancelWork"), false).Clicked() {
				p.cancelCurrent()
			}
		} else if s.scholarACPAction(c.Key("trial"), "book-open", s.tr("scholarPDFDiffAll"), false).Disabled(stale || p.busy).Clicked() {
			s.openScholarReview(p)
			s.trialScholarPatchSet(p, set)
		}
		ui.Spacer(c)
		s.scholarCompactIcon(c.Key("more"), "ellipsis", s.tr("scholarACPOptions"), false).Disabled(p.busy).Menu(func(m *ui.Menu) {
			if m.Item(s.tr("scholarAcceptFile")).Disabled(stale).Chosen() {
				var chosen []*scholarPatch
				for _, h := range set.hunks {
					if h.target.path == patch.target.path {
						chosen = append(chosen, h)
					}
				}
				s.acceptScholarHunks(p, chosen)
			}
			if m.Item(s.tr("scholarAcceptAll")).Chosen() {
				s.acceptScholarHunks(p, append([]*scholarPatch(nil), set.hunks...))
			}
			if m.Item(s.tr("scholarRejectAll")).Chosen() {
				p.agent.conversation = trimScholarConversation(p.agent.conversation + fmt.Sprintf("\nReview: rejected %d proposed changes", len(set.hunks)))
				p.agent.batch = nil
				if p.agent.trial.pending {
					p.agent.trial.close()
				}
			}
		})
	})
}

func (s *Shell) rejectScholarHunk(p *scholarState) {
	set := p.agent.batch
	if set == nil || p.busy || len(set.hunks) == 0 {
		return
	}
	i := min(set.selected, len(set.hunks)-1)
	resume, all := p.agent.trial.show && p.agent.trial.pending, p.agent.trial.allPending
	showSource := p.agent.trial.showSource
	advance := resume || !p.agent.reviewHidden && s.activeTab() != nil && s.activeTab().ID == set.hunks[i].target.tabID
	set.hunks = slices.Delete(set.hunks, i, i+1)
	p.agent.conversation = trimScholarConversation(p.agent.conversation + "\nReview: rejected one proposed change")
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
				s.resumeScholarTrial(p, all, showSource)
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
			if v := &p.agent.trial; v.show && v.pending && v.allPending {
				next := set.hunks[set.selected]
				s.locateScholarTrialPatch(p, next)
				if v.showSource {
					file := scholarTrialInitialSource(v.sources, next)
					if file != v.source.index {
						v.source.close()
						s.requestScholarTrialSource(p, file)
					} else {
						v.source.reveal(next)
					}
				}
				return
			}
		} else if v := &p.agent.trial; v.show && v.pending {
			return
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
			p.agent.conversation = trimScholarConversation(p.agent.conversation + fmt.Sprintf("\nReview: accepted %d proposed changes", len(chosen)))
			resume, all := p.agent.trial.show && p.agent.trial.pending, p.agent.trial.allPending
			showSource := p.agent.trial.showSource
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
						s.resumeScholarTrial(p, all, showSource)
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

package shell

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

type scholarTrialSource struct {
	path          string
	before, after editor.Snapshot
	patches       []*scholarPatch
}

func scholarTrialInitialSource(sources []scholarTrialSource, patch *scholarPatch) int {
	slices.SortFunc(sources, func(a, b scholarTrialSource) int { return strings.Compare(a.path, b.path) })
	for i, source := range sources {
		if slices.Contains(source.patches, patch) {
			return i
		}
	}
	if len(sources) == 0 {
		return -1
	}
	return 0
}

func (s scholarTrialSource) diff() (*editor.DiffState, []editor.DiffHunk, []int) {
	d := &editor.DiffState{
		Old: editor.NewBuffer(s.before.String()), New: editor.NewBuffer(s.after.String()),
		Layout: editor.DiffUnified, Indicators: editor.DiffClassic, Inline: editor.DiffInlineNone,
		ExpandUnchanged: true, Wrap: true, LineHeight: scholarDiffLineHeight,
	}
	hunks := d.Hunks()
	targets := make([]int, len(hunks))
	for i := range targets {
		targets[i] = -1
	}
	patchHunks := make([]int, len(s.patches))
	matches := make([]int, len(hunks))
	ends := make([]int, len(s.patches))
	shift := 0
	for i, patch := range s.patches {
		start := patch.target.start + shift
		newStart, newEnd := s.after.LineOf(start), s.after.LineOf(start+max(0, len(patch.next)-1))
		oldStart, oldEnd := patch.target.line, s.before.LineOf(max(patch.target.start, patch.target.end-1))
		ends[i], patchHunks[i] = newEnd, -1
		hits := 0
		for j, h := range hunks {
			oldMatch := patch.target.start < patch.target.end && h.OldStart < h.OldEnd && oldStart < h.OldEnd && h.OldStart <= oldEnd
			newMatch := len(patch.next) != 0 && h.NewStart < h.NewEnd && newStart < h.NewEnd && h.NewStart <= newEnd
			if !oldMatch && !newMatch {
				continue
			}
			matches[j]++
			hits++
			patchHunks[i] = j
		}
		if hits != 1 {
			patchHunks[i] = -1
		}
		shift += len(patch.next) - (patch.target.end - patch.target.start)
	}
	for i, j := range patchHunks {
		if j >= 0 && matches[j] == 1 {
			targets[j] = i
		}
		d.Annotations = append(d.Annotations, editor.DiffAnnotation{Side: editor.DiffNew, Line: ends[i], Height: 20, Key: i})
	}
	return d, hunks, targets
}

type scholarTrialSourceState struct {
	index, hunk int
	diff        *editor.DiffState
	hunks       []editor.DiffHunk
	targets     []int
	cancel      context.CancelFunc
}

func (s *scholarTrialSourceState) close() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.diff != nil {
		s.diff.RenderAnnotation = nil
	}
	*s = scholarTrialSourceState{index: -1}
}

func (s *scholarTrialSourceState) reveal(patch *scholarPatch) {
	if s.diff == nil {
		return
	}
	s.hunk = 0
	if patch != nil {
		for i, h := range s.hunks {
			if h.OldEnd >= patch.target.line {
				s.hunk = i
				break
			}
		}
	}
	s.diff.GoToHunk(s.hunk)
}

func (s *Shell) requestScholarTrialSource(p *scholarState, index int) {
	v := &p.agent.trial
	if v.source.cancel != nil || index == v.source.index || index < 0 || index >= len(v.sources) {
		return
	}
	source, pdfs := v.sources[index], v.pdfs
	// Keep only the displayed source model; the other files retain snapshots.
	v.source.close()
	ctx, cancel := context.WithCancel(p.ctx)
	v.source.index, v.source.cancel = index, cancel
	go func() {
		d, hunks, targets := source.diff()
		if ctx.Err() != nil {
			return
		}
		s.post(func() {
			defer cancel()
			if ctx.Err() != nil || s.scholar != p || v.pdfs != pdfs {
				return
			}
			v.source.diff, v.source.hunks, v.source.targets, v.source.cancel = d, hunks, targets, nil
			var patch *scholarPatch
			if len(source.patches) > 0 {
				patch = source.patches[0]
				s.locateScholarTrialPatch(p, patch)
			}
			v.source.reveal(patch)
		})
	}()
}

func (s *Shell) selectScholarTrialPatch(p *scholarState, patch *scholarPatch) bool {
	if set := p.agent.batch; set != nil {
		i := slices.Index(set.hunks, patch)
		if i < 0 {
			return false
		}
		set.selected = i
		return true
	}
	return p.agent.patch == patch
}

func (s *Shell) locateScholarTrialPatch(p *scholarState, patch *scholarPatch) {
	if s.selectScholarTrialPatch(p, patch) {
		if v := &p.agent.trial; v.anchors[patch].Page > 0 {
			v.jumpPage(v.anchors[patch].Page)
		}
	}
}

func (s *Shell) scholarTrialPatchActions(c *ui.Context, p *scholarState, patch *scholarPatch) {
	if patch == nil {
		return
	}
	if p.agent.trial.anchors[patch].Page > 0 && s.scholarReviewIcon(c.Key("locate"), "file-diff", s.tr("scholarTrialLocatePDF"), "", false).Clicked() {
		s.locateScholarTrialPatch(p, patch)
	}
	reject := s.scholarReviewIcon(c.Key("reject"), "undo-2", s.tr("scholarAgentReject"), "", false).Disabled(p.busy).Clicked()
	accept := s.scholarReviewIcon(c.Key("accept"), "check", s.tr("scholarAgentAccept"), "addition", false).Disabled(p.busy).Clicked()
	if reject || accept {
		pdfs := p.agent.trial.pdfs
		// A decision can release the diff that is currently building annotations.
		s.post(func() {
			if s.scholar != p || p.busy || p.agent.trial.pdfs != pdfs || !s.selectScholarTrialPatch(p, patch) {
				return
			}
			p.agent.reviewHidden = false
			switch {
			case accept && p.agent.batch != nil:
				s.acceptScholarHunks(p, []*scholarPatch{patch})
			case accept:
				s.acceptScholarPatch(p, patch)
			case p.agent.batch != nil:
				s.rejectScholarHunk(p)
			default:
				s.rejectScholarSingle(p)
			}
		})
	}
}

func (s *Shell) scholarTrialSourcePane(c *ui.Context, p *scholarState) {
	v := &p.agent.trial
	ui.Column(c).Fill().MinWidth(0).MinHeight(0).Padding(8).Gap(5).Children(func() {
		ui.Text(c, s.tr("scholarSourceDiff")).FontSize(12).Bold()
		if v.source.index < 0 {
			return
		}
		if len(v.sources) > 1 {
			labels := make([]string, len(v.sources))
			for i, f := range v.sources {
				labels[i] = f.path
			}
			selected := v.source.index
			if scholarSelectIndex(c.Key("trial-source-files"), &selected, labels, p.busy || v.source.cancel != nil) {
				s.requestScholarTrialSource(p, selected)
			}
		} else {
			ui.Text(c, v.sources[v.source.index].path).FontSize(11).SingleLine().MinWidth(0)
		}
		d := v.source.diff
		if d == nil {
			ui.Text(c, s.tr("scholarPreparingDiff")).FontSize(11)
			return
		}
		file := v.sources[v.source.index]
		ui.Row(c.Key("trial-source-actions")).FillWidth().Wrap().AlignItems(ui.Center).Gap(4).Children(func() {
			hunks := v.source.hunks
			if len(hunks) > 1 {
				for _, step := range []int{-1, 1} {
					label, symbol := "scholarPreviousChange", "↑"
					if step > 0 {
						label, symbol = "scholarNextChange", "↓"
					}
					i := v.source.hunk + step
					if s.scholarReviewGlyph(c.Key(step), symbol, s.tr(label)).Disabled(i < 0 || i >= len(hunks)).Clicked() {
						v.source.hunk = i
						d.GoToHunk(i)
						if target := v.source.targets[i]; target >= 0 {
							s.locateScholarTrialPatch(p, file.patches[target])
						}
					}
				}
				ui.Text(c, fmt.Sprintf("%d / %d", v.source.hunk+1, len(hunks))).FontSize(11)
			}
		})
		if !v.show {
			return
		}
		if p.err != "" {
			ui.Text(c, p.err).TextColor(scholarInk(c, "warning")).FontSize(11).Wrap()
		}
		d.RenderAnnotation = func(c *ui.Context, a editor.DiffAnnotation) {
			if !v.show {
				return
			}
			ui.Row(c).Fill().Padding(0, 10).Gap(1).Justify(ui.End).AlignItems(ui.Center).Children(func() {
				s.scholarTrialPatchActions(c, p, file.patches[a.Key.(int)])
			})
		}
		editor.DiffView(c.Key(v.source.index), d).FillWidth().Grow(1).MinHeight(0)
	})
}

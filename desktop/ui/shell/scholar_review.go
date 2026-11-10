package shell

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

const scholarReviewMaxFragments = 64

// Split at unchanged lines only if the resulting byte edits reconstruct the
// exact proposed text. For ambiguous boundaries keep the original edit.
func splitScholarReview(target scholarTarget, next string, limit, nextStart int) []*scholarPatch {
	whole := func() []*scholarPatch {
		return []*scholarPatch{{target: target, next: next, diff: scholarDiffView(target.before, next)}}
	}
	if limit < 2 || target.before == "" || next == "" {
		return whole()
	}
	d := editor.DiffState{Old: editor.NewBuffer(target.before), New: editor.NewBuffer(next)}
	hunks := d.Hunks()
	if len(hunks) < 2 || len(hunks) > limit {
		return whole()
	}
	old, proposed := d.Old.Snapshot(), d.New.Snapshot()
	parts := make([]*scholarPatch, 0, len(hunks))
	oldEnd, newEnd, lastStart := 0, 0, -1
	for _, h := range hunks {
		a, aEnd := old.LineStart(h.OldStart), old.LineStart(h.OldEnd)
		b, bEnd := proposed.LineStart(h.NewStart), proposed.LineStart(h.NewEnd)
		if a < oldEnd || b < newEnd || a == lastStart || target.start+a == nextStart ||
			target.before[oldEnd:a] != next[newEnd:b] || target.before[a:aEnd] == next[b:bEnd] {
			return whole()
		}
		t := target
		t.start, t.end = target.start+a, target.start+aEnd
		t.line = target.line + strings.Count(target.before[:a], "\n")
		t.before = strings.Clone(target.before[a:aEnd])
		t.digest = sha256.Sum256([]byte(t.before))
		parts = append(parts, &scholarPatch{target: t, next: strings.Clone(next[b:bEnd])})
		lastStart, oldEnd, newEnd = a, aEnd, bEnd
	}
	if target.before[oldEnd:] != next[newEnd:] {
		return whole()
	}
	for _, patch := range parts {
		patch.diff = scholarDiffView(patch.target.before, patch.next)
	}
	return parts
}

func (s *Shell) scholarReviewControl(c *ui.Context, label string, selected bool) ui.Element {
	b := ui.ButtonBase(c).Size(22, 19).Radius(4).AlignItems(ui.Center).Justify(ui.Center).
		Cursor(ui.CursorPointer).Label(label).Tooltip(label).FocusRing(true).Selected(selected)
	if selected {
		b.Background(s.ch.pressed)
	} else if b.Hovered() {
		b.Background(s.ch.hover)
	}
	return b
}

func (s *Shell) scholarReviewIcon(c *ui.Context, icon, label, channel string, selected bool) ui.Element {
	b := s.scholarReviewControl(c, label, selected)
	ink := s.ch.muted
	if channel != "" {
		ink = scholarInk(c, channel)
	}
	b.Children(func() { ui.Icon(c, s.icon(icon)).FontSize(12).TextColor(ink) })
	return b
}

func (s *Shell) scholarReviewGlyph(c *ui.Context, glyph, label string) ui.Element {
	b := s.scholarReviewControl(c, label, false)
	b.Children(func() { ui.Text(c, glyph).FontSize(13).TextColor(s.ch.muted) })
	return b
}

func (a *scholarAgentState) pendingPatch() *scholarPatch {
	if set := a.batch; set != nil && len(set.hunks) > 0 {
		return set.hunks[min(set.selected, len(set.hunks)-1)]
	}
	return a.patch
}

func (s *Shell) scholarReviewPatch(p *scholarState, tab *Tab) *scholarPatch {
	patch := p.agent.pendingPatch()
	if p.agent.reviewHidden || patch == nil || patch.target.tabID != tab.ID || !samePath(tab.Path, patch.target.path) ||
		tab.Source().Buffer.Version() != patch.target.version {
		return nil
	}
	return patch
}

func (s *Shell) presentScholarReview(p *scholarState) {
	p.agent.reviewHidden = true
	if p.winSettings == nil && !p.accountOpen {
		s.openScholarReview(p)
	}
}

func (s *Shell) openScholarReview(p *scholarState) {
	patch := p.agent.pendingPatch()
	if patch == nil {
		return
	}
	p.agent.trial.close()
	if !s.scholarTargetCurrent(patch.target) {
		p.agent.reviewHidden = true
		s.showScholarInspector(p, scholarCodex)
		return
	}
	t := s.tabWithID(patch.target.tabID)
	if p.references.catalog || p.references.focused != nil {
		s.selectScholarPDF(p, nil)
	}
	if s.win != nil {
		w, _ := s.win.ContentSize()
		width := float32(w)
		if p.drawer != scholarCodex || s.scholarSinglePane(width) || s.scholarWorkspaceWidthAt(width) < 930 {
			s.closeScholarInspector(p)
		}
	}
	p.accountOpen, p.winRailPopup, p.winCompactProject, p.compactNav = false, false, false, false
	p.agent.reviewHidden, p.agent.inline = false, false
	p.agent.reviewRelayout = true
	p.changes.compare, p.changes.peek, p.changes.peekView = false, nil, nil
	s.activate(s.findTabIndex(t.ID))
	t.Source().GoTo(editor.Pos{Line: patch.target.line})
	t.typesetFocusPDF = false
	s.focusEditor = true
}

func (s *Shell) scholarReviewLabel(patch *scholarPatch) string {
	return fmt.Sprintf(s.tr("scholarPendingLocation"), filepath.Base(patch.target.path), patch.target.line+1)
}

func (s *Shell) scholarInlineReview(c *ui.Context, p *scholarState, t *Tab, el ui.Element) {
	patch := s.scholarReviewPatch(p, t)
	if patch == nil || p.literature.picker || p.navigation.open {
		return
	}
	if p.agent.reviewRelayout {
		// The editor may have just replaced a PDF or a narrow-window drawer.
		// Bounds and measured rows become available after that frame lays out.
		p.agent.reviewRelayout = false
		c.After(16 * time.Millisecond)
	}
	// The editor exposes measured line offsets, but has no API to insert
	// controls into the text layout. Anchor the card beside the actual line.
	st, r := t.Source(), el.Bounds()
	top := float32(st.LineTop(patch.target.line) - st.ScrollY())
	bottom := float32(st.LineTop(patch.target.line+1) - st.ScrollY())
	if r.W <= 0 || r.H < 120 || bottom <= max(0, top) || top >= r.H {
		return
	}
	w, _ := c.Size()
	width := min(520, max(260, r.W-16), w-r.X-12)
	if width < 180 {
		return
	}
	height := min(208, r.H-8)
	y := bottom + 4
	if y+height > r.H && top >= height+4 {
		y = top - height - 4
	}
	y = max(4, min(y, r.H-height-4))
	ui.Overlay(c, func() {
		ck := c.Key(patch)
		card := ui.Column(ck).Absolute().Left(r.X+4).Top(r.Y+y).Width(width).Height(height).Padding(8).Gap(4).
			Radius(c.Theme().Radius).Background(c.Theme().Background).Border(1, scholarInk(c, "agent"))
		accept, reject, hide := false, false, false
		card.Children(func() {
			ui.Row(ck.Key("header")).FillWidth().Gap(4).AlignItems(ui.Center).Children(func() {
				ui.Text(ck, s.scholarReviewLabel(patch)).FontSize(11).TextColor(scholarInk(c, "agent")).SingleLine().Grow(1).MinWidth(0)
				if set := p.agent.batch; set != nil && len(set.hunks) > 1 {
					ui.Text(ck, fmt.Sprintf("%d / %d", set.selected+1, len(set.hunks))).FontSize(11)
					if s.toolButton(ck.Key("prev"), "chevron-up", s.tr("scholarPreviousChange"), false).Disabled(p.busy).Clicked() {
						s.navigateScholarHunk(p, -1)
					}
					if s.toolButton(ck.Key("next"), "chevron-down", s.tr("scholarNextChange"), false).Disabled(p.busy).Clicked() {
						s.navigateScholarHunk(p, 1)
					}
				}
				label := s.tr("scholarHideReview")
				hide = s.toolButton(ck.Key("hide"), "x", label, false).Label(label).FocusRing(true).Clicked()
			})
			scholarDiffElement(ck.Key("diff"), patch.diff).FillWidth().Grow(1).MinHeight(0)
			if p.err != "" {
				ui.Text(ck, p.err).TextColor(scholarInk(c, "warning")).FontSize(11).SingleLine().Tooltip(p.err)
			} else if p.agent.trialPreparing {
				ui.Text(ck, s.tr("scholarPDFPreparing")).FontSize(11)
			}
			ui.Row(ck.Key("actions")).FillWidth().Gap(4).Wrap().Justify(ui.End).Children(func() {
				if p.agent.trialPreparing {
					if ui.Button(ck.Key("cancel-trial"), s.tr("scholarAgentCancelWork")).Clicked() {
						p.cancelCurrent()
					}
				} else if ui.Button(ck.Key("pdf-diff"), s.tr("scholarPDFDiff")).Disabled(p.busy).Clicked() {
					s.trialScholarPatch(p, patch)
				}
				showReject := func() {
					reject = s.scholarReviewIcon(ck.Key("reject"), "undo-2", s.tr("scholarAgentReject"), "", false).Disabled(p.busy).Clicked()
				}
				if s.pf.mac {
					showReject()
				}
				accept = s.scholarReviewIcon(ck.Key("accept"), "check", s.tr("scholarAgentAccept"), "addition", false).Disabled(p.busy).Clicked()
				if !s.pf.mac {
					showReject()
				}
			})
		})
		if hide || card.OverlayShortcut(0, ui.KeyEscape) {
			p.agent.reviewHidden = true
			s.focusEditor = true
			return
		}
		if p.busy || p.agent.pendingPatch() != patch {
			return
		}
		switch {
		case accept || card.OverlayShortcut(ui.Cmd, ui.KeyEnter):
			if p.agent.batch != nil {
				s.acceptScholarHunks(p, []*scholarPatch{patch})
			} else {
				s.acceptScholarPatch(p, patch)
			}
		case reject || card.OverlayShortcut(ui.Cmd, ui.KeyBackspace):
			if p.agent.batch != nil {
				s.rejectScholarHunk(p)
			} else {
				s.rejectScholarSingle(p)
			}
			s.focusEditor = true
		}
	})
}

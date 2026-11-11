package shell

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"nomo/desktop/internal/scholar"
	"nomo/desktop/typeset"
)

const scholarSelectionLimit = 16 << 10

type scholarTarget struct {
	tabID, start, end, line int
	path                    string
	version                 uint64
	digest                  [32]byte
	before                  string
}

type scholarPatch struct {
	target scholarTarget
	next   string
	diff   *scholarDiff
}

type scholarAgentState struct {
	target, inlineTarget scholarTarget
	inline               bool
	anchor               ui.Rect
	inlineX              float32
	pressAt              time.Time
	pressX, pressY       float32
	pressUsed            bool
	instruction          string
	parts                [4]string
	include              [4]bool
	showParts            int
	attachments          bool
	acp                  scholarACPState
	running              bool
	patch                *scholarPatch
	batch                *scholarPatchSet
	multi, reviewHidden  bool
	reviewRelayout       bool
	files                []scholarContextFile
	conversation         string
	applied              []scholarAppliedFile
	trial                scholarTrialView
	trialPreparing       bool
}

func (s *Shell) openScholarAgent(p *scholarState) {
	s.scholarToggleDrawer(p, scholarCodex)
	if p.drawer == scholarClosed {
		s.focusEditor = true
		return
	}
}

func (s *Shell) openScholarInlinePrompt(p *scholarState) {
	a := &p.agent
	if a.pendingPatch() != nil {
		s.openScholarReview(p)
		return
	}
	if a.running {
		s.showScholarInspector(p, scholarCodex)
		return
	}
	a.inlineTarget = s.scholarSelection(p)
	a.anchor, a.inlineX = ui.Rect{}, 0
	if a.inlineTarget.tabID == 0 {
		s.showScholarInspector(p, scholarCodex)
		return
	}
	s.revealScholarSource(s.activeTab())
	a.inline = true
}

func (s *Shell) scholarSourceQuoteGesture(c *ui.Context, p *scholarState, el ui.Element) {
	a := &p.agent
	st := s.activeTab().Source()
	sel := st.Selection()
	if sel.Anchor == sel.Head || p.busy || a.pendingPatch() != nil {
		a.pressAt, a.pressUsed = time.Time{}, false
		return
	}
	pointer, _, held := el.PressedPointer()
	right := el.RightClicked()
	_, _, released := el.DragEnded()
	if !held && !right && !released {
		a.pressAt, a.pressUsed = time.Time{}, false
		return
	}
	x, y, over := el.PointerPosition()
	if !over || a.pressUsed {
		return
	}
	if !right && !released {
		button, _ := el.PressedButton()
		if button != 0 || pointer == ui.PointerMouse {
			return
		}
		if a.pressAt.IsZero() || absF32(x-a.pressX) > 5 || absF32(y-a.pressY) > 5 {
			a.pressAt, a.pressX, a.pressY = c.Now(), x, y
		}
		due := a.pressAt.Add(550 * time.Millisecond)
		if c.Now().Before(due) {
			c.After(due.Sub(c.Now()))
			return
		}
		a.pressUsed = true
	}
	if target := s.scholarSelection(p); target.tabID != 0 {
		a.inlineTarget, a.inline = target, true
		a.inlineX = x
	}
}

func absF32(n float32) float32 { return max(n, -n) }

func (s *Shell) anchorScholarInline(p *scholarState, st *editor.State, r ui.Rect) {
	a := &p.agent
	if !a.inline {
		return
	}
	target, sel := a.inlineTarget, st.Selection()
	if min(sel.Anchor, sel.Head) != target.start || max(sel.Anchor, sel.Head) != target.end {
		a.inline = false
		return
	}
	last := st.Buffer.Snapshot().LineOf(target.end - 1)
	top := max(0, float32(st.LineTop(target.line)-st.ScrollY()))
	bottom := min(r.H, float32(st.LineTop(last+1)-st.ScrollY()))
	a.anchor = ui.Rect{}
	if bottom > top && r.W > 0 {
		a.anchor = ui.Rect{X: r.X, Y: r.Y + top, W: r.W, H: bottom - top}
	}
}

func (s *Shell) scholarInlinePrompt(c *ui.Context, p *scholarState) {
	a := &p.agent
	if !a.inline {
		return
	}
	if !s.scholarTargetCurrent(a.inlineTarget) || s.activeTab() == nil || s.activeTab().ID != a.inlineTarget.tabID || p.references.focused != nil || p.references.catalog {
		a.inline = false
		return
	}
	r := a.anchor
	if r.W <= 12 {
		return
	}
	width := min(float32(330), r.W-12)
	x := min(r.X+max(6, a.inlineX-24), r.X+r.W-width-6)
	_, h := c.Size()
	y := r.Y + r.H + 5
	if y+36 > h-10 {
		y = max(44, r.Y-41)
	}
	panel := s.scholarACPSelectionBar(c.Key("source-selection"), x, y, width, func() {
		ref := s.scholarSourceQuote(p, a.inlineTarget)
		blocked := p.busy || a.pendingPatch() != nil
		if s.scholarACPAction(c.Key("append"), "message-square", s.tr("scholarACPAppend"), false).Disabled(blocked).Clicked() {
			s.quoteScholar(p, ref, false)
		}
		if s.scholarACPAction(c.Key("new"), "plus", s.tr("scholarACPNew"), false).Disabled(blocked).Clicked() {
			s.quoteScholar(p, ref, true)
		}
		ui.Box(c.Key("divider")).Size(1, 16).Shrink(0).Background(s.ch.separator)
		if s.scholarACPAction(c.Key("edit"), "file-pen-line", s.tr("scholarEditSelection"), true).Disabled(blocked).Clicked() {
			target := a.inlineTarget
			ref.text = ""
			s.quoteScholar(p, ref, false)
			a.target, a.multi = target, false
			a.parts[0], a.include[0] = target.before, true
		}
	})
	if panel.OverlayShortcut(0, ui.KeyEscape) || panel.PressedOutside() {
		a.inline = false
	}
}

func (s *Shell) scholarSelection(p *scholarState) scholarTarget {
	tab := s.activeTab()
	if !isManuscript(tab) || s.scholarForTab(tab) != p {
		return scholarTarget{}
	}
	st := tab.Source()
	sel := st.Selection()
	start, end := min(sel.Anchor, sel.Head), max(sel.Anchor, sel.Head)
	if start == end || end-start > scholarSelectionLimit {
		return scholarTarget{}
	}
	snap := st.Buffer.Snapshot()
	before := snap.Slice(start, end)
	return scholarTarget{tabID: tab.ID, start: start, end: end, line: snap.LineOf(start), path: tab.Path,
		version: snap.Version(), digest: sha256.Sum256([]byte(before)), before: before}
}

func (s *Shell) captureScholarTarget(p *scholarState) bool {
	target := s.scholarSelection(p)
	if target.tabID == 0 {
		p.err = s.tr("scholarSelectionLimit")
		return false
	}
	a := &p.agent
	a.target, a.parts[0], a.include[0] = target, target.before, true
	p.err = ""
	return true
}

var scholarContextNames = [4]string{"@selection", "@preamble", "@zotero", "@diag"}

var scholarAgentPrompts = [...]struct{ name, prompt string }{
	{"/shorten", "scholarAgentShorten"}, {"/translate", "scholarAgentTranslate"},
	{"/formula", "scholarAgentFixFormula"}, {"/convert", "scholarAgentConvert"},
}

func (s *Shell) scholarAgentAttachmentButton(c *ui.Context, p *scholarState, shortcut bool) {
	a := &p.agent
	label := s.tr("scholarAgentAttach")
	add := s.toolButton(c.Key("add"), "plus", label, a.attachments).Label(label).FocusRing(true).Disabled(p.busy)
	if !p.busy && (add.Clicked() || shortcut) {
		a.acp.modelDetails = false
		a.attachments = !a.attachments
	}
	ui.Popover(c, add, &a.attachments, func() {
		ui.Scroll(c).Width(250).MaxHeight(360).Children(func() {
			ui.Column(c).FillWidth().Padding(8).Gap(4).Children(func() {
				if ui.Button(c.Key("selection"), s.tr("scholarAgentUseSelection")).Disabled(p.busy).Clicked() {
					if s.captureScholarTarget(p) {
						a.multi, a.attachments = false, false
					}
				}
				s.scholarContextActions(c, p)
				s.scholarFileContexts(c, p)
				ui.Text(c, s.tr("scholarAgentDisclosure")).FontSize(11).Wrap()
			})
		})
	})
}

func (s *Shell) scholarAgentAttachments(c *ui.Context, p *scholarState) {
	a := &p.agent
	for i, part := range a.parts {
		if part == "" || !a.include[i] || i == 0 && a.multi {
			continue
		}
		ck := c.Key(i)
		label := scholarContextNames[i]
		if i == 0 {
			label = fmt.Sprintf(s.tr("scholarLineLabel"), filepath.Base(a.target.path), a.target.line+1)
		}
		ui.Row(ck).Gap(4).AlignItems(ui.Center).Children(func() {
			if ui.Button(ck.Key("view"), label).Clicked() {
				if a.showParts == i+1 {
					a.showParts = 0
				} else {
					a.showParts = i + 1
				}
			}
			if s.toolButton(ck.Key("remove"), "x", s.tr("scholarRemoveAttachment"), false).Disabled(p.busy).Clicked() {
				a.parts[i], a.include[i] = "", false
				if a.showParts == i+1 {
					a.showParts = 0
				}
			}
		})
		if a.showParts == i+1 && a.include[i] {
			ui.Text(ck.Key("text"), part).FontSize(11).Wrap()
		}
	}
	s.scholarAttachedFiles(c.Key("files"), p)
}

func (s *Shell) scholarTargetCurrent(target scholarTarget) bool {
	tab := s.tabWithID(target.tabID)
	return isManuscript(tab) && tab.ID == target.tabID && samePath(tab.Path, target.path) &&
		tab.Source().Buffer.Version() == target.version
}

func (s *Shell) scholarTargetValid(target scholarTarget) bool {
	if !s.scholarTargetCurrent(target) {
		return false
	}
	snap := s.tabWithID(target.tabID).Source().Buffer.Snapshot()
	return target.start >= 0 && target.end >= target.start && target.end <= snap.Len() &&
		sha256.Sum256([]byte(snap.Slice(target.start, target.end))) == target.digest
}

func (s *Shell) scholarPatchView(c *ui.Context, p *scholarState, patch *scholarPatch) {
	stale := !s.scholarTargetCurrent(patch.target)
	ui.Text(c, s.scholarReviewLabel(patch)).
		FontSize(12).TextColor(scholarInk(c, "agent"))
	if stale {
		ui.Text(c, s.tr("scholarBaselineChanged")).FontSize(11).TextColor(scholarInk(c, "warning"))
		if ui.Button(c, s.tr("scholarAgentReject")).Disabled(p.busy).Clicked() {
			s.rejectScholarSingle(p)
		}
		return
	}
	if ui.Button(c, s.tr("scholarShowReview")).Clicked() {
		s.openScholarReview(p)
	}
	ui.Row(c).Gap(4).Children(func() {
		if ui.Button(c, s.tr("scholarPDFDiff")).Disabled(p.busy).Clicked() {
			s.trialScholarPatch(p, patch)
		}
		if p.busy && ui.Button(c, s.tr("scholarAgentCancelWork")).Clicked() {
			p.cancelCurrent()
		}
	})
}

func (s *Shell) trialScholarPatch(p *scholarState, patch *scholarPatch) {
	s.trialScholarHunks(p, []*scholarPatch{patch})
}

func (s *Shell) acceptScholarPatch(p *scholarState, patch *scholarPatch) {
	if p.busy || p.agent.patch != patch || !s.scholarTargetValid(patch.target) {
		return
	}
	tab := s.tabWithID(patch.target.tabID)
	st := tab.Source()
	snap := st.Buffer.Snapshot()
	library := p.library
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		if err := validateScholarPatchReferences(ctx, library, snap, patch.target, patch.next); err != nil {
			return nil, err
		}
		return func() {
			if p.agent.patch != patch || s.tabWithID(tab.ID) != tab || !s.scholarTargetValid(patch.target) || p.library != library {
				p.err = s.tr("scholarSourceOrLibraryChanged")
				return
			}
			st.Buffer.Edit(editor.Edit{Start: patch.target.start, End: patch.target.end, Text: patch.next})
			s.recordScholarPatch(p, patch, snap, st.Buffer.Snapshot())
			end := patch.target.start + len(patch.next)
			st.Select(end, end)
			p.agent.conversation = trimScholarConversation(p.agent.conversation + "\nReview: accepted")
			p.agent.patch, p.agent.target = nil, scholarTarget{}
			p.agent.trial.close()
			p.agent.parts = [4]string{}
			s.focusEditor = true
		}, nil
	})
}

func validateScholarPatchReferences(ctx context.Context, library *scholar.Library, snap editor.Snapshot, target scholarTarget, next string) error {
	typst := strings.EqualFold(filepath.Ext(target.path), ".typ")
	begin := snap.LineStart(snap.LineOf(target.start))
	end := snap.LineEnd(snap.LineOf(target.end))
	if end-begin > 4<<20 {
		return fmt.Errorf("受影响的源码行超过引用校验上限")
	}
	old := snap.Slice(begin, end)
	if !strings.ContainsAny(old+next, "\\@") {
		return nil
	}
	prefix := begin
	if typst {
		if snap.Len() > 4<<20 {
			return fmt.Errorf("Typst 源码超过引用上下文校验上限")
		}
		prefix, end = 0, snap.Len()
	}
	prior := make(map[string]bool)
	oldEnd := snap.LineEnd(snap.LineOf(target.end))
	scholar.Citations(snap.Slice(prefix, end), typst, func(ref scholar.Citation) {
		if prefix+ref.End >= begin && prefix+ref.Start <= oldEnd && len(prior) < 4096 {
			prior[ref.Key] = true
		}
	})
	newEnd := oldEnd + len(next) - len(target.before)
	var unknown string
	scholar.Citations(snap.Slice(prefix, target.start)+next+snap.Slice(target.end, end), typst, func(ref scholar.Citation) {
		if prefix+ref.End < begin || prefix+ref.Start > newEnd || unknown != "" {
			return
		}
		if _, ok := library.Find(ref.Key); !ok && !prior[ref.Key] {
			unknown = ref.Key
		}
	})
	if unknown != "" {
		return fmt.Errorf("提议包含未知引用键 %s", unknown)
	}
	return ctx.Err()
}

// 编译会话释放后仅保留一份轻量报告，诊断仍可查看或发送给 Codex。
type scholarCompileReport struct {
	tabID        int
	source, path string
	message      string
	warnings     []typeset.Diagnostic
}

func (p *compilePane) report() scholarCompileReport {
	return scholarCompileReport{p.tab.ID, p.sourcePath, p.path, p.err, p.warnings}
}

func (s *Shell) scholarDiagnosticsView(c *ui.Context, p *scholarState) {
	tab := s.activeTab()
	report := p.diagnostics
	if pane := s.compile; pane != nil && pane.tab == tab && !pane.busy {
		report = pane.report()
	}
	switch {
	case report.tabID == tab.ID && samePath(report.source, tab.Path):
		ui.Text(c, s.tr("scholarLastTypeset")).FontSize(11).TextColor(s.ch.muted)
		if report.message == "" {
			ui.Text(c, s.tr("scholarTypesetSuccess"))
			for i, note := range report.warnings {
				ck := c.Key(i)
				ui.Text(ck, note.Text).FontSize(11).Wrap().TextColor(scholarInk(c, "warning"))
				if note.Line > 0 && note.File != "" && ui.Button(ck, fmt.Sprintf(s.tr("scholarJumpLine"), note.Line)).Clicked() {
					s.jumpScholarDiagnostic(p, report.path, note)
				}
			}
		} else {
			ui.Text(c, report.message).FontSize(12).Wrap()
			if location := typeset.DiagnosticLocation(report.message); location.Line > 0 {
				if ui.Button(c, fmt.Sprintf(s.tr("scholarJumpLine"), location.Line)).Clicked() {
					s.jumpScholarDiagnostic(p, report.path, location)
				}
			}
			if ui.Button(c, s.tr("scholarAttachDiagnostics")).Disabled(p.busy || p.agent.patch != nil).Clicked() {
				s.showScholarInspector(p, scholarCodex)
				s.captureScholarTarget(p)
				if p.agent.target.tabID != 0 {
					p.agent.parts[3], p.agent.include[3] = report.message, true
				}
			}
		}
	case tab.typesetPaused:
		ui.Text(c, s.tr("scholarTypesetPaused")).Wrap()
	default:
		ui.Text(c, s.tr("scholarOpenPreview")).Wrap()
	}
	if ui.Button(c, s.tr("scholarRebuildPreview")).Disabled(!s.canPreviewTypeset(tab)).Clicked() {
		s.rebuildScholarPreview(p, tab)
	}
	if p.err != "" {
		ui.Text(c, p.err).Wrap().FontSize(11)
	}
}

func (s *Shell) jumpScholarDiagnostic(p *scholarState, main string, note typeset.Diagnostic) {
	path := note.File
	switch filepath.Clean(path) {
	case "-", "<stdin>", "preview.tex":
		path = main
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(main), path)
	}
	name, err := scholar.Relative(p.project.Root, path)
	if err != nil {
		p.err = err.Error()
		return
	}
	path, err = p.project.Path(name)
	if err != nil {
		p.err = err.Error()
		return
	}
	s.OpenFiles([]string{path})
	if t := s.scholarTab(path); t != nil {
		snap := t.Source().Buffer.Snapshot()
		if note.Line <= snap.LineCount() {
			s.jumpScholarOutline(t, snap.LineStart(note.Line-1))
		}
	}
}

func (s *Shell) rejectScholarSingle(p *scholarState) {
	if p.busy {
		return
	}
	p.agent.patch = nil
	if p.agent.trial.pending {
		p.agent.trial.close()
	}
	p.agent.conversation = trimScholarConversation(p.agent.conversation + "\nReview: rejected")
}

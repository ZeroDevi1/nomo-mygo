package shell

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

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
	target              scholarTarget
	inline, focusInline bool
	anchor              ui.Rect
	instruction, stream string
	parts               [4]string
	include             [4]bool
	showParts           int
	attachments         bool
	info                scholar.CodexInfo
	probed, running     bool
	model               int
	patch               *scholarPatch
	batch               *scholarPatchSet
	multi, reviewHidden bool
	reviewRelayout      bool
	files               []scholarContextFile
	conversation        string
	session             *scholar.CodexConversation
	applied             []scholarAppliedFile
	trial               scholarTrialView
	trialPreparing      bool
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
	s.captureScholarTarget(p)
	if a.target.tabID == 0 {
		s.showScholarInspector(p, scholarCodex)
		return
	}
	s.revealScholarSource(s.activeTab())
	a.multi = false
	a.inline, a.focusInline = true, true
}

func (s *Shell) scholarInlinePrompt(c *ui.Context, p *scholarState) {
	a := &p.agent
	if !a.inline {
		return
	}
	if !s.scholarTargetCurrent(a.target) {
		a.inline = false
		return
	}
	w, _ := c.Size()
	width := min(float32(440), w-24)
	x := max(float32(12), min(a.anchor.X+16, w-width-12))
	y := max(float32(46), a.anchor.Y+12)
	ui.Overlay(c, func() {
		panel := ui.Column(c).Absolute().Left(x).Top(y).Width(width).Padding(10).Gap(6).
			Radius(c.Theme().Radius).Background(c.Theme().Background).Border(1, scholarInk(c, "agent"))
		panel.Children(func() {
			ui.Text(c, fmt.Sprintf("Codex · 第 %d 行", a.target.line+1)).FontSize(12).TextColor(scholarInk(c, "agent"))
			input := ui.TextInput(c, &a.instruction).FillWidth().Placeholder(s.tr("scholarAgentInput"))
			if a.focusInline {
				input.Focus()
				a.focusInline = false
			}
			ui.Row(c).Gap(4).Children(func() {
				for i, preset := range []string{s.tr("scholarAgentShorten"), s.tr("scholarAgentTranslate"), s.tr("scholarAgentFixFormula"), s.tr("scholarAgentConvert")} {
					if ui.Button(c.Key(i), preset).Clicked() {
						a.instruction = preset
					}
				}
			})
			if ui.Button(c, s.tr("scholarAgentReviewContext")).Clicked() || input.Submitted() {
				a.inline = false
				s.showScholarInspector(p, scholarCodex)
			}
		})
		if panel.OverlayShortcut(0, ui.KeyEscape) || panel.PressedOutside() {
			a.inline, s.focusEditor = false, true
		}
	})
}

func (s *Shell) captureScholarTarget(p *scholarState) {
	a := &p.agent
	tab := s.activeTab()
	if !isManuscript(tab) {
		a.target = scholarTarget{}
		a.parts[0], a.include[0] = "", false
		p.err = s.tr("scholarSelectSource")
		return
	}
	st := tab.Source()
	snap := st.Buffer.Snapshot()
	sel := st.Selection()
	start, end := min(sel.Anchor, sel.Head), max(sel.Anchor, sel.Head)
	if start == end || end-start > scholarSelectionLimit {
		p.err = s.tr("scholarSelectionLimit")
		a.resetSession()
		a.target = scholarTarget{}
		a.parts[0], a.include[0] = "", false
		return
	}
	before := snap.Slice(start, end)
	a.target = scholarTarget{tabID: tab.ID, start: start, end: end, line: snap.LineOf(start), path: tab.Path,
		version: snap.Version(), digest: sha256.Sum256([]byte(before)), before: before}
	p.err = ""
	a.parts[0], a.include[0] = before, true
	a.resetSession()
}

var scholarContextNames = [4]string{"@selection", "@preamble", "@zotero", "@diag"}

var scholarAgentPrompts = [...]struct{ name, prompt string }{
	{"/shorten", "scholarAgentShorten"}, {"/translate", "scholarAgentTranslate"},
	{"/formula", "scholarAgentFixFormula"}, {"/convert", "scholarAgentConvert"},
}

func (s *Shell) scholarAgentView(c *ui.Context, p *scholarState) {
	a := &p.agent
	ui.Text(c, s.tr("scholarAgentTitle")).Bold().TextColor(scholarInk(c, "agent"))
	if p.err != "" {
		ui.Text(c, p.err).FontSize(11).Wrap().TextColor(scholarInk(c, "warning"))
	}
	if a.batch != nil {
		s.scholarPatchSetView(c, p, a.batch)
		return
	}
	if a.patch != nil {
		s.scholarPatchView(c, p, a.patch)
		return
	}
	if len(a.applied) > 0 && ui.Button(c, s.tr("scholarRollbackFiles")).Disabled(p.busy).Clicked() {
		s.rollbackScholarFiles(p)
	}
	if a.conversation != "" {
		ui.Text(c, a.conversation).FontSize(11).Wrap()
		if ui.Button(c, s.tr("scholarClearConversation")).Disabled(p.busy).Clicked() {
			a.conversation = ""
			if a.session != nil {
				a.session.Close()
				a.session = nil
			}
		}
	}
	if !a.probed {
		ui.Text(c, s.tr("scholarCodexSetup")).FontSize(11).Wrap()
		if ui.Button(c, s.tr("scholarAgentConnect")).Disabled(p.busy).Clicked() {
			s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
				info, err := scholar.ProbeCodex(ctx)
				return func() { a.probed, a.info = err == nil, info }, err
			})
		}
	}
	s.scholarAgentAttachments(c.Key("attachments"), p)
	input := ui.TextInput(c, &a.instruction).FillWidth().Placeholder(s.tr("scholarAgentInput")).Disabled(a.running)
	command := strings.HasPrefix(strings.TrimSpace(a.instruction), "/")
	if command && !a.running {
		s.scholarAgentCommands(c.Key("prompts"), a, input.Submitted())
	}
	if a.running {
		ui.Text(c, s.tr("scholarGenerating")).FontSize(11)
		if ui.Button(c, s.tr("scholarAgentCancel")).Clicked() {
			p.cancelCurrent()
			a.running, a.stream = false, ""
		}
		if a.stream != "" {
			stream := a.stream
			if len(stream) > 8<<10 {
				stream = stream[len(stream)-(8<<10):]
				for len(stream) > 0 && stream[0]&0xc0 == 0x80 {
					stream = stream[1:]
				}
			}
			ui.Text(c, stream).FontSize(11).Wrap()
		}
		return
	}
	ready := a.probed && !p.busy && !command && strings.TrimSpace(a.instruction) != "" &&
		(a.multi && len(a.files) > 0 || !a.multi && a.target.tabID != 0 && a.include[0])
	if a.probed && !a.include[0] && len(a.files) == 0 {
		ui.Text(c, s.tr("scholarAgentChooseTarget")).FontSize(11).Wrap()
	}
	send := false
	ui.Row(c.Key("controls")).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
		label := s.tr("scholarCommandPalette")
		s.toolButton(c.Key("prompt-menu"), "slash", label, false).Disabled(p.busy).Label(label).Menu(func(m *ui.Menu) {
			for _, command := range scholarAgentPrompts {
				if m.Item(command.name + "  " + s.tr(command.prompt)).Chosen() {
					a.instruction = s.tr(command.prompt)
				}
			}
		})
		if a.probed {
			labels := []string{s.tr("scholarAgentDefaultModel")}
			for _, model := range a.info.Models {
				label := model.DisplayName
				if label == "" {
					label = model.Model
				}
				labels = append(labels, label)
			}
			if scholarSelectIndex(c.Key("model"), &a.model, labels, p.busy) {
				a.resetSession()
			}
		}
		ui.Box(c).Grow(1)
		send = ui.Button(c, s.tr("scholarAgentSend")).Disabled(!ready).Clicked()
	})
	if ready && (send || input.Submitted()) {
		if a.multi {
			s.sendScholarPatchSet(p)
		} else {
			s.sendScholarAgent(p)
		}
	}
}

func (s *Shell) scholarAgentCommands(c *ui.Context, a *scholarAgentState, submitted bool) {
	query := strings.TrimSpace(a.instruction)
	for i, command := range scholarAgentPrompts {
		if strings.HasPrefix(command.name, query) {
			label := command.name + "  " + s.tr(command.prompt)
			if ui.Button(c.Key(i), label).Clicked() || submitted && command.name == query {
				a.instruction = s.tr(command.prompt)
			}
		}
	}
}

func (s *Shell) scholarAgentAttachments(c *ui.Context, p *scholarState) {
	a := &p.agent
	add := s.toolButton(c.Key("add"), "plus", s.tr("scholarAgentAttach"), a.attachments).Disabled(p.busy)
	if add.Clicked() {
		a.attachments = !a.attachments
	}
	ui.Popover(c, add, &a.attachments, func() {
		ui.Scroll(c).Width(250).MaxHeight(360).Children(func() {
			ui.Column(c).FillWidth().Padding(8).Gap(4).Children(func() {
				if ui.Button(c.Key("selection"), s.tr("scholarAgentUseSelection")).Disabled(p.busy).Clicked() {
					s.captureScholarTarget(p)
					if a.target.tabID != 0 {
						a.multi, a.attachments = false, false
						a.resetSession()
					}
				}
				s.scholarContextActions(c, p)
				s.scholarFileContexts(c, p)
				ui.Text(c, s.tr("scholarAgentDisclosure")).FontSize(11).Wrap()
			})
		})
	})
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
				a.resetSession()
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

func (s *Shell) sendScholarAgent(p *scholarState) {
	a := &p.agent
	target := a.target
	if !s.scholarTargetValid(target) {
		p.err = s.tr("scholarSelectionStale")
		return
	}
	contexts := make(map[string]string)
	for i, part := range a.parts {
		if a.include[i] && part != "" {
			contexts[scholarContextNames[i]] = part
		}
	}
	payload, err := json.Marshal(map[string]any{"instruction": a.instruction, "context": contexts, "previous": a.conversation})
	if err != nil {
		p.err = err.Error()
		return
	}
	model := ""
	if a.model > 0 && a.model <= len(a.info.Models) {
		model = a.info.Models[a.model-1].Model
		if model == "" {
			model = a.info.Models[a.model-1].ID
		}
	}
	if a.session == nil {
		a.session = scholar.NewCodexConversation(p.ctx)
	}
	session := a.session
	a.running, a.stream = true, ""
	s.scholarJob(p, func(ctx context.Context, job uint64) (func(), error) {
		var mu sync.Mutex
		queued := false
		var latest string
		next, err := session.Run(ctx, model, string(payload), func(delta string) {
			mu.Lock()
			latest = delta
			post := !queued
			queued = true
			mu.Unlock()
			if post {
				s.post(func() {
					mu.Lock()
					value := latest
					queued = false
					mu.Unlock()
					if s.scholar == p && p.job == job && a.running {
						a.stream = value
					}
				})
			}
		}, false)
		next = strings.ReplaceAll(strings.ReplaceAll(next, "\r\n", "\n"), "\r", "\n")
		var parts []*scholarPatch
		if err == nil && next != target.before && !strings.ContainsRune(next, 0) {
			parts = splitScholarReview(target, next, scholarReviewMaxFragments, -1)
		}
		return func() {
			a.running, a.stream = false, ""
			if err != nil {
				return
			}
			if !s.scholarTargetValid(target) {
				p.err = s.tr("scholarProposalStale")
				return
			}
			if strings.ContainsRune(next, 0) {
				p.err = s.tr("scholarNullProposal")
				return
			}
			if next == target.before {
				p.err = s.tr("scholarUnchangedProposal")
				return
			}
			a.conversation = trimScholarConversation(a.conversation + "\nUser: " + a.instruction + "\nProposal: selection replacement")
			if len(parts) == 1 {
				a.patch = parts[0]
			} else {
				a.batch = &scholarPatchSet{hunks: parts}
			}
			s.presentScholarReview(p)
		}, err
	})
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
			p.agent.resetSession()
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

func (s *Shell) scholarBudgetControls(c *ui.Context, p *scholarState) {
	ui.Checkbox(c, &p.excludeBibliography, s.tr("scholarExcludeBibliography"))
	if p.excludeBibliography {
		if p.bodyConfirmed == nil {
			ui.Text(c, s.tr("scholarBodyAwaiting")).FontSize(11).Wrap()
		}
		ui.Text(c, s.tr("scholarBibliographyBoundary")).FontSize(11).Wrap()
		ui.TextInput(c, &p.bodyBoundary).FillWidth()
	}
	ui.Text(c, s.tr("scholarPDFBudget")).FontSize(12).Wrap()
	in := ui.TextInput(c, &p.budget).FillWidth().Placeholder(s.tr("scholarZeroDisables"))
	if ui.Button(c, s.tr("scholarSaveBudget")).Disabled(p.busy).Clicked() || in.Submitted() {
		n, err := strconv.Atoi(strings.TrimSpace(p.budget))
		if err != nil || n < 0 || n > 10000 {
			p.err = s.tr("scholarInvalidBudget")
		} else {
			p.err = ""
			boundary, boundErr := strconv.Atoi(strings.TrimSpace(p.bodyBoundary))
			if boundErr != nil || boundary < 0 || boundary > 10000 {
				p.err = s.tr("scholarInvalidBudget")
				return
			}
			pref := s.prefs.ScholarProjects[p.project.Root]
			pref.BodyEndPage = boundary
			pref.ExcludeBibliography = p.excludeBibliography
			if s.prefs.ScholarProjects == nil {
				s.prefs.ScholarProjects = make(map[string]scholarProjectPrefs)
			}
			s.prefs.ScholarProjects[p.project.Root] = pref
			s.storeScholarProject(p, n)
			if p.excludeBibliography && boundary > 0 {
				s.confirmScholarBody(p)
			} else {
				p.bodyConfirmed = nil
			}
		}
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

func (a *scholarAgentState) resetSession() {
	if a.session != nil {
		a.session.Close()
		a.session = nil
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

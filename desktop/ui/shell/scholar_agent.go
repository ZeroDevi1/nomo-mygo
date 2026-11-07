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
	target    scholarTarget
	next      string
	diff      *scholarDiff
	trial     *ui.Bitmap
	pages     int
	trialAt   time.Duration
	trialPage int
}

type scholarAgentState struct {
	target              scholarTarget
	inline, focusInline bool
	anchor              ui.Rect
	instruction, stream string
	parts               [4]string
	include             [4]bool
	showParts           bool
	info                scholar.CodexInfo
	probed, running     bool
	model               int
	patch               *scholarPatch
	batch               *scholarPatchSet
	multi, reviewHidden bool
	files               []scholarContextFile
	conversation        string
	session             *scholar.CodexConversation
	applied             []scholarAppliedFile
	trial               scholarTrialView
}

func (s *Shell) openScholarAgent(p *scholarState) {
	s.scholarToggleDrawer(p, scholarCodex)
	if p.drawer == scholarClosed {
		s.focusEditor = true
		return
	}
	if p.agent.patch == nil && p.agent.batch == nil && !p.agent.running {
		s.captureScholarTarget(p)
		if p.agent.target.tabID == 0 {
			p.agent.multi = true
			p.err = ""
		}
	}
}

func (s *Shell) openScholarInlinePrompt(p *scholarState) {
	a := &p.agent
	if a.patch != nil || a.running {
		p.drawer, p.compactNav = scholarCodex, false
		return
	}
	s.captureScholarTarget(p)
	if a.target.tabID == 0 {
		p.drawer, p.compactNav = scholarCodex, false
		return
	}
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
				p.drawer, p.compactNav = scholarCodex, false
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
		a.parts = [4]string{}
		return
	}
	before := snap.Slice(start, end)
	a.target = scholarTarget{tabID: tab.ID, start: start, end: end, line: snap.LineOf(start), path: tab.Path,
		version: snap.Version(), digest: sha256.Sum256([]byte(before)), before: before}
	p.err = ""
	parts := [4]string{before}
	var main string
	if mt := s.scholarTab(p.project.Main); mt != nil {
		ms := mt.Source().Buffer.Snapshot()
		main = ms.Slice(0, min(ms.Len(), 8<<10))
	}
	if typeset.IsLaTeXPath(p.project.Main) {
		if end := strings.Index(main, `\begin{document}`); end >= 0 {
			parts[1] = main[:end]
		}
	} else {
		var declarations strings.Builder
		for line := range strings.SplitSeq(main, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#let ") {
				declarations.WriteString(line)
				declarations.WriteByte('\n')
			}
		}
		parts[1] = declarations.String()
	}
	var refs strings.Builder
	if p.library != nil {
		for _, e := range p.library.Entries {
			if p.citations.counts[e.Key] == 0 && !strings.Contains(before, e.Key) {
				continue
			}
			row := e.Key + " — " + e.Title + "\n"
			if refs.Len()+len(row) > 8<<10 {
				break
			}
			refs.WriteString(row)
		}
	}
	parts[2] = refs.String()
	if pane := s.compile; pane != nil && pane.tab == tab && pane.sourceVersion == snap.Version() {
		parts[3] = pane.err
		if parts[3] == "" {
			var diagnostics strings.Builder
			for _, warning := range pane.warnings {
				diagnostics.WriteString(warning.Text)
				diagnostics.WriteByte('\n')
			}
			parts[3] = diagnostics.String()
		}
	}
	a.parts, a.include = parts, [4]bool{true, false, false, false}
}

var scholarContextNames = [4]string{"@selection", "@preamble", "@zotero", "@diag"}

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
	s.scholarFileContexts(c, p)
	s.scholarContextActions(c, p)
	if a.patch != nil {
		s.scholarPatchView(c, p, a.patch)
		return
	}
	if !a.probed {
		ui.Text(c, s.tr("scholarCodexSetup")).FontSize(11).Wrap()
		if ui.Button(c, s.tr("scholarAgentConnect")).Disabled(p.busy).Clicked() {
			s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
				info, err := scholar.ProbeCodex(ctx)
				return func() { a.probed, a.info = err == nil, info }, err
			})
		}
	} else {
		labels := []string{s.tr("scholarAgentDefaultModel")}
		for _, model := range a.info.Models {
			label := model.DisplayName
			if label == "" {
				label = model.Model
			}
			labels = append(labels, label)
		}
		scholarSelectIndex(c, &a.model, labels, a.running)
		if a.info.More {
			ui.Text(c, s.tr("scholarFirstModels")).FontSize(11)
		}
	}
	if ui.Button(c, s.tr("scholarAgentUseSelection")).Disabled(p.busy).Clicked() {
		s.captureScholarTarget(p)
	}
	if a.target.tabID == 0 && !a.multi {
		return
	}
	if a.target.tabID != 0 {
		ui.Text(c, fmt.Sprintf(s.tr("scholarLineLabel"), filepath.Base(a.target.path), a.target.line+1)).FontSize(12).Wrap()
	}
	for i, name := range scholarContextNames {
		if a.parts[i] == "" {
			continue
		}
		if ui.Checkbox(c.Key(i), &a.include[i], fmt.Sprintf("%s · %d B", name, len(a.parts[i]))).Disabled(a.running).Changed() {
			a.resetSession()
		}
	}
	if ui.Button(c, s.tr("scholarAgentViewContext")).Clicked() {
		a.showParts = !a.showParts
	}
	if a.showParts {
		for i, part := range a.parts {
			if a.include[i] && part != "" {
				ui.Text(c.Key(i), scholarContextNames[i]+"\n"+part).FontSize(11).Wrap()
			}
		}
	}
	ui.TextInput(c, &a.instruction).FillWidth().Placeholder(s.tr("scholarAgentInput")).Disabled(a.running)
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
	ui.Text(c, s.tr("scholarAgentDisclosure")).FontSize(11).Wrap()
	if ui.Button(c, s.tr("scholarAgentSend")).Disabled(p.busy || !a.probed || strings.TrimSpace(a.instruction) == "" ||
		(!a.multi && !a.include[0])).Clicked() {
		if a.multi {
			s.sendScholarPatchSet(p)
		} else {
			s.sendScholarAgent(p)
		}
	}
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
		return func() {
			a.running, a.stream = false, ""
			if err != nil {
				return
			}
			if !s.scholarTargetValid(target) {
				p.err = s.tr("scholarProposalStale")
				return
			}
			next = strings.ReplaceAll(strings.ReplaceAll(next, "\r\n", "\n"), "\r", "\n")
			if strings.ContainsRune(next, 0) {
				p.err = s.tr("scholarNullProposal")
				return
			}
			if next == target.before {
				p.err = s.tr("scholarUnchangedProposal")
				return
			}
			a.reviewHidden = false
			a.conversation = trimScholarConversation(a.conversation + "\nUser: " + a.instruction + "\nProposal: selection replacement")
			a.patch = &scholarPatch{target: target, next: next, diff: scholarDiffView(target.before, next)}
		}, err
	})
}

func (s *Shell) scholarPatchView(c *ui.Context, p *scholarState, patch *scholarPatch) {
	stale := !s.scholarTargetCurrent(patch.target)
	ui.Text(c, fmt.Sprintf(s.tr("scholarPendingLocation"), filepath.Base(patch.target.path), patch.target.line+1)).
		FontSize(12).TextColor(scholarInk(c, "agent"))
	if stale {
		ui.Text(c, s.tr("scholarBaselineChanged")).FontSize(11).TextColor(scholarInk(c, "warning"))
	}
	scholarDiffElement(c, patch.diff).FillWidth().Height(220)
	if patch.trial != nil {
		ui.Text(c, fmt.Sprintf("试排 · PDF 共 %d 页 · %d ms · 第 %d 页", patch.pages, patch.trialAt.Milliseconds(), patch.trialPage)).FontSize(11)
		w, h := patch.trial.Size()
		ui.Image(c, patch.trial).FillWidth().AspectRatio(float32(w) / float32(h))
	}
	ui.Row(c).Gap(4).Children(func() {
		if ui.Button(c, s.tr("scholarAgentTrial")).Disabled(p.busy || stale).Clicked() {
			s.trialScholarPatch(p, patch)
		}
		if p.busy && ui.Button(c, s.tr("scholarAgentCancelWork")).Clicked() {
			p.cancelCurrent()
		}
		accept := func() {
			if ui.Button(c, s.tr("scholarAgentAccept")).Disabled(stale || p.busy).Clicked() {
				s.acceptScholarPatch(p, patch)
			}
		}
		reject := func() {
			if ui.Button(c, s.tr("scholarAgentReject")).Disabled(p.busy).Clicked() {
				s.rejectScholarSingle(p)
			}
		}
		if s.pf.mac {
			reject()
			accept()
		} else {
			accept()
			reject()
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
			p.agent.trial = scholarTrialView{}
			p.agent.parts = [4]string{}
			if tab.typesetMode == typesetPreview {
				tab.typesetMode = typesetSplit
			}
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

func (s *Shell) scholarDiagnosticsView(c *ui.Context, p *scholarState) {
	if pane := s.compile; pane != nil && pane.tab == s.activeTab() {
		switch {
		case pane.busy:
			ui.Text(c, s.tr("scholarTypesetting"))
		case pane.err == "":
			ui.Text(c, s.tr("scholarTypesetSuccess"))
			for i, note := range pane.warnings {
				ck := c.Key(i)
				ui.Text(ck, note.Text).FontSize(11).Wrap().TextColor(scholarInk(c, "warning"))
				if note.Line > 0 && note.File != "" && ui.Button(ck, fmt.Sprintf(s.tr("scholarJumpLine"), note.Line)).Clicked() {
					s.jumpScholarDiagnostic(p, pane.path, note)
				}
			}
		default:
			if ui.Button(c, s.tr("scholarRebuildPreview")).Clicked() {
				pane.tab.typesetPaused = false
				s.closeCompile()
			}
			ui.Text(c, pane.err).FontSize(12).Wrap()
			if location := typeset.DiagnosticLocation(pane.err); location.Line > 0 {
				if ui.Button(c, fmt.Sprintf(s.tr("scholarJumpLine"), location.Line)).Clicked() {
					s.jumpScholarDiagnostic(p, pane.path, location)
				}
			}
			if ui.Button(c, s.tr("scholarAttachDiagnostics")).Disabled(p.busy || p.agent.patch != nil).Clicked() {
				p.drawer, p.compactNav = scholarCodex, false
				s.captureScholarTarget(p)
				if p.agent.target.tabID != 0 {
					p.agent.parts[3], p.agent.include[3] = pane.err, true
				}
			}
		}
	} else if tab := s.activeTab(); tab != nil && tab.typesetPaused {
		ui.Text(c, s.tr("scholarTypesetPaused")).Wrap()
		if ui.Button(c, s.tr("scholarRebuildPreview")).Clicked() {
			tab.typesetPaused = false
		}
	} else {
		ui.Text(c, s.tr("scholarOpenPreview")).Wrap()
	}
	ui.Text(c, s.tr("scholarDrawerWidth")).FontSize(12)
	width := p.drawerWidth(s.prefs)
	ui.Row(c).Gap(4).Children(func() {
		for _, px := range []int{320, 360, 420} {
			if ui.Button(c.Key(px), fmt.Sprint(px)).Disabled(px == width).Clicked() {
				if s.prefs.ScholarProjects == nil {
					s.prefs.ScholarProjects = make(map[string]scholarProjectPrefs)
				}
				pref := s.prefs.ScholarProjects[p.project.Root]
				pref.DrawerWidth = px
				p.drawerSize = float32(px)
				p.drawerSave = time.Time{}
				s.prefs.ScholarProjects[p.project.Root] = pref
				s.setPref("scholarProjects")
			}
		}
	})
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
		p.agent.trial = scholarTrialView{}
	}
	p.agent.conversation = trimScholarConversation(p.agent.conversation + "\nReview: rejected")
}

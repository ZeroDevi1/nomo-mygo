package shell

import (
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"nomo/desktop/internal/scholar"
	"nomo/desktop/provider"
	"nomo/desktop/typeset"
)

type manuscriptPDFNote struct {
	scholarPDFQuote
	page          int
	text, excerpt string
	anchor        ui.Point
}

type manuscriptNoteRequest struct {
	ctx                   context.Context
	note                  *manuscriptPDFNote
	version, serial       uint64
	page                  int
	region                ui.Rect
	excerpt               string
	anchor                ui.Point
	withImage, forceImage bool
	done                  func(scholarACPQuote, typeset.ProjectLocation, error)
}

const (
	manuscriptNoteLimit  = 2000
	manuscriptNoteHeight = 166
)

func scholarACPFileBudget(hasNotes bool) (int, string) {
	if hasNotes {
		return 128 << 10, "scholarACPNoteFileLimit"
	}
	return 32 << 10, "scholarFileContextLimit"
}

func (ref scholarACPQuote) textBytes() int {
	return len(ref.label) + len(ref.locator) + len(ref.text) + len(ref.note) + len(ref.manuscript)
}

func (a *scholarACPState) hasManuscriptNotes() bool {
	return slices.ContainsFunc(a.quotes, func(ref scholarACPQuote) bool { return ref.note != "" })
}

func (a *scholarAgentState) removeACPQuote(index int) {
	ref := a.acp.quotes[index]
	a.acp.quotes = slices.Delete(a.acp.quotes, index, index+1)
	if ref.manuscriptTarget != 0 && !slices.ContainsFunc(a.acp.quotes, func(other scholarACPQuote) bool {
		return other.manuscriptTarget == ref.manuscriptTarget
	}) {
		a.files = slices.DeleteFunc(a.files, func(file scholarContextFile) bool {
			return file.fromNote && file.target.tabID == ref.manuscriptTarget
		})
	}
}

func (p *compilePane) cancelManuscriptNote() {
	if p.note != nil {
		p.note.cancelCapture()
		p.note = nil
	}
	select {
	case <-p.notes:
	default:
	}
}

func (s *Shell) manuscriptPDFNoteButton(c *ui.Context, p *scholarState, pane *compilePane) {
	label := s.tr("scholarACPMarkManuscript")
	if note := pane.note; note != nil && note.selecting && !note.popup {
		label = s.tr("scholarACPDragRegion")
	}
	if s.toolButton(c, "message-square", label, pane.note != nil).Label(label).
		Role(ui.RoleToggleButton).Checked(pane.note != nil).
		Disabled(pane.note == nil && (p.busy || p.agent.pendingPatch() != nil || !pane.canLocate())).Clicked() {
		if pane.note != nil {
			pane.cancelManuscriptNote()
		} else {
			pane.sync.invalidate()
			pane.pageEdit = nil
			pane.pdfText.clear()
			pane.note = &manuscriptPDFNote{scholarPDFQuote: scholarPDFQuote{selecting: true, focusReader: true}}
		}
	}
}

func (s *Shell) manuscriptPDFNotePage(c *ui.Context, pane *compilePane, el ui.Element, page int, firstVisible bool) {
	note := pane.note
	bounds := el.Bounds()
	el.Cursor(ui.CursorCrosshair).Focusable().KeyShortcuts(0, ui.KeyEnter).KeyShortcuts(0, ui.KeyEscape).
		Description(s.tr("scholarACPPDFKeyboard"))
	if note.focusReader && firstVisible {
		if bounds.W > 0 {
			el.Focus(ui.FocusOptions{PreventScroll: true})
			note.focusReader = false
		} else {
			c.Invalidate()
		}
	}
	if el.Shortcut(0, ui.KeyEscape) {
		pane.cancelManuscriptNote()
		return
	}
	if el.Shortcut(0, ui.KeyEnter) && !note.popup {
		note.page, note.region, note.popup = page, ui.Rect{W: 1, H: 1}, true
	}
	el.HandleInput(func(ev ui.InputEvent) bool {
		if pane.note != note || note.popup || note.dragging && note.page != page || ev.Button != 0 {
			return false
		}
		if ev.Kind == ui.InputPointerDown {
			note.page = page
		}
		handled := note.regionInput(bounds, ev)
		if handled {
			c.Invalidate()
		}
		return handled
	})
	if note.page == page {
		note.bounds = bounds
		if r := note.region; scholarValidPDFRegion(r) {
			ui.Box(c.Key("note-selection")).Absolute().Left(r.X*bounds.W).Top(r.Y*bounds.H).Size(r.W*bounds.W, r.H*bounds.H).
				Background(s.ch.accent.Alpha(.16)).Border(1, s.ch.accent).PassThrough()
		}
	}
}

func (s *Shell) manuscriptPDFNoteEditor(c *ui.Context, p *scholarState, pane *compilePane) {
	note := pane.note
	if note == nil || !note.popup || !scholarValidPDFRegion(note.region) {
		return
	}
	view, box, r := pane.sync.pdfViewport, note.bounds, note.region
	width := min(float32(340), view.W-16)
	if width <= 0 {
		return
	}
	x := max(view.X+8, min(box.X+r.X*box.W, view.X+view.W-width-8))
	y := box.Y + (r.Y+r.H)*box.H + 6
	if y+manuscriptNoteHeight > view.Y+view.H {
		y = box.Y + r.Y*box.H - manuscriptNoteHeight
	}
	y = max(view.Y+8, min(y, view.Y+view.H-manuscriptNoteHeight))
	label := s.tr("scholarACPManuscriptNote")
	ui.Overlay(c, func() {
		ck := c.Key(note)
		panel := ui.Column(ck).Absolute().Left(x).Top(y).Width(width).Padding(9).Gap(6).
			Radius(c.Theme().Radius).Background(s.ch.content).Border(1, s.ch.separator).Label(label)
		panel.Children(func() {
			ui.Text(ck, fmt.Sprintf("%s · p.%d", label, note.page)).Bold().FontSize(12)
			field := ui.TextAreaBase(ck.Key("note"), &note.text).FillWidth().Height(64).MaxLength(manuscriptNoteLimit).
				Placeholder(s.tr("scholarACPNotePlaceholder")).Label(label).Disabled(note.cancel != nil).AutoFocus()
			if note.err != "" {
				ui.Text(ck, note.err).Wrap().FontSize(11).TextColor(scholarInk(c, "warning"))
			}
			ui.Row(ck.Key("actions")).FillWidth().Gap(5).Justify(ui.End).Children(func() {
				imageLabel := s.tr("scholarACPImage")
				if s.toolButton(ck.Key("screenshot"), "image", imageLabel, note.picture).
					Label(imageLabel).Role(ui.RoleToggleButton).Checked(note.picture).
					Disabled(note.cancel != nil || !manuscriptNoteCanAttachImage(p)).Clicked() {
					note.picture = !note.picture
				}
				ui.Spacer(ck)
				if ui.Button(ck.Key("cancel"), s.tr("cancel")).Clicked() || panel.OverlayShortcut(0, ui.KeyEscape) {
					pane.cancelManuscriptNote()
					return
				}
				ready := note.cancel == nil && !p.busy && p.agent.pendingPatch() == nil && strings.TrimSpace(note.text) != ""
				label := "scholarACPAddNote"
				if note.cancel != nil {
					label = "scholarACPReadingNote"
				}
				if ui.PrimaryButton(ck.Key("add"), s.tr(label)).Disabled(!ready).Clicked() ||
					ready && field.Shortcut(ui.Cmd, ui.KeyEnter) {
					s.stageManuscriptNote(p, pane, note)
				}
			})
		})
	})
}

func manuscriptNoteCanAttachImage(p *scholarState) bool {
	if known, available := scholarACPImageCapability(); known && !available {
		return false
	}
	return !slices.ContainsFunc(p.agent.acp.quotes, func(ref scholarACPQuote) bool { return len(ref.png) > 0 })
}

func (s *Shell) stageManuscriptNote(p *scholarState, pane *compilePane, note *manuscriptPDFNote) {
	if !pane.canLocate() || !s.previewBuffersCurrent(pane) {
		note.err = s.tr("scholarPageEditStale")
		return
	}
	noteText := strings.TrimSpace(note.text)
	if len(noteText) > 4*manuscriptNoteLimit || !utf8.ValidString(noteText) {
		note.err = s.tr("scholarACPQuoteLimit")
		return
	}
	withImage := manuscriptNoteCanAttachImage(p)
	ctx, cancel := context.WithCancel(pane.lifetime)
	note.cancel, note.err = cancel, ""
	thread := p.agent.acp.thread
	request := manuscriptNoteRequest{
		ctx: ctx, note: note, version: pane.fullVersion, serial: pane.fullSerial, page: note.page, region: note.region, withImage: withImage,
		forceImage: note.picture, excerpt: note.excerpt, anchor: note.anchor,
		done: func(ref scholarACPQuote, location typeset.ProjectLocation, err error) {
			defer cancel()
			note.cancel = nil
			if err != nil {
				if ctx.Err() == nil {
					note.err = err.Error()
				}
				return
			}
			ref.note = noteText
			ref.noteRegion, ref.notePage, ref.noteStamp = note.region, note.page, pane.noteStamp
			if strings.TrimSpace(ref.text) == "" && len(ref.png) == 0 && location.Path == "" {
				note.err = s.tr("scholarACPNoteNoContext")
				return
			}
			if p != s.scholar || p.busy || p.agent.pendingPatch() != nil || p.agent.acp.thread != thread {
				note.err = s.tr("scholarACPChooseDraft")
				return
			}
			if !s.previewBuffersCurrent(pane) {
				note.err = s.tr("scholarPageEditStale")
				return
			}
			s.addManuscriptNote(p, pane, note, ref, location)
		},
	}
	select {
	case <-pane.notes:
	default:
	}
	pane.notes <- request
}

// A paper mark is drawn only on the same inputs that produced its PDF.
// The draft carries this small token across preview and chat switches.
func manuscriptNoteStamp(pane *compilePane) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%q:%d:%d\n", pane.path, pane.tab.ID, pane.sourceVersion)
	if inputs := pane.projectInput; inputs != nil {
		fmt.Fprintf(hash, "%d\n", inputs.disk)
		for _, name := range slices.Sorted(maps.Keys(inputs.inputs)) {
			input := inputs.inputs[name]
			fmt.Fprintf(hash, "%q:%d:%d\n", name, input.tabID, input.version)
		}
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func (s *Shell) manuscriptPDFNoteMarks(c *ui.Context, pane *compilePane, refs []scholarACPQuote, page int, bounds ui.Rect) {
	for i, ref := range refs {
		if ref.notePage != page || ref.noteStamp != pane.noteStamp {
			continue
		}
		r := ref.noteRegion
		ui.Box(c.Key(i)).Absolute().Left(r.X*bounds.W).Top(r.Y*bounds.H).Size(r.W*bounds.W, r.H*bounds.H).
			Background(s.ch.accent.Alpha(.12)).Border(1, s.ch.accent).PassThrough()
	}
}

func (p *compilePane) captureManuscriptNote(ctx context.Context, session provider.Session, base editor.Snapshot, req manuscriptNoteRequest) (scholarACPQuote, typeset.ProjectLocation, error) {
	ref := scholarACPQuote{label: filepath.Base(p.path), locator: "p." + strconv.Itoa(req.page)}
	engine, ok := session.(*typeset.Session)
	if !ok {
		return ref, typeset.ProjectLocation{}, nil
	}
	r := req.region
	text, x, y := req.excerpt, float64(req.anchor.X), float64(req.anchor.Y)
	if text == "" {
		var err error
		text, x, y, err = engine.ManuscriptRegion(ctx, req.page, float64(r.X), float64(r.Y), float64(r.W), float64(r.H), scholarSelectionLimit-2048)
		if err != nil {
			return ref, typeset.ProjectLocation{}, err
		}
	}
	ref.text = text
	location := engine.ProjectSourceLocation(ctx, req.page, x, y)
	if location.Path == "" && p.project.Root == "" {
		if line := engine.SourceLine(ctx, req.page, x, y, base.String); line >= 0 && line < base.LineCount() {
			location = typeset.ProjectLocation{Path: p.sourcePath, Line: line, Text: strings.TrimPrefix(base.Line(line), "\ufeff")}
		}
	}
	// Use the draft's one image slot for a region without extractable text.
	if req.withImage && (req.forceImage || strings.TrimSpace(text) == "") {
		img, err := session.PageAtSize(ctx, req.page, scholarReferenceEdge)
		if err != nil {
			return ref, location, err
		}
		ref.png, err = encodeScholarACPImage(img, r, image.White)
		if err != nil {
			return ref, location, err
		}
	}
	return ref, location, ctx.Err()
}

func (s *Shell) addManuscriptNote(p *scholarState, pane *compilePane, note *manuscriptPDFNote, ref scholarACPQuote, location typeset.ProjectLocation) {
	a := &p.agent
	path := location.Path
	if path == "" {
		path = pane.path
	}
	name, err := scholar.Relative(p.project.Root, path)
	if err != nil || !isManuscript(&Tab{Path: path}) || scholarPathReserved(path) {
		note.err = s.tr("scholarACPSourceUnavailable")
		return
	}
	if location.Path != "" {
		ref.locator += fmt.Sprintf(" · %s:%d", filepath.ToSlash(name), location.Line+1)
	} else {
		ref.locator += " · " + filepath.ToSlash(name)
	}
	ref.manuscript = filepath.ToSlash(name)
	if !a.currentACPDraft().acceptsQuote(ref) {
		note.err = s.tr("scholarACPQuoteLimit")
		return
	}
	t := s.scholarTab(path)
	if t == nil {
		prior := s.activeTab()
		s.OpenFiles([]string{path})
		s.activate(s.findTabIndex(prior.ID))
		t = s.scholarTab(path)
	}
	if t == nil || t.Source().ReadOnly {
		note.err = s.tr("scholarACPSourceUnavailable")
		return
	}
	snap := t.Source().Buffer.Snapshot()
	if snap.Len() > scholar.ACPFileLimit {
		note.err = s.tr("scholarACPNoteFileLimit")
		return
	}
	if location.Path != "" {
		if location.Line < 0 || location.Line >= snap.LineCount() ||
			strings.TrimSuffix(strings.TrimPrefix(snap.Line(location.Line), "\ufeff"), "\r") != strings.TrimSuffix(location.Text, "\r") {
			note.err = s.tr("scholarPageEditStale")
			return
		}
	}
	// Paper notes and full-file attachments share the normal ACP edit scope.
	// Preserve a prior selection in a different file when entering multi-file mode.
	files := a.files
	if !a.multi && a.include[0] && a.target.tabID != 0 && a.target.tabID != t.ID {
		if !slices.ContainsFunc(files, func(f scholarContextFile) bool { return f.target.tabID == a.target.tabID }) {
			if !s.scholarTargetValid(a.target) {
				note.err = s.tr("scholarStaleBaseline")
				return
			}
			files = append(slices.Clone(files), scholarContextFile{target: a.target})
		}
	}
	total := snap.Len()
	for _, file := range files {
		if file.target.tabID != t.ID {
			total += len(file.target.before)
		}
	}
	budget, limitLabel := scholarACPFileBudget(true)
	if total > budget || len(files) >= 16 && !slices.ContainsFunc(files, func(f scholarContextFile) bool { return f.target.tabID == t.ID }) {
		note.err = s.tr(limitLabel)
		return
	}
	target := fullManuscriptTarget(t, snap)
	ref.manuscriptTarget = target.tabID
	if index := slices.IndexFunc(files, func(f scholarContextFile) bool { return f.target.tabID == t.ID }); index >= 0 {
		if files[index].target.version != target.version || !samePath(files[index].target.path, target.path) {
			note.err = s.tr("scholarStaleBaseline")
			return
		}
		files[index].target = target
	} else {
		files = append(files, scholarContextFile{target: target, fromNote: !a.include[0] || a.target.tabID != t.ID})
	}
	a.files, a.multi = files, true
	a.target, a.parts[0], a.include[0] = scholarTarget{}, "", false
	a.acp.quotes = append(a.acp.quotes, ref)
	a.acp.historyOpen = false
	a.inline = false
	if s.win != nil {
		width, _ := s.win.ContentSize()
		if !s.scholarReadingInspectorPrimary(float32(width)) {
			s.showScholarInspector(p, scholarCodex)
		}
	}
	p.err, s.status = "", s.tr("scholarACPNoteAdded")
	pane.cancelManuscriptNote()
}

func fullManuscriptTarget(tab *Tab, snap editor.Snapshot) scholarTarget {
	body := snap.String()
	return scholarTarget{tabID: tab.ID, path: tab.Path, end: snap.Len(), version: snap.Version(), before: body, digest: sha256.Sum256([]byte(body))}
}

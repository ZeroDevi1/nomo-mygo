package shell

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"golang.org/x/image/draw"

	"nomo/desktop/internal/scholar"
	"nomo/desktop/typeset"
)

type scholarPDFQuote struct {
	selecting, dragging, popup, picture bool
	focusReader, focusActions           bool
	x, y                                float32
	region, bounds                      ui.Rect
	err                                 string
	cancel                              context.CancelFunc
	attempt                             uint64
}

func (q *scholarPDFQuote) cancelCapture() {
	if q.cancel != nil {
		q.cancel()
		q.cancel = nil
	}
	q.attempt++
}

func (s *Shell) scholarPDFQuoteControls(c *ui.Context, p *scholarState, t *scholarReferenceTab) {
	q := &t.quote
	ui.Row(c).FillWidth().Padding(4, 8).Gap(4).AlignItems(ui.Center).Children(func() {
		label := s.tr("scholarACPPDFRegion")
		if q.selecting {
			label = s.tr("cancel")
		}
		button := s.scholarACPAction(c.Key("region"), "maximize-2", label, q.selecting)
		if button.Disabled(p.busy && !q.selecting).Clicked() {
			if p.references.focused != t {
				if group := s.groups.root.containing(workspaceItem{reference: t}); group != nil {
					s.activateWorkspaceItem(group, workspaceItem{reference: t})
				}
			}
			q.cancelCapture()
			*q = scholarPDFQuote{selecting: !q.selecting, focusReader: true, attempt: q.attempt}
		}
		if q.selecting && q.region.W > 0 && !q.dragging && !q.popup {
			s.scholarPDFQuoteActions(c.Key("actions"), p, t)
		} else if q.selecting && !q.popup {
			ui.Text(c, s.tr("scholarACPDragRegion")).FontSize(11).SingleLine().MinWidth(0).TextColor(s.ch.muted).
				Tooltip(s.tr("scholarACPPDFKeyboard"))
		}
	})
	message := q.err
	if known, available := scholarACPImageCapability(); message == "" && q.picture && known && !available {
		message = s.tr("scholarACPImageUnsupported")
	}
	if message != "" {
		ui.Text(c.Key("error"), message).Wrap().Padding(0, 8).FontSize(11).TextColor(scholarInk(c, "warning")).
			Role(ui.RoleStatus).Label(message)
	}
}

func scholarACPImageCapability() (known, available bool) {
	info := registry.acp.info
	prompt := info.Capabilities.Session.Prompt
	return info.ProtocolVersion != 0, prompt != nil && prompt.Image != nil
}

func (s *Shell) scholarPDFQuoteActions(c *ui.Context, p *scholarState, t *scholarReferenceTab) {
	q := &t.quote
	ref := scholarACPQuote{scope: scholarReferenceScope(t), label: t.title}
	thread := s.scholarACPQuoteThread(p, ref, false)
	current := p.agent.acp.thread
	conflict := p.agent.hasDraft() && !sameScholarACPThread(current, thread)
	disabled := p.busy || p.agent.pendingPatch() != nil
	known, imageOK := scholarACPImageCapability()
	imageBlocked := q.picture && known && !imageOK
	label, glyph := "scholarACPAskText", "quote"
	mode, modeIcon := "scholarACPSwitchImage", "image"
	if q.picture {
		label, glyph = "scholarACPAskImage", "image"
		mode, modeIcon = "scholarACPSwitchText", "type"
	}
	ui.Row(c).Grow(1).MinWidth(0).Gap(4).AlignItems(ui.Center).Role(ui.RoleToolbar).
		Label(s.tr("scholarACPPDFRegion")).Children(func() {
		if q.cancel != nil {
			label := s.tr("cancel")
			if s.scholarACPAction(c.Key("cancel-capture"), "x", label, false).Clicked() {
				q.cancelCapture()
				q.focusActions = true
			}
			return
		}
		secondary := func(m *ui.Menu) {
			if m.Item(s.tr("scholarACPNew")).Disabled(disabled || imageBlocked).Chosen() {
				s.captureScholarPDFQuote(p, t, s.scholarACPQuoteThread(p, ref, true), true, false, "")
			}
			if m.Item(s.tr("scholarACPCopyText")).Chosen() {
				s.captureScholarPDFQuote(p, t, current, false, true, "")
			}
			m.Separator()
			if m.Item(s.tr(mode)).Chosen() {
				q.picture, q.err = !q.picture, ""
			}
		}
		var ask ui.Element
		if s.pf.windows && !conflict {
			split := ui.PrimarySplitButton(c.Key("ask-split"), s.tr(label))
			split.Group.Grow(1).MinWidth(0)
			ask = split.Button.MinWidth(0).Shrink(1)
			split.Arrow.Label(s.tr("scholarACPOptions")).Tooltip(s.tr("scholarACPOptions")).Menu(secondary)
		} else {
			ask = s.scholarACPAction(c.Key("ask"), glyph, s.tr(label), true).Grow(1)
			s.toolButton(c.Key("more"), "ellipsis", s.tr("scholarACPOptions"), false).
				Label(s.tr("scholarACPOptions")).FocusRing(true).Shrink(0).Menu(secondary)
		}
		ask.Disabled(disabled || imageBlocked).Tooltip(s.tr(label))
		if q.focusActions {
			if disabled || imageBlocked {
				q.focusActions = false
			} else if ask.Bounds().W > 0 {
				ask.Focus(ui.FocusOptions{PreventScroll: true})
				q.focusActions = false
			} else {
				c.After(time.Millisecond)
			}
		}
		if conflict {
			ask.Menu(func(m *ui.Menu) {
				if m.Item(s.tr("scholarACPJoinDraft")).Chosen() {
					s.captureScholarPDFQuote(p, t, current, false, false, "")
				}
				if m.Item(s.tr("scholarACPSwitchThread")).Chosen() {
					s.captureScholarPDFQuote(p, t, thread, false, false, "")
				}
			})
		} else if ask.Clicked() {
			s.captureScholarPDFQuote(p, t, thread, false, false, "")
		}
		if s.toolButton(c.Key("mode"), modeIcon, s.tr(mode), q.picture).Label(s.tr(mode)).
			FocusRing(true).Shrink(0).Clicked() {
			q.picture, q.err = !q.picture, ""
		}
	})
}

func (s *Shell) scholarPDFQuotePopup(c *ui.Context, p *scholarState) {
	t := p.references.focused
	if t == nil || t.web != nil || p.references.catalog || !t.quote.popup || t.bitmap == nil {
		return
	}
	q := &t.quote
	w, h := c.Size()
	if s.scholarInspectorVisible(p, w) && s.scholarInspectorPrimary(p, w) {
		return
	}
	width := min(float32(250), max(1, w-24))
	left := q.bounds.X + q.region.X*q.bounds.W
	top := q.bounds.Y + q.region.Y*q.bounds.H
	bottom := top + q.region.H*q.bounds.H
	x := max(12, min(left, w-width-12))
	y := bottom + 6
	if y+36 > h-28 {
		y = top - 42
	}
	y = max(44, min(y, h-64))
	panel := s.scholarACPSelectionBar(c.Key(t).Key("pdf-selection"), x, y, width, func() {
		s.scholarPDFQuoteActions(c, p, t)
	})
	if panel.OverlayShortcut(0, ui.KeyEscape) {
		q.cancelCapture()
		q.popup, q.selecting, q.focusReader, q.err = false, false, true, ""
	} else if panel.PressedOutside() {
		q.popup = false
	}
}

func (s *Shell) scholarPDFQuotePage(c *ui.Context, p *scholarState, t *scholarReferenceTab, width float32) {
	q := &t.quote
	ui.Column(c.Key("pdf-image")).WidthPercent(width).AspectRatio(t.pageAspect).Children(func() {
		el := ui.Image(c.Key("page"), t.bitmap).FillWidth().AspectRatio(t.pageAspect).Label(t.title).Focusable().FocusRing(true)
		bounds := el.Bounds()
		q.bounds = bounds
		if q.focusReader || t.focusPage {
			if bounds.W > 0 {
				el.Focus(ui.FocusOptions{PreventScroll: true})
				q.focusReader, t.focusPage = false, false
			} else {
				c.After(time.Millisecond)
			}
		}
		if q.selecting {
			el.Description(s.tr("scholarACPPDFKeyboard")).KeyShortcuts(0, ui.KeyEnter).KeyShortcuts(0, ui.KeyEscape)
			el.Cursor(ui.CursorCrosshair).HandleInput(func(ev ui.InputEvent) bool { return q.regionInput(bounds, ev) })
			if el.Shortcut(0, ui.KeyEscape) {
				q.cancelCapture()
				q.selecting, q.popup, q.err = false, false, ""
			}
			if el.Shortcut(0, ui.KeyEnter) {
				if !scholarValidPDFRegion(q.region) {
					q.region = ui.Rect{W: 1, H: 1}
				}
				q.popup, q.focusActions = true, true
			}
			if r := q.region; scholarValidPDFRegion(r) {
				ui.Box(c.Key("selection")).Absolute().Left(r.X*bounds.W).Top(r.Y*bounds.H).Size(r.W*bounds.W, r.H*bounds.H).
					Background(s.ch.accent.Alpha(.16)).Border(1, s.ch.accent).PassThrough()
			}
		}
		s.scholarPDFSourceHighlight(c, p, t, bounds)
	})
}

func (q *scholarPDFQuote) regionInput(bounds ui.Rect, ev ui.InputEvent) bool {
	if !q.selecting || ev.Button != 0 || bounds.W <= 0 || bounds.H <= 0 {
		return false
	}
	x, y := max(0, min(1, ev.X/bounds.W)), max(0, min(1, ev.Y/bounds.H))
	switch ev.Kind {
	case ui.InputPointerDown:
		q.cancelCapture()
		q.x, q.y, q.dragging, q.popup, q.region, q.err = x, y, true, false, ui.Rect{}, ""
		return true
	case ui.InputPointerMove, ui.InputPointerUp:
		if !q.dragging {
			return false
		}
		q.region = ui.Rect{X: min(x, q.x), Y: min(y, q.y), W: absF32(x - q.x), H: absF32(y - q.y)}
		if ev.Kind == ui.InputPointerUp {
			q.dragging = false
			if ev.Canceled || q.region.W*bounds.W < 3 || q.region.H*bounds.H < 3 {
				q.region = ui.Rect{}
			} else {
				q.popup, q.focusActions = true, true
			}
		}
		return true
	}
	return false
}

func (s *Shell) captureScholarPDFQuote(p *scholarState, t *scholarReferenceTab, thread scholarACPThread, newChat, copyText bool, prompt string) {
	q := &t.quote
	if q.cancel != nil || !scholarValidPDFRegion(q.region) {
		return
	}
	path, page, generation, shown := t.path, t.page, t.generation, t.fileInfo
	picture, region := q.picture && !copyText, q.region
	pdf := scholarPDFSource{Item: t.entry.RemoteID, Library: t.zotero.Group, Attachment: t.key, Page: page, Region: region}
	if t.key == "" {
		name, err := scholar.Relative(p.project.Root, path)
		if err != nil {
			q.err = s.tr("scholarACPSourceUnavailable")
			return
		}
		pdf.Path = filepath.ToSlash(name)
	}
	label := t.title
	if t.entry.Author != "" && t.entry.Year != "" {
		label = strings.TrimSpace(t.entry.Author) + " " + strings.TrimSpace(t.entry.Year)
	}
	ref := scholarACPQuote{scope: scholarReferenceScope(t), label: label}
	empty, unavailable := s.tr("scholarACPEmptyPDFText"), s.tr("scholarACPSourceUnavailable")
	ctx, cancel := context.WithTimeout(p.ctx, 20*time.Second)
	q.cancel, q.err = cancel, ""
	attempt := q.attempt
	original := p.agent.acp.thread
	draft, instruction := p.agent.hasDraft(), p.agent.instruction
	switching := newChat || !sameScholarACPThread(original, thread)
	stillWanted := func() bool {
		return s.scholar == p && !p.references.catalog && p.references.focused == t &&
			t.generation == generation && q.attempt == attempt
	}
	go func() {
		defer cancel()
		var err error
		pdf.Version, err = scholarPDFVersion(ctx, path, shown)
		if err != nil {
			err = errors.New(unavailable)
		} else if picture {
			img, readErr := typeset.ReadPDFPage(ctx, path, page, scholarReferenceEdge)
			if readErr != nil {
				err = readErr
			} else {
				ref.png, err = encodeScholarACPImage(img, region, image.White)
			}
		} else {
			ref.text, err = typeset.PDFRegionText(ctx, path, page, float64(region.X), float64(region.Y), float64(region.W), float64(region.H), scholarSelectionLimit)
			if err == nil && strings.TrimSpace(ref.text) == "" {
				err = errors.New(empty)
			}
		}
		if err == nil && !copyText {
			pdf.PageLabel = typeset.PDFPageLabel(ctx, path, page)
			ref.pdf = pdf
			label := pdf.PageLabel
			if label == "" {
				label = strconv.Itoa(page)
			}
			ref.locator = "p." + label
		}
		if err == nil {
			err = ctx.Err()
			if after, statErr := os.Stat(path); err == nil && (statErr != nil || shown == nil || !scholarSamePDFVersion(shown, after)) {
				err = errors.New(unavailable)
			}
		}
		s.post(func() {
			if !stillWanted() {
				return
			}
			q.cancel = nil
			if err != nil {
				q.err = err.Error()
			} else if copyText {
				s.svc.WriteClipboard(ref.text)
			} else if p.busy || p.agent.acp.thread != original ||
				switching && (instruction != p.agent.instruction || !draft && p.agent.hasDraft()) {
				q.err = s.tr("scholarACPChooseDraft")
			} else if known, available := scholarACPImageCapability(); picture && known && !available {
				q.err = s.tr("scholarACPImageUnsupported")
			} else if !s.attachScholarACPQuote(p, ref, thread, newChat, stillWanted, func() {
				if prompt != "" && strings.TrimSpace(p.agent.instruction) == "" {
					p.agent.instruction = prompt
				}
				if q.attempt == attempt && t.generation == generation {
					*q = scholarPDFQuote{attempt: q.attempt + 1}
				}
			}) && p.err != "" {
				q.err = p.err
			}
		})
	}()
}

func (s *Shell) scholarWebNativeQuoteMenu(p *scholarState, t *scholarReferenceTab, event *mygo.ContextMenuEvent) {
	w := t.web
	if event.Menu == nil || w.loading || p.busy || p.agent.pendingPatch() != nil {
		return
	}
	view, address := w.view, w.url
	selection := strings.TrimSpace(event.SelectionText)
	available := func() bool {
		return s.scholar == p && p.references.focused == t && w.view == view && w.url == address && !p.busy && p.agent.pendingPatch() == nil
	}
	var items []*mygo.MenuItem
	if selection != "" && len(selection) <= scholarSelectionLimit {
		for _, newChat := range []bool{false, true} {
			label := "scholarACPAppendText"
			if newChat {
				label = "scholarACPNewText"
			}
			items = append(items, &mygo.MenuItem{Label: s.tr(label), Click: func(*mygo.MenuItem, *mygo.Window) {
				if available() {
					ref := scholarWebACPQuote(t, address)
					ref.text = selection
					s.quoteScholar(p, ref, newChat, nil)
					s.win.Invalidate()
				}
			}})
		}
	}
	for _, newChat := range []bool{false, true} {
		label := "scholarACPAppendImage"
		if newChat {
			label = "scholarACPNewImage"
		}
		items = append(items, &mygo.MenuItem{Label: s.tr(label), Click: func(*mygo.MenuItem, *mygo.Window) {
			if available() {
				s.captureScholarWebQuote(p, t, true, newChat)
				s.win.Invalidate()
			}
		}})
	}
	event.Menu.Append(mygo.Separator(), &mygo.MenuItem{Label: s.tr("scholarACPQuote"), Submenu: items})
}

func (s *Shell) scholarWebQuoteMenu(m *ui.Menu, p *scholarState, t *scholarReferenceTab) {
	for _, action := range []struct {
		picture, newChat bool
		key              string
	}{
		{false, false, "scholarACPAppendText"}, {false, true, "scholarACPNewText"},
		{true, false, "scholarACPAppendImage"}, {true, true, "scholarACPNewImage"},
	} {
		if m.Item(s.tr(action.key)).Disabled(p.agent.pendingPatch() != nil).Chosen() {
			s.captureScholarWebQuote(p, t, action.picture, action.newChat)
		}
	}
}

func (s *Shell) captureScholarWebQuote(p *scholarState, t *scholarReferenceTab, picture, newChat bool) {
	w := t.web
	view, address := w.view, w.url
	if view == nil || w.loading {
		return
	}
	ref := scholarWebACPQuote(t, address)
	empty := s.tr("scholarACPNoSelection")
	canvas := s.ch.content
	background := image.NewUniform(color.NRGBA{R: canvas.R, G: canvas.G, B: canvas.B, A: 255})
	w.quoteErr = ""
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		fail := func(err error) (func(), error) {
			return func() {
				if w.view == view && w.url == address {
					w.quoteErr = err.Error()
				}
			}, nil
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if picture {
			raw, err := view.CapturePage()
			if err != nil {
				return fail(err)
			}
			if len(raw) > 32<<20 {
				return fail(errors.New("网页截图超过 32 MiB"))
			}
			conf, err := png.DecodeConfig(bytes.NewReader(raw))
			if err != nil || conf.Width < 1 || conf.Height < 1 || int64(conf.Width)*int64(conf.Height) > 20_000_000 {
				return fail(errors.New("无法读取网页截图尺寸"))
			}
			img, err := png.Decode(bytes.NewReader(raw))
			if err != nil {
				return fail(err)
			}
			ref.png, err = encodeScholarACPImage(img, ui.Rect{W: 1, H: 1}, background)
			if err != nil {
				return fail(err)
			}
		} else {
			// Evaluate only on demand. The remote document has no Go bridge or
			// ability to initiate a submission or choose a destination session.
			data, err := view.Page().EvalContext(ctx, `String(window.getSelection() || '').slice(0, 16385)`)
			if err != nil {
				return fail(err)
			}
			ref.text, _ = data.(string)
			ref.text = strings.TrimSpace(ref.text)
			if ref.text == "" {
				return fail(errors.New(empty))
			}
		}
		return func() {
			if p.references.focused == t && w.view == view && w.url == address {
				s.quoteScholar(p, ref, newChat, nil)
			}
		}, ctx.Err()
	})
}

func scholarWebACPQuote(t *scholarReferenceTab, address string) scholarACPQuote {
	locator := address
	if u, err := url.Parse(address); err == nil {
		u.RawQuery, u.Fragment, u.ForceQuery = "", "", false
		locator = u.String()
	}
	return scholarACPQuote{scope: scholarReferenceScope(t), label: t.title, locator: locator}
}

func encodeScholarACPImage(img image.Image, region ui.Rect, background *image.Uniform) ([]byte, error) {
	src := img.Bounds()
	w, h := float64(src.Dx()), float64(src.Dy())
	crop := image.Rect(
		src.Min.X+int(math.Floor(w*float64(region.X))), src.Min.Y+int(math.Floor(h*float64(region.Y))),
		src.Min.X+int(math.Ceil(w*float64(region.X+region.W))), src.Min.Y+int(math.Ceil(h*float64(region.Y+region.H))),
	).Intersect(src)
	if crop.Empty() {
		return nil, errors.New("empty screenshot region")
	}
	var buf bytes.Buffer
	for _, edge := range [...]int{2048, 1400, 700, 350} {
		scale := min(1, float64(edge)/float64(max(crop.Dx(), crop.Dy())))
		dst := image.NewRGBA(image.Rect(0, 0, max(1, int(float64(crop.Dx())*scale)), max(1, int(float64(crop.Dy())*scale))))
		// WebKit can leave the page canvas transparent in a snapshot.
		draw.Draw(dst, dst.Bounds(), background, image.Point{}, draw.Src)
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, crop, draw.Over, nil)
		buf.Reset()
		if err := png.Encode(&buf, dst); err != nil {
			return nil, err
		}
		if buf.Len() <= scholarACPImageLimit {
			return buf.Bytes(), nil
		}
	}
	return nil, errors.New("截图超过 ACP 引用大小限制")
}

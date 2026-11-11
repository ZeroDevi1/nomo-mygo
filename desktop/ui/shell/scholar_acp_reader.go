package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"golang.org/x/image/draw"

	"nomo/desktop/typeset"
)

type scholarPDFQuote struct {
	selecting, dragging bool
	popup               bool
	x, y                float32
	region, anchor      ui.Rect
	err                 string
}

func (s *Shell) scholarPDFQuoteControls(c *ui.Context, p *scholarState, t *scholarReferenceTab) {
	q := &t.quote
	ui.Row(c).FillWidth().Padding(4, 8).Gap(4).AlignItems(ui.Center).Children(func() {
		label := s.tr("scholarACPPDFRegion")
		if q.selecting {
			label = s.tr("cancel")
		}
		if s.scholarACPAction(c.Key("region"), "maximize-2", label, q.selecting).Disabled(p.busy).Clicked() {
			*q = scholarPDFQuote{selecting: !q.selecting}
		}
		if q.selecting && q.region.W > 0 && q.region.H > 0 && !q.dragging && !q.popup {
			s.scholarPDFQuoteActions(c.Key("actions"), p, t)
		} else if q.selecting && !q.popup {
			ui.Text(c, s.tr("scholarACPDragRegion")).FontSize(11).SingleLine().TextColor(s.ch.muted)
		}
	})
	if q.err != "" {
		ui.Text(c.Key("error"), q.err).Wrap().Padding(0, 8).FontSize(11).TextColor(scholarInk(c, "warning"))
	}
}

func (s *Shell) scholarPDFQuoteActions(c *ui.Context, p *scholarState, t *scholarReferenceTab) {
	q := &t.quote
	ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
		if s.scholarACPAction(c.Key("copy"), "copy", s.tr("scholarACPCopyText"), false).Disabled(p.busy).Clicked() {
			q.popup = false
			s.captureScholarPDFQuote(p, t, q.region, false, true, false)
		}
		for _, action := range []struct {
			key, label, icon string
			picture          bool
		}{
			{"text", s.tr("scholarACPText"), "type", false},
			{"image", s.tr("scholarACPImage"), "image", true},
		} {
			s.scholarACPAction(c.Key(action.key), action.icon, action.label, false).
				Disabled(p.busy || p.agent.pendingPatch() != nil).Menu(func(m *ui.Menu) {
				if m.Item(s.tr("scholarACPAppend")).Chosen() {
					q.popup = false
					s.captureScholarPDFQuote(p, t, q.region, action.picture, false, false)
				}
				if m.Item(s.tr("scholarACPNew")).Chosen() {
					q.popup = false
					s.captureScholarPDFQuote(p, t, q.region, action.picture, false, true)
				}
			})
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
	width := min(float32(342), max(1, w-24))
	x := max(12, min(q.anchor.X, w-width-12))
	y := max(44, min(q.anchor.Y+6, h-62))
	panel := s.scholarACPSelectionBar(c.Key(t).Key("pdf-selection"), x, y, width, func() {
		s.scholarPDFQuoteActions(c, p, t)
	})
	if panel.OverlayShortcut(0, ui.KeyEscape) || panel.PressedOutside() {
		q.popup = false
	}
}

func (s *Shell) scholarPDFQuotePage(c *ui.Context, t *scholarReferenceTab, width float32) {
	q := &t.quote
	if !q.selecting {
		ui.Image(c.Key("page"), t.bitmap).WidthPercent(width).AspectRatio(t.pageAspect).Label(t.title)
		return
	}
	ui.Column(c.Key("region")).WidthPercent(width).AspectRatio(t.pageAspect).Children(func() {
		el := ui.Image(c.Key("selectable-page"), t.bitmap).FillWidth().AspectRatio(t.pageAspect).
			Label(t.title).Cursor(ui.CursorCrosshair)
		bounds := el.Bounds()
		el.HandleInput(func(ev ui.InputEvent) bool { return q.regionInput(bounds, ev) })
		if r := q.region; r.W > 0 && r.H > 0 {
			ui.Box(c.Key("highlight")).Absolute().Left(r.X*bounds.W).Top(r.Y*bounds.H).Size(r.W*bounds.W, r.H*bounds.H).
				Background(ui.RGBA(31, 128, 255, 38)).Border(1, s.ch.accent).PassThrough()
		}
	})
}

func (q *scholarPDFQuote) regionInput(bounds ui.Rect, ev ui.InputEvent) bool {
	if !q.selecting || ev.Button != 0 || bounds.W <= 0 || bounds.H <= 0 {
		return false
	}
	x, y := max(0, min(1, ev.X/bounds.W)), max(0, min(1, ev.Y/bounds.H))
	switch ev.Kind {
	case ui.InputPointerDown:
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
				q.anchor = ui.Rect{X: bounds.X + q.region.X*bounds.W, Y: bounds.Y + (q.region.Y+q.region.H)*bounds.H}
				q.popup = true
			}
		}
		return true
	}
	return false
}

func (s *Shell) captureScholarPDFQuote(p *scholarState, t *scholarReferenceTab, region ui.Rect, picture, copyText, newChat bool) {
	path, page, generation := t.path, t.page, t.generation
	ref := scholarACPQuote{scope: scholarReferenceScope(t), label: t.title, locator: fmt.Sprintf(s.tr("scholarACPPage"), page)}
	empty := s.tr("scholarACPNoSelection")
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		var err error
		if picture {
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
		return func() {
			if p.references.focused != t || t.generation != generation {
				return
			}
			if err != nil {
				t.quote.err = err.Error()
				return
			}
			if copyText {
				s.svc.WriteClipboard(ref.text)
			} else {
				t.quote = scholarPDFQuote{}
				s.quoteScholar(p, ref, newChat)
			}
		}, ctx.Err()
	})
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
					s.quoteScholar(p, ref, newChat)
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
				s.quoteScholar(p, ref, newChat)
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

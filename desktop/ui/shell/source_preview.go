package shell

import (
	"context"
	"image"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"golang.org/x/image/draw"

	"nomo/desktop/typeset"
	"nomo/desktop/ui/mdedit"
)

const sourcePreviewDelay = 350 * time.Millisecond

type sourcePreviewInput struct {
	project              mdedit.ManuscriptProject
	version, mainVersion uint64
	mainBuffer           *editor.Buffer
}

type sourcePreviewView struct {
	tab               *Tab
	buffer            *editor.Buffer
	input             sourcePreviewInput
	line, first, last int
	due               time.Time
	cancel            context.CancelFunc
	bitmap            *ui.Bitmap
	status            string
	ready, dismissed  bool
}

func (s *Shell) closeSourcePreview() {
	if v := s.sourcePreview; v != nil {
		if v.cancel != nil {
			v.cancel()
		}
		s.sourcePreview = nil
	}
}

func (s *Shell) sourcePreviewInput(t *Tab, workspace *scholarState, version uint64) sourcePreviewInput {
	input := sourcePreviewInput{version: version, project: mdedit.ManuscriptProject{Path: t.Path}}
	if workspace == nil {
		return input
	}
	p := &input.project
	p.Root, p.Main, p.DiskRevision = workspace.project.Root, workspace.project.Main, workspace.previewDisk
	if typeset.IsLaTeXPath(p.Path) && typeset.IsLaTeXPath(p.Main) && !samePath(p.Path, p.Main) {
		if main := s.scholarTab(p.Main); main != nil && main.Dirty() {
			input.mainBuffer = main.Source().Buffer
			input.mainVersion = input.mainBuffer.Version()
		}
	}
	return input
}

func (s *Shell) sourcePreviewOverlay(c *ui.Context, t *Tab, workspace *scholarState, el ui.Element) {
	if s.post == nil || s.hiddenToTray || t != s.activeTab() || t.sourcePreviewOff {
		return
	}
	st := t.Source()
	if st.ReadOnly || !st.Focused() {
		return
	}
	selection := st.Selection()
	snap := st.Buffer.Snapshot()
	if snap.Len() > 4<<20 || snap.LineCount() > 32000 {
		return
	}
	r := el.Bounds()
	line := snap.LineOf(selection.Head)
	scroll := st.ScrollY()
	top, bottom := float32(st.LineTop(line)-scroll), float32(st.LineTop(line+1)-scroll)
	viewport := min(r.H, float32(st.ViewportHeight()))
	if r.W < 200 || viewport < 72 || bottom <= max(0, top) || top >= viewport {
		return
	}
	top, bottom = r.Y+max(0, top), r.Y+min(viewport, bottom)
	input := s.sourcePreviewInput(t, workspace, snap.Version())
	v := s.sourcePreview
	if v == nil || v.tab != t || v.buffer != st.Buffer || v.input.project.Path != t.Path {
		s.closeSourcePreview()
		v = &sourcePreviewView{tab: t, buffer: st.Buffer, line: -1}
		s.sourcePreview = v
	}
	s.sourcePreviewVisible = true
	sameBlock := v.ready && v.first <= line && line <= v.last
	if v.dismissed {
		if sameBlock || line == v.line {
			return
		}
		v.dismissed = false
	}
	if input != v.input || line != v.line && !sameBlock {
		if v.cancel != nil {
			v.cancel()
			v.cancel = nil
		}
		if !sameBlock || input.project != v.input.project || input.mainBuffer != v.input.mainBuffer {
			v.bitmap = nil
			v.ready = false
		}
		v.input, v.line, v.due = input, line, c.Now().Add(sourcePreviewDelay)
		v.status = "compiling"
	}
	if !v.due.IsZero() {
		if wait := v.due.Sub(c.Now()); wait > 0 {
			c.After(wait)
		} else if !s.sourcePreviewBusy {
			v.due = time.Time{}
			s.startSourcePreview(v, snap)
		}
	}
	if v.bitmap != nil || v.due.IsZero() && v.status != "" {
		s.drawSourcePreview(c.Key("source-preview"), v, r, top, bottom, viewport)
	}
}

func (s *Shell) startSourcePreview(v *sourcePreviewView, snap editor.Snapshot) {
	input, line, post := v.input, v.line, s.post
	var main editor.Snapshot
	if input.mainBuffer != nil {
		main = input.mainBuffer.Snapshot()
	}
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel = cancel
	s.sourcePreviewBusy = true
	go func() {
		defer cancel()
		project := input.project
		if input.mainBuffer != nil {
			project.MainPreamble = mdedit.ManuscriptLaTeXPreamble(main.Slice(0, min(main.Len(), 64<<10)))
		}
		block, first, last, incomplete := mdedit.ManuscriptSourcePreview(ctx, snap, project, line)
		var img image.Image
		status := ""
		if incomplete {
			status = "sourcePreviewFailed"
		} else if block.Source != "" && ctx.Err() == nil {
			// A floating preview should not provision a compiler while the user types.
			if typeset.IsLaTeXPath(block.Path) && !typeset.LaTeXReady() {
				status = "sourcePreviewLaTeXUnavailable"
			} else if result, err := typeset.RenderBlockPreview(ctx, block); err != nil {
				status = "sourcePreviewFailed"
			} else {
				img = fitSourcePreview(result)
			}
		}
		// Revalidate on the UI thread: the buffer or target can change while the callback is queued.
		post(func() {
			s.sourcePreviewBusy = false
			if s.sourcePreview != v || v.dismissed || v.input != input || v.line != line ||
				!v.due.IsZero() || v.buffer.Version() != input.version ||
				input.mainBuffer != nil && input.mainBuffer.Version() != input.mainVersion {
				return
			}
			v.cancel = nil
			v.status, v.ready = status, true
			if !incomplete || v.bitmap == nil {
				v.first, v.last = first, last
			}
			if img != nil {
				v.bitmap = ui.NewBitmap(img)
			} else if status != "sourcePreviewFailed" {
				v.bitmap = nil
			}
		})
	}()
}

// Keep at most one preview bitmap (under 3.1 MiB of base pixels) per window.
func fitSourcePreview(img image.Image) image.Image {
	b := img.Bounds()
	if b.Dx() <= 1000 && b.Dy() <= 800 {
		return img
	}
	scale := min(1000/float64(b.Dx()), 800/float64(b.Dy()))
	dst := image.NewRGBA(image.Rect(0, 0, max(1, int(float64(b.Dx())*scale)), max(1, int(float64(b.Dy())*scale))))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

func (s *Shell) drawSourcePreview(c *ui.Context, v *sourcePreviewView, r ui.Rect, top, bottom, viewport float32) {
	const gap, header, padding = 6, 32, 8
	width := min(float32(500), r.W-24)
	above, below := top-r.Y-gap, r.Y+viewport-bottom-gap
	bodyH, bodyW := float32(0), float32(0)
	if v.bitmap != nil {
		w, h := v.bitmap.Size()
		available := min(210, max(above, below)-header-padding)
		if available >= 18 {
			scale := min(.75, (width-2*padding)/float32(w), available/float32(h))
			bodyW, bodyH = float32(w)*scale, float32(h)*scale
		}
	}
	height := header + bodyH
	if bodyH > 0 {
		height += padding
	}
	y := bottom + gap
	if below < height {
		if above < height {
			return
		}
		y = top - gap - height
	}
	title := s.tr("sourceLocalPreview")
	th := c.Theme()
	ui.Overlay(c, func() {
		panel := ui.Column(c.Key("local-preview-panel")).Absolute().Left(r.X+12).Top(y).Size(width, height).
			Padding(0, padding).Background(th.Background).Border(1, s.ch.separator).
			Radius(th.Radius+4).Shadow(0, 4, 16, 0, ui.RGBA(0, 0, 0, .18)).PassThrough().Role(ui.RoleDialog).Label(title)
		closeClicked := false
		panel.Children(func() {
			ui.Row(c).Height(header - 2).Shrink(0).FillWidth().Gap(8).AlignItems(ui.Center).PassThrough().Children(func() {
				ui.Text(c, title).FontSize(12).Bold().Shrink(0).PassThrough()
				if v.status != "" {
					ui.Text(c, s.tr(v.status)).FontSize(11).TextColor(s.ch.muted).MaxLines(1).Ellipsis("…").PassThrough()
				}
				ui.Spacer(c).PassThrough()
				closeClicked = s.toolButton(c.Key("close"), "x", s.tr("close"), false).Label(s.tr("close")).KeepFocus().Clicked()
			})
			if bodyH > 0 {
				ui.Image(c, v.bitmap).Size(bodyW, bodyH).Shrink(0).Label(title).PassThrough()
			}
		})
		if closeClicked || panel.OverlayShortcut(0, ui.KeyEscape) {
			if v.cancel != nil {
				v.cancel()
				v.cancel = nil
			}
			v.due, v.bitmap, v.dismissed = time.Time{}, nil, true
			c.Invalidate()
		}
	})
}

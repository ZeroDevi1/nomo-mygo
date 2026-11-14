package mdedit

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown/parser"
)

const (
	embedEditLimit = 16 << 10
	embedEditDelay = 200 * time.Millisecond
)

type embedEdit struct {
	version        uint64
	start, end     int
	open, close    string
	prefix         string
	kind           string
	block          bool
	focus          bool
	text, original string
	preview        string
	pending        string
	requested      string
	due            time.Time
	invalid        bool
	renderFailed   bool
}

// BeginEmbedEdit 为指定公式或图表打开有大小上限的草稿。
func (e *Editor) BeginEmbedEdit(pos int) bool {
	if !e.canRender() || e.Doc.manuscript != nil {
		return false
	}
	if _, ok := e.Doc.Marked(); ok {
		return false
	}
	s, ok := e.segAt(pos)
	if !ok {
		return false
	}
	top := e.Doc.blocks[s.i]
	r := &e.entries[s.i].rows[s.k]
	idx := top.Index + r.rel
	d := e.Doc
	draft := &embedEdit{version: d.Version(), focus: true}
	switch {
	case r.htmlKind == previewMath || r.htmlKind == previewMermaid:
		draft.start, draft.end = s.base, s.end
		raw := d.slice(s.base, s.end)
		if len(raw) > embedEditLimit {
			return false
		}
		draft.kind = RenderMathDisplay
		trimmed := strings.TrimRight(raw, " \t")
		if r.htmlKind == previewMath && !strings.Contains(raw, "\n") {
			draft.open, draft.close = "$$", "$$"
			if strings.HasPrefix(raw, `\[`) {
				draft.open, draft.close = `\[`, `\]`
			}
			if len(trimmed) < 4 || !strings.HasSuffix(trimmed, draft.close) {
				return false
			}
			draft.text, draft.close = raw[2:len(trimmed)-2], raw[len(trimmed)-2:]
			draft.block = true
		} else if !draft.readBlock(raw, strings.TrimSuffix(d.md.Lines(idx), "\n"), r.htmlKind == previewMermaid) {
			return false
		}
	default:
		if s.kind != rowText {
			return false
		}
		for _, in := range d.md.Inlines(idx) {
			if in.Kind != parser.Math || pos < in.Start || pos > in.End {
				continue
			}
			raw := d.slice(in.Start, in.End)
			width := 1
			if strings.HasPrefix(raw, "$$") || strings.HasPrefix(raw, `\`) {
				width = 2
			}
			if len(raw) < 2*width || len(raw) > embedEditLimit || strings.Contains(raw, "\n") {
				return false
			}
			draft.start, draft.end = in.Start, in.End
			draft.open, draft.close = raw[:width], raw[len(raw)-width:]
			draft.text = raw[width : len(raw)-width]
			draft.kind = RenderMathInline
			if in.Display {
				draft.kind = RenderMathDisplay
			}
			break
		}
		if draft.kind == "" {
			return false
		}
	}
	draft.open, draft.close, draft.prefix = strings.Clone(draft.open), strings.Clone(draft.close), strings.Clone(draft.prefix)
	draft.text = strings.Clone(draft.text)
	draft.original = draft.text
	draft.pending = strings.TrimSpace(draft.text)
	e.embed = draft
	return true
}

func (d *embedEdit) readBlock(raw, body string, mermaid bool) bool {
	first, _, multiline := strings.Cut(raw, "\n")
	if !multiline {
		return false
	}
	last := raw[strings.LastIndexByte(raw, '\n')+1:]
	close := strings.TrimLeft(last, " \t>")
	if mermaid {
		d.kind = RenderMermaid
		width := len(first) - len(strings.TrimLeft(first, "`~"))
		fence := strings.TrimSpace(close)
		if width < 3 || len(fence) < width || strings.Trim(fence, first[:1]) != "" {
			return false
		}
	} else {
		closer := "$$"
		if strings.HasPrefix(first, `\[`) {
			closer = `\]`
		}
		if strings.TrimSpace(close) != closer || len(first) < 2 || strings.TrimSpace(first[2:]) != "" {
			return false
		}
	}
	d.block = true
	d.open, d.close = first+"\n", last
	d.prefix = last[:len(last)-len(close)]
	d.text = body
	return true
}

// CloseEmbedEdit 丢弃当前原位草稿。
func (e *Editor) CloseEmbedEdit() { e.embed = nil }

func (e *Editor) applyEmbedEdit() bool {
	d := e.embed
	if d == nil || d.version != e.Doc.Version() {
		return false
	}
	body := strings.ReplaceAll(strings.ReplaceAll(d.text, "\r\n", "\n"), "\r", "\n")
	if len(body) > embedEditLimit || !d.block && strings.TrimSpace(body) == "" {
		d.invalid = true
		return false
	}
	if body == d.original {
		e.embed = nil
		return true
	}
	multiline := strings.HasSuffix(d.open, "\n")
	if !multiline && strings.Contains(body, "\n") {
		d.invalid = true
		return false
	}
	check := parser.New(parser.Default | parser.ExtDollarMath)
	raw := d.open + body + d.close
	if multiline {
		content := body
		if content != "" {
			content += "\n"
		}
		raw = d.open + content + strings.TrimLeft(d.close, " \t>")
	}
	check.Set(raw)
	if check.Len() < 2 {
		d.invalid = true
		return false
	}
	first := check.Block(1)
	switch {
	case d.block && d.kind == RenderMermaid:
		d.invalid = first.Kind != parser.CodeBlock || first.Open || first.Next != check.Len()
	case d.block:
		d.invalid = first.Kind != parser.MathBlock || first.Open || first.Next != check.Len()
	default:
		// Display-style math may still be an inline object inside a paragraph.
		const leader = "x "
		check.Set(leader + raw)
		in := check.Inlines(1)
		d.invalid = len(in) != 2 || in[1].Kind != parser.Math || in[1].Start != len(leader) || in[1].End != len(leader)+len(raw)
	}
	if d.invalid {
		return false
	}
	if multiline {
		content := ""
		if body != "" {
			content = d.prefix + strings.ReplaceAll(body, "\n", "\n"+d.prefix) + "\n"
		}
		raw = d.open + content + d.close
	} else {
		raw = e.tableInput(d.start, d.end, raw)
	}
	e.embed = nil
	if raw != e.Doc.slice(d.start, d.end) {
		e.Replace(d.start, d.end, raw)
	}
	return true
}

func (e *Editor) embedEditor(c *ui.Context, box ui.Element) {
	d := e.embed
	if d == nil {
		return
	}
	if d.version != e.Doc.Version() {
		e.embed = nil
		return
	}
	bounds := box.Bounds()
	if bounds.W < 100 || bounds.H < 140 {
		return
	}
	s, ok := e.segAt(d.start)
	if !ok {
		e.embed = nil
		return
	}
	v, visible := e.visibleSeg(s.i, s.k)
	if !visible {
		e.embed = nil
		return
	}
	w := min(550, bounds.W-16)
	h := min(295, bounds.H-16)
	x := bounds.X + clampF(e.left()+s.x, 8, bounds.W-w-8)
	y := bounds.Y + clampF(v.y, 8, bounds.H-h-8)
	ui.Overlay(c, func() {
		panel := ui.Column(c).Absolute().Left(x).Top(y).Width(w).MaxHeight(h).Padding(10).Gap(8).
			Radius(8).Border(1, e.pal.accentFill).Background(e.pal.surface).Role(ui.RoleDialog).Label(e.embedLabel(d))
		panel.Children(func() {
			label := e.embedLabel(d)
			var input ui.Element
			if strings.HasSuffix(d.open, "\n") {
				input = ui.TextArea(c.Key("source"), &d.text).Height(min(92, h/3))
			} else {
				input = ui.TextInput(c.Key("source"), &d.text)
			}
			input.FillWidth().Font(e.fontMono).Label(label).MaxLength(embedEditLimit)
			if d.focus {
				input.Focus()
				d.focus = false
			}
			if input.Changed() {
				d.invalid = false
				d.renderFailed = false
				d.due = c.Now().Add(embedEditDelay)
			}
			if !d.due.IsZero() && !c.Now().Before(d.due) {
				d.pending = strings.TrimSpace(d.text)
				d.due = time.Time{}
			} else if !d.due.IsZero() {
				c.After(d.due.Sub(c.Now()))
			}
			if !d.renderFailed && d.requested == "" && d.pending != "" && d.pending != d.preview {
				d.requested = d.pending
			}
			if d.requested != "" {
				obj := imgObj{src: d.requested, render: d.kind}
				if en := e.imageEntry(obj); en != nil && en.state == imageReady {
					d.preview, d.requested = d.requested, ""
					if d.preview != d.pending {
						c.AnimationFrame()
					}
				} else if en != nil && en.state == imageFailed {
					d.renderFailed = d.requested == d.pending
					d.requested = ""
					if !d.renderFailed {
						c.AnimationFrame()
					}
				}
			}
			if d.preview != "" {
				src := d.preview
				ui.Box(c.Key("preview")).FillWidth().Height(min(115, h/3)).Clip().Draw(func(p *ui.Painter, r ui.Rect) {
					obj := imgObj{src: src, render: d.kind}
					size := e.renderSize(obj, e.imageEntry(obj), r.W)
					factor := min(1, r.H/max(1, size[1]))
					pw, ph := size[0]*factor, size[1]*factor
					e.drawImageAt(p, obj, ui.Rect{X: r.X + (r.W-pw)/2, Y: r.Y + (r.H-ph)/2, W: pw, H: ph})
				})
			}
			if d.invalid || d.renderFailed {
				ui.Text(c.Key("invalid"), e.labels.EmbedInvalid).TextColor(c.Theme().Danger).Wrap()
			}
			ui.Row(c.Key("actions")).FillWidth().Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.Button(c.Key("cancel"), e.labels.EmbedCancel).Clicked() {
					e.embed = nil
					box.Focus()
				}
				if ui.PrimaryButton(c.Key("save"), e.labels.EmbedSave).Clicked() || input.Submitted() && !strings.HasSuffix(d.open, "\n") {
					if e.applyEmbedEdit() {
						box.Focus()
					}
				}
			})
		})
		if panel.Shortcut(0, ui.KeyEscape) {
			e.embed = nil
			box.Focus()
		}
		if panel.PressedOutside() {
			e.embed = nil
		}
	})
}

func (e *Editor) embedLabel(d *embedEdit) string {
	if d.kind == RenderMermaid {
		return e.labels.EmbedDiagram
	}
	return e.labels.EmbedFormula
}

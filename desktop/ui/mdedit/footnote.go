package mdedit

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown/parser"
)

type footnoteHover struct {
	ref  int
	text string
}

type footnoteReturn struct {
	ref, def int
	valid    bool
}

func (e *Editor) remapFootnoteReturn(ch Change) {
	note := &e.noteReturn
	if !note.valid {
		return
	}
	for _, pos := range []*int{&note.ref, &note.def} {
		if *pos < ch.At {
			continue
		}
		if *pos < ch.At+ch.OldLen {
			*note = footnoteReturn{}
			return
		}
		*pos += ch.NewLen - ch.OldLen
	}
}

func (e *Editor) footnoteRefHit(pt ui.Point) (int, string) {
	x := pt.X - e.left()
	for _, v := range e.vsegs {
		r := &e.entries[v.i].rows[v.k]
		if r.kind != rowText || r.proj == nil || r.layout == nil || pt.Y < v.y || pt.Y >= v.y+r.bandH {
			continue
		}
		left, width := e.rowBox(r)
		if x < left || x >= left+width {
			continue
		}
		for k, run := range r.proj.runs {
			if run.st&stFootRef == 0 || run.srcB-run.srcA < 4 {
				continue
			}
			p := r.proj
			label := p.text[run.dispByte:p.runEnd(k)]
			rects := r.layout.SelectionRects(ui.TextInputRange{Start: run.disp, End: run.disp + u16Len(label)})
			for _, rect := range rects {
				if rect.Contains(x-left, pt.Y-v.y-r.padTop) {
					return e.Doc.blocks[v.i].Start + r.a + run.srcA, label
				}
			}
		}
	}
	return 0, ""
}

func (d *Document) footnoteDefinition(label string) (parser.Block, int, bool) {
	label = strings.Join(strings.Fields(label), " ")
	for _, b := range d.blocks {
		if b.Kind != parser.FootnoteDefinition {
			continue
		}
		pb := d.md.Block(b.Index)
		if strings.EqualFold(strings.Join(strings.Fields(pb.Label()), " "), label) {
			if first, ok := d.firstChildStart(b.Index); ok {
				return pb, first, true
			}
			return pb, b.Start, true
		}
	}
	return parser.Block{}, 0, false
}

func (e *Editor) followFootnote(pt ui.Point) bool {
	ref, label := e.footnoteRefHit(pt)
	if label != "" {
		if def, pos, ok := e.Doc.footnoteDefinition(label); ok {
			e.noteReturn = footnoteReturn{ref: ref, def: def.Start, valid: true}
			e.noteHover = nil
			e.JumpTo(pos)
			return true
		}
	}
	if e.footnoteReturnHit(pt) {
		e.JumpTo(e.noteReturn.ref)
		e.noteReturn = footnoteReturn{}
		return true
	}
	return false
}

func (e *Editor) footnoteReturnHit(pt ui.Point) bool {
	if !e.noteReturn.valid {
		return false
	}
	for _, v := range e.vsegs {
		b := e.Doc.blocks[v.i]
		if b.Kind != parser.FootnoteDefinition || b.Start != e.noteReturn.def || v.k != 0 {
			continue
		}
		r := &e.entries[v.i].rows[v.k]
		s := e.segOf(v.i, v.k)
		box := e.footnoteMarkerBox(r, s)
		box.Y += v.y + s.padTop
		return box.Contains(pt.X, pt.Y)
	}
	return false
}

func (e *Editor) hoverFootnote(pt ui.Point) bool {
	ref, label := e.footnoteRefHit(pt)
	if prev := e.noteHover; prev != nil && label != "" && prev.ref == ref {
		return false
	}
	if label == "" && e.noteHover == nil {
		return false
	}
	e.noteHover = nil
	if label != "" {
		h := &footnoteHover{ref: ref}
		if def, pos, ok := e.Doc.footnoteDefinition(label); ok && pos < def.End {
			z := min(def.End, pos+8192)
			if z < def.End {
				z = runeStartBefore(e.Doc.Source(), z)
			}
			text := strings.TrimSpace(plainText(e.Doc.slice(pos, z)))
			count := 0
			for i := range text {
				if count == 360 {
					text = text[:i] + "…"
					break
				}
				count++
			}
			h.text = strings.Clone(text)
		}
		if h.text == "" {
			h.text = e.labels.FootnoteEmpty
		}
		e.noteHover = h
	}
	return true
}

func (e *Editor) drawFootnotePreview(p *ui.Painter, bounds ui.Rect) {
	h := e.noteHover
	if h == nil || h.text == "" || bounds.W < 70 {
		return
	}
	s, ok := e.segAt(h.ref)
	if !ok {
		return
	}
	v, visible := e.visibleSeg(s.i, s.k)
	if !visible {
		return
	}
	rc := e.caretIn(s, h.ref)
	w := min(340, bounds.W-16)
	span := ui.Span{Text: h.text, Font: e.fontBody, Size: 13, Color: e.pal.text}
	_, hgt := p.MeasureText(w-20, span)
	hgt = min(hgt+16, 200, max(16, bounds.H-16))
	x := bounds.X + clampF(e.left()+s.x+rc.X, 8, bounds.W-w-8)
	y := v.y + s.padTop + rc.Y + rc.H + 6
	if y+hgt > bounds.H-8 {
		y = max(8, v.y+s.padTop+rc.Y-hgt-6)
	}
	box := ui.Rect{X: x, Y: bounds.Y + y, W: w, H: hgt}
	p.Fill(box, e.pal.surface, 6)
	p.Stroke(box, e.pal.border, 6, 1)
	p.Clip(box, 6, func() { p.RichText(box.X+10, box.Y+8, w-20, span) })
}

func (e *Editor) footnoteMarkerBox(r *row, s seg) ui.Rect {
	first := s.layout.Caret(0)
	x := max(0, e.left()+r.indent-listIndent-1)
	return ui.Rect{X: x, Y: first.Y, W: listIndent, H: max(e.lineH, first.H)}
}

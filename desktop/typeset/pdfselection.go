package typeset

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"nomo/desktop/internal/pdfium"
)

// PDFTextBox uses fractions of the displayed page, from its upper left.
type PDFTextBox struct {
	X, Y, Right, Bottom float32
}

type PDFTextWord struct {
	PDFTextBox
	Text string
	Line int
}

// PageText reads one compiled page in the order supplied by the text layer.
func (s *Session) PageText(ctx context.Context, page int) []PDFTextWord {
	p, err := pdfium.ReadSelectionPage(ctx, filepath.Join(s.dir, "pages", "current.pdf"), page)
	if errors.Is(err, pdfium.ErrUnavailable) {
		fallback := s.readSourcePage(ctx, page)
		if fallback == nil {
			return nil
		}
		p = fallback.TextPage
	} else if err != nil {
		return nil
	}
	var words []PDFTextWord
	for line, row := range p.Lines {
		if ctx.Err() != nil {
			return nil
		}
		for _, w := range row.Words {
			box := PDFTextBox{float32(w.X / p.Width), float32(w.Y / p.Height), float32(w.R / p.Width), float32(w.B / p.Height)}
			if box.Right > box.X && box.Bottom > box.Y && w.Text != "" {
				words = append(words, PDFTextWord{box, w.Text, line})
			}
		}
	}
	return words
}

// PDFWordAt finds the nearest word in reading order; its distance is
// measured in screen pixels so landscape and portrait pages behave alike.
func PDFWordAt(words []PDFTextWord, x, y, width, height float32) (int, float32) {
	best, distance := -1, float32(math.Inf(1))
	for i, w := range words {
		dx := max(0, w.X-x, x-w.Right) * width
		dy := max(0, w.Y-y, y-w.Bottom) * height
		if d := dx + 4*dy; d < distance {
			best, distance = i, d
		}
	}
	return best, distance
}

// PDFSelection joins a contiguous word range and keeps its original line boxes.
// Oversized selections have visible geometry but no copyable text.
func PDFSelection(words []PDFTextWord, a, b, limit int) (string, []PDFTextBox) {
	start, end := min(a, b), max(a, b)
	if start < 0 || end >= len(words) {
		return "", nil
	}
	var text strings.Builder
	var boxes []PDFTextBox
	tooLong := false
	for i := start; i <= end; i++ {
		w := words[i]
		if !tooLong && text.Len()+len(w.Text)+1 > limit {
			tooLong = true
		}
		if i == start || words[i-1].Line != w.Line {
			if i > start && !tooLong {
				text.WriteByte('\n')
			}
			boxes = append(boxes, w.PDFTextBox)
		} else {
			prev, _ := utf8.DecodeLastRuneInString(words[i-1].Text)
			next, _ := utf8.DecodeRuneInString(w.Text)
			if !tooLong && !pdfium.IsCJK(prev) && !pdfium.IsCJK(next) {
				text.WriteByte(' ')
			}
			box := &boxes[len(boxes)-1]
			box.X, box.Y = min(box.X, w.X), min(box.Y, w.Y)
			box.Right, box.Bottom = max(box.Right, w.Right), max(box.Bottom, w.Bottom)
		}
		if !tooLong {
			text.WriteString(w.Text)
		}
	}
	if tooLong {
		return "", boxes
	}
	return text.String(), boxes
}

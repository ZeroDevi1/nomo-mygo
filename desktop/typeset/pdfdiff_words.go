package typeset

import (
	"context"
	"image"
	"image/draw"
	"math"
	"strings"

	"golang.org/x/text/unicode/norm"
)

type trialPDFWord struct {
	text   string
	bounds image.Rectangle
}

const trialPDFWordLimit = 384

func trialPDFBandWords(p *pdfSourcePage, page, band image.Rectangle) ([]trialPDFWord, bool) {
	if p == nil || p.Width <= 0 || p.Height <= 0 {
		return nil, false
	}
	sx, sy := float64(page.Dx())/p.Width, float64(page.Dy())/p.Height
	var words []trialPDFWord
	for _, line := range p.Lines {
		for _, word := range line.Words {
			x, y := word.X*sx, word.Y*sy
			r, b := word.R*sx, word.B*sy
			center := image.Pt(int((x+r)/2), int((y+b)/2))
			if !center.In(band) {
				continue
			}
			box := image.Rect(int(math.Floor(x)), int(math.Floor(y)), int(math.Ceil(r)), int(math.Ceil(b)))
			box = box.Inset(-2).Intersect(page)
			text := strings.ReplaceAll(norm.NFKC.String(word.Text), "\u00ad", "")
			if box.Empty() || text == "" {
				continue
			}
			if len(words) == trialPDFWordLimit {
				return nil, false
			}
			words = append(words, trialPDFWord{text, box})
		}
	}
	return words, true
}

// Compare printed words in a raster change band. The small LCS table lets
// unchanged words keep their appearance when a nearby edit reflows the line.
func trialPDFWordMarks(ctx context.Context, oldText, newText *pdfSourcePage, before, after *image.RGBA, page, band image.Rectangle) (removed, added []image.Rectangle, ok bool) {
	old, oldOK := trialPDFBandWords(oldText, page, band)
	next, newOK := trialPDFBandWords(newText, page, band)
	if !oldOK || !newOK || len(old)+len(next) == 0 {
		return nil, nil, false
	}
	width := len(next) + 1
	lcs := make([]uint16, (len(old)+1)*width)
	for i := len(old) - 1; i >= 0; i-- {
		if ctx.Err() != nil {
			return nil, nil, false
		}
		for j := len(next) - 1; j >= 0; j-- {
			at := i*width + j
			if old[i].text == next[j].text {
				lcs[at] = lcs[at+width+1] + 1
			} else {
				lcs[at] = max(lcs[at+width], lcs[at+1])
			}
		}
	}
	// A non-nil empty removal list identifies a pure insertion to the cropper.
	removed = make([]image.Rectangle, 0)
	for i, j := 0, 0; i < len(old) || j < len(next); {
		switch {
		case i < len(old) && j < len(next) && old[i].text == next[j].text:
			i++
			j++
		case i < len(old) && (j == len(next) || lcs[(i+1)*width+j] >= lcs[i*width+j+1]):
			removed = append(removed, old[i].bounds)
			i++
		default:
			added = append(added, next[j].bounds)
			j++
		}
	}
	if len(removed)+len(added) == 0 || !trialPDFTextCoversChanges(ctx, before, after, band, old, next) {
		return nil, nil, false
	}
	return removed, added, true
}

// Keep the raster fallback when a graphic or other non-text ink changes in
// the same band. Word boxes are only trusted where they cover the visual diff.
func trialPDFTextCoversChanges(ctx context.Context, before, after *image.RGBA, band image.Rectangle, old, next []trialPDFWord) bool {
	mask := image.NewAlpha(band)
	for _, side := range [][]trialPDFWord{old, next} {
		for _, word := range side {
			draw.Draw(mask, word.bounds, image.Opaque, image.Point{}, draw.Src)
		}
	}
	oldMin, newMin := before.Bounds().Min, after.Bounds().Min
	for y := band.Min.Y; y < band.Max.Y; y++ {
		if ctx.Err() != nil {
			return false
		}
		covered := mask.Pix[mask.PixOffset(band.Min.X, y):]
		for x := band.Min.X; x < band.Max.X; x++ {
			if covered[x-band.Min.X] != 0 {
				continue
			}
			a := before.RGBAAt(x+oldMin.X, y+oldMin.Y)
			b := after.RGBAAt(x+newMin.X, y+newMin.Y)
			if trialPixelChanged(a, b) {
				return false
			}
		}
	}
	return true
}

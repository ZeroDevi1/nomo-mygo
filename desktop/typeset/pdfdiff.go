package typeset

import (
	"context"
	"errors"
	"image"
	"image/draw"
	"os"
	"path/filepath"
	"sync"
)

// TrialPDFPair owns only the compiled outputs; the compiler sessions can
// remove both project trees as soon as the outputs have been moved here.
type TrialPDFPair struct {
	mu     sync.Mutex
	dir    string
	pages  [2]int
	active int
	closed bool
}

var trialPDFNames = [2]string{"before.pdf", "after.pdf"}

// KeepTrialPDFs moves the outputs out of their trial sessions. The caller
// still closes both sessions and closes the pair when comparison ends.
func KeepTrialPDFs(before, after *Session) (*TrialPDFPair, error) {
	if before.trialRoot == "" || after.trialRoot == "" || before.pages < 1 || after.pages < 1 {
		return nil, errors.New("no compiled project trial to compare")
	}
	dir, err := os.MkdirTemp("", "nomo-pdf-diff-*")
	if err != nil {
		return nil, err
	}
	for i, session := range [2]*Session{before, after} {
		if err := os.Rename(filepath.Join(session.dir, "pages", "current.pdf"), filepath.Join(dir, trialPDFNames[i])); err != nil {
			os.RemoveAll(dir)
			return nil, err
		}
	}
	return &TrialPDFPair{dir: dir, pages: [2]int{before.pages, after.pages}}, nil
}

func (p *TrialPDFPair) Close() {
	p.mu.Lock()
	p.closed = true
	dir := p.cleanupDir()
	p.mu.Unlock()
	if dir != "" {
		os.RemoveAll(dir)
	}
}

// On Windows a renderer can still be reading a PDF when its view is closed.
// Leave cleanup to the last render in that case.
func (p *TrialPDFPair) cleanupDir() string {
	if !p.closed || p.active != 0 {
		return ""
	}
	dir := p.dir
	p.dir = ""
	return dir
}

const trialDiffEdge = 1200

type TrialPageDiff struct {
	After                      image.Image
	Regions                    []image.Rectangle
	Excerpts                   []TrialPDFExcerpt
	Changed, DimensionsChanged bool
}

// TrialPDFExcerpt is a cropped baseline raster to insert before At, in
// proposed-page pixels. X preserves the old content's horizontal position.
type TrialPDFExcerpt struct {
	Raster image.Image
	At, X  int
}

// DiffPage marks changes at the same page number on the proposed PDF. The
// baseline raster is temporary; callers only display the proposed page.
func (p *TrialPDFPair) DiffPage(ctx context.Context, page int) (TrialPageDiff, error) {
	p.mu.Lock()
	if p.closed || page < 1 || page > p.pages[1] {
		p.mu.Unlock()
		return TrialPageDiff{}, errors.New("invalid trial PDF page")
	}
	dir := p.dir
	p.active++
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.active--
		dir := p.cleanupDir()
		p.mu.Unlock()
		if dir != "" {
			os.RemoveAll(dir)
		}
	}()
	var imgs [2]*image.RGBA
	for i, name := range trialPDFNames {
		if page > p.pages[i] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return TrialPageDiff{}, err
		}
		img, err := ReadPDFPage(ctx, filepath.Join(dir, name), page, trialDiffEdge)
		if err != nil {
			return TrialPageDiff{}, err
		}
		rgba, ok := img.(*image.RGBA)
		if !ok {
			rgba = image.NewRGBA(img.Bounds())
			draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
		}
		imgs[i] = rgba
	}
	return compareTrialRasters(ctx, imgs[0], imgs[1])
}

type trialDiffRow struct {
	changed, oldInk, newInk image.Rectangle
	oldChanged              bool
}

func compareTrialRasters(ctx context.Context, before, after *image.RGBA) (TrialPageDiff, error) {
	diff := TrialPageDiff{After: after}
	if before == nil {
		diff.Changed = true
		return diff, ctx.Err()
	}
	oldBounds, newBounds := before.Bounds(), after.Bounds()
	diff.DimensionsChanged = oldBounds.Size() != newBounds.Size()
	const tolerance = 12
	w, h := newBounds.Dx(), newBounds.Dy()
	page := image.Rect(0, 0, w, h)
	rows := make([]trialDiffRow, h)
	for y := range rows {
		if y%24 == 0 {
			if err := ctx.Err(); err != nil {
				return TrialPageDiff{}, err
			}
		}
		row := &rows[y]
		for x := 0; x < w; x++ {
			a := before.RGBAAt(oldBounds.Min.X+x, oldBounds.Min.Y+y)
			b := after.RGBAAt(newBounds.Min.X+x, newBounds.Min.Y+y)
			if a.A == 0 {
				a.R, a.G, a.B = 255, 255, 255
			}
			px := image.Rect(x, y, x+1, y+1)
			oldInk := min(a.R, a.G, a.B) < 224
			if oldInk {
				row.oldInk = row.oldInk.Union(px)
			}
			if min(b.R, b.G, b.B) < 224 {
				row.newInk = row.newInk.Union(px)
			}
			if max(channelDelta(a.R, b.R), channelDelta(a.G, b.G), channelDelta(a.B, b.B)) > tolerance {
				row.changed = row.changed.Union(px)
				row.oldChanged = row.oldChanged || oldInk
			}
		}
	}
	diff.Regions = trialPDFBands(page, rows)
	diff.Changed = diff.DimensionsChanged || len(diff.Regions) > 0
	diff.Excerpts = trialPDFExcerpts(before, page, rows, diff.Regions)
	return diff, nil
}

// Group nearby changes in the reading direction, then extend each band's
// background to the full lines of text within it.
func trialPDFBands(page image.Rectangle, rows []trialDiffRow) []image.Rectangle {
	var bands []image.Rectangle
	for _, row := range rows {
		if row.changed.Empty() {
			continue
		}
		i := len(bands) - 1
		if i >= 0 && row.changed.Min.Y-bands[i].Max.Y <= 32 {
			bands[i] = bands[i].Union(row.changed)
		} else {
			bands = append(bands, row.changed)
		}
	}
	for i, band := range bands {
		for y := band.Min.Y; y < band.Max.Y; y++ {
			band = band.Union(rows[y].oldInk).Union(rows[y].newInk)
		}
		bands[i] = band.Inset(-4).Intersect(page)
	}
	return bands
}

func trialPDFExcerpts(before *image.RGBA, page image.Rectangle, rows []trialDiffRow, bands []image.Rectangle) []TrialPDFExcerpt {
	// Keep representative crops when a layout shift affects much of a page.
	// The baseline raster itself is not kept by the reader.
	budget := min(360, page.Dy()/3)
	oldBounds := before.Bounds()
	clip := page.Intersect(image.Rect(0, 0, oldBounds.Dx(), oldBounds.Dy()))
	var excerpts []TrialPDFExcerpt
	for _, band := range bands {
		if budget < 18 || len(excerpts) == 4 {
			break
		}
		var oldText image.Rectangle
		for y := band.Min.Y; y < band.Max.Y; y++ {
			if rows[y].oldChanged {
				oldText = oldText.Union(rows[y].oldInk)
			}
		}
		if oldText.Dy() < 5 || oldText.Dx() < 6 {
			continue
		}
		// Expand to the complete old text lines, including unchanged words.
		crop := image.Rect(band.Min.X, oldText.Min.Y-4, band.Max.X, oldText.Max.Y+4).Intersect(clip)
		end := min(crop.Max.Y, crop.Min.Y+min(160, budget))
		if end < crop.Max.Y {
			for row := end - 1; row >= max(crop.Min.Y+18, end-25); row-- {
				if rows[row].oldInk.Empty() {
					end = row + 1
					break
				}
			}
		}
		crop.Max.Y = end
		if crop.Empty() {
			continue
		}
		// Insert the crop at a nearby blank scan line above the change, so a
		// printed line in the proposed PDF is never cut in two.
		at := 0
		for row := max(2, min(band.Min.Y, crop.Min.Y)-1); row >= max(2, band.Min.Y-76); row-- {
			if rows[row].newInk.Empty() && rows[row-1].newInk.Empty() && rows[row-2].newInk.Empty() {
				at = row - 1
				break
			}
		}
		if len(excerpts) > 0 && at <= excerpts[len(excerpts)-1].At+8 {
			continue
		}
		raster := image.NewRGBA(image.Rect(0, 0, crop.Dx(), crop.Dy()))
		draw.Draw(raster, raster.Bounds(), before, crop.Min.Add(oldBounds.Min), draw.Src)
		excerpts = append(excerpts, TrialPDFExcerpt{Raster: raster, At: at, X: crop.Min.X})
		budget -= crop.Dy()
	}
	return excerpts
}

func channelDelta(a, b uint8) uint8 { return max(a, b) - min(a, b) }

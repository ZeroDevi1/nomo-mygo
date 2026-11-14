package typeset

import (
	"context"
	"errors"
	"image"
	"image/color"
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
	Raster                     image.Image
	Regions                    []image.Rectangle
	Additions                  []image.Rectangle
	Excerpts                   []TrialPDFExcerpt
	Changed, DimensionsChanged bool
}

// TrialPDFExcerpt is a cropped baseline raster to insert before At, in
// proposed-page pixels. X preserves the old content's horizontal position.
type TrialPDFExcerpt struct {
	Raster  image.Image
	Regions []image.Rectangle
	At, X   int
}

// DiffPage compares the same page number in both PDFs, including pages that
// exist on only one side. For paired pages only crops of the baseline survive.
func (p *TrialPDFPair) DiffPage(ctx context.Context, page int) (TrialPageDiff, error) {
	p.mu.Lock()
	if p.closed || page < 1 || page > max(p.pages[0], p.pages[1]) {
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
	if imgs[0] == nil || imgs[1] == nil {
		raster := imgs[1]
		if raster == nil {
			raster = imgs[0]
		}
		return TrialPageDiff{Raster: raster, Changed: true}, ctx.Err()
	}
	rows, err := compareTrialRasters(ctx, imgs[0], imgs[1])
	if err != nil {
		return TrialPageDiff{}, err
	}
	pageBounds := image.Rectangle{Max: imgs[1].Bounds().Size()}
	bands := trialPDFBands(pageBounds, rows)
	resized := imgs[0].Bounds().Size() != pageBounds.Size()
	diff := TrialPageDiff{
		Raster: imgs[1], Changed: len(bands) > 0 || resized, DimensionsChanged: resized,
	}
	if len(bands) == 0 {
		return diff, nil
	}
	var oldText, newText *pdfSourcePage
	if !diff.DimensionsChanged {
		oldText, _ = readPDFSourcePage(ctx, "", filepath.Join(dir, trialPDFNames[0]), page)
		if oldText != nil {
			newText, _ = readPDFSourcePage(ctx, "", filepath.Join(dir, trialPDFNames[1]), page)
		}
	}
	marks := make([][]image.Rectangle, len(bands))
	for i, band := range bands {
		old, added, ok := trialPDFWordMarks(ctx, oldText, newText, imgs[0], imgs[1], pageBounds, band)
		if ok {
			marks[i] = old
			diff.Additions = append(diff.Additions, added...)
		}
	}
	diff.Excerpts = trialPDFExcerpts(imgs[0], pageBounds, rows, bands, marks)
	for i, band := range bands {
		// A coarse mark remains visible for old text outside the crop budget.
		if marks[i] == nil || len(marks[i]) > 0 {
			diff.Regions = append(diff.Regions, band)
		}
	}
	return diff, ctx.Err()
}

type trialDiffRow struct {
	changed, oldInk, newInk image.Rectangle
	oldChanged              bool
}

func compareTrialRasters(ctx context.Context, before, after *image.RGBA) ([]trialDiffRow, error) {
	oldBounds, newBounds := before.Bounds(), after.Bounds()
	w, h := newBounds.Dx(), newBounds.Dy()
	rows := make([]trialDiffRow, h)
	for y := range rows {
		if y%24 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
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
			if trialPixelChanged(a, b) {
				row.changed = row.changed.Union(px)
				row.oldChanged = row.oldChanged || oldInk
			}
		}
	}
	return rows, nil
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

func trialPDFExcerpts(before *image.RGBA, page image.Rectangle, rows []trialDiffRow, bands []image.Rectangle, deletions [][]image.Rectangle) []TrialPDFExcerpt {
	// Keep representative crops when a layout shift affects much of a page.
	// The baseline raster itself is not kept by the reader.
	budget := min(360, page.Dy()/3)
	oldBounds := before.Bounds()
	clip := page.Intersect(image.Rect(0, 0, oldBounds.Dx(), oldBounds.Dy()))
	var excerpts []TrialPDFExcerpt
	for i, band := range bands {
		if budget < 18 || len(excerpts) == 4 {
			break
		}
		marks := deletions[i]
		if marks != nil && len(marks) == 0 {
			continue
		}
		var oldText image.Rectangle
		if len(marks) != 0 {
			for _, mark := range marks {
				oldText = oldText.Union(mark)
			}
		} else {
			for y := band.Min.Y; y < band.Max.Y; y++ {
				if rows[y].oldChanged {
					oldText = oldText.Union(rows[y].oldInk)
				}
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
		if len(marks) == 0 {
			marks = []image.Rectangle{raster.Bounds()}
		} else {
			fullyShown := true
			for j, mark := range marks {
				fullyShown = fullyShown && mark.In(crop)
				marks[j] = mark.Intersect(crop).Sub(crop.Min)
			}
			if fullyShown {
				deletions[i] = marks[:0]
			}
		}
		excerpts = append(excerpts, TrialPDFExcerpt{Raster: raster, Regions: marks, At: at, X: crop.Min.X})
		budget -= crop.Dy()
	}
	return excerpts
}

func trialPixelChanged(a, b color.RGBA) bool {
	const tolerance = 12
	return max(channelDelta(a.R, b.R), channelDelta(a.G, b.G), channelDelta(a.B, b.B)) > tolerance
}

func channelDelta(a, b uint8) uint8 { return max(a, b) - min(a, b) }

package shell

import (
	"image"
	"image/draw"
)

// darkPDFPage recolors a freshly rasterized page before it reaches the preview
// cache. Invert luminance, retaining some of the original chroma for figures.
func darkPDFPage(src image.Image) *image.RGBA {
	page, ok := src.(*image.RGBA)
	if !ok {
		page = image.NewRGBA(image.Rectangle{Max: src.Bounds().Size()})
		draw.Draw(page, page.Bounds(), src, src.Bounds().Min, draw.Src)
	}
	width := page.Bounds().Dx() * 4
	for y := range page.Bounds().Dy() {
		row := page.Pix[y*page.Stride : y*page.Stride+width]
		for x := 0; x < len(row); x += 4 {
			white := 255 - int(row[x+3])
			r, g, b := int(row[x])+white, int(row[x+1])+white, int(row[x+2])+white
			light := (54*r + 183*g + 19*b) >> 8
			tone := 224 - (192*light+127)/255
			row[x] = uint8(min(255, max(0, tone+3*(r-light)/4)))
			row[x+1] = uint8(min(255, max(0, tone+3*(g-light)/4)))
			row[x+2] = uint8(min(255, max(0, tone+3*(b-light)/4)))
			row[x+3] = 255
		}
	}
	return page
}

package pdfium

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"runtime"
	"strings"
	"sync"
	"unicode"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ffi"
)

var ErrUnavailable = errors.New("bundled PDFium is unavailable")

var native = struct {
	InitLibrary        func()                                                                                   `ffi:"FPDF_InitLibrary"`
	LoadCustomDocument func(*fileAccess, uintptr) uintptr                                                       `ffi:"FPDF_LoadCustomDocument"`
	CloseDocument      func(uintptr)                                                                            `ffi:"FPDF_CloseDocument"`
	GetPageCount       func(uintptr) int32                                                                      `ffi:"FPDF_GetPageCount"`
	LoadPage           func(uintptr, int32) uintptr                                                             `ffi:"FPDF_LoadPage"`
	ClosePage          func(uintptr)                                                                            `ffi:"FPDF_ClosePage"`
	GetPageWidthF      func(uintptr) float32                                                                    `ffi:"FPDF_GetPageWidthF"`
	GetPageHeightF     func(uintptr) float32                                                                    `ffi:"FPDF_GetPageHeightF"`
	PageToDevice       func(uintptr, int32, int32, int32, int32, int32, float64, float64, *int32, *int32) int32 `ffi:"FPDF_PageToDevice"`
	BitmapCreate       func(int32, int32, int32, *byte, int32) uintptr                                          `ffi:"FPDFBitmap_CreateEx"`
	BitmapFill         func(uintptr, int32, int32, int32, int32, uint32) int32                                  `ffi:"FPDFBitmap_FillRect"`
	BitmapClose        func(uintptr)                                                                            `ffi:"FPDFBitmap_Destroy"`
	RenderPageBitmap   func(uintptr, uintptr, int32, int32, int32, int32, int32, int32)                         `ffi:"FPDF_RenderPageBitmap"`
	TextOpen           func(uintptr) uintptr                                                                    `ffi:"FPDFText_LoadPage"`
	TextClose          func(uintptr)                                                                            `ffi:"FPDFText_ClosePage"`
	TextCount          func(uintptr) int32                                                                      `ffi:"FPDFText_CountChars"`
	TextRune           func(uintptr, int32) uint32                                                              `ffi:"FPDFText_GetUnicode"`
	TextBox            func(uintptr, int32, *float64, *float64, *float64, *float64) int32                       `ffi:"FPDFText_GetCharBox"`
}{}

var initialize = sync.OnceValue(func() error {
	lib := ffi.New("pdfium", nil)
	if _, err := lib.Path(); err != nil {
		if mygo.App.IsPackaged() {
			return err
		}
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if err := lib.Bind(&native); err != nil {
		return err
	}
	native.InitLibrary()
	return nil
})

// PDFium requires serialized access. A document and its file are released before
// another caller enters, so loading pages never keeps a second PDF in memory.
var gate = make(chan struct{}, 1)

// FPDF_FILEACCESS uses unsigned long, followed by two pointers. On
// Windows x64 unsigned long occupies the lower four bytes of the first word.
type fileAccess struct {
	size     uintptr
	getBlock uintptr
	param    uintptr
}

var currentFile *os.File // read only by the synchronous callback while gate is held

var getBlock = sync.OnceValue(func() uintptr {
	return purego.NewCallback(func(_ uintptr, offset uintptr, dst *byte, size uintptr) uintptr {
		if runtime.GOOS == "windows" {
			offset, size = uintptr(uint32(offset)), uintptr(uint32(size))
		}
		_, err := currentFile.ReadAt(unsafe.Slice(dst, int(size)), int64(offset))
		if err != nil {
			return 0
		}
		return 1
	})
})

func withDocument(ctx context.Context, path string, visit func(uintptr, int) error) error {
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := initialize(); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > math.MaxUint32 {
		return errors.New("invalid PDF file size")
	}
	currentFile = f
	defer func() { currentFile = nil }()
	access := fileAccess{size: uintptr(info.Size()), getBlock: getBlock()}
	var pin runtime.Pinner
	pin.Pin(&access)
	defer pin.Unpin()
	doc := native.LoadCustomDocument(&access, 0)
	if doc == 0 {
		return errors.New("PDFium could not open PDF")
	}
	defer native.CloseDocument(doc)
	count := int(native.GetPageCount(doc))
	if count < 1 {
		return errors.New("PDF has no pages")
	}
	if err := visit(doc, count); err != nil {
		return err
	}
	return ctx.Err()
}

// PageCount reports the number of pages in a local PDF.
func PageCount(ctx context.Context, path string) (int, error) {
	var pages int
	err := withDocument(ctx, path, func(_ uintptr, count int) error {
		pages = count
		return nil
	})
	return pages, err
}

func withPage(doc uintptr, count, number int, visit func(uintptr) error) error {
	if number < 1 || number > count {
		return fmt.Errorf("invalid PDF page %d", number)
	}
	page := native.LoadPage(doc, int32(number-1))
	if page == 0 {
		return fmt.Errorf("PDFium could not load page %d", number)
	}
	defer native.ClosePage(page)
	return visit(page)
}

// Render draws a page on white, with the long edge measured in pixels.
func Render(ctx context.Context, path string, number, edge int) (image.Image, error) {
	if edge < 1 || edge > 5120 {
		return nil, errors.New("invalid PDF raster size")
	}
	var img *image.RGBA
	err := withDocument(ctx, path, func(doc uintptr, count int) error {
		return withPage(doc, count, number, func(page uintptr) error {
			w, h := dimensions(page)
			if !(w > 0 && h > 0 && w < math.MaxFloat32 && h < math.MaxFloat32) {
				return errors.New("invalid PDF page dimensions")
			}
			scale := float64(edge) / max(w, h)
			width, height := max(1, int(math.Ceil(w*scale))), max(1, int(math.Ceil(h*scale)))
			img = image.NewRGBA(image.Rect(0, 0, width, height))
			var pin runtime.Pinner
			pin.Pin(&img.Pix[0])
			defer pin.Unpin()
			const bgra = 4
			bitmap := native.BitmapCreate(int32(width), int32(height), bgra, &img.Pix[0], int32(img.Stride))
			if bitmap == 0 {
				return errors.New("PDFium could not create bitmap")
			}
			defer native.BitmapClose(bitmap)
			native.BitmapFill(bitmap, 0, 0, int32(width), int32(height), 0xffffffff)
			const reverseByteOrder = 0x10 // PDFium's BGRA buffer becomes RGBA.
			native.RenderPageBitmap(bitmap, page, 0, 0, int32(width), int32(height), 0, reverseByteOrder)
			return ctx.Err()
		})
	})
	if err != nil {
		return nil, err
	}
	return img, nil
}

func dimensions(page uintptr) (float64, float64) {
	return float64(native.GetPageWidthF(page)), float64(native.GetPageHeightF(page))
}

type Word struct {
	X    float64 `xml:"xMin,attr"`
	Y    float64 `xml:"yMin,attr"`
	R    float64 `xml:"xMax,attr"`
	B    float64 `xml:"yMax,attr"`
	Text string  `xml:",chardata"`
}

type Line struct {
	Words []Word `xml:"word"`
}

type TextPage struct {
	Width  float64 `xml:"width,attr"`
	Height float64 `xml:"height,attr"`
	Lines  []Line  `xml:"flow>block>line"`
}

func DecodePoppler(r io.Reader) (TextPage, error) {
	var doc struct {
		Page TextPage `xml:"body>doc>page"`
	}
	err := xml.NewDecoder(r).Decode(&doc)
	return doc.Page, err
}

func withText(page uintptr, visit func(uintptr, int32) error) error {
	t := native.TextOpen(page)
	if t == 0 {
		return errors.New("PDFium could not load text")
	}
	defer native.TextClose(t)
	return visit(t, native.TextCount(t))
}

// VisitTextPages reports text in reading order as supplied by the PDF, with
// boxes in points from the upper-left corner of each displayed page.
// A zero page number visits the whole document.
func VisitTextPages(ctx context.Context, path string, number int, visit func(int, TextPage) error) error {
	return visitTextPages(ctx, path, number, false, visit)
}

// ReadSelectionPage emits individual CJK glyph boxes for text selection.
func ReadSelectionPage(ctx context.Context, path string, number int) (TextPage, error) {
	if number < 1 {
		return TextPage{}, errors.New("invalid PDF selection page")
	}
	var result TextPage
	err := visitTextPages(ctx, path, number, true, func(_ int, p TextPage) error {
		result = p
		return nil
	})
	return result, err
}

func visitTextPages(ctx context.Context, path string, number int, splitCJK bool, visit func(int, TextPage) error) error {
	return withDocument(ctx, path, func(doc uintptr, count int) error {
		first, last := number, number
		if number == 0 {
			first, last = 1, count
		}
		for n := first; n <= last; n++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			err := withPage(doc, count, n, func(page uintptr) error {
				p, err := extractBoxes(ctx, page, splitCJK)
				if err != nil {
					return err
				}
				return visit(n, p)
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func extractBoxes(ctx context.Context, page uintptr, splitCJK bool) (TextPage, error) {
	w, h := dimensions(page)
	p := TextPage{Width: w, Height: h}
	if !(w > 0 && h > 0 && w < 1e6 && h < 1e6) {
		return p, errors.New("invalid PDF text dimensions")
	}
	// Sample PDFium's transform with one large step on each axis to
	// preserve fractional glyph positions, including crop and page rotation.
	const scale, basis = 32, 1024
	pw, ph := int32(math.Ceil(w*scale)), int32(math.Ceil(h*scale))
	project := func(x, y float64) (float64, float64) {
		var dx, dy int32
		native.PageToDevice(page, 0, 0, pw, ph, 0, x, y, &dx, &dy)
		return float64(dx) * w / float64(pw), float64(dy) * h / float64(ph)
	}
	ox, oy := project(0, 0)
	xx, xy := project(basis, 0)
	yx, yy := project(0, basis)
	device := func(x, y float64) (float64, float64) {
		return ox + (xx-ox)*x/basis + (yx-ox)*y/basis, oy + (xy-oy)*x/basis + (yy-oy)*y/basis
	}
	err := withText(page, func(text uintptr, count int32) error {
		if count < 0 || count > 50000 {
			return errors.New("PDF text layer exceeds the page budget")
		}
		var line Line
		var word Word
		var value strings.Builder
		previousCJK := false
		flushWord := func() {
			if value.Len() > 0 {
				word.Text = value.String()
				line.Words = append(line.Words, word)
				value.Reset()
			}
		}
		flushLine := func() {
			flushWord()
			if len(line.Words) > 0 {
				p.Lines = append(p.Lines, line)
				line = Line{}
			}
		}
		for i := int32(0); i < count; i++ {
			if i%128 == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
			r := rune(native.TextRune(text, i))
			if r == '\r' || r == '\n' {
				flushLine()
				continue
			}
			if unicode.IsSpace(r) || r == 0 {
				flushWord()
				continue
			}
			var l, right, b, top float64
			if native.TextBox(text, i, &l, &right, &b, &top) == 0 {
				continue
			}
			x0, y0 := device(l, top)
			x1, y1 := device(right, b)
			x, y, rgt, bot := min(x0, x1), min(y0, y1), max(x0, x1), max(y0, y1)
			if rgt <= x || bot <= y {
				if value.Len() > 0 && unicode.IsMark(r) {
					value.WriteRune(r)
				}
				continue
			}
			if value.Len() > 0 {
				height := max(word.B-word.Y, bot-y)
				if y > word.B || bot < word.Y || x-word.R > height*2 {
					flushLine()
				} else if x-word.R > height*.32 || splitCJK && (previousCJK || IsCJK(r)) && !unicode.IsMark(r) {
					flushWord()
				}
			}
			if value.Len() == 0 {
				word = Word{X: x, Y: y, R: rgt, B: bot}
			} else {
				word.X, word.Y = min(word.X, x), min(word.Y, y)
				word.R, word.B = max(word.R, rgt), max(word.B, bot)
			}
			value.WriteRune(r)
			previousCJK = splitCJK && IsCJK(r)
		}
		flushLine()
		return nil
	})
	return p, err
}

func IsCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) ||
		r >= 0x3000 && r <= 0x303f || r >= 0xff00 && r <= 0xffef
}

// Text reads the full text layer up to the caller's UTF-8 byte budget.
func Text(ctx context.Context, path string, limit int) (string, error) {
	var out strings.Builder
	err := withDocument(ctx, path, func(doc uintptr, count int) error {
		for n := 1; n <= count; n++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			err := withPage(doc, count, n, func(page uintptr) error {
				return withText(page, func(text uintptr, chars int32) error {
					for i := int32(0); i < chars; i++ {
						if i%128 == 0 && ctx.Err() != nil {
							return ctx.Err()
						}
						r := rune(native.TextRune(text, i))
						if r != 0 {
							out.WriteRune(r)
						}
						if out.Len() >= limit {
							return io.ErrShortBuffer
						}
					}
					out.WriteByte('\n')
					return nil
				})
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return out.String(), nil
}

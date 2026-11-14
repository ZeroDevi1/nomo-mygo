// PDF 反查按需读取当前页的文本框，坐标使用 PDF 左上角为原点的点数。
package typeset

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"nomo/desktop/internal/pdfium"
)

type pdfWord = pdfium.Word

// SourceLine 返回主文件中从零起算的行。空白、外部 TeX 文件或无法唯一定位时返回 -1。
func (s *Session) SourceLine(ctx context.Context, page int, x, y float64, source func() string) int {
	return s.sourceLine(ctx, page, x, y, source, false)
}

func (s *Session) SourceLineNear(ctx context.Context, page int, x, y float64, source func() string) int {
	return s.sourceLine(ctx, page, x, y, source, true)
}

func (s *Session) sourceLine(ctx context.Context, page int, x, y float64, source func() string, nearest bool) int {
	hit := s.sourceHit(ctx, page, x, y, nearest)
	if hit == nil {
		return -1
	}
	if IsLaTeXPath(s.path) {
		if line, ok := s.syncTeXLine(ctx, page, hit.x, hit.y); ok || len(hit.words) == 0 {
			return line
		}
	}
	return matchSourceLine(ctx, source(), hit.words, hit.index)
}

type pdfSourceHit struct {
	x, y  float64
	words []pdfWord
	index int
}

type pdfSourcePage struct {
	number int
	pdfium.TextPage
}

func (s *Session) readSourcePage(ctx context.Context, page int) *pdfSourcePage {
	if p := s.sourcePage; p != nil && p.number == page {
		return p
	}
	p, err := readPDFSourcePage(ctx, filepath.Join(s.dir, "pages"), "current.pdf", page)
	if err != nil {
		return nil
	}
	s.sourcePage = p
	return p
}

func readPDFSourcePage(ctx context.Context, dir, path string, page int) (*pdfSourcePage, error) {
	if page < 1 {
		return nil, fmt.Errorf("invalid PDF page %d", page)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	result := &pdfSourcePage{number: page}
	err := pdfium.VisitTextPages(ctx, path, page, func(_ int, p pdfium.TextPage) error {
		result.TextPage = p
		return nil
	})
	if err == nil {
		return result, nil
	}
	if !errors.Is(err, pdfium.ErrUnavailable) {
		return nil, err
	}
	if dir == "" {
		dir, err = os.MkdirTemp("", "nomo-pdf-text-*")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
	}
	n := strconv.Itoa(page)
	file := filepath.Join(dir, "click.xhtml")
	defer os.Remove(file)
	if _, err := run(ctx, dir, "", nil, "pdftotext", "-f", n, "-l", n, "-bbox-layout", path, file); err != nil {
		return nil, err
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if stat, err := f.Stat(); err != nil || stat.Size() > 2<<20 {
		return nil, fmt.Errorf("PDF text layer exceeds the page budget")
	}
	result.TextPage, err = pdfium.DecodePoppler(pdfXML{f})
	if err != nil {
		return nil, err
	}
	if result.Width <= 0 || result.Height <= 0 {
		return nil, fmt.Errorf("invalid PDF text layer")
	}
	return result, nil
}

// PDFRegionText copies words centered inside a page-relative selection rectangle.
func PDFRegionText(ctx context.Context, path string, page int, x, y, width, height float64, limit int) (string, error) {
	source, err := readPDFSourcePage(ctx, "", path, page)
	if err != nil {
		return "", err
	}
	var text strings.Builder
	for _, line := range source.Lines {
		first := true
		for _, word := range line.Words {
			cx, cy := (word.X+word.R)/(2*source.Width), (word.Y+word.B)/(2*source.Height)
			if cx < x || cx > x+width || cy < y || cy > y+height {
				continue
			}
			if text.Len()+len(word.Text)+1 > limit {
				return "", fmt.Errorf("PDF region text exceeds %d bytes", limit)
			}
			if text.Len() > 0 {
				if first {
					text.WriteByte('\n')
				} else {
					text.WriteByte(' ')
				}
			}
			text.WriteString(word.Text)
			first = false
		}
	}
	return text.String(), nil
}

func (s *Session) sourceHit(ctx context.Context, page int, x, y float64, nearest bool) *pdfSourceHit {
	p := s.readSourcePage(ctx, page)
	if p == nil {
		return nil
	}
	x, y = x*p.Width, y*p.Height
	var hit pdfSourceHit
	distance := math.Inf(1)
	for _, line := range p.Lines {
		for i, w := range line.Words {
			dx, dy := max(0, w.X-x, x-w.R), max(0, w.Y-y, y-w.B)
			if dx == 0 && dy == 0 {
				return &pdfSourceHit{x: x, y: y, words: line.Words, index: i}
			}
			if score := dy*4 + dx; nearest && score < distance {
				distance = score
				hit = pdfSourceHit{x: (w.X + w.R) / 2, y: (w.Y + w.B) / 2, words: line.Words, index: i}
			}
		}
	}
	if IsLaTeXPath(s.path) && !nearest {
		// SyncTeX also covers figures and equations with no extractable words.
		return &pdfSourceHit{x: x, y: y}
	}
	if len(hit.words) == 0 {
		return nil
	}
	return &hit
}

func (s *Session) syncTeXLine(ctx context.Context, page int, x, y float64) (int, bool) {
	out, err := run(ctx, s.dir, "", nil, "synctex", "edit", "-o", fmt.Sprintf("%d:%f:%f:%s", page, x, y, filepath.Base(s.pdf)))
	if err == nil {
		main := false
		for row := range strings.Lines(out) {
			row = strings.TrimSpace(row)
			if path, ok := strings.CutPrefix(row, "Input:"); ok {
				if !filepath.IsAbs(path) {
					path = filepath.Join(s.dir, path)
				}
				input, e1 := os.Stat(path)
				original, e2 := os.Stat(filepath.Join(s.dir, "preview.tex"))
				main = e1 == nil && e2 == nil && os.SameFile(input, original)
				if !main {
					return -1, true
				}
			}
			if value, ok := strings.CutPrefix(row, "Line:"); ok && main {
				if line, e := strconv.Atoi(value); e == nil {
					return line - 1, true
				}
			}
		}
	}
	return -1, false
}

// Typst 普通文本按点击词及左右文匹配；保留最强匹配，多个源码行同分时放弃。
func matchSourceLine(ctx context.Context, source string, words []pdfWord, hit int) int {
	line, _ := scoreSourceLine(ctx, source, words, hit)
	return line
}

func scoreSourceLine(ctx context.Context, source string, words []pdfWord, hit int) (int, int) {
	compact := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) || r == '*' || r == '_' {
				return -1
			}
			return r
		}, s)
	}
	target := compact(words[hit].Text)
	if target == "" {
		return -1, -1
	}
	var left, right string
	for _, w := range words[max(0, hit-4):hit] {
		left += compact(w.Text)
	}
	for _, w := range words[hit+1 : min(len(words), hit+5)] {
		right += compact(w.Text)
	}
	best, found := -1, -1
	line := 0
	for raw := range strings.Lines(source) {
		if ctx.Err() != nil {
			return -1, -1
		}
		row := compact(raw)
		for at := 0; at < len(row); {
			offset := strings.Index(row[at:], target)
			if offset < 0 {
				break
			}
			start := at + offset
			at = start + len(target)
			score := 0
			for k := 1; k <= len(left) && k <= start && row[start-k] == left[len(left)-k]; k++ {
				score++
			}
			for k := 0; k < len(right) && at+k < len(row) && row[at+k] == right[k]; k++ {
				score++
			}
			if score > best {
				best, found = score, line
			} else if score == best && found != line {
				found = -1
			}
		}
		line++
	}
	return found, best
}

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
	"unicode/utf8"

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

// PDFTextHit maps a rendered line to a contiguous literal span of one source line.
// Coordinates are fractions of the displayed page, from its upper left.
type PDFTextHit struct {
	ProjectLocation
	Start, End  int
	X, Y, W     float64
	DisplayText string
}

func (s *Session) EditablePDFText(ctx context.Context, page int, x, y float64, path string, source func() string) PDFTextHit {
	hit := s.sourceHit(ctx, page, x, y, false)
	if hit == nil || len(hit.words) == 0 {
		return PDFTextHit{}
	}
	var location ProjectLocation
	if s.mirror != nil {
		location = s.projectSourceLocation(ctx, page, x, y, false)
	} else {
		text := source()
		var line int
		if IsLaTeXPath(s.path) {
			line, _ = s.syncTeXLine(ctx, page, hit.x, hit.y)
		} else {
			line = matchSourceLine(ctx, text, hit.words, hit.index)
		}
		if line < 0 {
			return PDFTextHit{}
		}
		n := 0
		for row := range strings.Lines(text) {
			if n == line {
				row = strings.TrimSuffix(row, "\n")
				location = ProjectLocation{Path: path, Line: line, Text: strings.TrimSuffix(strings.TrimPrefix(row, "\ufeff"), "\r")}
				break
			}
			n++
		}
	}
	if location.Path == "" || len(location.Text) > 16<<10 {
		return PDFTextHit{}
	}
	// Editing is scoped to the active source. Preserve the location so the
	// caller can distinguish text from another included file.
	if s.mirror != nil {
		clicked, ok := s.mirror.sourcePath(location.Path)
		active, activeOK := s.mirror.sourcePath(path)
		if !ok || !activeOK || clicked != active {
			return PDFTextHit{ProjectLocation: ProjectLocation{Path: location.Path}}
		}
		location.Path = path
	}
	words := make([]string, 0, len(hit.words))
	left, top, right := hit.words[0].X, hit.words[0].Y, hit.words[0].R
	for _, word := range hit.words {
		words = append(words, word.Text)
		left, top = min(left, word.X), min(top, word.Y)
		right = max(right, word.R)
	}
	latex := IsLaTeXPath(location.Path)
	text := strings.Join(words, " ")
	start, end := literalPDFSpan(location.Text, text, latex)
	if start < 0 {
		// PDF text extraction may divide adjacent CJK glyphs into words.
		text = strings.Join(words, "")
		start, end = literalPDFSpan(location.Text, text, latex)
	}
	if start < 0 {
		return PDFTextHit{}
	}
	p := s.sourcePage // sourceHit loaded this page's text layer.
	location.Text = strings.Clone(location.Text)
	return PDFTextHit{ProjectLocation: location, Start: start, End: end, X: left / p.Width, Y: top / p.Height,
		W: (right - left) / p.Width, DisplayText: text}
}

func literalPDFSpan(source, text string, latex bool) (start, end int) {
	if text == "" || len(text) > 4096 {
		return -1, -1
	}
	escaped := LiteralManuscriptText(text, latex)
	start = strings.Index(source, text)
	literal := text
	if other := strings.Index(source, escaped); escaped != text && other >= 0 {
		if start >= 0 && (start < other || start+len(text) > other+len(escaped)) {
			return -1, -1
		}
		start, literal = other, escaped
	}
	if start < 0 || strings.LastIndex(source, literal) != start {
		return -1, -1
	}
	end = start + len(literal)
	before, _ := utf8.DecodeLastRuneInString(source[:start])
	after, _ := utf8.DecodeRuneInString(source[end:])
	first, _ := utf8.DecodeRuneInString(text)
	last, _ := utf8.DecodeLastRuneInString(text)
	if before == '\\' || before == '#' || before == '@' || !editableMarkupSpan(source[:end], latex) ||
		isLatinWordEdge(before) && isLatinWordEdge(first) || isLatinWordEdge(after) && isLatinWordEdge(last) {
		return -1, -1
	}
	return start, end
}

// The inverse map provides a source line, not a parse tree. Avoid rewriting
// text reached through a comment, math, or Typst code on that line.
func editableMarkupSpan(text string, latex bool) bool {
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\\':
			if latex && i+1 < len(text) && (text[i+1] == '(' || text[i+1] == '[') {
				return false
			}
			i++
		case '$':
			return false
		case '%':
			if latex {
				return false
			}
		case '#', '\x60':
			if !latex {
				return false
			}
		case '/':
			if !latex && i+1 < len(text) && (text[i+1] == '/' || text[i+1] == '*') {
				return false
			}
		}
	}
	return true
}

func isLatinWordEdge(r rune) bool {
	return unicode.In(r, unicode.Latin) || unicode.IsDigit(r) || unicode.IsMark(r)
}

var latexLiteralText = strings.NewReplacer(
	"\\", "\\textbackslash{}", "{", "\\{", "}", "\\}", "$", "\\$", "&", "\\&", "#", "\\#",
	"%", "\\%", "_", "\\_", "^", "\\textasciicircum{}", "~", "\\textasciitilde{}",
	"<", "\\textless{}", ">", "\\textgreater{}", "|", "\\textbar{}",
)

var typstLiteralText = strings.NewReplacer(
	"\\", "\\\\", "\x60", "\\\x60", "#", "\\#", "@", "\\@", "$", "\\$", "*", "\\*", "_", "\\_",
	"[", "\\[", "]", "\\]", "<", "\\<", ">", "\\>", "~", "\\~", "=", "\\=", "+", "\\+",
	"-", "\\-", "...", "\\...", "//", "\\//", "/*", "\\/*",
)

// LiteralManuscriptText escapes replacement text for use in manuscript markup.
func LiteralManuscriptText(text string, latex bool) string {
	if latex {
		return latexLiteralText.Replace(text)
	}
	return typstLiteralText.Replace(text)
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
	text, _, _, err := source.regionText(x, y, width, height, limit)
	return text, err
}

// ManuscriptRegion reads the compiled page and locates the first printed word
// in the selection, or its center for a figure with no printed text.
func (s *Session) ManuscriptRegion(ctx context.Context, page int, x, y, width, height float64, limit int) (string, float64, float64, error) {
	p := s.readSourcePage(ctx, page)
	if p == nil {
		return "", x + width/2, y + height/2, ctx.Err()
	}
	return p.regionText(x, y, width, height, limit)
}

func (p *pdfSourcePage) regionText(x, y, width, height float64, limit int) (string, float64, float64, error) {
	var text strings.Builder
	hitX, hitY := x+width/2, y+height/2
	for _, line := range p.Lines {
		first := true
		for _, word := range line.Words {
			cx, cy := (word.X+word.R)/(2*p.Width), (word.Y+word.B)/(2*p.Height)
			if cx < x || cx > x+width || cy < y || cy > y+height {
				continue
			}
			if text.Len()+len(word.Text)+1 > limit {
				return "", 0, 0, fmt.Errorf("PDF region text exceeds %d bytes", limit)
			}
			if text.Len() == 0 {
				hitX, hitY = cx, cy
			} else {
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
	return text.String(), hitX, hitY, nil
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

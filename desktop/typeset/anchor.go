package typeset

import (
	"context"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"image"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"nomo/desktop/internal/pdfium"
)

// PageAnchor 使用 PDF 的左上角坐标（点）；页面缩放时原框随整页等比缩放。
type PageAnchor struct {
	Page                     int
	X, Y, W, H, PageW, PageH float64
	FontPt, LinePt, IndentPt float64
}

// ParagraphSession 提供正式页面的可选段落定位和快速试排能力。
type ParagraphSession interface {
	PrepareAnchors(context.Context)
	Anchor(context.Context, string) PageAnchor
	Paragraph(context.Context, string, string) image.Image
}

func blockKey(text string) [32]byte {
	return sha256.Sum256([]byte(strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, text)))
}

// Anchor 只接受上一份成功 PDF 中唯一且完整匹配的文本块。重复、跨页、公式或多栏拆块不猜位置。
func (s *Session) Anchor(ctx context.Context, text string) PageAnchor {
	if s.pages == 0 || len(text) > 8192 {
		return PageAnchor{}
	}
	s.PrepareAnchors(ctx)
	// PDF 的文本层不含字体命令和分组括号。仅消去快览也认识的行内字体命令。
	if strings.ContainsAny(text, `\{}$%&#_^~`) {
		var b strings.Builder
		depth := 0
		for i := 0; i < len(text); {
			switch text[i] {
			case '\\':
				a := i
				i++
				for i < len(text) && text[i] >= 'a' && text[i] <= 'z' {
					i++
				}
				cmd := text[a+1 : i]
				if (cmd != "textbf" && cmd != "emph" && cmd != "textrm" && cmd != "textit") || i >= len(text) || text[i] != '{' {
					return PageAnchor{}
				}
				depth++
				i++
			case '}':
				if depth == 0 {
					return PageAnchor{}
				}
				depth--
				i++
			case '{', '$', '%', '&', '#', '_', '^', '~':
				return PageAnchor{}
			default:
				b.WriteByte(text[i])
				i++
			}
		}
		if depth != 0 {
			return PageAnchor{}
		}
		text = b.String()
	}
	return s.anchors[blockKey(text)]
}

// PrepareAnchors 在整页可见前建立可选索引，避免把首次查询的 I/O 计入输入延迟。
func (s *Session) PrepareAnchors(ctx context.Context) {
	if !s.indexed && s.pages > 0 {
		s.anchors = s.readAnchors(ctx)
		s.indexed = ctx.Err() == nil
	}
}

func (s *Session) readAnchors(ctx context.Context) map[[32]byte]PageAnchor {
	dir := filepath.Join(s.dir, "pages")
	idx := make(map[[32]byte]PageAnchor)
	var words int
	err := pdfium.VisitTextPages(ctx, filepath.Join(dir, "current.pdf"), 0, func(page int, p pdfium.TextPage) error {
		for _, line := range p.Lines {
			words += len(line.Words)
		}
		if words > 96000 || !indexPDFiumAnchors(idx, page, p) {
			return errors.New("PDF paragraph index exceeds budget")
		}
		return nil
	})
	if err == nil {
		return idx
	}
	if !errors.Is(err, pdfium.ErrUnavailable) {
		return nil
	}
	file := filepath.Join(dir, "boxes.xhtml")
	defer os.Remove(file)
	if _, e := run(ctx, dir, "", nil, "pdftotext", "-bbox-layout", "current.pdf", "boxes.xhtml"); e != nil {
		return nil
	}
	f, e := os.Open(file)
	if e != nil {
		return nil
	}
	defer f.Close()
	if st, e := f.Stat(); e != nil || st.Size() > 12<<20 {
		return nil
	}
	return parseAnchors(f)
}

// PDFium provides glyph positions rather than paragraphs. Only merge successive
// lines with a common left edge, normal line spacing and a near-full previous
// line. Anchor still requires the complete paragraph text to match uniquely.
func indexPDFiumAnchors(idx map[[32]byte]PageAnchor, page int, p pdfium.TextPage) bool {
	type row struct {
		x, y, r, b float64
		text       string
	}
	rows := make([]row, 0, len(p.Lines))
	var pageRight float64
	for _, line := range p.Lines {
		if len(line.Words) == 0 {
			continue
		}
		first := line.Words[0]
		bounds := row{x: first.X, y: first.Y, r: first.R, b: first.B}
		var text strings.Builder
		for _, word := range line.Words {
			bounds.x, bounds.y = min(bounds.x, word.X), min(bounds.y, word.Y)
			bounds.r, bounds.b = max(bounds.r, word.R), max(bounds.b, word.B)
			text.WriteString(word.Text)
		}
		bounds.text = text.String()
		rows = append(rows, bounds)
		pageRight = max(pageRight, bounds.r)
	}
	joins := func(a, b row) bool {
		line := b.y - a.y
		font := a.b - a.y
		return font > 0 && line > font*.7 && line < font*2 &&
			math.Abs(a.x-b.x) < font*2 && a.r >= pageRight-font*2
	}
	for first := 0; first < len(rows); {
		a := rows[first]
		x, y, r, b := a.x, a.y, a.r, a.b
		font, leading := a.b-a.y, (a.b-a.y)*1.25
		var text strings.Builder
		text.WriteString(a.text)
		last := first + 1
		for ; last < len(rows) && joins(rows[last-1], rows[last]); last++ {
			next := rows[last]
			if text.Len()+len(next.text) > 8192 {
				break
			}
			if last == first+1 {
				leading = next.y - a.y
			}
			text.WriteString(next.text)
			x, r, b = min(x, next.x), max(r, next.r), max(b, next.b)
		}
		first = last
		if !addAnchor(idx, text.String(), page, p.Width, p.Height, x, y, r, b, font, leading, a.x-x) {
			return false
		}
	}
	return true
}

func addAnchor(idx map[[32]byte]PageAnchor, text string, page int, w, h, x, y, r, b, font, leading, indent float64) bool {
	if len(idx) >= 8192 {
		return false
	}
	key := blockKey(text)
	if _, seen := idx[key]; seen {
		idx[key] = PageAnchor{}
		return true
	}
	if !(x >= 0 && y >= 0 && r > x && b > y && r <= w && b <= h && w <= 10000 && h <= 10000) {
		return true
	}
	x, y = max(0, x-.5), max(0, y-.5)
	idx[key] = PageAnchor{page, x, y, min(w, r+.5) - x, min(h, b+.5) - y, w, h, font, leading, indent}
	return true
}

func parseAnchors(r io.Reader) map[[32]byte]PageAnchor {
	dec := xml.NewDecoder(pdfXML{r})
	idx := make(map[[32]byte]PageAnchor)
	page := 0
	for {
		t, e := dec.Token()
		if e != nil {
			if e == io.EOF {
				return idx
			}
			return nil
		}
		start, ok := t.(xml.StartElement)
		if !ok || start.Name.Local != "page" {
			continue
		}
		page++
		var p struct {
			Width  float64 `xml:"width,attr"`
			Height float64 `xml:"height,attr"`
			Blocks []struct {
				X     float64 `xml:"xMin,attr"`
				Y     float64 `xml:"yMin,attr"`
				R     float64 `xml:"xMax,attr"`
				B     float64 `xml:"yMax,attr"`
				Lines []struct {
					X     float64  `xml:"xMin,attr"`
					Y     float64  `xml:"yMin,attr"`
					B     float64  `xml:"yMax,attr"`
					Words []string `xml:"word"`
				} `xml:"line"`
			} `xml:"flow>block"`
		}
		if dec.DecodeElement(&p, &start) != nil {
			return nil
		}
		for _, b := range p.Blocks {
			if len(b.Lines) == 0 {
				continue
			}
			var words strings.Builder
			for _, l := range b.Lines {
				for _, w := range l.Words {
					words.WriteString(w)
				}
			}
			font := b.Lines[0].B - b.Lines[0].Y
			line := font * 1.25
			if len(b.Lines) > 1 {
				line = b.Lines[1].Y - b.Lines[0].Y
			}
			if !addAnchor(idx, words.String(), page, p.Width, p.Height, b.X, b.Y, b.R, b.B, font, line, b.Lines[0].X-b.X) {
				return nil
			}
		}
	}
}

// 部分数学字体的文本层含 XML 1.0 禁止的 C0 字节。保留占位符，使这一块无法误与普通正文拼接。
type pdfXML struct{ io.Reader }

func (r pdfXML) Read(p []byte) (int, error) {
	n, e := r.Reader.Read(p)
	for i, c := range p[:n] {
		if c < 32 && c != 9 && c != 10 && c != 13 {
			p[i] = 127
		}
	}
	return n, e
}

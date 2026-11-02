package typeset

import (
	"context"
	"crypto/sha256"
	"encoding/xml"
	"image"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode"
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
			if len(idx) >= 8192 {
				return nil
			}
			if len(b.Lines) == 0 {
				continue
			}
			var words strings.Builder
			for _, l := range b.Lines {
				for _, w := range l.Words {
					words.WriteString(w)
				}
			}
			key := blockKey(words.String())
			if _, seen := idx[key]; seen {
				idx[key] = PageAnchor{}
				continue
			}
			if !(b.X >= 0 && b.Y >= 0 && b.R > b.X && b.B > b.Y && b.R <= p.Width && b.B <= p.Height && p.Width <= 10000 && p.Height <= 10000) {
				continue
			}
			x, y := math.Max(0, b.X-.5), math.Max(0, b.Y-.5)
			font := b.Lines[0].B - b.Lines[0].Y
			line := font * 1.25
			if len(b.Lines) > 1 {
				line = b.Lines[1].Y - b.Lines[0].Y
			}
			idx[key] = PageAnchor{page, x, y, math.Min(p.Width, b.R+.5) - x, math.Min(p.Height, b.B+.5) - y, p.Width, p.Height, font, line, b.Lines[0].X - b.X}
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

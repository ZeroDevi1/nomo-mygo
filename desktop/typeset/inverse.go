// PDF 反查仅在点击时读取当前页的文本框，坐标使用 PDF 左上角为原点的点数。
package typeset

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

type pdfWord struct {
	X    float64 `xml:"xMin,attr"`
	Y    float64 `xml:"yMin,attr"`
	R    float64 `xml:"xMax,attr"`
	B    float64 `xml:"yMax,attr"`
	Text string  `xml:",chardata"`
}

// SourceLine 返回主文件中从零起算的行。空白、外部 TeX 文件或无法唯一定位时返回 -1。
func (s *Session) SourceLine(ctx context.Context, page int, x, y float64, source func() string) int {
	hit := s.sourceHit(ctx, page, x, y)
	if hit == nil {
		return -1
	}
	if IsLaTeXPath(s.path) {
		if line, ok := s.syncTeXLine(ctx, page, hit.x, hit.y); ok {
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

func (s *Session) sourceHit(ctx context.Context, page int, x, y float64) *pdfSourceHit {
	dir := filepath.Join(s.dir, "pages")
	n := strconv.Itoa(page)
	file := filepath.Join(dir, "click.xhtml")
	defer os.Remove(file)
	if _, err := run(ctx, dir, "", nil, "pdftotext", "-f", n, "-l", n, "-bbox-layout", "current.pdf", "click.xhtml"); err != nil {
		return nil
	}
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	if stat, err := f.Stat(); err != nil || stat.Size() > 2<<20 {
		return nil
	}
	var doc struct {
		Page struct {
			Width  float64 `xml:"width,attr"`
			Height float64 `xml:"height,attr"`
			Lines  []struct {
				Words []pdfWord `xml:"word"`
			} `xml:"flow>block>line"`
		} `xml:"body>doc>page"`
	}
	if xml.NewDecoder(pdfXML{f}).Decode(&doc) != nil {
		return nil
	}
	x *= doc.Page.Width
	y *= doc.Page.Height
	for _, line := range doc.Page.Lines {
		for i, w := range line.Words {
			if x < w.X || x > w.R || y < w.Y || y > w.B {
				continue
			}
			return &pdfSourceHit{x: x, y: y, words: line.Words, index: i}
		}
	}
	return nil
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

package typeset

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
)

type Bookmark struct {
	Title       string
	Level, Page int
}

// PDF 书签包含排版后的标题和目标页，也覆盖从其他源文件引入的章节。
func (s *Session) Bookmarks(ctx context.Context) []Bookmark {
	f, err := os.Open(filepath.Join(s.dir, "pages", "current.pdf"))
	if err != nil {
		return nil
	}
	defer f.Close()
	tree, err := api.Bookmarks(ctx, f, nil)
	if err != nil {
		return nil
	}
	var result []Bookmark
	var walk func([]pdfcpu.Bookmark, int)
	walk = func(nodes []pdfcpu.Bookmark, level int) {
		for _, n := range nodes {
			if len(result) >= 8192 || level > 32 {
				return
			}
			if n.PageFrom >= 1 && n.PageFrom <= s.pages {
				result = append(result, Bookmark{n.Title, level, n.PageFrom})
			}
			walk(n.Kids, level+1)
		}
	}
	walk(tree, 1)
	return result
}

// 未写 PDF 书签的 LaTeX 文档可通过编译时的行号定位页面。
func (s *Session) PageForLine(ctx context.Context, line int) int {
	return s.PositionForLine(ctx, line, "").Page
}

// PDFPosition is a page and, when known, its vertical position from the top.
// Y is a fraction of the page height, or -1 for a page-only result.
type PDFPosition struct {
	Page int
	Y    float64
}

func (s *Session) PositionForLine(ctx context.Context, line int, text string) PDFPosition {
	if !IsLaTeXPath(s.path) {
		return s.positionForText(ctx, text)
	}
	return s.syncTeXPosition(ctx, "preview.tex", line)
}

func (s *Session) syncTeXPosition(ctx context.Context, input string, line int) PDFPosition {
	out, err := run(ctx, s.dir, "", nil, "synctex", "view", "-i", fmt.Sprintf("%d:0:%s", line+1, input), "-o", filepath.Base(s.pdf))
	if err != nil {
		return PDFPosition{}
	}
	pos := PDFPosition{Y: -1}
	for row := range strings.Lines(out) {
		row = strings.TrimSpace(row)
		if n, ok := strings.CutPrefix(row, "Page:"); ok {
			if pos.Page > 0 {
				break
			}
			pos.Page, _ = strconv.Atoi(n)
		}
		if n, ok := strings.CutPrefix(row, "y:"); ok && pos.Page > 0 {
			if y, err := strconv.ParseFloat(n, 64); err == nil {
				if p := s.readSourcePage(ctx, pos.Page); p != nil && y >= 0 && y <= p.Height {
					pos.Y = y / p.Height
				}
			}
			break
		}
	}
	return pos
}

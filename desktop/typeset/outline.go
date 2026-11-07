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
	if !IsLaTeXPath(s.path) {
		return 0
	}
	return s.syncTeXPage(ctx, "preview.tex", line)
}

func (s *Session) syncTeXPage(ctx context.Context, input string, line int) int {
	out, err := run(ctx, s.dir, "", nil, "synctex", "view", "-i", fmt.Sprintf("%d:0:%s", line+1, input), "-o", filepath.Base(s.pdf))
	if err == nil {
		for row := range strings.Lines(out) {
			if n, ok := strings.CutPrefix(strings.TrimSpace(row), "Page:"); ok {
				page, _ := strconv.Atoi(n)
				return page
			}
		}
	}
	return 0
}

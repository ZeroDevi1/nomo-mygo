package typeset

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type ProjectLocation struct {
	Path string
	Line int
	Text string
}

func (m *projectMirror) sourcePath(original string) (string, bool) {
	path, err := filepath.EvalSymlinks(original)
	if err != nil {
		return "", false
	}
	name, err := filepath.Rel(m.root, path)
	if err != nil || !filepath.IsLocal(name) || !m.files[name].valid {
		return "", false
	}
	return filepath.Join(m.dir, name), true
}

// ProjectPreamble reads only the bounded beginning of the compiled main input.
func (s *Session) ProjectPreamble() string {
	f, err := os.Open(s.path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 32768))
	if err != nil {
		return ""
	}
	return latexPreamble(string(data))
}

func latexPreamble(text string) string {
	if at := strings.Index(text, `\begin{document}`); at >= 0 {
		return text[:at]
	}
	return ""
}

func (s *Session) PageForProjectLine(ctx context.Context, original string, line int, expected string) int {
	return s.PositionForProjectLine(ctx, original, line, expected).Page
}

func (s *Session) PositionForProjectLine(ctx context.Context, original string, line int, expected string) PDFPosition {
	if s.mirror == nil || line < 0 {
		return PDFPosition{}
	}
	path, ok := s.mirror.sourcePath(original)
	if !ok {
		return PDFPosition{}
	}
	text, ok := projectLine(ctx, path, line)
	if !ok || text != expected {
		return PDFPosition{}
	}
	if IsLaTeXPath(s.path) {
		if path == s.path {
			path = "preview.tex"
		}
		return s.syncTeXPosition(ctx, path, line)
	}
	// Typst CLI has no SyncTeX output. Locate a uniquely matching rendered block.
	name, err := filepath.Rel(s.mirror.dir, path)
	if err != nil || !slices.Contains(s.sourceDeps, name) || strings.TrimSpace(text) == "" {
		return PDFPosition{}
	}
	return s.positionForText(ctx, text)
}

func (s *Session) positionForText(ctx context.Context, text string) PDFPosition {
	text = strings.TrimSpace(text)
	if text == "" {
		return PDFPosition{}
	}
	if marks := strings.TrimLeft(text, "="); marks != text && strings.HasPrefix(marks, " ") {
		text = strings.TrimSpace(marks)
	} else if strings.HasPrefix(text, "#") {
		if _, body, ok := strings.Cut(text, "["); ok && strings.HasSuffix(body, "]") {
			if body := strings.TrimSuffix(body, "]"); !strings.ContainsAny(body, "[]") {
				text = strings.TrimSpace(body)
			}
		}
	}
	if a := s.Anchor(ctx, text); a.Page > 0 {
		return PDFPosition{Page: a.Page, Y: a.Y / a.PageH}
	}
	return PDFPosition{}
}

// ProjectSourceLocation resolves only real project sources in the compiled tree.
// Ambiguous Typst text and generated LaTeX input are left without a location.
func (s *Session) ProjectSourceLocation(ctx context.Context, page int, x, y float64) ProjectLocation {
	return s.projectSourceLocation(ctx, page, x, y, false)
}

func (s *Session) ProjectSourceNear(ctx context.Context, page int, x, y float64) ProjectLocation {
	return s.projectSourceLocation(ctx, page, x, y, true)
}

func (s *Session) projectSourceLocation(ctx context.Context, page int, x, y float64, nearest bool) ProjectLocation {
	if s.mirror == nil {
		return ProjectLocation{}
	}
	hit := s.sourceHit(ctx, page, x, y, nearest)
	if hit == nil {
		return ProjectLocation{}
	}
	if IsLaTeXPath(s.path) {
		out, err := run(ctx, s.dir, "", nil, "synctex", "edit", "-o", fmt.Sprintf("%d:%f:%f:%s", page, hit.x, hit.y, filepath.Base(s.pdf)))
		if err != nil {
			return ProjectLocation{}
		}
		var input string
		for row := range strings.Lines(out) {
			row = strings.TrimSpace(row)
			if value, ok := strings.CutPrefix(row, "Input:"); ok {
				input = strings.TrimSpace(value)
			}
			if value, ok := strings.CutPrefix(row, "Line:"); ok && input != "" {
				line, err := strconv.Atoi(strings.TrimSpace(value))
				if err != nil || line < 1 {
					return ProjectLocation{}
				}
				return s.projectTeXLocation(ctx, input, line-1)
			}
		}
		return ProjectLocation{}
	}
	remaining := int64(8 << 20)
	best := -1
	var result ProjectLocation
	for _, name := range s.sourceDeps {
		entry := s.mirror.files[name]
		if ctx.Err() != nil || entry.size > remaining {
			return ProjectLocation{}
		}
		path := filepath.Join(s.mirror.dir, name)
		data, err := os.ReadFile(path)
		if err != nil || int64(len(data)) > remaining {
			return ProjectLocation{}
		}
		remaining -= int64(len(data))
		line, score := scoreSourceLine(ctx, string(data), hit.words, hit.index)
		if score < 0 || score < best {
			continue
		}
		if score == best || line < 0 {
			result, best = ProjectLocation{}, score
			continue
		}
		text, ok := projectLine(ctx, path, line)
		if !ok {
			return ProjectLocation{}
		}
		best = score
		result = ProjectLocation{Path: filepath.Join(s.mirror.sourceRoot, name), Line: line, Text: text}
	}
	return result
}

func (s *Session) projectTeXLocation(ctx context.Context, input string, line int) ProjectLocation {
	// The engine is launched with ./preview.tex in its output directory.
	if filepath.Clean(input) == "preview.tex" {
		if text, ok := projectLine(ctx, s.path, line); ok {
			return ProjectLocation{Path: s.mirror.sourceMain, Line: line, Text: text}
		}
		return ProjectLocation{}
	}
	paths := []string{input}
	if !filepath.IsAbs(input) {
		paths = []string{filepath.Join(s.dir, input), filepath.Join(filepath.Dir(s.path), input), filepath.Join(s.mirror.dir, input)}
	}
	var found ProjectLocation
	for _, path := range paths {
		path, err := filepath.EvalSymlinks(path)
		if err != nil {
			continue
		}
		original, source := "", path
		if path == filepath.Join(s.dir, "preview.tex") {
			original, source = s.mirror.sourceMain, s.path
		} else if name, err := filepath.Rel(s.mirror.dir, path); err == nil && filepath.IsLocal(name) && s.mirror.files[name].valid {
			original = filepath.Join(s.mirror.sourceRoot, name)
		}
		if !IsLaTeXPath(original) {
			continue
		}
		if found.Path != "" && found.Path != original {
			return ProjectLocation{}
		}
		text, ok := projectLine(ctx, source, line)
		if ok {
			found = ProjectLocation{Path: original, Line: line, Text: text}
		}
	}
	return found
}

func projectLine(ctx context.Context, path string, line int) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	scan := bufio.NewScanner(io.LimitReader(f, 8<<20))
	scan.Buffer(make([]byte, 4096), 1<<20)
	for n := 0; scan.Scan() && ctx.Err() == nil; n++ {
		if n == line {
			return strings.TrimPrefix(scan.Text(), "\ufeff"), true
		}
	}
	return "", false
}

package typeset

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
)

func (s *Session) trackPreviewFiles(compiler string) {
	bin, err := tool(compiler)
	if err != nil {
		return
	}
	s.previewFiles = make(map[string]os.FileInfo)
	s.previewPathBytes = 0
	if !s.recordPreviewFile(bin) {
		s.previewFiles = nil
	}
}

func (s *Session) recordPreviewFile(path string) bool {
	for _, root := range []string{s.dir, s.trialRoot} {
		if root != "" {
			if rel, err := filepath.Rel(root, path); err == nil && filepath.IsLocal(rel) {
				return true
			}
		}
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if prior := s.previewFiles[path]; prior != nil {
		return samePreviewFile(prior, info)
	}
	if len(s.previewFiles) >= 4096 || s.previewPathBytes+len(path) > 1<<20 {
		return false
	}
	s.previewPathBytes += len(path)
	s.previewFiles[path] = info
	return true
}

func samePreviewFile(prior, current os.FileInfo) bool {
	return os.SameFile(prior, current) && prior.Size() == current.Size() && prior.ModTime().Equal(current.ModTime())
}

func (s *Session) recordTeXInputs(ctx context.Context, dir, job string) bool {
	f, err := os.Open(filepath.Join(dir, job+".fls"))
	if err != nil {
		return false
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || info.Size() > 4<<20 {
		return false
	}
	seenInput := false
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		if ctx.Err() != nil {
			return false
		}
		path, ok := strings.CutPrefix(scan.Text(), "INPUT ")
		if !ok {
			continue
		}
		seenInput = true
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		if !s.recordPreviewFile(filepath.Clean(path)) {
			return false
		}
	}
	return seenInput && scan.Err() == nil
}

// PreviewInputsCurrent checks the last successful compiler inputs without
// changing the private mirror or starting another compiler.
func (s *Session) PreviewInputsCurrent(ctx context.Context, buffers map[string]ProjectBuffer) bool {
	if s.previewFiles == nil {
		return false
	}
	if m := s.mirror; m != nil {
		root, rootErr := filepath.EvalSymlinks(m.sourceRoot)
		main, mainErr := filepath.EvalSymlinks(m.sourceMain)
		if m.refresh || rootErr != nil || mainErr != nil || root != m.root || main != m.main {
			return false
		}
		files, _, err := m.scanDisk(ctx)
		if err != nil || len(files) != len(m.files) {
			return false
		}
		for name, info := range files {
			buffer, overlaid := buffers[name]
			if !m.files[name].matches(info, buffer, overlaid) {
				return false
			}
		}
	}
	for path, prior := range s.previewFiles {
		if ctx.Err() != nil {
			return false
		}
		info, err := os.Stat(path)
		if err != nil || !samePreviewFile(prior, info) {
			return false
		}
	}
	return ctx.Err() == nil
}

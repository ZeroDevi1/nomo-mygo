package shell

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
)

type scholarFileSearch struct {
	query      string
	paths      []string
	ready      bool
	cancel     context.CancelFunc
	generation uint64
}

func (s *Shell) scholarSearchFiles(c *ui.Context, p *scholarState, query string) []string {
	v := &p.navigation.files
	if v.ready && v.query == query {
		return v.paths
	}
	if v.cancel != nil {
		v.cancel()
	}
	v.query, v.paths, v.ready = query, nil, true
	v.generation++
	generation := v.generation
	ctx, cancel := context.WithCancel(p.ctx)
	v.cancel = cancel
	root, main := p.project.Root, p.project.Main
	go func() {
		defer cancel()
		var paths []string
		lower := strings.ToLower(strings.TrimSpace(query))
		if strings.Contains(strings.ToLower(filepath.Base(main)), lower) {
			paths = append(paths, main)
		}
		inspected := 0
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if ctx.Err() != nil || len(paths) >= 50 || inspected >= 20000 {
				return fs.SkipAll
			}
			if d.IsDir() {
				switch d.Name() {
				case ".git", ".agents", "node_modules":
					return filepath.SkipDir
				}
				return nil
			}
			inspected++
			if d.Type()&fs.ModeSymlink != 0 || path == main {
				return nil
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".tex", ".ltx", ".typ", ".bib", ".yaml", ".yml", ".json":
			default:
				return nil
			}
			name, _ := filepath.Rel(root, path)
			if strings.Contains(strings.ToLower(name), lower) {
				paths = append(paths, path)
			}
			return nil
		})
		s.post(func() {
			if s.scholar == p && v.generation == generation {
				v.paths, v.cancel = paths, nil
			}
		})
	}()
	return nil
}

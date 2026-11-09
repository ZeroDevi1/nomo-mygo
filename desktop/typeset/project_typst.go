package typeset

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func (s *Session) compileTypstProject(ctx context.Context, cwd, pdf string) (string, error) {
	s.sourceDeps = nil
	args := []string{"compile", "--root", s.trialRoot}
	deps := filepath.Join(s.dir, "typst-deps.json")
	if s.liveProject && !s.depsUnsupported {
		os.Remove(deps)
		s.trackPreviewFiles("typst")
		log, err := run(ctx, cwd, "", nil, "typst", append(args, "--deps", deps, "--deps-format", "json", s.path, pdf)...)
		if err == nil {
			s.sourceDeps = s.readTypstDependencies(cwd, deps)
			return log, nil
		}
		s.previewFiles = nil
		// Older CLI releases can still compile the project without a source list.
		message := err.Error()
		if ctx.Err() != nil || (!strings.Contains(message, "unexpected argument '--deps") && !strings.Contains(message, "Found argument '--deps")) {
			return log, err
		}
		s.depsUnsupported = true
	}
	return run(ctx, cwd, "", nil, "typst", append(args, s.path, pdf)...)
}

func (s *Session) readTypstDependencies(cwd, path string) []string {
	var recorded bool
	defer func() {
		if !recorded {
			s.previewFiles = nil
		}
	}()
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	const limit = 2 << 20
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(raw) > limit {
		return nil
	}
	var deps struct{ Inputs []string }
	if json.Unmarshal(raw, &deps) != nil {
		return nil
	}
	recorded = len(deps.Inputs) > 0 && s.previewFiles != nil
	inputs := []string{s.path}
	inputs = append(inputs, deps.Inputs...)
	seen := make(map[string]bool)
	var result []string
	for _, input := range inputs {
		if !filepath.IsAbs(input) {
			input = filepath.Join(cwd, input)
		}
		if recorded && !s.recordPreviewFile(filepath.Clean(input)) {
			recorded = false
		}
		if !strings.EqualFold(filepath.Ext(input), ".typ") {
			continue
		}
		rel, err := filepath.Rel(s.trialRoot, input)
		if err == nil && filepath.IsLocal(rel) && s.mirror.files[rel].valid && !seen[rel] {
			seen[rel] = true
			result = append(result, rel)
		}
	}
	return result
}

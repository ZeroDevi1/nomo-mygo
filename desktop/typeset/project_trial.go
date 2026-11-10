package typeset

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
)

const trialProjectBytes = 512 << 20

// OpenProjectTrial copies inputs to a private tree. No hardlinks or external
// symlinks are admitted, so compiler writes cannot reach the actual project.
// The returned session owns both the copied tree and its output directory.
func OpenProjectTrial(ctx context.Context, root, main string, overlays map[string]io.Reader) (*Session, error) {
	buffers := make(map[string]ProjectBuffer, len(overlays))
	for name, r := range overlays {
		buffers[name] = ProjectBuffer{Reader: r}
	}
	return openProjectSession(ctx, root, main, buffers)
}

// CompileProjectTrial compiles the private main input without publishing a PDF.
func (s *Session) CompileProjectTrial(ctx context.Context) (int, error) {
	if s.trialRoot == "" {
		return 0, errors.New("not an isolated project trial")
	}
	f, err := os.Open(s.path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return s.compile(ctx, f, false, true)
}

func (s *Session) TrialRoot() string { return strings.Clone(s.trialRoot) }

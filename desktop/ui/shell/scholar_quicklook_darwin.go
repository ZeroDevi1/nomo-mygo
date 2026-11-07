package shell

import (
	"context"
	"io"
	"os/exec"

	"nomo/desktop/internal/childproc"
)

func scholarQuickLook(ctx context.Context, path string) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/qlmanage", "-p", path)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cleanup, err := childproc.Start(cmd)
	if err != nil {
		return err
	}
	defer cleanup()
	return cmd.Wait()
}

package typeset

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"nomo/desktop/internal/childproc"
)

// 保留诊断末尾（TeX 通常在那里报告出错行），长日志不随文档大小常驻内存。
type previewLog []byte

func (l *previewLog) Write(b []byte) (int, error) {
	const limit = 8192
	n := len(b)
	if n >= limit {
		*l = append((*l)[:0], b[n-limit:]...)
	} else {
		drop := max(0, len(*l)+n-limit)
		*l = append((*l)[drop:], b...)
	}
	return n, nil
}

func run(parent context.Context, dir, input string, env []string, name string, args ...string) (string, error) {
	return runReader(parent, dir, strings.NewReader(input), env, name, args...)
}
func runReader(parent context.Context, dir string, input io.Reader, env []string, name string, args ...string) (string, error) {
	return runReaderOutput(parent, dir, input, env, nil, name, args...)
}

func runReaderOutput(parent context.Context, dir string, input io.Reader, env []string, stdout io.Writer, name string, args ...string) (string, error) {
	path, err := tool(name)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	cmd.Stdin = input
	cmd.Env = append(cmd.Environ(), env...)
	if runtime.GOOS == "darwin" {
		cmd.Env = append(cmd.Env, "PATH="+os.Getenv("PATH")+":/Library/TeX/texbin:/opt/homebrew/bin:/usr/local/bin")
	}
	var log previewLog
	if stdout == nil {
		stdout = &log
	}
	cmd.Stdout = stdout
	cmd.Stderr = &log
	cmd.WaitDelay = time.Second
	cleanup, err := childproc.Start(cmd)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	defer cleanup()
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return "", fmt.Errorf("%s: %w\n%s", name, err, log)
	}
	return string(log), nil
}

func tool(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil && runtime.GOOS == "darwin" {
		for _, folder := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/Library/TeX/texbin"} {
			if path, err = exec.LookPath(filepath.Join(folder, name)); err == nil {
				break
			}
		}
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return path, nil
}

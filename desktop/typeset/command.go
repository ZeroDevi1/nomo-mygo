package typeset

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo"

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
	if name == "typst" {
		args = typstArgs(path, args)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	cmd.Stdin = input
	cmd.Env = append(cmd.Environ(), env...)
	if dir := filepath.Dir(path); isLaTeXTool(name) && path == managedLatexTool(name) {
		// XeTeX starts xdvipdfmx by name. Only the compiler child sees this PATH.
		cmd.Env = append(cmd.Env, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	} else if runtime.GOOS == "darwin" {
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

func isLaTeXTool(name string) bool {
	switch name {
	case "xelatex", "biber", "bibtex", "synctex", "kpsewhich":
		return true
	}
	return false
}

func executableAt(dir, name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path, err := exec.LookPath(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return path
}

func bundledTypst() string {
	dir, err := mygo.App.Path(mygo.PathResources)
	if err != nil {
		return ""
	}
	return executableAt(dir, "typst")
}

func typstArgs(bin string, args []string) []string {
	if len(args) == 0 || (args[0] != "compile" && args[0] != "watch" && args[0] != "eval") {
		return args
	}
	fonts := filepath.Join(filepath.Dir(bin), "typeset-fonts")
	if info, err := os.Stat(fonts); err == nil && info.IsDir() {
		return append([]string{args[0], "--font-path", fonts}, args[1:]...)
	}
	return args
}

func tool(name string) (string, error) {
	if name == "typst" {
		if path := bundledTypst(); path != "" {
			return path, nil
		}
		if mygo.App.IsPackaged() {
			return "", errors.New("bundled Typst is unavailable")
		}
	}
	if isLaTeXTool(name) {
		if path := managedLatexTool(name); path != "" {
			return path, nil
		}
		if mygo.App.IsPackaged() && latexManagementSupported() {
			return "", fmt.Errorf("managed LaTeX: %s is unavailable", name)
		}
	}
	return systemTool(name)
}

func systemTool(name string) (string, error) {
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

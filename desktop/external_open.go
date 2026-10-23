package main

import (
	"os"
	"path/filepath"
	"strings"

	"nomo/desktop/internal/docfs"
)

// 外部打开：从命令行参数、系统文件关联与单实例转发的参数中识别要打开的文档与文件夹，交给原生外壳
// （native.go 的 deliver）。macOS 打开文件事件直接给出路径，不经这里。

type externalOpenTargets struct {
	markdownPaths []string
	folderPaths   []string
}

// collectExternalOpenTargets 从参数中识别受支持的文档与文件夹（去重、忽略选项与可执行文件自身）。
func collectExternalOpenTargets(args []string, workingDir string) externalOpenTargets {
	exe, _ := os.Executable()
	seen := map[string]bool{}
	var targets externalOpenTargets
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") || arg == "" {
			continue
		}
		absolute := arg
		if !filepath.IsAbs(absolute) && workingDir != "" {
			absolute = filepath.Join(workingDir, absolute)
		}
		if exe != "" && samePath(absolute, exe) {
			continue
		}
		info, err := os.Stat(absolute)
		if err != nil {
			continue
		}
		normalized := normalizeDisplayPath(absolute)
		if seen[normalized] {
			continue
		}
		switch {
		case info.Mode().IsRegular() && docfs.IsSupportedDocument(absolute):
			seen[normalized] = true
			targets.markdownPaths = append(targets.markdownPaths, normalized)
		case info.IsDir():
			seen[normalized] = true
			targets.folderPaths = append(targets.folderPaths, normalized)
		}
	}
	return targets
}

func samePath(left, right string) bool {
	return normalizeDisplayPath(left) == normalizeDisplayPath(right)
}

// normalizeDisplayPath 解析符号链接并去掉 Windows 扩展长度前缀，得到便于展示的绝对路径。
func normalizeDisplayPath(path string) string {
	resolved := path
	if absolute, err := filepath.Abs(path); err == nil {
		resolved = absolute
	}
	if real, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = real
	}
	if rest, ok := strings.CutPrefix(resolved, `\\?\UNC\`); ok {
		return `\\` + rest
	}
	return strings.TrimPrefix(resolved, `\\?\`)
}

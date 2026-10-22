package docfs

import (
	"os"
	"runtime"

	"nomo/desktop/internal/fsutil"
)

func replaceFile(source, target string) error { return fsutil.ReplaceFile(source, target) }

// syncDir 在 macOS / Linux 上 fsync 目录，使 rename 本身也落盘；Windows 不支持也不需要。
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	if handle, err := os.Open(dir); err == nil {
		_ = handle.Sync()
		handle.Close()
	}
}

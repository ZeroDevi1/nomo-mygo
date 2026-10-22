package textdoc

import "golang.org/x/sys/unix"

// cloneFileCOW 用 APFS clonefile 建立写时复制副本，常数时间且不占额外空间。
func cloneFileCOW(source, target string) error {
	return unix.Clonefile(source, target, 0)
}

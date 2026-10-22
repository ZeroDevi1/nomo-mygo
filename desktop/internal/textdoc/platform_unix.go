//go:build unix

package textdoc

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// syncParentDir fsync 目标所在目录，使新建与 rename 的目录项落盘。
func syncParentDir(path string) error {
	return syncDir(filepath.Dir(path))
}

func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

func replaceFile(source, target string) error { return os.Rename(source, target) }

// platformFileID 用 dev:ino 识别同路径上的原子替换（长度与 mtime 可被外部工具原样保留）。
func platformFileID(file *os.File, info os.FileInfo) (*string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, nil
	}
	id := fmt.Sprintf("unix:%d:%d", uint64(stat.Dev), stat.Ino)
	return &id, nil
}

// platformChangeStamp 用 ctime 识别同 inode 的原地改写：普通写入无法把 ctime 恢复原值。
func platformChangeStamp(file *os.File, info os.FileInfo) (*string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, nil
	}
	sec, nsec := statCtime(stat)
	stamp := fmt.Sprintf("unix:%d:%d", sec, nsec)
	return &stamp, nil
}

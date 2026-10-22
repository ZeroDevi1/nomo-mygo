// Package fsutil 收拢桌面端共用的文件写入原语。
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileSynced 写入并 fsync 文件，保证随后的 rename 替换到磁盘上的是完整内容。
func WriteFileSynced(path string, data []byte) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// ReplaceFile 用 source 原子替换 target。
//
// 同目录替换不能先删除旧文件：进程中断时磁盘上至少保留完整的旧内容或新内容。
// Windows 下使用 MoveFileExW(REPLACE_EXISTING|WRITE_THROUGH)，其他平台 rename(2) 本身即原子。
func ReplaceFile(source, target string) error {
	return replaceFile(source, target)
}

// WriteFileAtomic 先写同目录临时文件（target + suffix）并同步，再替换目标文件。
func WriteFileAtomic(target string, data []byte, tempSuffix string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("创建目录失败：%w", err)
	}
	temp := target + tempSuffix
	if err := WriteFileSynced(temp, data); err != nil {
		_ = os.Remove(temp)
		return err
	}
	if err := ReplaceFile(temp, target); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}

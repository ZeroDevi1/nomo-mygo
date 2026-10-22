//go:build windows

package textdoc

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"

	"nomo/desktop/internal/fsutil"
)

// Windows 不支持也不需要 fsync 目录。
func syncParentDir(path string) error { return nil }

func syncDir(dir string) error { return nil }

func replaceFile(source, target string) error { return fsutil.ReplaceFile(source, target) }

// platformFileID 用卷序列号与文件索引识别同路径上的原子替换。
func platformFileID(file *os.File, info os.FileInfo) (*string, error) {
	var data windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &data); err != nil {
		return nil, newErrorf("file-identity-failed", "读取 Windows 文件 ID 失败：%v", err)
	}
	index := uint64(data.FileIndexHigh)<<32 | uint64(data.FileIndexLow)
	id := fmt.Sprintf("windows:%d:%d", data.VolumeSerialNumber, index)
	return &id, nil
}

// fileBasicInfo 对应 Win32 FILE_BASIC_INFO。
type fileBasicInfo struct {
	CreationTime   int64
	LastAccessTime int64
	LastWriteTime  int64
	ChangeTime     int64
	FileAttributes uint32
	_              uint32
}

// platformChangeStamp 用 ChangeTime 识别同一文件的原地改写。
func platformChangeStamp(file *os.File, info os.FileInfo) (*string, error) {
	var data fileBasicInfo
	err := windows.GetFileInformationByHandleEx(windows.Handle(file.Fd()), windows.FileBasicInfo, (*byte)(unsafe.Pointer(&data)), uint32(unsafe.Sizeof(data)))
	if err != nil {
		return nil, newErrorf("file-identity-failed", "读取 Windows 文件 ChangeTime 失败：%v", err)
	}
	stamp := fmt.Sprintf("windows-change:%d", data.ChangeTime)
	return &stamp, nil
}

// Package docfs 实现整文档读写：编码探测（UTF-8 / BOM / UTF-16 BOM / GBK）、原子保存与文件状态。
//
// 大文档走 textdoc 的分段会话；这里只服务于一次性读入内存的普通文档、示例文档和导出文件。
package docfs

import (
	"fmt"
	"nomo/desktop/internal/textcodec"
	"nomo/desktop/providers"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Encoding = textcodec.Encoding

const (
	EncodingUTF8       = textcodec.EncodingUTF8
	EncodingUTF8BOM    = textcodec.EncodingUTF8BOM
	EncodingUTF16LEBOM = textcodec.EncodingUTF16LEBOM
	EncodingUTF16BEBOM = textcodec.EncodingUTF16BEBOM
	EncodingGBK        = textcodec.EncodingGBK
)

// DocumentPayload 是读写文档后返回给前端的内容与元数据；时间为 Unix 秒。
type DocumentPayload struct {
	Path       string   `json:"path"`
	FileName   string   `json:"file_name"`
	Markdown   string   `json:"markdown"`
	Encoding   Encoding `json:"encoding"`
	ModifiedAt int64    `json:"modified_at"`
	SizeBytes  int64    `json:"size_bytes"`
	Readonly   bool     `json:"readonly"`
}

// FileStatus 是路径的存在性与基本元数据；不存在时数值字段为 0。
type FileStatus struct {
	Path       string `json:"path"`
	Exists     bool   `json:"exists"`
	IsFile     bool   `json:"is_file"`
	ModifiedAt int64  `json:"modified_at"`
	SizeBytes  int64  `json:"size_bytes"`
	Readonly   bool   `json:"readonly"`
}

// ReadDocument 读取整份文档并探测编码。
func ReadDocument(path string) (DocumentPayload, error) {
	status := Stat(path)
	if !status.Exists {
		return DocumentPayload{}, fmt.Errorf("文件不存在：%s", path)
	}
	if !status.IsFile {
		return DocumentPayload{}, fmt.Errorf("路径不是文件：%s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return DocumentPayload{}, fmt.Errorf("读取 Markdown 文件失败：%w", err)
	}
	markdown, encoding, err := Decode(data)
	if err != nil {
		return DocumentPayload{}, err
	}
	return payload(path, markdown, encoding), nil
}

// DetectFileEncoding 返回已有文件的编码；文件不存在时 ok 为 false。
func DetectFileEncoding(path string) (encoding Encoding, ok bool, err error) {
	info, statErr := os.Stat(path)
	if statErr != nil || !info.Mode().IsRegular() {
		return "", false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, fmt.Errorf("读取原 Markdown 文件编码失败：%w", err)
	}
	_, encoding, err = Decode(data)
	if err != nil {
		return "", false, err
	}
	return encoding, true, nil
}

// WriteDocument 按 encoding（空值视为 UTF-8）编码并原子写入；保存目录必须已存在。
func WriteDocument(path, markdown string, encoding Encoding) (DocumentPayload, error) {
	if parent := filepath.Dir(path); parent != "" {
		if _, err := os.Stat(parent); err != nil {
			return DocumentPayload{}, fmt.Errorf("保存目录不存在：%s", parent)
		}
	}
	if encoding == "" {
		encoding = EncodingUTF8
	}
	data, err := Encode(markdown, encoding)
	if err != nil {
		return DocumentPayload{}, err
	}
	if err := WriteFileAtomically(path, data); err != nil {
		return DocumentPayload{}, err
	}
	return payload(path, markdown, encoding), nil
}

func payload(path, markdown string, encoding Encoding) DocumentPayload {
	name := filepath.Base(path)
	if name == "." || name == string(filepath.Separator) {
		name = "untitled.md"
	}
	status := Stat(path)
	return DocumentPayload{
		Path:       path,
		FileName:   name,
		Markdown:   markdown,
		Encoding:   encoding,
		ModifiedAt: status.ModifiedAt,
		SizeBytes:  status.SizeBytes,
		Readonly:   status.Readonly,
	}
}

func Decode(data []byte) (string, Encoding, error) { return textcodec.Decode(data) }
func Encode(markdown string, encoding Encoding) ([]byte, error) {
	return textcodec.Encode(markdown, encoding)
}

// WriteFileAtomically 在目标同目录创建唯一临时文件，写入并 fsync 后原子替换，最后同步父目录。
// 任一步失败都会删除临时文件，原文件保持不变。
func WriteFileAtomically(path string, data []byte) error {
	parent := filepath.Dir(path)
	if parent == "" {
		parent = "."
	}
	name := filepath.Base(path)
	if name == "" || name == "." {
		name = "document.md"
	}
	temp := filepath.Join(parent, "."+name+"."+strconv.Itoa(os.Getpid())+"."+strconv.FormatInt(time.Now().UnixNano(), 10)+".tmp")
	file, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("创建临时保存文件失败：%w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(temp)
		return fmt.Errorf("写入临时保存文件失败：%w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(temp)
		return fmt.Errorf("同步临时保存文件失败：%w", err)
	}
	file.Close()
	if err := replaceFile(temp, path); err != nil {
		os.Remove(temp)
		return fmt.Errorf("保存文件失败：%w", err)
	}
	syncDir(parent)
	return nil
}

// Stat 返回路径状态；任何读取错误都按“不存在 / 零值”处理。
func Stat(path string) FileStatus {
	status := FileStatus{Path: path}
	info, err := os.Stat(path)
	if err != nil {
		return status
	}
	status.Exists = true
	status.IsFile = info.Mode().IsRegular()
	status.ModifiedAt = info.ModTime().Unix()
	status.SizeBytes = info.Size()
	status.Readonly = info.Mode().Perm()&0o222 == 0
	return status
}

// ModifiedAt 返回文件修改时间（Unix 秒），失败时为 0。
func ModifiedAt(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.ModTime().Unix()
}

// IsSupportedDocument 判断扩展名是否属于可打开文档；文件夹树与外部目录入口共享此契约，
// 避免某个入口静默隐藏分段文档。
func IsSupportedDocument(path string) bool {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "md", "markdown", "txt", "json":
		return true
	}
	return providers.ForPath(path) != nil
}

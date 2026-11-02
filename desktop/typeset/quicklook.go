package typeset

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"nomo/desktop/internal/textcodec"
)

const quickLookBudget = 128 << 20

// Quick Look 只读取已保存主文件对应的成功排版。PDF 是上次构建的快照，外部图片、章节可能在之后改变。
func storeQuickLook(ctx context.Context, sourcePath, pdfPath string, compiled []byte) {
	if runtime.GOOS != "darwin" || ctx.Err() != nil {
		return
	}
	path, err := filepath.EvalSymlinks(sourcePath)
	if err != nil {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	// 非 UTF-8 或 CRLF 主文件在编辑器中已解码、统一换行。用原始字节作缓存键，
	// 仅在快照内容与磁盘正文一致时发布，避免暴露未保存的修改。
	defer f.Close()
	const sourceLimit = 16 << 20
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() || st.Size() > sourceLimit {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(f, sourceLimit+1))
	if err != nil || len(raw) > sourceLimit {
		return
	}
	disk := sha256.Sum256(raw)
	if !bytes.Equal(disk[:], compiled) {
		body, _, err := textcodec.Decode(raw)
		if err != nil {
			return
		}
		body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
		normalized := sha256.Sum256([]byte(body))
		if !bytes.Equal(normalized[:], compiled) {
			return
		}
	}
	root, err := os.UserCacheDir()
	if err != nil {
		return
	}
	dir := filepath.Join(root, "com.nomo.desktop", "QuickLookPDF")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return
	}
	file, err := os.Open(pdfPath)
	if err != nil {
		return
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil || st.Size() <= 0 || st.Size() > quickLookBudget {
		return
	}
	name := fmt.Sprintf("%x-%x.pdf", sha256.Sum256([]byte(path)), disk)
	tmp, err := os.CreateTemp(dir, ".preview-")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, file)
	closeErr := tmp.Close()
	if err != nil || closeErr != nil || ctx.Err() != nil {
		return
	}
	if os.Rename(tmp.Name(), filepath.Join(dir, name)) != nil {
		return
	}
	entries, _ := os.ReadDir(dir)
	var files []os.FileInfo
	var total int64
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".pdf") {
			continue
		}
		if info, err := entry.Info(); err == nil {
			total += info.Size()
			files = append(files, info)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].ModTime().Before(files[j].ModTime()) })
	for i, info := range files {
		if total <= quickLookBudget && len(files)-i <= 12 {
			break
		}
		if os.Remove(filepath.Join(dir, info.Name())) == nil {
			total -= info.Size()
		}
	}
}

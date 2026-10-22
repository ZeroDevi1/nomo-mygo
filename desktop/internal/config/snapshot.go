package config

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strconv"
)

// SnapshotLimit 是每个文档保留的保存前快照数量上限（超出时丢弃最旧的）。
const SnapshotLimit = 20

// SnapshotRecord 是带正文的文档快照（WebView 版 list_document_snapshots 的返回结构）。
type SnapshotRecord struct {
	ID           string `json:"id"`
	DocumentPath string `json:"document_path"`
	ContentHash  string `json:"content_hash"`
	Markdown     string `json:"markdown"`
	CreatedAt    int64  `json:"created_at"`
	Reason       string `json:"reason"`
}

// HashText 是正文内容的稳定短散列（FNV-64a），只用作存储文件名，不承担安全用途。
func HashText(text string) string {
	hasher := fnv.New64a()
	hasher.Write([]byte(text))
	return strconv.FormatUint(hasher.Sum64(), 16)
}

// WriteMarkdownStoreFile 把正文写入数据目录下的内容存储 dir/<id>.md；errorPrefix 用于错误文案。
func WriteMarkdownStoreFile(dir, id, markdown, errorPrefix string) error {
	path, err := MarkdownStorePath(dir, id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("%s：%w", errorPrefix, err)
	}
	if err := os.WriteFile(path, []byte(markdown), 0o644); err != nil {
		return fmt.Errorf("%s：%w", errorPrefix, err)
	}
	return nil
}

// ReadMarkdownStoreFile 读取 WriteMarkdownStoreFile 写入的正文。
func ReadMarkdownStoreFile(dir, id, errorPrefix string) (string, error) {
	path, err := MarkdownStorePath(dir, id)
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s：%w", errorPrefix, err)
	}
	return string(content), nil
}

// CreateSnapshot 为 path 记录一份正文快照（reason 如 before-save）：正文按内容散列存入
// snapshots/，记录写入配置并只保留最近 SnapshotLimit 份。两个外壳共用同一份快照。
func (m *Manager) CreateSnapshot(path, markdown, reason string) error {
	now := NowTS()
	contentHash := HashText(markdown)
	if err := WriteMarkdownStoreFile(filepath.Join(m.DataDir(), SnapshotsDirName), contentHash, markdown, "写入快照正文失败"); err != nil {
		return err
	}
	record := StoredSnapshotRecord{
		ID:           fmt.Sprintf("%d-%s", now, contentHash),
		DocumentPath: path,
		ContentHash:  contentHash,
		CreatedAt:    now,
		Reason:       reason,
	}
	return m.Update(func(c *AppConfig) {
		records := append([]StoredSnapshotRecord{record}, c.Snapshots.Documents[record.DocumentPath]...)
		if len(records) > SnapshotLimit {
			records = records[:SnapshotLimit]
		}
		c.Snapshots.Documents[record.DocumentPath] = records
	})
}

// ListSnapshots 返回 path 的快照（新的在前），并读出各自正文。
func (m *Manager) ListSnapshots(path string) ([]SnapshotRecord, error) {
	stored := m.Get().Snapshots.Documents[path]
	if len(stored) > SnapshotLimit {
		stored = stored[:SnapshotLimit]
	}
	dir := filepath.Join(m.DataDir(), SnapshotsDirName)
	records := []SnapshotRecord{}
	for _, item := range stored {
		markdown, err := ReadMarkdownStoreFile(dir, item.ContentHash, "读取快照正文失败")
		if err != nil {
			return nil, err
		}
		records = append(records, SnapshotRecord{
			ID: item.ID, DocumentPath: item.DocumentPath, ContentHash: item.ContentHash,
			Markdown: markdown, CreatedAt: item.CreatedAt, Reason: item.Reason,
		})
	}
	return records, nil
}

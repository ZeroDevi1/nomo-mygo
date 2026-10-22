// Package config 管理应用数据目录下的 config.json：设置、最近文件、窗口与工作区状态、快照索引。
//
// 文件格式与 Tauri 版本保持字节级兼容（字段名沿用 snake_case），升级后的用户数据可直接读取。
// Manager 在启动时创建一次，进程内共享，所有方法并发安全。
package config

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/fsutil"
)

const (
	FileName         = "config.json"
	DraftsDirName    = "drafts"
	SnapshotsDirName = "snapshots"
)

// AppConfig 是 config.json 的完整结构；缺失的分区按零值补齐。
type AppConfig struct {
	App       AppSection      `json:"app"`
	Editor    ValueSection    `json:"editor"`
	Window    ValueSection    `json:"window"`
	Recent    RecentSection   `json:"recent"`
	Workspace ValueSection    `json:"workspace"`
	Snapshots SnapshotSection `json:"snapshots"`
}

type AppSection struct {
	Settings map[string]SettingRecord `json:"settings"`
}

// ValueSection 保存按设置键路由出的 JSON 值；值以原始字节保存且只整体替换，不原地修改。
type ValueSection struct {
	Settings map[string]jsontext.Value `json:"settings"`
}

type RecentSection struct {
	Entries []RecentEntry `json:"entries"`
}

type SnapshotSection struct {
	Documents map[string][]StoredSnapshotRecord `json:"documents"`
}

// SettingRecord 是一条应用设置；ValueJSON 为前端序列化后的 JSON 文本。
type SettingRecord struct {
	Key       string `json:"key"`
	ValueJSON string `json:"value_json"`
	UpdatedAt int64  `json:"updated_at"`
}

// RecentEntry 是最近打开的文件或文件夹；时间戳为 Unix 秒。
type RecentEntry struct {
	Path       string  `json:"path"`
	EntryType  string  `json:"entry_type"`
	Title      *string `json:"title"`
	ModifiedAt int64   `json:"modified_at"`
	WordCount  int64   `json:"word_count"`
	OpenedAt   int64   `json:"opened_at"`
}

// StoredSnapshotRecord 是快照索引项，正文存放在 snapshots/<content_hash>.md。
// Markdown 仅用于读取旧版本内联正文并迁移，写回时总为空。
type StoredSnapshotRecord struct {
	ID           string  `json:"id"`
	DocumentPath string  `json:"document_path"`
	ContentHash  string  `json:"content_hash"`
	CreatedAt    int64   `json:"created_at"`
	Reason       string  `json:"reason"`
	Markdown     *string `json:"markdown,omitzero"`
}

func (c *AppConfig) normalize() {
	if c.App.Settings == nil {
		c.App.Settings = map[string]SettingRecord{}
	}
	if c.Editor.Settings == nil {
		c.Editor.Settings = map[string]jsontext.Value{}
	}
	if c.Window.Settings == nil {
		c.Window.Settings = map[string]jsontext.Value{}
	}
	if c.Workspace.Settings == nil {
		c.Workspace.Settings = map[string]jsontext.Value{}
	}
	if c.Recent.Entries == nil {
		c.Recent.Entries = []RecentEntry{}
	}
	if c.Snapshots.Documents == nil {
		c.Snapshots.Documents = map[string][]StoredSnapshotRecord{}
	}
}

// Clone 返回可独立修改的副本；JSON 值字节被共享，因为它们只会被整体替换。
func (c *AppConfig) Clone() AppConfig {
	next := AppConfig{
		App:       AppSection{Settings: maps.Clone(c.App.Settings)},
		Editor:    ValueSection{Settings: maps.Clone(c.Editor.Settings)},
		Window:    ValueSection{Settings: maps.Clone(c.Window.Settings)},
		Recent:    RecentSection{Entries: slices.Clone(c.Recent.Entries)},
		Workspace: ValueSection{Settings: maps.Clone(c.Workspace.Settings)},
		Snapshots: SnapshotSection{Documents: make(map[string][]StoredSnapshotRecord, len(c.Snapshots.Documents))},
	}
	for key, records := range c.Snapshots.Documents {
		next.Snapshots.Documents[key] = slices.Clone(records)
	}
	next.normalize()
	return next
}

// Manager 持有内存中的配置与其磁盘路径。
type Manager struct {
	path   string
	mu     sync.RWMutex
	config AppConfig
}

// Load 读取配置；文件不存在时使用默认值，文件损坏时备份为 config.broken.<ts>.json 后使用默认值。
// 返回前会把规范化后的配置写回磁盘。
func Load(path string) (*Manager, error) {
	var config AppConfig
	content, err := os.ReadFile(path)
	switch {
	case err == nil:
		if decodeErr := json.Unmarshal(content, &config); decodeErr != nil {
			if err := backupBrokenConfig(path); err != nil {
				return nil, err
			}
			applog.Error("Config", "配置文件损坏，已备份并使用默认配置："+decodeErr.Error())
			config = AppConfig{}
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		return nil, fmt.Errorf("读取配置文件失败：%w", err)
	}
	config.normalize()
	if err := migrateLegacySnapshotMarkdown(path, &config); err != nil {
		return nil, err
	}
	manager := &Manager{path: path, config: config}
	if err := manager.save(&manager.config); err != nil {
		return nil, err
	}
	return manager, nil
}

// Path 返回 config.json 的绝对路径。
func (m *Manager) Path() string { return m.path }

// DataDir 返回 config.json 所在的应用数据目录。
func (m *Manager) DataDir() string { return filepath.Dir(m.path) }

// Get 返回当前配置的独立副本。
func (m *Manager) Get() AppConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config.Clone()
}

// Read 在读锁内把配置交给 fn；fn 不得修改或保留其中的切片与映射。
func (m *Manager) Read(fn func(config *AppConfig)) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	fn(&m.config)
}

// Update 在副本上执行 updater 并原子写盘，写盘成功后才替换内存状态。
//
// 写锁覆盖磁盘提交：多窗口并发更新不会用较旧副本覆盖较新的磁盘内容；
// 写盘失败时内存保持旧值，不留下“已保存”的假状态。
func (m *Manager) Update(updater func(config *AppConfig)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.config.Clone()
	updater(&next)
	next.normalize()
	if err := m.save(&next); err != nil {
		return err
	}
	m.config = next
	return nil
}

// Setting 返回设置键对应的 JSON 文本；不存在时 ok 为 false。
func (m *Manager) Setting(key string) (value string, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	record, ok := m.config.App.Settings[key]
	return record.ValueJSON, ok
}

// PutSetting 写一条设置（调用方已持有 Update 回调），时间戳取当前时间。
func PutSetting(c *AppConfig, key, valueJSON string) { PutSettingAt(c, key, valueJSON, NowTS()) }

// PutSettingAt 写一条设置：记录到 App.Settings，并按键路由一份到窗口、工作区或编辑器分区，
// 供按分区读取的旧逻辑使用。WebView 外壳与原生外壳共用此规则。
func PutSettingAt(c *AppConfig, key, valueJSON string, now int64) {
	value := jsontext.Value(valueJSON)
	if !value.IsValid() {
		value = jsontext.Value("null")
	}
	switch {
	case strings.HasPrefix(key, "windowState:"):
		c.Window.Settings[strings.TrimPrefix(key, "windowState:")] = value
	case strings.HasPrefix(key, "workspaceTabs:"):
		c.Workspace.Settings[strings.TrimPrefix(key, "workspaceTabs:")] = value
	case strings.HasPrefix(key, "pendingFolder:"), strings.HasPrefix(key, "pendingExternalOpen:"):
		c.Window.Settings[key] = value
	case isEditorSettingKey(key):
		c.Editor.Settings[key] = value
	}
	c.App.Settings[key] = SettingRecord{Key: key, ValueJSON: valueJSON, UpdatedAt: now}
}

func isEditorSettingKey(key string) bool {
	switch key {
	case "fontSize", "lineHeight", "contentWidthPercent", "blockStyle", "editorMode", "autoSave", "sourceCodeFontSize", "sourceCodeLineHeight":
		return true
	}
	return false
}

func (m *Manager) save(config *AppConfig) error {
	content, err := json.Marshal(config, json.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		return fmt.Errorf("序列化配置失败：%w", err)
	}
	if err := fsutil.WriteFileAtomic(m.path, content, ".tmp"); err != nil {
		return fmt.Errorf("写入配置文件失败：%w", err)
	}
	return nil
}

func backupBrokenConfig(path string) error {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	backup := filepath.Join(filepath.Dir(path), "config.broken."+strconv.FormatInt(NowTS(), 10)+".json")
	if err := os.Rename(path, backup); err != nil {
		return fmt.Errorf("备份损坏配置失败：%w", err)
	}
	return nil
}

func migrateLegacySnapshotMarkdown(configPath string, config *AppConfig) error {
	snapshotDir := filepath.Join(filepath.Dir(configPath), SnapshotsDirName)
	migrated := 0
	for key, records := range config.Snapshots.Documents {
		for index := range records {
			markdown := records[index].Markdown
			if markdown == nil {
				continue
			}
			records[index].Markdown = nil
			path, err := MarkdownStorePath(snapshotDir, records[index].ContentHash)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
				return fmt.Errorf("创建快照目录失败：%w", err)
			}
			if err := os.WriteFile(path, []byte(*markdown), 0o644); err != nil {
				return fmt.Errorf("迁移快照正文失败：%w", err)
			}
			migrated++
		}
		config.Snapshots.Documents[key] = records
	}
	if migrated > 0 {
		applog.Info("Config", fmt.Sprintf("已迁移旧快照正文：%d 个", migrated))
	}
	return nil
}

// NowTS 返回当前 Unix 秒。
func NowTS() int64 { return time.Now().Unix() }

// ValidateContentID 校验正文存储标识，只允许 1~96 位 ASCII 字母数字、- 与 _，防止路径穿越。
func ValidateContentID(id string) error {
	if id == "" || len(id) > 96 {
		return errors.New("正文存储标识非法")
	}
	for i := 0; i < len(id); i++ {
		b := id[i]
		if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_') {
			return errors.New("正文存储标识非法")
		}
	}
	return nil
}

// MarkdownStorePath 返回 root/<id>.md，id 非法时返回错误。
func MarkdownStorePath(root, id string) (string, error) {
	if err := ValidateContentID(id); err != nil {
		return "", err
	}
	return filepath.Join(root, id+".md"), nil
}

// ResolveAppDataDir 按应用标识符返回与 Tauri app_data_dir() 一致的应用数据目录，
// 以便沿用旧版本的配置、草稿与快照。
//
// macOS: ~/Library/Application Support/<id>；Windows: %APPDATA%\<id>；其他：~/.local/share/<id>。
func ResolveAppDataDir(identifier string) (string, error) {
	switch runtime.GOOS {
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return "", errors.New("无法定位 APPDATA 目录")
		}
		return filepath.Join(appData, identifier), nil
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", identifier), nil
	default:
		if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
			return filepath.Join(dataHome, identifier), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share", identifier), nil
	}
}

// ReadSettingFromDisk 在配置管理器创建前直接读取磁盘上的设置值（JSON 文本），用于启动早期开关。
func ReadSettingFromDisk(dataDir, key string) (string, bool) {
	content, err := os.ReadFile(filepath.Join(dataDir, FileName))
	if err != nil {
		return "", false
	}
	var config AppConfig
	if json.Unmarshal(content, &config) != nil {
		return "", false
	}
	record, ok := config.App.Settings[key]
	return record.ValueJSON, ok
}

// IsDeveloperMode 报告磁盘配置是否开启了开发者模式（决定启动阶段是否写日志）。
func IsDeveloperMode(dataDir string) bool {
	value, ok := ReadSettingFromDisk(dataDir, "developerMode")
	return ok && value == "true"
}

// IsSoftwareRenderMode 报告磁盘配置是否显式选择了软件渲染：WebView 版用于 Windows WebView2，原生版用于 MyGo 的 CPU 渲染。
func IsSoftwareRenderMode(dataDir string) bool {
	value, ok := ReadSettingFromDisk(dataDir, "renderMode")
	return ok && value == `"software"`
}

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/config"
	"nomo/desktop/internal/docfs"
)

// 最近列表、设置、快照、草稿与用户主题命令；数据全部落在 config.json 与应用数据目录。

type settingInput struct {
	Key       string `json:"key"`
	ValueJSON string `json:"value_json"`
}

type recentEntryInput struct {
	Path      string  `json:"path"`
	EntryType string  `json:"entry_type"`
	Title     *string `json:"title"`
	WordCount int64   `json:"word_count"`
}

type snapshotInput struct {
	Path     string `json:"path"`
	Markdown string `json:"markdown"`
	Reason   string `json:"reason"`
}

type workspaceDraftInput struct {
	DraftID  *string `json:"draft_id"`
	Markdown string  `json:"markdown"`
}

type workspaceDraftPayload struct {
	DraftID   string  `json:"draft_id"`
	Markdown  *string `json:"markdown,omitzero"`
	UpdatedAt int64   `json:"updated_at"`
}

// recentMenuLimit 是原生“打开最近”菜单展示的条数；列表命令返回 20 条。
const (
	recentListLimit = 20
	recentMenuLimit = 8
)

func init() {
	register("remember_recent_entry", func(_ *commandCall, args struct {
		Input recentEntryInput `json:"input"`
	}) (any, error) {
		return nil, rememberRecentEntry(args.Input)
	})
	register("list_recent_entries", func(_ *commandCall, _ noArgs) ([]config.RecentEntry, error) {
		return queryRecentEntries(recentListLimit), nil
	})
	register("clear_recent_entries", func(_ *commandCall, _ noArgs) (any, error) {
		if err := app.config.Update(func(c *config.AppConfig) { c.Recent.Entries = nil }); err != nil {
			return nil, err
		}
		notifyRecentChanged()
		return nil, nil
	})
	register("remove_recent_entry", func(_ *commandCall, args struct {
		Path string `json:"path"`
	}) (any, error) {
		err := app.config.Update(func(c *config.AppConfig) {
			kept := c.Recent.Entries[:0]
			for _, entry := range c.Recent.Entries {
				if !sameRecentPath(entry.Path, args.Path) {
					kept = append(kept, entry)
				}
			}
			c.Recent.Entries = kept
		})
		if err != nil {
			return nil, err
		}
		notifyRecentChanged()
		return nil, nil
	})
	register("create_document_snapshot", func(_ *commandCall, args struct {
		Input snapshotInput `json:"input"`
	}) (any, error) {
		return nil, app.config.CreateSnapshot(args.Input.Path, args.Input.Markdown, args.Input.Reason)
	})
	register("list_document_snapshots", func(_ *commandCall, args struct {
		Path string `json:"path"`
	}) ([]config.SnapshotRecord, error) {
		return app.config.ListSnapshots(args.Path)
	})
	register("write_workspace_draft", func(_ *commandCall, args struct {
		Input workspaceDraftInput `json:"input"`
	}) (*workspaceDraftPayload, error) {
		draftID := ""
		if args.Input.DraftID != nil && config.ValidateContentID(*args.Input.DraftID) == nil {
			draftID = *args.Input.DraftID
		} else {
			draftID = fmt.Sprintf("draft-%d-%s", config.NowTS(), config.HashText(args.Input.Markdown))
		}
		dir := contentStoreDir(config.DraftsDirName)
		if err := config.WriteMarkdownStoreFile(dir, draftID, args.Input.Markdown, "写入草稿失败"); err != nil {
			return nil, err
		}
		return &workspaceDraftPayload{DraftID: draftID, UpdatedAt: markdownStoreUpdatedAt(dir, draftID)}, nil
	})
	register("read_workspace_draft", func(_ *commandCall, args struct {
		DraftID string `json:"draftId"`
	}) (*workspaceDraftPayload, error) {
		dir := contentStoreDir(config.DraftsDirName)
		markdown, err := config.ReadMarkdownStoreFile(dir, args.DraftID, "读取草稿失败")
		if err != nil {
			return nil, err
		}
		return &workspaceDraftPayload{DraftID: args.DraftID, Markdown: &markdown, UpdatedAt: markdownStoreUpdatedAt(dir, args.DraftID)}, nil
	})
	register("delete_workspace_draft", func(_ *commandCall, args struct {
		DraftID string `json:"draftId"`
	}) (any, error) {
		path, err := config.MarkdownStorePath(contentStoreDir(config.DraftsDirName), args.DraftID)
		if err != nil {
			return nil, err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("删除草稿失败：%w", err)
		}
		return nil, nil
	})
	register("update_app_setting", func(_ *commandCall, args struct {
		Input settingInput `json:"input"`
	}) (any, error) {
		return nil, updateSettings([]settingInput{args.Input})
	})
	register("update_app_settings", func(_ *commandCall, args struct {
		Inputs []settingInput `json:"inputs"`
	}) (any, error) {
		return nil, updateSettings(args.Inputs)
	})
	register("list_app_settings", func(_ *commandCall, _ noArgs) ([]config.SettingRecord, error) {
		var records []config.SettingRecord
		app.config.Read(func(c *config.AppConfig) {
			keys := make([]string, 0, len(c.App.Settings))
			for key := range c.App.Settings {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				records = append(records, c.App.Settings[key])
			}
		})
		return records, nil
	})

	register("list_user_themes", func(_ *commandCall, _ noArgs) ([]config.UserThemeFile, error) {
		return config.ListUserThemes(contentStoreDir(config.ThemesDirName))
	})
	register("read_user_theme_source", func(_ *commandCall, args struct {
		Path string `json:"path"`
	}) (string, error) {
		if err := config.RequireJSONExtension(args.Path); err != nil {
			return "", err
		}
		return config.ReadThemeText(args.Path)
	})
	register("save_user_theme", func(_ *commandCall, args struct {
		ID      string `json:"id"`
		Content string `json:"content"`
	}) (any, error) {
		return nil, config.SaveUserTheme(contentStoreDir(config.ThemesDirName), args.ID, args.Content)
	})
	register("delete_user_theme", func(_ *commandCall, args struct {
		ID string `json:"id"`
	}) (any, error) {
		return nil, config.DeleteUserTheme(contentStoreDir(config.ThemesDirName), args.ID)
	})
	register("write_user_theme_template", func(_ *commandCall, args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}) (any, error) {
		if err := config.RequireJSONExtension(args.Path); err != nil {
			return nil, err
		}
		if err := config.RequireThemeContentSize(args.Content); err != nil {
			return nil, err
		}
		return nil, config.WriteThemeAtomically(args.Path, args.Content)
	})
}

func rememberRecentEntry(input recentEntryInput) error {
	if input.EntryType != "file" && input.EntryType != "folder" {
		return fmt.Errorf("未知的最近条目类型：%s", input.EntryType)
	}
	var modifiedAt int64
	if input.EntryType == "file" {
		modifiedAt = docfs.ModifiedAt(input.Path)
	}
	entry := config.RecentEntry{
		Path:       input.Path,
		EntryType:  input.EntryType,
		Title:      input.Title,
		ModifiedAt: modifiedAt,
		WordCount:  input.WordCount,
		OpenedAt:   config.NowTS(),
	}
	err := app.config.Update(func(c *config.AppConfig) {
		kept := []config.RecentEntry{entry}
		for _, item := range c.Recent.Entries {
			if !sameRecentPath(item.Path, entry.Path) {
				kept = append(kept, item)
			}
		}
		c.Recent.Entries = kept
	})
	if err != nil {
		return err
	}
	notifyRecentChanged()
	return nil
}

func sameRecentPath(left, right string) bool {
	leftKey, leftOK := normalizeTargetPath(left)
	rightKey, rightOK := normalizeTargetPath(right)
	if leftOK && rightOK {
		return leftKey == rightKey
	}
	return left == right
}

// notifyRecentChanged 通知页面刷新最近列表，并重建原生菜单中的“打开最近”。
func notifyRecentChanged() {
	broadcast("nomo://recent-entries-changed", nil)
	refreshApplicationMenu()
}

// queryRecentEntries 按打开时间倒序返回最多 limit 条最近条目。
func queryRecentEntries(limit int) []config.RecentEntry {
	entries := app.config.Get().Recent.Entries
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].OpenedAt > entries[j].OpenedAt })
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries
}

// updateSettings 写入设置并按键前缀同步到 window/workspace/editor 分区；
// 工作区快照经标签移交的归属守卫过滤，避免旧窗口覆盖移交后的状态。
func updateSettings(inputs []settingInput) error {
	if len(inputs) == 0 {
		return nil
	}
	now := config.NowTS()
	return withGuardedSettings(inputs, func(accepted []settingInput) error {
		return app.config.Update(func(c *config.AppConfig) {
			for _, input := range accepted {
				config.PutSettingAt(c, input.Key, input.ValueJSON, now)
			}
		})
	})
}

// putSettingRecord 直接写一条设置（调用方已持有 Update 回调）。
func putSettingRecord(c *config.AppConfig, key, valueJSON string) {
	config.PutSetting(c, key, valueJSON)
}

func markdownStoreUpdatedAt(dir, id string) int64 {
	path, err := config.MarkdownStorePath(dir, id)
	if err != nil {
		return 0
	}
	return docfs.ModifiedAt(path)
}

func logWarnf(tag, format string, args ...any) { applog.Warn(tag, fmt.Sprintf(format, args...)) }

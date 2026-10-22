package config

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFillsMissingSectionsWithDefaults(t *testing.T) {
	var config AppConfig
	if err := json.Unmarshal([]byte(`{"app":{}}`), &config); err != nil {
		t.Fatal(err)
	}
	config.normalize()
	if len(config.App.Settings) != 0 || len(config.Recent.Entries) != 0 || len(config.Snapshots.Documents) != 0 {
		t.Fatalf("unexpected config %+v", config)
	}
}

func TestCreatesDefaultConfigFileWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	manager, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if len(manager.Get().Recent.Entries) != 0 {
		t.Fatal("expected no recent entries")
	}
	// 写回的文件应保留 Tauri 版本的字段名，空集合写为 [] / {} 而不是 null。
	content, _ := os.ReadFile(path)
	if !strings.Contains(string(content), `"entries": []`) {
		t.Fatalf("unexpected content %s", content)
	}
}

func TestBacksUpBrokenConfigAndUsesDefaults(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, FileName)
	if err := os.WriteFile(path, []byte("{ broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(manager.Get().App.Settings) != 0 {
		t.Fatal("expected default settings")
	}
	entries, _ := os.ReadDir(root)
	found := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "config.broken.") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected backup of broken config")
	}
}

func TestMigratesLegacySnapshotMarkdownToContentFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, FileName)
	legacy := `{
	  "app": {},
	  "snapshots": {
	    "documents": {
	      "D:\\docs\\a.md": [
	        {
	          "id": "1-abc123",
	          "document_path": "D:\\docs\\a.md",
	          "content_hash": "abc123",
	          "markdown": "# legacy",
	          "created_at": 1,
	          "reason": "before-save"
	        }
	      ]
	    }
	  }
	}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	manager, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	records := manager.Get().Snapshots.Documents[`D:\docs\a.md`]
	if len(records) != 1 || records[0].Markdown != nil {
		t.Fatalf("unexpected records %+v", records)
	}
	content, err := os.ReadFile(filepath.Join(root, SnapshotsDirName, "abc123.md"))
	if err != nil || string(content) != "# legacy" {
		t.Fatalf("snapshot content %q err %v", content, err)
	}
	written, _ := os.ReadFile(path)
	if strings.Contains(string(written), "# legacy") {
		t.Fatal("inline markdown should not be written back")
	}
}

func TestUpdateKeepsMemoryWhenSaveFails(t *testing.T) {
	root := t.TempDir()
	manager, err := Load(filepath.Join(root, FileName))
	if err != nil {
		t.Fatal(err)
	}
	// 把配置路径指向一个无法创建的位置，模拟写盘失败。
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	manager.path = filepath.Join(blocker, FileName)
	err = manager.Update(func(config *AppConfig) {
		config.App.Settings["a"] = SettingRecord{Key: "a", ValueJSON: "1"}
	})
	if err == nil {
		t.Fatal("expected save failure")
	}
	if _, ok := manager.Setting("a"); ok {
		t.Fatal("memory must not change when save fails")
	}
}

func TestReadsSoftwareRenderModeFromDisk(t *testing.T) {
	root := t.TempDir()
	content := `{"app":{"settings":{"renderMode":{"key":"renderMode","value_json":"\"software\"","updated_at":1}}}}`
	if err := os.WriteFile(filepath.Join(root, FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if !IsSoftwareRenderMode(root) {
		t.Fatal("expected software render mode")
	}
	if IsDeveloperMode(root) {
		t.Fatal("developer mode should be off")
	}
}

func TestValidatesUserThemeIDs(t *testing.T) {
	for _, id := range []string{"user-ocean", "user-ocean-2"} {
		if err := ValidateUserThemeID(id); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	for _, id := range []string{"nomo-default", "user-", "user--a", "user-A", "user-a/b", "user-../x"} {
		if ValidateUserThemeID(id) == nil {
			t.Fatalf("%s should be invalid", id)
		}
	}
}

func TestSavesListsAndSkipsInvalidThemeFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ThemesDirName)
	themes, err := ListUserThemes(dir)
	if err != nil || len(themes) != 0 {
		t.Fatalf("themes %v err %v", themes, err)
	}
	for _, step := range []struct{ id, content string }{
		{"user-b", `{"b":1}`}, {"user-a", `{"a":1}`}, {"user-a", `{"a":2}`},
	} {
		if err := SaveUserTheme(dir, step.id, step.content); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(dir, "nomo-default.json"), []byte("{}"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	themes, err = ListUserThemes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(themes) != 2 || themes[0].ID != "user-a" || themes[1].ID != "user-b" || themes[0].Content != `{"a":2}` {
		t.Fatalf("unexpected themes %+v", themes)
	}
	if SaveUserTheme(dir, "../x", "{}") == nil {
		t.Fatal("expected invalid id error")
	}
}

func TestRejectsOversizedThemeAndNonJSONPaths(t *testing.T) {
	dir := t.TempDir()
	if SaveUserTheme(dir, "user-big", strings.Repeat("x", maxThemeFileBytes+1)) == nil {
		t.Fatal("expected size error")
	}
	if RequireJSONExtension("/tmp/theme.JSON") != nil {
		t.Fatal("JSON extension should be accepted")
	}
	if RequireJSONExtension("/tmp/theme.txt") == nil {
		t.Fatal("txt extension should be rejected")
	}
}

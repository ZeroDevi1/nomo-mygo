package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/fsutil"
)

// 用户自定义主题保存在应用数据目录的 themes/<id>.json。
// Go 侧只负责文件边界（ID 形状、扩展名、大小与数量上限、原子写入），
// 主题内容的结构与令牌校验由前端主题注册表完成，前端校验通过后才调用保存。
const (
	ThemesDirName = "themes"
	// maxThemeFileBytes：完整主题约 10 KB，上限留足注释与格式空间，同时拦截误选的大文件。
	maxThemeFileBytes = 256 * 1024
	// maxUserThemes：设置页一次性列出全部主题，上限防止目录被大量文件填充后拖慢启动。
	maxUserThemes = 64
	// userThemeIDPrefix 与前端 USER_THEME_ID_PATTERN 保持一致，避免与内置主题 ID 冲突。
	userThemeIDPrefix    = "user-"
	maxUserThemeIDLength = 64
)

// UserThemeFile 是一个用户主题的 ID 与原文。
type UserThemeFile struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

// ListUserThemes 列出目录中的用户主题，按 ID 排序；单个文件不可读、过大或文件名不合规时跳过。
func ListUserThemes(directory string) ([]UserThemeFile, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return []UserThemeFile{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取主题目录失败：%w", err)
	}
	themes := []UserThemeFile{}
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || ValidateUserThemeID(id) != nil {
			continue
		}
		content, err := ReadThemeText(filepath.Join(directory, entry.Name()))
		if err != nil {
			applog.Warn("Themes", fmt.Sprintf("跳过主题文件 %s：%v", id, err))
			continue
		}
		themes = append(themes, UserThemeFile{ID: id, Content: content})
	}
	sort.Slice(themes, func(i, j int) bool { return themes[i].ID < themes[j].ID })
	if len(themes) > maxUserThemes {
		applog.Warn("Themes", fmt.Sprintf("用户主题超过 %d 个，仅加载前 %d 个", maxUserThemes, maxUserThemes))
		themes = themes[:maxUserThemes]
	}
	return themes, nil
}

// SaveUserTheme 保存已通过前端校验的主题原文，同 ID 覆盖；新增时受数量上限约束。
func SaveUserTheme(directory, id, content string) error {
	path, err := userThemePath(directory, id)
	if err != nil {
		return err
	}
	if err := RequireThemeContentSize(content); err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		existing, err := ListUserThemes(directory)
		if err != nil {
			return err
		}
		if len(existing) >= maxUserThemes {
			return fmt.Errorf("最多保存 %d 个自定义主题", maxUserThemes)
		}
	}
	return WriteThemeAtomically(path, content)
}

// DeleteUserTheme 删除主题文件；文件不存在视为成功。
func DeleteUserTheme(directory, id string) error {
	path, err := userThemePath(directory, id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("删除主题失败：%w", err)
	}
	return nil
}

// ValidateUserThemeID 要求 ID 形如 user-<段>[-<段>...]，每段为非空小写字母或数字。
func ValidateUserThemeID(id string) error {
	rest, ok := strings.CutPrefix(id, userThemeIDPrefix)
	if !ok {
		return errors.New("主题 ID 必须以 user- 开头")
	}
	valid := len(id) <= maxUserThemeIDLength && rest != ""
	if valid {
		for _, part := range strings.Split(rest, "-") {
			if part == "" {
				valid = false
				break
			}
			for i := 0; i < len(part); i++ {
				if !(part[i] >= 'a' && part[i] <= 'z' || part[i] >= '0' && part[i] <= '9') {
					valid = false
					break
				}
			}
		}
	}
	if !valid {
		return errors.New("主题 ID 只能包含小写字母、数字和单个连字符")
	}
	return nil
}

func userThemePath(directory, id string) (string, error) {
	if err := ValidateUserThemeID(id); err != nil {
		return "", err
	}
	return filepath.Join(directory, id+".json"), nil
}

// RequireJSONExtension 要求路径扩展名为 .json（不区分大小写）。
func RequireJSONExtension(path string) error {
	if strings.EqualFold(filepath.Ext(path), ".json") {
		return nil
	}
	return errors.New("主题文件必须是 .json 文件")
}

// RequireThemeContentSize 拒绝超过 256 KB 的主题原文。
func RequireThemeContentSize(content string) error {
	if len(content) > maxThemeFileBytes {
		return errors.New("主题文件过大（上限 256 KB）")
	}
	return nil
}

// ReadThemeText 读取不超过 256 KB 的主题文件原文。
func ReadThemeText(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("读取主题文件失败：%w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("主题路径不是文件")
	}
	if info.Size() > maxThemeFileBytes {
		return "", errors.New("主题文件过大（上限 256 KB）")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取主题文件失败：%w", err)
	}
	return string(content), nil
}

// WriteThemeAtomically 先写同目录临时文件再替换，避免写入中断留下半个 JSON。
func WriteThemeAtomically(path, content string) error {
	if err := fsutil.WriteFileAtomic(path, []byte(content), ".tmp"); err != nil {
		return fmt.Errorf("写入主题失败：%w", err)
	}
	return nil
}

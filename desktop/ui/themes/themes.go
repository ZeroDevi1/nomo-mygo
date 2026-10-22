// Package themes 是原生界面的颜色主题：内置主题由 scripts/generate-native-themes.mjs 从
// src/app/services/themeRegistry.ts 生成（builtin_gen.go），用户主题读写数据目录 themes/<id>.json，
// 解析、校验与导出规则与 src/app/services/userThemeDefinition.ts 一致，两个外壳的主题文件互通。
package themes

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

// Variant 是主题的明或暗变体：颜色令牌、样式令牌、代码高亮与 Mermaid 主题名。
type Variant struct {
	Tokens  map[string]string
	Style   map[string]string
	Shiki   string
	Mermaid string
}

// Theme 是一个颜色主题；Variants[0] 为浅色，[1] 为深色。
type Theme struct {
	ID, Version, Author, StyleProfile string
	Names                             map[string]string
	Builtin                           bool
	Variants                          [2]Variant
}

// Name 返回 locale 下的名称，缺失时依次回退英文、简体中文与 ID。
func (t *Theme) Name(locale string) string {
	for _, l := range []string{locale, "en-US", "zh-CN"} {
		if n := t.Names[l]; n != "" {
			return n
		}
	}
	return t.ID
}

// Builtin 返回内置主题（只读，调用方不应修改）。
func Builtin() []*Theme { return builtin }

// Find 在 list 中按 ID 查找主题，找不到返回默认主题。
func Find(list []*Theme, id string) *Theme {
	for _, t := range list {
		if t.ID == id {
			return t
		}
	}
	return builtin[0]
}

var (
	userIDPattern  = regexp.MustCompile(`^user-[a-z0-9]+(?:-[a-z0-9]+)*$`)
	lengthPattern  = regexp.MustCompile(`^(?:0|\d+(?:\.\d+)?(?:px|rem))$`)
	unsafePattern  = regexp.MustCompile(`[;{}\r\n]`)
	unsafeFunction = regexp.MustCompile(`(?i)url\s*\(|@import|!important`)
	lengthTokens   = map[string]bool{
		"radiusXs": true, "radiusSm": true, "radiusMd": true, "radiusLg": true, "radiusPill": true, "borderWidth": true,
		"spaceXs": true, "spaceSm": true, "spaceMd": true, "spaceLg": true, "spaceXl": true,
		"controlHeightSm": true, "controlHeightMd": true, "controlHeightLg": true,
	}
	styleProfiles = []string{"modern", "paper", "classic"}
)

// userFile 是用户主题 JSON 的结构（令牌可只写覆盖项，缺省项从 baseThemeId 补齐）。
type userFile struct {
	SchemaVersion  int                    `json:"schemaVersion"`
	ID             string                 `json:"id"`
	Version        string                 `json:"version,omitzero"`
	Author         string                 `json:"author,omitzero"`
	LocalizedNames map[string]string      `json:"localizedNames"`
	BaseThemeID    string                 `json:"baseThemeId,omitzero"`
	StyleProfile   string                 `json:"styleProfile,omitzero"`
	Variants       map[string]userVariant `json:"variants,omitzero"`
}

type userVariant struct {
	Tokens      map[string]string `json:"tokens,omitzero"`
	StyleTokens map[string]string `json:"styleTokens,omitzero"`
	ShikiTheme  string            `json:"shikiTheme,omitzero"`
	Mermaid     *struct {
		Theme string `json:"theme"`
	} `json:"mermaid,omitzero"`
}

// ParseUser 解析用户主题源文；结构不符、ID 非法、含未知令牌或令牌值不安全时返回错误，
// 规则与原版 parseUserTheme / validateThemeDefinition 一致。
func ParseUser(src []byte) (*Theme, error) {
	var f userFile
	if err := json.Unmarshal(src, &f); err != nil {
		return nil, fmt.Errorf("主题 JSON 无法解析：%w", err)
	}
	if f.SchemaVersion != 1 {
		return nil, errors.New("schemaVersion 必须为 1")
	}
	if len(f.ID) > 64 || !userIDPattern.MatchString(f.ID) {
		return nil, errors.New("id 必须以 user- 开头，只含小写字母、数字和单个连字符，且不超过 64 字符")
	}
	if len(f.LocalizedNames) == 0 || len(f.LocalizedNames) > 16 {
		return nil, errors.New("localizedNames 需要 1-16 个名称")
	}
	baseID := f.BaseThemeID
	if baseID == "" {
		baseID = DefaultThemeID
	}
	base := Find(builtin, baseID)
	if base.ID != baseID {
		return nil, fmt.Errorf("baseThemeId 必须是内置主题：%s", baseID)
	}
	t := &Theme{ID: f.ID, Version: or(f.Version, "1.0.0"), Author: or(f.Author, "User"), StyleProfile: or(f.StyleProfile, base.StyleProfile),
		Names: maps.Clone(f.LocalizedNames)}
	if !slices.Contains(styleProfiles, t.StyleProfile) {
		return nil, fmt.Errorf("不支持的主题样式档案：%s", t.StyleProfile)
	}
	for key := range f.Variants {
		if key != "light" && key != "dark" {
			return nil, fmt.Errorf("未知变体：variants.%s", key)
		}
	}
	for i, scheme := range []string{"light", "dark"} {
		src := f.Variants[scheme]
		bv := base.Variants[i]
		v := Variant{Tokens: maps.Clone(bv.Tokens), Style: maps.Clone(bv.Style), Shiki: or(src.ShikiTheme, bv.Shiki), Mermaid: bv.Mermaid}
		if src.Mermaid != nil && src.Mermaid.Theme != "" {
			v.Mermaid = src.Mermaid.Theme
		}
		if err := merge(v.Tokens, src.Tokens, ColorTokenNames, "variants."+scheme+".tokens", nil); err != nil {
			return nil, err
		}
		if err := merge(v.Style, src.StyleTokens, StyleTokenNames, "variants."+scheme+".styleTokens", lengthTokens); err != nil {
			return nil, err
		}
		t.Variants[i] = v
	}
	return t, nil
}

func merge(dst, src map[string]string, names []string, path string, lengths map[string]bool) error {
	for name, value := range src {
		if !slices.Contains(names, name) {
			return fmt.Errorf("未知令牌：%s.%s", path, name)
		}
		value = strings.TrimSpace(value)
		if value == "" || unsafePattern.MatchString(value) || unsafeFunction.MatchString(value) || (lengths[name] && !lengthPattern.MatchString(value)) {
			return fmt.Errorf("主题令牌值非法：%s.%s", path, name)
		}
		dst[name] = value
	}
	return nil
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// Serialize 把主题导出为可编辑、可再次导入的完整 JSON（全部令牌显式列出）。内置主题换成
// user-<名称>-custom ID 并在名称后加“自定义”后缀；用户主题保持原 ID，修改后可重新导入覆盖。
func Serialize(t *Theme) ([]byte, error) {
	suffix := map[string]string{"zh-CN": "（自定义）", "zh-TW": "（自訂）", "en-US": " Custom", "ja-JP": "（カスタム）"}
	f := userFile{SchemaVersion: 1, ID: t.ID, Version: t.Version, Author: t.Author, LocalizedNames: maps.Clone(t.Names),
		StyleProfile: t.StyleProfile, Variants: map[string]userVariant{}}
	if t.Builtin {
		f.ID = "user-" + strings.TrimPrefix(t.ID, "nomo-") + "-custom"
		f.Author, f.BaseThemeID = "User", t.ID
		for l, n := range f.LocalizedNames {
			s, ok := suffix[l]
			if !ok {
				s = " Custom"
			}
			f.LocalizedNames[l] = n + s
		}
	}
	for i, scheme := range []string{"light", "dark"} {
		v := t.Variants[i]
		uv := userVariant{Tokens: v.Tokens, StyleTokens: v.Style, ShikiTheme: v.Shiki}
		uv.Mermaid = &struct {
			Theme string `json:"theme"`
		}{v.Mermaid}
		f.Variants[scheme] = uv
	}
	out, err := json.Marshal(f, json.Deterministic(true))
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// Color 解析令牌颜色：#rgb、#rrggbb、#rrggbbaa、rgb()/rgba()、transparent。无法解析
// （如 CSS 函数）时返回 false，调用方回退到基础主题的同名令牌。
func Color(v string) (ui.Color, bool) {
	v = strings.TrimSpace(strings.ToLower(v))
	switch {
	case v == "transparent":
		return ui.Transparent, true
	case strings.HasPrefix(v, "#"):
		h := v[1:]
		if len(h) == 3 {
			h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
		}
		if len(h) != 6 && len(h) != 8 {
			return ui.Color{}, false
		}
		n, err := strconv.ParseUint(h, 16, 32)
		if err != nil {
			return ui.Color{}, false
		}
		if len(h) == 6 {
			return ui.RGB(uint8(n>>16), uint8(n>>8), uint8(n)), true
		}
		return ui.RGBA(uint8(n>>24), uint8(n>>16), uint8(n>>8), float32(uint8(n))/255), true
	case strings.HasPrefix(v, "rgb"):
		open, close := strings.IndexByte(v, '('), strings.LastIndexByte(v, ')')
		if open < 0 || close < open {
			return ui.Color{}, false
		}
		parts := strings.FieldsFunc(v[open+1:close], func(r rune) bool { return r == ',' || r == ' ' || r == '/' })
		if len(parts) < 3 {
			return ui.Color{}, false
		}
		var c [3]uint8
		for i := range 3 {
			f, err := strconv.ParseFloat(parts[i], 64)
			if err != nil {
				return ui.Color{}, false
			}
			c[i] = uint8(max(0, min(255, f)))
		}
		a := 1.0
		if len(parts) > 3 {
			s := parts[3]
			pct := strings.HasSuffix(s, "%")
			f, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
			if err != nil {
				return ui.Color{}, false
			}
			if pct {
				f /= 100
			}
			a = max(0, min(1, f))
		}
		return ui.RGBA(c[0], c[1], c[2], float32(a)), true
	}
	return ui.Color{}, false
}

// Tokens 返回主题某一变体的颜色查询函数：无法解析的值回退到默认主题同一变体的令牌。
func (t *Theme) Tokens(dark bool) func(name string) ui.Color {
	i := 0
	if dark {
		i = 1
	}
	v, def := t.Variants[i], builtin[0].Variants[i]
	return func(name string) ui.Color {
		if c, ok := Color(v.Tokens[name]); ok {
			return c
		}
		c, _ := Color(def.Tokens[name])
		return c
	}
}

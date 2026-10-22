package shell

import (
	"encoding/json/v2"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/config"
)

// Prefs 是原生外壳读写的应用偏好。字段名、默认值、取值范围与原版
// src/app/services/settings.ts 的 AppPreferences / normalizeAppPreferences 一致，
// 每项以同名键单独存入 config.json，WebView 版与原生版读到的是同一份设置。
//
// 由主窗口与设置窗口共享（同一进程、同一 UI 线程）；修改后调用 Store 写盘。
type Prefs struct {
	ThemeMode                   string  `json:"themeMode"`
	InterfaceLanguage           string  `json:"interfaceLanguage"`
	EditorMode                  string  `json:"editorMode"`
	AutoSaveEnabled             bool    `json:"autoSaveEnabled"`
	AutoSaveDelayMs             float64 `json:"autoSaveDelayMs"`
	CreateSnapshotBeforeSave    bool    `json:"createSnapshotBeforeSave"`
	FontSize                    float64 `json:"fontSize"`
	LineHeight                  float64 `json:"lineHeight"`
	TypographyEnabled           bool    `json:"typographyEnabled"`
	ContentWidthPercent         float64 `json:"contentWidthPercent"`
	LargeDocumentLimit          float64 `json:"largeDocumentLimit"`
	DocumentStyleID             string  `json:"documentStyleId"`
	OpenDefaultBehavior         string  `json:"openDefaultBehavior"`
	FilePreviewEnabled          bool    `json:"filePreviewEnabled"`
	CloseWindowBehavior         string  `json:"closeWindowBehavior"`
	ExternalFileChangeBehavior  string  `json:"externalFileChangeBehavior"`
	SidebarHidden               bool    `json:"sidebarHidden"`
	ToolbarHidden               bool    `json:"toolbarHidden"`
	OutlineVisible              bool    `json:"outlineVisible"`
	WritingStatsVisible         bool    `json:"writingStatsVisible"`
	WritingStatsMetric          string  `json:"writingStatsMetric"`
	ReadingTimeVisible          bool    `json:"readingTimeVisible"`
	DefaultCodeBlockLanguage    string  `json:"defaultCodeBlockLanguage"`
	ZoomPercent                 float64 `json:"zoomPercent"`
	CtrlWheelZoomEnabled        bool    `json:"ctrlWheelZoomEnabled"`
	OutlineDefaultExpandLevel   float64 `json:"outlineDefaultExpandLevel"`
	CodeBlockLineNumbersVisible bool    `json:"codeBlockLineNumbersVisible"`
	CodeBlockIndent             string  `json:"codeBlockIndent"`
	InlineCodeRenderingEnabled  bool    `json:"inlineCodeRenderingEnabled"`
	CopyMarkdownSyntaxEnabled   bool    `json:"copyMarkdownSyntaxEnabled"`
	RenderMode                  string  `json:"renderMode"`
	DeveloperMode               bool    `json:"developerMode"`
	SoftwareUpdateAutoCheck     bool    `json:"softwareUpdateAutoCheckEnabled"`

	ColorThemeID          string            `json:"colorThemeId"`
	UIFontFamily          string            `json:"uiFontFamily"`
	DocumentFontFamily    string            `json:"documentFontFamily"`
	MonoFontFamily        string            `json:"monoFontFamily"`
	QuickInsertEnabled    bool              `json:"quickInsertEnabled"`
	QuickInsertTrigger    string            `json:"quickInsertTrigger"`
	MarkdownLintEnabled   bool              `json:"markdownLintEnabled"`
	MarkdownLintRuleSet   string            `json:"markdownLintRuleSet"`
	ShortcutPreferences   map[string]string `json:"shortcutPreferences"`
	ImageHandlingSettings ImageSettings     `json:"imageHandlingSettings"`
}

// ImageSettings 与原版 ImageHandlingSettings（src/lib/services/render.ts）一致。
type ImageSettings struct {
	ImageInsertStrategy         string `json:"imageInsertStrategy"`
	AutoDeleteUnusedLocalImages bool   `json:"autoDeleteUnusedLocalImages"`
	UploadProvider              string `json:"uploadProvider"`
	PicgoServerURL              string `json:"picgoServerUrl"`
	PicgoCoreCommand            string `json:"picgoCoreCommand"`
	PicgoCoreConfigPath         string `json:"picgoCoreConfigPath"`
	DefaultImageWidth           string `json:"defaultImageWidth"`
	DefaultImageAlign           string `json:"defaultImageAlign"`
}

// defaultShortcuts 与原版 DEFAULT_SHORTCUT_PREFERENCES 一致（Ctrl 在 macOS 上为 Command）。
var defaultShortcuts = map[string]string{
	"new-file": "Ctrl+N", "open-file": "Ctrl+O", "save-file": "Ctrl+S", "export-html": "Ctrl+Shift+E",
	"export-pdf": "Ctrl+Shift+P", "toggle-source": "Ctrl+E", "toggle-theme": "Ctrl+Shift+L",
	"toggle-focus": "Ctrl+Shift+F", "toggle-toolbar": "Ctrl+Shift+B", "toggle-markdown-mini": "Ctrl+Alt+M",
	"insert-code-block": "Ctrl+Shift+K", "insert-table": "Ctrl+Shift+T", "insert-math-block": "Ctrl+Shift+M",
	"menu-link": "Ctrl+K", "menu-clear-format": "Ctrl+\\",
}

// defaultPrefs 与原版 DEFAULT_APP_PREFERENCES 一致。
func defaultPrefs() Prefs {
	return Prefs{
		ThemeMode: "system", InterfaceLanguage: "system", EditorMode: modeSemantic,
		AutoSaveDelayMs: 1000, CreateSnapshotBeforeSave: true, FontSize: 16, LineHeight: 1.75,
		TypographyEnabled: true, ContentWidthPercent: 60, LargeDocumentLimit: 500_000, DocumentStyleID: "nomo-modern",
		OpenDefaultBehavior: "ask-every-time", FilePreviewEnabled: true, CloseWindowBehavior: "ask-every-time",
		ExternalFileChangeBehavior: "reload-external", OutlineVisible: true, WritingStatsVisible: true,
		WritingStatsMetric: "words", DefaultCodeBlockLanguage: "ts", ZoomPercent: 100, CtrlWheelZoomEnabled: true,
		OutlineDefaultExpandLevel: 2, CodeBlockLineNumbersVisible: true, CodeBlockIndent: "spaces-2",
		InlineCodeRenderingEnabled: true, CopyMarkdownSyntaxEnabled: true, RenderMode: "hardware",
		SoftwareUpdateAutoCheck: true, ColorThemeID: "nomo-default", QuickInsertEnabled: true, QuickInsertTrigger: "/",
		MarkdownLintRuleSet: "relaxed", ShortcutPreferences: maps.Clone(defaultShortcuts),
		ImageHandlingSettings: ImageSettings{ImageInsertStrategy: "copy-assets", UploadProvider: "picgo",
			PicgoServerURL: "http://127.0.0.1:36677/upload", PicgoCoreCommand: "picgo", DefaultImageAlign: "none"},
	}
}

// prefKeys 列出每个字段的设置键与读写入口（逐键存储，未写过的键保持默认值）。
func (p *Prefs) fields() map[string]any {
	return map[string]any{
		"themeMode": &p.ThemeMode, "interfaceLanguage": &p.InterfaceLanguage, "editorMode": &p.EditorMode,
		"autoSaveEnabled": &p.AutoSaveEnabled, "autoSaveDelayMs": &p.AutoSaveDelayMs,
		"createSnapshotBeforeSave": &p.CreateSnapshotBeforeSave, "fontSize": &p.FontSize, "lineHeight": &p.LineHeight,
		"typographyEnabled": &p.TypographyEnabled, "contentWidthPercent": &p.ContentWidthPercent,
		"largeDocumentLimit": &p.LargeDocumentLimit, "documentStyleId": &p.DocumentStyleID,
		"openDefaultBehavior": &p.OpenDefaultBehavior, "filePreviewEnabled": &p.FilePreviewEnabled,
		"closeWindowBehavior": &p.CloseWindowBehavior, "externalFileChangeBehavior": &p.ExternalFileChangeBehavior,
		"sidebarHidden": &p.SidebarHidden, "toolbarHidden": &p.ToolbarHidden, "outlineVisible": &p.OutlineVisible,
		"writingStatsVisible": &p.WritingStatsVisible, "writingStatsMetric": &p.WritingStatsMetric,
		"readingTimeVisible": &p.ReadingTimeVisible, "defaultCodeBlockLanguage": &p.DefaultCodeBlockLanguage,
		"zoomPercent": &p.ZoomPercent, "ctrlWheelZoomEnabled": &p.CtrlWheelZoomEnabled,
		"outlineDefaultExpandLevel":   &p.OutlineDefaultExpandLevel,
		"codeBlockLineNumbersVisible": &p.CodeBlockLineNumbersVisible, "codeBlockIndent": &p.CodeBlockIndent,
		"inlineCodeRenderingEnabled": &p.InlineCodeRenderingEnabled, "copyMarkdownSyntaxEnabled": &p.CopyMarkdownSyntaxEnabled,
		"renderMode": &p.RenderMode, "developerMode": &p.DeveloperMode,
		"softwareUpdateAutoCheckEnabled": &p.SoftwareUpdateAutoCheck,
		"colorThemeId":                   &p.ColorThemeID, "uiFontFamily": &p.UIFontFamily, "documentFontFamily": &p.DocumentFontFamily,
		"monoFontFamily": &p.MonoFontFamily, "quickInsertEnabled": &p.QuickInsertEnabled, "quickInsertTrigger": &p.QuickInsertTrigger,
		"markdownLintEnabled": &p.MarkdownLintEnabled, "markdownLintRuleSet": &p.MarkdownLintRuleSet,
		"shortcutPreferences": &p.ShortcutPreferences, "imageHandlingSettings": &p.ImageHandlingSettings,
	}
}

// loadPrefs 读取全部偏好并按原版规则规范化：类型不符、取值越界或不在枚举内的回退默认。
func loadPrefs(cfg *config.Manager) Prefs {
	p := defaultPrefs()
	for key, ptr := range p.fields() {
		if v, ok := cfg.Setting(key); ok {
			_ = json.Unmarshal([]byte(v), ptr)
		}
	}
	p.normalize()
	return p
}

func (p *Prefs) normalize() {
	d := defaultPrefs()
	oneOf := func(v *string, def string, allowed ...string) {
		if !slices.Contains(allowed, *v) {
			*v = def
		}
	}
	clampF := func(v *float64, lo, hi, def float64) {
		if *v != *v || *v < lo || *v > hi { // NaN 或越界
			*v = def
		}
	}
	oneOf(&p.ThemeMode, d.ThemeMode, "system", "light", "dark")
	oneOf(&p.InterfaceLanguage, d.InterfaceLanguage, "system", "zh-CN", "zh-TW", "en-US", "ja-JP")
	// 原版还有 split（双栏），原生外壳暂未实现，读到时按语义模式显示但不改写设置。
	oneOf(&p.EditorMode, d.EditorMode, modeSemantic, modeSource, "split")
	clampF(&p.AutoSaveDelayMs, 500, 5000, d.AutoSaveDelayMs)
	clampF(&p.FontSize, 14, 22, d.FontSize)
	clampF(&p.LineHeight, 1.4, 2.1, d.LineHeight)
	clampF(&p.ContentWidthPercent, 45, 90, d.ContentWidthPercent)
	clampF(&p.LargeDocumentLimit, 100_000, 1_000_000, d.LargeDocumentLimit)
	oneOf(&p.DocumentStyleID, d.DocumentStyleID, "nomo-modern", "nomo-classic")
	if !themeIDPattern.MatchString(p.ColorThemeID) {
		p.ColorThemeID = d.ColorThemeID // 是否为已注册主题在加载主题后再核对（见 Shell.currentTheme）
	}
	for _, f := range []*string{&p.UIFontFamily, &p.DocumentFontFamily, &p.MonoFontFamily} {
		*f = normalizeFontFamily(*f)
	}
	if !isQuickInsertTrigger(p.QuickInsertTrigger) {
		p.QuickInsertTrigger = d.QuickInsertTrigger
	}
	oneOf(&p.MarkdownLintRuleSet, d.MarkdownLintRuleSet, "relaxed", "default")
	p.ShortcutPreferences = normalizeShortcuts(p.ShortcutPreferences)
	img, di := &p.ImageHandlingSettings, d.ImageHandlingSettings
	oneOf(&img.ImageInsertStrategy, di.ImageInsertStrategy, "copy-current-folder", "copy-assets", "copy-document-assets", "upload")
	oneOf(&img.UploadProvider, di.UploadProvider, "picgo", "picgo-core")
	oneOf(&img.DefaultImageAlign, di.DefaultImageAlign, "none", "left", "center", "right")
	oneOf(&p.OpenDefaultBehavior, d.OpenDefaultBehavior, "current-window", "new-window", "ask-every-time")
	oneOf(&p.CloseWindowBehavior, d.CloseWindowBehavior, "ask-every-time", "close-window", "close-to-tray")
	oneOf(&p.ExternalFileChangeBehavior, d.ExternalFileChangeBehavior, "reload-external", "overwrite-external", "ignore")
	oneOf(&p.WritingStatsMetric, d.WritingStatsMetric, "lines", "words", "visibleChars", "chars")
	clampF(&p.ZoomPercent, 80, 160, d.ZoomPercent)
	clampF(&p.OutlineDefaultExpandLevel, 1, 6, d.OutlineDefaultExpandLevel)
	oneOf(&p.CodeBlockIndent, d.CodeBlockIndent, "spaces-2", "spaces-4", "tab")
	oneOf(&p.RenderMode, d.RenderMode, "hardware", "software")
}

// Store 把单个偏好写回 config.json；key 为原版设置键。
func (p *Prefs) Store(cfg *config.Manager, key string) {
	ptr, ok := p.fields()[key]
	if !ok {
		return
	}
	raw, err := json.Marshal(ptr)
	if err != nil {
		return
	}
	if err := cfg.Update(func(c *config.AppConfig) { config.PutSetting(c, key, string(raw)) }); err != nil {
		applog.Warn("Native", "保存设置失败："+err.Error())
	}
}

var (
	themeIDPattern    = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	unsafeFontPattern = regexp.MustCompile("[\"'\\\\;:{}()<>,!\\x00-\\x1f\\x7f]")
	shortcutPattern   = regexp.MustCompile(`^[A-Za-z0-9+\\\-\[\]` + "`" + ` ]{3,40}$`)
)

// normalizeFontFamily 与原版 normalizeFontFamilyName 一致：单个字体族名，去首尾空白并折叠
// 连续空白；超过 100 字符或含引号、逗号、括号等不安全字符时视为未设置（跟随主题）。
func normalizeFontFamily(v string) string {
	v = strings.Join(strings.Fields(v), " ")
	if v == "" || len([]rune(v)) > 100 || unsafeFontPattern.MatchString(v) {
		return ""
	}
	return v
}

// isQuickInsertTrigger 与原版一致：只接受一个标点或符号。
func isQuickInsertTrigger(v string) bool {
	r := []rune(v)
	return len(r) == 1 && (unicode.IsPunct(r[0]) || unicode.IsSymbol(r[0]))
}

// normalizeShortcuts 与原版 normalizeShortcutPreferences 一致：只保留已知命令，格式不合法的
// 回退默认；各段去空白后以“+”连接。
func normalizeShortcuts(in map[string]string) map[string]string {
	out := maps.Clone(defaultShortcuts)
	for id, v := range in {
		if _, ok := out[id]; !ok || !shortcutPattern.MatchString(strings.TrimSpace(v)) {
			continue
		}
		var parts []string
		for _, part := range strings.Split(v, "+") {
			if part = strings.TrimSpace(part); part != "" {
				parts = append(parts, part)
			}
		}
		out[id] = strings.Join(parts, "+")
	}
	return out
}

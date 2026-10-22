package main

import (
	"encoding/json/v2"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/egoist/mygo"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/i18n"
)

// 原生菜单：macOS 为应用级菜单，命令发给当前聚焦的文档窗口；Windows/Linux 每个文档窗口一份菜单，
// 命令只发给该窗口。菜单项 ID 即前端 executeDesktopAppCommand 的命令名。

const menuCommandEvent = "nomo://menu-command"

func tr(key string) string {
	text := i18n.Text(effectiveLocale(), key)
	if runtime.GOOS == "darwin" {
		// macOS 菜单不显示助记符：与 Tauri 一致去掉单个 &，&& 还原为 &。
		text = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(text, "&&", "\x00"), "&", ""), "\x00", "&")
	}
	return text
}

// installWindowMenu 为文档窗口安装菜单；macOS 上刷新应用级菜单。
func installWindowMenu(window *appWindow) {
	if runtime.GOOS == "darwin" {
		refreshApplicationMenu()
		return
	}
	if isDocumentWindowLabel(window.label) {
		window.win.SetMenu(buildWindowMenu())
	} else {
		window.win.SetMenu(mygo.NewMenu(nil))
	}
}

// refreshApplicationMenu 在最近列表、语言或快捷键变化后重建菜单。
func refreshApplicationMenu() {
	if runtime.GOOS == "darwin" {
		mygo.App.SetMenu(buildWindowMenu())
		return
	}
	for _, window := range windows.documentWindows() {
		window.win.SetMenu(buildWindowMenu())
	}
}

func buildWindowMenu() *mygo.Menu {
	items := []*mygo.MenuItem{}
	if runtime.GOOS == "darwin" {
		items = append(items, buildMacAppMenu())
	}
	items = append(items, buildFileMenu(), buildEditMenu(), buildParagraphMenu(), buildFormatMenu(), buildViewMenu(), buildSettingsMenu())
	if runtime.GOOS == "darwin" {
		items = append(items, &mygo.MenuItem{Role: mygo.RoleWindowMenu})
	}
	return mygo.NewMenu(items)
}

// buildMacAppMenu 是 macOS 的应用菜单：退出走工作区退出事务，而不是直接结束进程。
func buildMacAppMenu() *mygo.MenuItem {
	return &mygo.MenuItem{Label: "Nomo", Submenu: []*mygo.MenuItem{
		{Role: mygo.RoleAbout},
		mygo.Separator(),
		commandItem("open-settings", tr("menu_preferences"), "Cmd+,"),
		mygo.Separator(),
		{Role: mygo.RoleServices},
		mygo.Separator(),
		{Role: mygo.RoleHide},
		{Role: mygo.RoleHideOthers},
		{Role: mygo.RoleUnhide},
		mygo.Separator(),
		commandItem("quit", tr("menu_quit"), "Cmd+Q"),
	}}
}

// commandItem 创建发送菜单命令的普通菜单项。
func commandItem(id, label, accelerator string) *mygo.MenuItem {
	return &mygo.MenuItem{ID: id, Label: label, Accelerator: accelerator, Click: onMenuCommand}
}

func onMenuCommand(item *mygo.MenuItem, win *mygo.Window) {
	command := item.ID
	switch command {
	case "quit":
		go func() { _ = beginExit() }()
		return
	case "open-settings":
		go func() {
			if err := openSettingsWindow(); err != nil {
				applog.Error("Settings", err.Error())
			}
		}()
		return
	}
	target := windows.byWindow(win)
	if runtime.GOOS == "darwin" || target == nil {
		target = focusedDocumentWindow()
	}
	if target == nil || !isDocumentWindowLabel(target.label) {
		return
	}
	if err := emitToWindow(target, menuCommandEvent, command); err != nil {
		applog.Warn("Menu", "发送菜单命令失败："+err.Error())
	}
}

// focusedDocumentWindow 返回聚焦的文档窗口，没有时回退到主窗口。
func focusedDocumentWindow() *appWindow {
	for _, window := range windows.documentWindows() {
		if window.win.IsFocused() {
			return window
		}
	}
	return windows.get(mainWindowLabel)
}

func buildFileMenu() *mygo.MenuItem {
	items := []*mygo.MenuItem{
		commandItem("new-file", tr("menu_new"), "CmdOrCtrl+N"),
		commandItem("new-window", tr("menu_new_window"), "CmdOrCtrl+Shift+N"),
		commandItem("open-file", tr("menu_open"), "CmdOrCtrl+O"),
		commandItem("open-directory", tr("menu_open_folder"), "CmdOrCtrl+Shift+O"),
	}
	recent := queryRecentEntries(recentMenuLimit)
	if len(recent) > 0 {
		var recentItems []*mygo.MenuItem
		for _, entry := range recent {
			label := filepath.Base(entry.Path)
			if entry.Title != nil {
				label = *entry.Title
			} else if label == "" || label == "." || label == string(filepath.Separator) {
				label = tr("menu_untitled")
			}
			recentItems = append(recentItems, commandItem("open-recent:"+entry.EntryType+":"+entry.Path, label, ""))
		}
		items = append(items, &mygo.MenuItem{Label: tr("menu_open_recent"), Submenu: recentItems})
	} else {
		items = append(items, &mygo.MenuItem{ID: "no-recent", Label: tr("menu_no_recent"), Disabled: true})
	}
	closeWindowAccelerator, quitAccelerator := "Alt+F4", "Alt+F4"
	if runtime.GOOS == "darwin" {
		closeWindowAccelerator, quitAccelerator = "", "Cmd+Q"
	}
	items = append(items,
		mygo.Separator(),
		commandItem("save-file", tr("menu_save"), "CmdOrCtrl+S"),
		commandItem("save-file-as", tr("menu_save_as"), "CmdOrCtrl+Shift+S"),
		mygo.Separator(),
		&mygo.MenuItem{Label: tr("menu_export"), Submenu: []*mygo.MenuItem{
			commandItem("export-html", tr("menu_export_html"), ""),
			commandItem("export-pdf", tr("menu_export_pdf"), ""),
			commandItem("export-word", tr("menu_export_word"), ""),
		}},
		mygo.Separator(),
		commandItem("close-current-file", tr("menu_close_file"), "CmdOrCtrl+W"),
		commandItem("close-current-window", tr("menu_close_window"), closeWindowAccelerator),
	)
	// macOS 的退出已在应用菜单中提供 Cmd+Q，这里保留菜单项但不重复绑定快捷键。
	if runtime.GOOS == "darwin" {
		quitAccelerator = ""
	}
	items = append(items, mygo.Separator(), commandItem("quit", tr("menu_quit"), quitAccelerator))
	return &mygo.MenuItem{Label: tr("menu_file"), Submenu: items}
}

func buildEditMenu() *mygo.MenuItem {
	redo := "CmdOrCtrl+Y"
	if runtime.GOOS == "darwin" {
		redo = "CmdOrCtrl+Shift+Z"
	}
	return &mygo.MenuItem{Label: tr("menu_edit"), Submenu: []*mygo.MenuItem{
		commandItem("undo", tr("menu_undo"), "CmdOrCtrl+Z"),
		commandItem("redo", tr("menu_redo"), redo),
		mygo.Separator(),
		{Role: mygo.RoleCut},
		{Role: mygo.RoleCopy},
		{Role: mygo.RolePaste},
		{Role: mygo.RoleSelectAll},
	}}
}

func buildParagraphMenu() *mygo.MenuItem {
	headings := []*mygo.MenuItem{}
	for level := 1; level <= 6; level++ {
		digit := string(rune('0' + level))
		headings = append(headings, commandItem("set-heading-"+digit, tr("menu_heading_"+digit), "CmdOrCtrl+"+digit))
	}
	return &mygo.MenuItem{Label: tr("menu_paragraph"), Submenu: []*mygo.MenuItem{
		{Label: tr("menu_heading"), Submenu: headings},
		commandItem("set-paragraph", tr("menu_paragraph"), "CmdOrCtrl+0"),
		commandItem("menu-heading-up", tr("menu_lift_heading"), "CmdOrCtrl+="),
		commandItem("menu-heading-down", tr("menu_sink_heading"), "CmdOrCtrl+-"),
		mygo.Separator(),
		commandItem("insert-table", tr("menu_table"), "CmdOrCtrl+Shift+T"),
		commandItem("insert-code-block", tr("menu_code_block"), "CmdOrCtrl+Shift+K"),
		commandItem("insert-math-block", tr("menu_math_block"), "CmdOrCtrl+Shift+M"),
		mygo.Separator(),
		commandItem("toggle-blockquote", tr("menu_blockquote"), "CmdOrCtrl+Shift+Q"),
		commandItem("insert-callout", tr("menu_callout"), "CmdOrCtrl+Shift+A"),
		commandItem("menu-comment-block", tr("menu_comment_block"), ""),
		commandItem("toggle-ordered-list", tr("menu_ordered_list"), "CmdOrCtrl+Shift+["),
		commandItem("toggle-bullet-list", tr("menu_bullet_list"), "CmdOrCtrl+Shift+]"),
		commandItem("toggle-task-list", tr("menu_task_list"), "CmdOrCtrl+Shift+X"),
		mygo.Separator(),
		commandItem("menu-insert-paragraph-before", tr("menu_insert_before"), "CmdOrCtrl+Shift+Enter"),
		commandItem("menu-insert-paragraph-after", tr("menu_insert_after"), "CmdOrCtrl+Enter"),
		mygo.Separator(),
		{Label: tr("menu_chart"), Submenu: []*mygo.MenuItem{
			commandItem("menu-chart", tr("menu_chart_blank"), ""),
			commandItem("menu-chart:flowchart", tr("menu_chart_flowchart"), ""),
			commandItem("menu-chart:sequenceDiagram", tr("menu_chart_sequence"), ""),
			commandItem("menu-chart:classDiagram", tr("menu_chart_class"), ""),
			commandItem("menu-chart:stateDiagram", tr("menu_chart_state"), ""),
			commandItem("menu-chart:pie", tr("menu_chart_pie"), ""),
			commandItem("menu-chart:gantt", tr("menu_chart_gantt"), ""),
			commandItem("menu-chart:erDiagram", tr("menu_chart_er"), ""),
		}},
		commandItem("menu-footnote", tr("menu_footnote"), ""),
		commandItem("menu-horizontal-rule", tr("menu_horizontal_rule"), "CmdOrCtrl+Shift+H"),
		commandItem("menu-content-directory", tr("menu_toc"), ""),
		commandItem("menu-yaml-front-matter", tr("menu_front_matter"), ""),
	}}
}

func buildFormatMenu() *mygo.MenuItem {
	return &mygo.MenuItem{Label: tr("menu_format"), Submenu: []*mygo.MenuItem{
		commandItem("toggle-bold", tr("menu_bold"), "CmdOrCtrl+B"),
		commandItem("toggle-italic", tr("menu_italic"), "CmdOrCtrl+I"),
		commandItem("toggle-underline", tr("menu_underline"), "CmdOrCtrl+U"),
		commandItem("toggle-inline-code", tr("menu_inline_code"), "CmdOrCtrl+`"),
		commandItem("menu-inline-math", tr("menu_inline_math"), ""),
		mygo.Separator(),
		commandItem("toggle-strikethrough", tr("menu_strike"), "Alt+Shift+5"),
		commandItem("menu-highlight", tr("menu_highlight"), ""),
		commandItem("menu-comment", tr("menu_comment"), ""),
		mygo.Separator(),
		commandItem("menu-link", tr("menu_link"), "CmdOrCtrl+K"),
		commandItem("menu-image", tr("menu_image"), ""),
		mygo.Separator(),
		commandItem("menu-clear-format", tr("menu_clear_format"), `CmdOrCtrl+\`),
	}}
}

func buildViewMenu() *mygo.MenuItem {
	return &mygo.MenuItem{Label: tr("menu_view"), Submenu: []*mygo.MenuItem{
		commandItem("toggle-source", tr("menu_toggle_source"), "CmdOrCtrl+E"),
		commandItem("toggle-outline", tr("menu_toggle_outline"), ""),
		commandItem("reset-outline-position", tr("menu_reset_outline_position"), ""),
		commandItem("toggle-toolbar", tr("menu_toggle_toolbar"), toolbarAccelerator()),
		commandItem("toggle-theme", tr("menu_toggle_theme"), "CmdOrCtrl+Shift+L"),
		commandItem("toggle-focus", tr("menu_toggle_explorer"), "CmdOrCtrl+Shift+F"),
	}}
}

func buildSettingsMenu() *mygo.MenuItem {
	return &mygo.MenuItem{Label: tr("menu_settings"), Submenu: []*mygo.MenuItem{
		commandItem("open-settings", tr("menu_preferences"), ""),
	}}
}

// toolbarAccelerator 读取用户自定义的“显示/隐藏工具栏”快捷键，无效时回退默认值。
func toolbarAccelerator() string {
	const fallback = "CmdOrCtrl+Shift+B"
	value, ok := settingValue("shortcutPreferences")
	if !ok {
		return fallback
	}
	var preferences map[string]any
	if json.Unmarshal([]byte(value), &preferences) != nil {
		return fallback
	}
	shortcut, _ := preferences["toggle-toolbar"].(string)
	if accelerator, ok := toNativeAccelerator(shortcut); ok {
		return accelerator
	}
	return fallback
}

// toNativeAccelerator 把前端快捷键（如 "Ctrl+Shift+B"）转换为菜单加速键格式；不支持的键返回 false。
func toNativeAccelerator(shortcut string) (string, bool) {
	var parts []string
	for _, part := range strings.Split(shortcut, "+") {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	key := parts[len(parts)-1]
	var normalizedKey string
	switch lower := strings.ToLower(key); {
	case lower == "space":
		normalizedKey = "Space"
	case lower == `\`, lower == "[", lower == "]", lower == "`", lower == "-":
		normalizedKey = lower
	case len(lower) == 1 && (lower[0] >= 'a' && lower[0] <= 'z' || lower[0] >= '0' && lower[0] <= '9'):
		normalizedKey = strings.ToUpper(lower)
	default:
		return "", false
	}
	var modifiers []string
	for _, modifier := range parts[:len(parts)-1] {
		var value string
		switch strings.ToLower(modifier) {
		case "ctrl":
			value = "CmdOrCtrl"
		case "shift":
			value = "Shift"
		case "alt":
			value = "Alt"
		default:
			return "", false
		}
		duplicate := false
		for _, existing := range modifiers {
			duplicate = duplicate || existing == value
		}
		if !duplicate {
			modifiers = append(modifiers, value)
		}
	}
	return strings.Join(append(modifiers, normalizedKey), "+"), true
}

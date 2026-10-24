package shell

import (
	"runtime"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// command 是一条菜单命令；同一张表生成标题栏菜单、macOS 菜单栏与快捷键。
// 快捷键沿用原版（Ctrl 在 macOS 上为 Cmd）。
type command struct {
	id    string
	label string // 文案键
	mods  ui.Modifiers
	key   ui.Key
	run   func(s *Shell)
	// sep 为 true 时在本条之前加分隔线。
	sep bool
	// sc 为原版可自定义快捷键的命令 ID（如 "toggle-source"）；非空时快捷键取自偏好
	// shortcutPreferences，覆盖 mods/key 的默认值。
	sc string
}

type menuGroup struct {
	label string // 文案键
	items []command
}

func (s *Shell) menuGroups() []menuGroup {
	cmd, shift, alt := ui.Cmd, ui.Shift, ui.Alt
	format := func(id, label string, mods ui.Modifiers, key ui.Key) command {
		return command{id: id, label: label, mods: mods, key: key, run: func(s *Shell) { s.run(id) }}
	}
	custom := func(c command, sc string) command { c.sc = sc; return c }
	groups := []menuGroup{
		{"file", []command{
			custom(command{id: "new", label: "newMarkdown", run: (*Shell).NewDocument}, "new-file"),
			custom(command{id: "open", label: "openFileEllipsis", run: func(s *Shell) { s.openDialog(false) }}, "open-file"),
			{id: "openFolder", label: "openFolderEllipsis", mods: cmd | shift, key: ui.KeyO, run: func(s *Shell) { s.openDialog(true) }},
			custom(command{id: "save", label: "save", sep: true, run: func(s *Shell) { s.save(s.activeTab(), false, nil) }}, "save-file"),
			{id: "saveAs", label: "saveAs", mods: cmd | shift, key: ui.KeyS, run: func(s *Shell) { s.save(s.activeTab(), true, nil) }},
			custom(command{id: "exportHtml", label: "exportHtml", sep: true, run: (*Shell).exportHTML}, "export-html"),
			custom(command{id: "exportPdf", label: "exportPdf", run: (*Shell).exportPDF}, "export-pdf"),
			{id: "exportWord", label: "exportWord", run: (*Shell).exportWord},
			{id: "snapshots", label: "documentSnapshotsEllipsis", sep: true, run: (*Shell).openSnapshots},
			{id: "close", label: "closeCurrentFile", mods: cmd, key: ui.KeyW, sep: true, run: func(s *Shell) {
				if t := s.activeTab(); t != nil {
					s.requestClose([]int{t.ID}, false)
				}
			}},
			{id: "settings", label: "settings", mods: cmd, key: ui.KeyComma, sep: true, run: (*Shell).openSettings},
		}},
		{"editMenu", []command{
			format("undo", "undo", 0, 0), format("redo", "redo", 0, 0),
			{id: "find", label: "searchReplace", mods: cmd, key: ui.KeyF, sep: true, run: func(s *Shell) { s.openFind(false) }},
			{id: "replace", label: "replace", mods: cmd | alt, key: ui.KeyF, run: func(s *Shell) { s.openFind(true) }},
		}},
		{"format", []command{
			format("paragraph", "paragraph", cmd, ui.Key0),
			format("heading1", "setHeadingOne", cmd, ui.Key1), format("heading2", "heading", cmd, ui.Key2),
			format("heading3", "heading", cmd, ui.Key3),
			{id: "bold", label: "bold", mods: cmd, key: ui.KeyB, sep: true, run: func(s *Shell) { s.run("bold") }},
			format("italic", "italic", cmd, ui.KeyI), format("underline", "underline", cmd, ui.KeyU),
			format("strikethrough", "strikethrough", alt|shift, ui.Key5), format("highlight", "highlight", 0, 0),
			format("inlineCode", "inlineCode", cmd, ui.KeyBackquote), custom(format("link", "link", 0, 0), "menu-link"),
			custom(format("clearFormat", "clearStyle", 0, 0), "menu-clear-format"),
			custom(command{id: "textColor", label: "applyTextColor", run: func(s *Shell) { s.applyTextColor(s.tbar.lastColor) }}, "apply-text-color"),
			{id: "unorderedList", label: "unorderedList", mods: cmd | shift, key: ui.KeyBracketRight, sep: true, run: func(s *Shell) { s.run("unorderedList") }},
			format("orderedList", "orderedList", cmd|shift, ui.KeyBracketLeft), format("taskList", "taskList", cmd|shift, ui.KeyX),
			format("quote", "quote", cmd|shift, ui.KeyQ), format("callout", "callout", cmd|shift, ui.KeyA),
			format("details", "details", 0, 0),
			custom(format("codeBlock", "codeBlock", 0, 0), "insert-code-block"),
			custom(format("mathBlock", "mathBlock", 0, 0), "insert-math-block"),
			format("mermaid", "mermaidDiagram", 0, 0),
			custom(format("table", "table", 0, 0), "insert-table"), format("horizontalRule", "horizontalRule", 0, 0),
			format("pageBreak", "insertPageBreak", 0, 0),
			{id: "image", label: "image", run: (*Shell).pickImages},
		}},
		{"view", []command{
			custom(command{id: "toggleSource", label: "sourceMode", run: func(s *Shell) {
				if s.mode() == modeSource {
					s.setMode(modeSemantic)
				} else {
					s.setMode(modeSource)
				}
			}}, "toggle-source"),
			{id: "sidebar", label: "showHideExplorer", sep: true, run: (*Shell).toggleSidebar},
			{id: "outline", label: "showOutline", run: (*Shell).toggleOutline},
			custom(command{id: "toolbar", label: "showToolbar", run: (*Shell).toggleToolbar}, "toggle-toolbar"),
			custom(command{id: "focus", label: "enterFocusMode", run: (*Shell).toggleFocus}, "toggle-focus"),
			custom(command{id: "mini", label: "markdownMiniShortcut", run: (*Shell).toggleMini}, "toggle-markdown-mini"),
			{id: "zoomIn", label: "increaseZoom", mods: cmd, key: ui.KeyEqual, sep: true, run: func(s *Shell) { s.zoom(10) }},
			{id: "zoomOut", label: "decreaseZoom", mods: cmd, key: ui.KeyMinus, run: func(s *Shell) { s.zoom(-10) }},
			custom(command{id: "theme", label: "switchTheme", sep: true, run: func(s *Shell) { s.toggleTheme(s.prefs.ThemeMode == "dark") }}, "toggle-theme"),
		}},
	}
	for _, g := range groups {
		for i := range g.items {
			if it := &g.items[i]; it.sc != "" {
				it.mods, it.key = parseShortcut(s.prefs.ShortcutPreferences[it.sc])
			}
		}
	}
	return groups
}

// parseShortcut 把原版快捷键文本（如 "Ctrl+Shift+K"，Ctrl 在 macOS 上为 Command）解析为修饰键与按键；
// 无法识别时返回 0，菜单项不显示快捷键。
func parseShortcut(text string) (ui.Modifiers, ui.Key) {
	var mods ui.Modifiers
	var key ui.Key
	for _, part := range strings.Split(text, "+") {
		switch p := strings.TrimSpace(part); strings.ToLower(p) {
		case "ctrl", "cmd", "cmdorctrl", "mod":
			mods |= ui.Cmd
		case "shift":
			mods |= ui.Shift
		case "alt", "option":
			mods |= ui.Alt
		default:
			for k, name := range keyNames {
				if strings.EqualFold(name, p) {
					key = k
				}
			}
		}
	}
	if key == 0 {
		return 0, 0
	}
	return mods, key
}

// undo/redo 在标题栏菜单中转给当前编辑区：语义模式走 mdedit，源码模式走 ui/editor。
func (s *Shell) editCommand(id string) bool {
	t := s.activeTab()
	if t == nil {
		return false
	}
	if s.mode() == modeSource {
		if id == "undo" {
			t.Source().Buffer.Undo()
		} else {
			t.Source().Buffer.Redo()
		}
		return true
	}
	return t.Ed.Run(id)
}

// menus 画标题栏中的菜单按钮（Windows / Linux 上即应用菜单；macOS 另有系统菜单栏）。
func (s *Shell) menus(c *ui.Context) {
	t := c.Theme()
	for _, g := range s.menuGroups() {
		ck := c.Key(g.label)
		b := ui.ButtonBase(ck).Padding(4, 10).Radius(6)
		if b.Hovered() {
			b.Background(t.SurfaceHover)
		}
		b.Children(func() { ui.Text(ck, s.tr(g.label)).FontSize(13) })
		b.Menu(func(m *ui.Menu) {
			for i, it := range g.items {
				if it.sep && i > 0 {
					m.Separator()
				}
				item := m.Item(s.itemLabel(it))
				if it.key != 0 {
					item.Shortcut(it.mods, it.key)
				}
				if item.Chosen() {
					s.runCommand(it)
				}
			}
			if g.label == "file" {
				m.Separator()
				m.Submenu(s.tr("openRecent"), func(sub *ui.Menu) {
					n := 0
					for _, e := range s.recent {
						if n >= 10 {
							break
						}
						n++
						if sub.Item(e.Path).Chosen() {
							s.OpenFiles([]string{e.Path})
						}
					}
				})
			}
		})
	}
}

func (s *Shell) itemLabel(it command) string {
	label := strings.TrimSuffix(s.tr(it.label), "…")
	switch it.id {
	case "focus":
		if s.focusSaved != nil {
			return s.tr("exitFocusMode")
		}
	case "heading2":
		return s.tr("heading") + " 2"
	case "heading3":
		return s.tr("heading") + " 3"
	case "openFolder", "open":
		return s.tr(it.label)
	}
	return label
}

func (s *Shell) runCommand(it command) {
	if it.id == "undo" || it.id == "redo" {
		s.editCommand(it.id)
		return
	}
	it.run(s)
}

// shortcuts 在非 macOS 平台上把快捷键挂到根元素（macOS 由菜单栏的快捷键处理，避免触发两次）。
func (s *Shell) shortcuts(root ui.Element) {
	if runtime.GOOS == "darwin" {
		return
	}
	for _, g := range s.menuGroups() {
		for _, it := range g.items {
			if it.key != 0 && root.Shortcut(it.mods, it.key) {
				s.runCommand(it)
			}
		}
	}
}

// installMenu 在 macOS 上安装系统菜单栏：应用菜单、编辑菜单（交给焦点元素处理复制粘贴
// 与输入法相关命令）与外壳命令。
func (s *Shell) installMenu() {
	if runtime.GOOS != "darwin" {
		return
	}
	// 应用菜单按 macOS 惯例自行组织：关于、设置…（⌘,）、服务、隐藏、退出。
	app := &mygo.MenuItem{Label: "Nomo", Submenu: []*mygo.MenuItem{
		{Role: mygo.RoleAbout}, mygo.Separator(),
		{Label: s.tr("settings") + "…", Accelerator: "CmdOrCtrl+,", Click: func(_ *mygo.MenuItem, w *mygo.Window) {
			if t := shellForWindow(w); t != nil {
				t.win.Update(t.openSettings)
			}
		}},
		mygo.Separator(), {Role: mygo.RoleServices}, mygo.Separator(),
		{Role: mygo.RoleHide}, {Role: mygo.RoleHideOthers}, {Role: mygo.RoleUnhide}, mygo.Separator(), {Role: mygo.RoleQuit},
	}}
	items := []*mygo.MenuItem{app}
	for _, g := range s.menuGroups() {
		if g.label == "editMenu" {
			edit := &mygo.MenuItem{Role: mygo.RoleEditMenu}
			items = append(items, edit)
			sub := &mygo.MenuItem{Label: s.tr("searchReplace")}
			for _, it := range g.items[2:] {
				sub.Submenu = append(sub.Submenu, s.nativeItem(it))
			}
			items = append(items, sub)
			continue
		}
		top := &mygo.MenuItem{Label: s.tr(g.label)}
		for i, it := range g.items {
			if it.id == "settings" {
				continue // macOS 的设置在应用菜单
			}
			if it.sep && i > 0 {
				top.Submenu = append(top.Submenu, mygo.Separator())
			}
			top.Submenu = append(top.Submenu, s.nativeItem(it))
		}
		items = append(items, top)
	}
	items = append(items, &mygo.MenuItem{Role: mygo.RoleWindowMenu})
	mygo.App.SetMenu(mygo.NewMenu(items))
}

func (s *Shell) nativeItem(it command) *mygo.MenuItem {
	return &mygo.MenuItem{Label: s.itemLabel(it), Accelerator: accelerator(it.mods, it.key), Click: func(_ *mygo.MenuItem, w *mygo.Window) {
		// 系统菜单只有一份：命令发给菜单事件所在窗口的外壳（多窗口时为当前聚焦的窗口）。
		t := shellForWindow(w)
		if t == nil {
			return
		}
		// 设置窗口在前时，⌘W 关闭设置窗口，其余文档命令不作用于它。
		if t.settings != nil && w == t.settings.win {
			if it.id == "close" {
				w.Close()
			}
			return
		}
		t.win.Update(func() { t.runCommand(it) })
	}}
}

// accelerator 把修饰键与按键写成 MyGo 菜单快捷键字符串，如“CmdOrCtrl+Shift+S”。
func accelerator(mods ui.Modifiers, key ui.Key) string {
	if key == 0 {
		return ""
	}
	var parts []string
	if mods&ui.Cmd != 0 {
		parts = append(parts, "CmdOrCtrl")
	}
	if mods&ui.Alt != 0 {
		parts = append(parts, "Alt")
	}
	if mods&ui.Shift != 0 {
		parts = append(parts, "Shift")
	}
	name, ok := keyNames[key]
	if !ok {
		return ""
	}
	return strings.Join(append(parts, name), "+")
}

var keyNames = func() map[ui.Key]string {
	m := map[ui.Key]string{ui.KeyBracketLeft: "[", ui.KeyBracketRight: "]", ui.KeyBackquote: "`", ui.KeyComma: ",",
		ui.KeyEqual: "=", ui.KeyMinus: "-", ui.KeyBackslash: "\\"}
	for k := ui.KeyA; k <= ui.KeyZ; k++ {
		m[k] = string(rune('A' + (k - ui.KeyA)))
	}
	for k := ui.Key0; k <= ui.Key9; k++ {
		m[k] = string(rune('0' + (k - ui.Key0)))
	}
	return m
}()

// shortcutText 返回命令 id 的快捷键显示文本（macOS 用 ⌘⌥⇧ 符号，其他平台如“Ctrl+Shift+O”）；没有快捷键时为空串。
func (s *Shell) shortcutText(id string) string {
	for _, g := range s.menuGroups() {
		for _, it := range g.items {
			if it.id != id || it.key == 0 {
				continue
			}
			name := keyNames[it.key]
			if runtime.GOOS == "darwin" {
				var b strings.Builder
				for _, m := range [...]struct {
					mod ui.Modifiers
					sym string
				}{{ui.Ctrl, "⌃"}, {ui.Alt, "⌥"}, {ui.Shift, "⇧"}, {ui.Cmd, "⌘"}} {
					if it.mods&m.mod != 0 {
						b.WriteString(m.sym)
					}
				}
				return b.String() + name
			}
			parts := []string{}
			for _, m := range [...]struct {
				mod  ui.Modifiers
				name string
			}{{ui.Cmd, "Ctrl"}, {ui.Alt, "Alt"}, {ui.Shift, "Shift"}} {
				if it.mods&m.mod != 0 {
					parts = append(parts, m.name)
				}
			}
			return strings.Join(append(parts, name), "+")
		}
	}
	return ""
}

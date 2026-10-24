package shell

import "testing"

// TestShortcutDisplay：macOS 上设置页显示 Cmd / Option，其他平台原样；输入的 Cmd、Command、⌘、Option 与连写符号
// 都转回原版存储格式（Ctrl / Alt）。
func TestShortcutDisplay(t *testing.T) {
	if got := shortcutDisplay("Ctrl+Alt+Shift+K", "darwin"); got != "Cmd+Option+Shift+K" {
		t.Errorf("macOS 显示 %q", got)
	}
	if got := shortcutDisplay("Ctrl+Shift+K", "windows"); got != "Ctrl+Shift+K" {
		t.Errorf("Windows 显示 %q", got)
	}
	for in, want := range map[string]string{
		"Cmd+Shift+K":        "Ctrl+Shift+K",
		"command + option+E": "Ctrl+Alt+E",
		"⌘⇧K":                "Ctrl+Shift+K",
		"⌘⌥T":                "Ctrl+Alt+T",
		"Ctrl+N":             "Ctrl+N",
	} {
		if got := shortcutStored(in); got != want {
			t.Errorf("shortcutStored(%q) = %q，应为 %q", in, got, want)
		}
	}
}

package shell

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/i18n"
	"nomo/desktop/ui/icons"

	// MyGo 自身的文字（系统菜单项、对话框按钮、查找栏、编辑右键菜单等）的中文翻译：没有 MyGo i18n 目录的应用须自行
	// 导入，否则这些文字只有英文（与 shell.applyAppLocale 声明的语言一致）。
	_ "github.com/egoist/mygo/locales/zh-Hans"
	_ "github.com/egoist/mygo/locales/zh-Hant"
)

// tr 返回界面文案。文案表在 messages.go（原由前端文案生成，前端移除后直接维护），
// 键名与原版 t.xxx() 一致；繁体缺失时回退简体，日文与其余缺失回退英文，再缺失返回键名。
// args 为占位符的名值对，替换 {name}。
func (s *Shell) tr(key string, args ...string) string {
	var v string
	var ok bool
	switch s.locale {
	case i18n.ZhCN:
		v, ok = msgZhCN[key]
	case i18n.ZhTW:
		if v, ok = msgZhTW[key]; !ok {
			v, ok = msgZhCN[key]
		}
	default:
		v, ok = msgEnUS[key]
	}
	if !ok {
		if v, ok = msgEnUS[key]; !ok {
			v = key
		}
	}
	for i := 0; i+1 < len(args); i += 2 {
		v = strings.ReplaceAll(v, "{"+args[i]+"}", args[i+1])
	}
	return v
}

// icon 返回界面图标（见 desktop/ui/icons）。
func (s *Shell) icon(name string) *ui.SVG { return icons.Get(name) }

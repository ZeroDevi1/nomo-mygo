package shell

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/i18n"
	"nomo/desktop/ui/icons"
)

// tr 返回界面文案。文案表由 scripts/generate-native-messages.mjs 从前端文案生成，
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

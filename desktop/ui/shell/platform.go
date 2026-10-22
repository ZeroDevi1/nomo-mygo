package shell

import (
	"runtime"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// platform 是外壳在各平台上的原生风格：布局结构、窗口材质与配色。
//
//   - macOS：系统菜单栏（窗口内不画菜单）、交通灯位于侧栏顶部、侧栏使用 vibrancy
//     材质（与 Finder、备忘录一致）、统一的标题栏工具区、13 pt 系统字体、系统强调色。
//   - Windows 11：标题栏内菜单（文件/编辑/格式/查看）、Mica 材质窗口、内容区为圆角
//     卡片、14 pt Segoe UI、4 px 圆角、系统强调色。
//   - Linux：Adwaita 风格的标题栏与配色，菜单在标题栏内。
type platform struct {
	mac, windows  bool
	menusInWindow bool
	titleH        float32
	vibrancy      mygo.Vibrancy
}

func currentPlatform() platform {
	switch runtime.GOOS {
	case "darwin":
		return platform{mac: true, titleH: 52, vibrancy: mygo.VibrancySidebar}
	case "windows":
		return platform{windows: true, menusInWindow: true, titleH: 40, vibrancy: mygo.VibrancyMica}
	}
	return platform{menusInWindow: true, titleH: 46}
}

// chrome 是外壳自己的配色（侧栏、标题栏、标签栏、内容区），与控件主题分开。
// material 为 true 时侧栏 / 标题栏留空让窗口材质透出。
type chrome struct {
	content, sidebar, titlebar, tabBar, tabActive, separator ui.Color
	hover, pressed, text, muted, accent                      ui.Color
	material                                                 bool
}

// platformTheme 返回当前平台、明暗下的控件主题；accent 为系统强调色（A 为 0 时用平台默认）。
func (pf platform) theme(dark bool, accent ui.Color) *ui.Theme {
	var t *ui.Theme
	if dark {
		t = ui.DarkTheme()
	} else {
		t = ui.LightTheme()
	}
	switch {
	case pf.mac:
		def := ui.Hex("#007AFF")
		if dark {
			def = ui.Hex("#0A84FF")
		}
		t.Radius, t.FontSize = 6, 13
		if dark {
			t.Background, t.Surface, t.SurfaceHover, t.SurfacePressed = ui.Hex("#1E1E1E"), ui.Hex("#3A3A3C"), ui.Hex("#48484A"), ui.Hex("#545456")
			t.Text, t.TextMuted, t.Border = ui.RGBA(255, 255, 255, 0.86), ui.RGBA(255, 255, 255, 0.55), ui.RGBA(255, 255, 255, 0.14)
		} else {
			t.Background, t.Surface, t.SurfaceHover, t.SurfacePressed = ui.Hex("#FFFFFF"), ui.Hex("#FFFFFF"), ui.Hex("#F2F2F2"), ui.Hex("#E5E5E5")
			t.Text, t.TextMuted, t.Border = ui.RGBA(0, 0, 0, 0.86), ui.RGBA(0, 0, 0, 0.5), ui.RGBA(0, 0, 0, 0.12)
		}
		setAccent(t, accent, def)
	case pf.windows:
		def := ui.Hex("#005FB8")
		if dark {
			def = ui.Hex("#60CDFF")
		}
		t.Radius, t.FontSize, t.Font = 4, 14, "Segoe UI Variable Text, Segoe UI"
		if dark {
			t.Background, t.Surface, t.SurfaceHover, t.SurfacePressed = ui.Hex("#272727"), ui.Hex("#2D2D2D"), ui.Hex("#323232"), ui.Hex("#272727")
			t.Text, t.TextMuted, t.Border = ui.Hex("#FFFFFF"), ui.RGBA(255, 255, 255, 0.79), ui.RGBA(255, 255, 255, 0.08)
		} else {
			t.Background, t.Surface, t.SurfaceHover, t.SurfacePressed = ui.Hex("#FFFFFF"), ui.Hex("#FBFBFB"), ui.Hex("#F6F6F6"), ui.Hex("#F0F0F0")
			t.Text, t.TextMuted, t.Border = ui.RGBA(0, 0, 0, 0.9), ui.RGBA(0, 0, 0, 0.6), ui.RGBA(0, 0, 0, 0.1)
		}
		setAccent(t, accent, def)
		if dark {
			t.AccentText = ui.Hex("#000000")
		}
	default:
		t.Radius = 6
		setAccent(t, accent, ui.Hex("#3584E4"))
	}
	return t
}

func setAccent(t *ui.Theme, accent, def ui.Color) {
	if accent.A == 0 {
		accent = def
	}
	t.Accent, t.AccentHover, t.AccentPressed = accent, accent, accent
	t.Focus = ui.RGBA(accent.R, accent.G, accent.B, 0.5)
	t.Selection = ui.RGBA(accent.R, accent.G, accent.B, 0.25)
}

// chrome 返回外壳配色；vibrancy 为窗口材质是否实际可用（c.Vibrancy()）。
func (pf platform) chrome(t *ui.Theme, vibrancy bool) chrome {
	ch := chrome{content: t.Background, text: t.Text, muted: t.TextMuted, accent: t.Accent, separator: t.Border, material: vibrancy}
	switch {
	case pf.mac && t.Dark:
		ch.sidebar, ch.titlebar, ch.tabBar, ch.tabActive = ui.Hex("#2A2A2C"), ui.Hex("#1E1E1E"), ui.Hex("#161616"), ui.Hex("#1E1E1E")
		ch.hover, ch.pressed = ui.RGBA(255, 255, 255, 0.08), ui.RGBA(255, 255, 255, 0.14)
	case pf.mac:
		ch.sidebar, ch.titlebar, ch.tabBar, ch.tabActive = ui.Hex("#F0F0F0"), ui.Hex("#FFFFFF"), ui.Hex("#E9E9E9"), ui.Hex("#FFFFFF")
		ch.hover, ch.pressed = ui.RGBA(0, 0, 0, 0.06), ui.RGBA(0, 0, 0, 0.1)
	case pf.windows && t.Dark:
		ch.sidebar, ch.titlebar, ch.tabBar, ch.tabActive = ui.Hex("#202020"), ui.Hex("#202020"), ui.Hex("#272727"), ui.Hex("#2D2D2D")
		ch.hover, ch.pressed = ui.RGBA(255, 255, 255, 0.06), ui.RGBA(255, 255, 255, 0.04)
	case pf.windows:
		ch.sidebar, ch.titlebar, ch.tabBar, ch.tabActive = ui.Hex("#F3F3F3"), ui.Hex("#F3F3F3"), ui.Hex("#FFFFFF"), ui.Hex("#F9F9F9")
		ch.hover, ch.pressed = ui.RGBA(0, 0, 0, 0.04), ui.RGBA(0, 0, 0, 0.03)
	case t.Dark:
		ch.sidebar, ch.titlebar, ch.tabBar, ch.tabActive = ui.Hex("#2E2E32"), ui.Hex("#2E2E32"), ui.Hex("#242424"), ui.Hex("#303030")
		ch.hover, ch.pressed = ui.RGBA(255, 255, 255, 0.07), ui.RGBA(255, 255, 255, 0.12)
	default:
		ch.sidebar, ch.titlebar, ch.tabBar, ch.tabActive = ui.Hex("#EBEBED"), ui.Hex("#EBEBED"), ui.Hex("#F6F5F4"), ui.Hex("#FFFFFF")
		ch.hover, ch.pressed = ui.RGBA(0, 0, 0, 0.06), ui.RGBA(0, 0, 0, 0.1)
	}
	return ch
}

// paint 返回区域背景：有窗口材质时侧栏与标题栏透明，让材质透出。
func (ch chrome) paint(c ui.Color, materialArea bool) ui.Color {
	if ch.material && materialArea {
		return ui.Transparent
	}
	return c
}

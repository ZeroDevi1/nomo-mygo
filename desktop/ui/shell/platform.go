package shell

import (
	"math"
	"runtime"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/config"
)

// platform 是外壳在各平台上的原生风格：布局结构、窗口材质与配色。
//
//   - macOS：系统菜单栏（窗口内不画菜单）、交通灯位于侧栏顶部、侧栏使用 vibrancy
//     材质（与 Finder、备忘录一致）、统一的标题栏工具区、13 pt 系统字体、系统强调色。
//   - Windows：Mica 用于标题栏和导航底层，内容区使用独立底色；不可用时使用对应的实色。
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
		return platform{windows: true, menusInWindow: true, titleH: 32, vibrancy: mygo.VibrancyMica}
	}
	return platform{menusInWindow: true, titleH: 46}
}

// chrome 是外壳自己的配色（侧栏、标题栏、标签栏、内容区），与控件主题分开。
// material 为 true 时侧栏 / 标题栏留空让窗口材质透出。
type chrome struct {
	content, sidebar, titlebar, tabBar, tabActive, separator ui.Color
	hover, pressed, selected, text, muted, accent            ui.Color
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
		t.Radius, t.FontSize, t.Font = 4, 14, "Segoe UI Variable Text, Segoe UI"
		if dark {
			def = ui.Hex("#60CDFF")
			t.Background, t.Surface, t.SurfaceHover, t.SurfacePressed = ui.Hex("#272727"), ui.Hex("#2D2D2D"), ui.Hex("#323232"), ui.Hex("#292929")
			t.Text, t.TextMuted, t.Border = ui.Hex("#FFFFFF"), ui.Hex("#C5C5C5"), ui.RGBA(255, 255, 255, 0.12)
		} else {
			t.Background, t.Surface, t.SurfaceHover, t.SurfacePressed = ui.Hex("#F9F9F9"), ui.Hex("#FFFFFF"), ui.Hex("#F6F6F6"), ui.Hex("#F0F0F0")
			t.Text, t.TextMuted, t.Border = ui.Hex("#1B1B1B"), ui.Hex("#606060"), ui.RGBA(0, 0, 0, 0.12)
		}
		setWindowsAccent(t, accent, def)
	default:
		t.Radius = 6
		setAccent(t, accent, ui.Hex("#3584E4"))
	}
	return t
}

// setWindowsAccent 保证强调色在内容底层和主按钮上均可读，包含悬停和按下状态。
func setWindowsAccent(t *ui.Theme, accent, fallback ui.Color) {
	luminance := func(c ui.Color) float64 {
		linear := func(v uint8) float64 {
			s := float64(v) / 255
			if s <= 0.04045 {
				return s / 12.92
			}
			return math.Pow((s+0.055)/1.055, 2.4)
		}
		return 0.2126*linear(c.R) + 0.7152*linear(c.G) + 0.0722*linear(c.B)
	}
	ink, tint := ui.RGB(255, 255, 255), ui.RGB(0, 0, 0)
	if t.Dark {
		ink, tint = tint, ink
	}
	if accent.A > 0 {
		accent.A = 255
		// 以当前明暗的内容底色为对比基准。按钮文字是同一方向的纯黑或纯白。
		background := luminance(t.Background) + 0.05
		original := accent
		for step := 1; step <= 6; step++ {
			brightness := luminance(accent) + 0.05
			if !t.Dark && background/brightness >= 4.5 || t.Dark && brightness/background >= 4.5 {
				break
			}
			accent = original.Mix(tint, float32(step)/10)
		}
	}
	setAccent(t, accent, fallback)
	t.AccentText = ink
	t.AccentHover, t.AccentPressed = t.Accent.Mix(tint, 0.08), t.Accent.Mix(tint, 0.16)
	t.Focus = t.Text
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
		ch.sidebar, ch.titlebar, ch.tabBar, ch.tabActive = ui.Hex("#202020"), ui.Hex("#202020"), ui.Hex("#202020"), t.Background
		ch.hover, ch.pressed, ch.selected = ui.RGBA(255, 255, 255, 0.06), ui.RGBA(255, 255, 255, 0.04), ui.RGBA(255, 255, 255, 0.10)
	case pf.windows:
		ch.sidebar, ch.titlebar, ch.tabBar, ch.tabActive = ui.Hex("#F3F3F3"), ui.Hex("#F3F3F3"), ui.Hex("#F3F3F3"), t.Background
		ch.hover, ch.pressed, ch.selected = ui.RGBA(0, 0, 0, 0.04), ui.RGBA(0, 0, 0, 0.03), ui.RGBA(0, 0, 0, 0.06)
	case t.Dark:
		ch.sidebar, ch.titlebar, ch.tabBar, ch.tabActive = ui.Hex("#2E2E32"), ui.Hex("#2E2E32"), ui.Hex("#242424"), ui.Hex("#303030")
		ch.hover, ch.pressed = ui.RGBA(255, 255, 255, 0.07), ui.RGBA(255, 255, 255, 0.12)
	default:
		ch.sidebar, ch.titlebar, ch.tabBar, ch.tabActive = ui.Hex("#EBEBED"), ui.Hex("#EBEBED"), ui.Hex("#F6F5F4"), ui.Hex("#FFFFFF")
		ch.hover, ch.pressed = ui.RGBA(0, 0, 0, 0.06), ui.RGBA(0, 0, 0, 0.1)
	}
	return ch
}

const windowsTabBarH = 40

func (ch chrome) windowsNavButton(c *ui.Context, icon *ui.SVG, label string, active, expanded bool) ui.Element {
	row := ui.ButtonBase(c).FillWidth().Height(40).Gap(12).Radius(4).
		Label(label).Tooltip(label).FocusRing(true).Selected(active).TextColor(ch.text)
	if expanded {
		row.Justify(ui.Start).Padding(0, 12)
	}
	switch {
	case row.Pressed():
		row.Background(ch.pressed)
	case active:
		row.Background(ch.selected)
	case row.Hovered():
		row.Background(ch.hover)
	}
	row.Children(func() {
		if active {
			ui.Box(c).Absolute().Left(0).Width(3).Top(12).Height(16).Radius(2).Background(ch.accent)
		}
		ui.Icon(c, icon).FontSize(20)
		if expanded {
			ui.Text(c, label).SingleLine().MinWidth(0).Shrink(1)
		}
	})
	return row
}

func (s *Shell) windowsAppTitle(c *ui.Context, tip string) {
	icon := s.appIcon[0]
	if c.Theme().Dark && s.appIcon[1] != nil {
		icon = s.appIcon[1]
	}
	if icon != nil {
		ui.Image(c, icon).Size(16, 16).Shrink(0)
	}
	ui.Text(c, config.AppName).FontSize(12).SingleLine().MinWidth(0).Shrink(1).Tooltip(tip)
}

func (ch chrome) windowsTab(box ui.Element, selected bool) {
	box.Radius(4, 4, 0, 0)
	if selected {
		box.Border(1, ch.separator).BorderWidth(1, 1, 0, 1)
	}
}

// paint 返回区域背景：有窗口材质时侧栏与标题栏透明，让材质透出。
func (ch chrome) paint(c ui.Color, materialArea bool) ui.Color {
	if ch.material && materialArea {
		return ui.Transparent
	}
	return c
}

const (
	scholarCompactWidth    = 1060
	scholarSinglePaneWidth = 1000 // 窗口最小宽度为 920，侧栏收起后也能进入单栏。
	scholarSplitWidth      = 620
)

func (pf platform) scholarNavHeight() float32 {
	if pf.windows {
		return 32
	}
	return 27
}

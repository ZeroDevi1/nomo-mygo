package i18n

import "testing"

func TestResolvesExplicitPreferences(t *testing.T) {
	cases := []struct {
		preference, system string
		want               Locale
	}{
		{"zh-CN", "en-US", ZhCN},
		{"zh-TW", "zh-CN", ZhTW},
		{"en-US", "zh-CN", EnUS},
		{"ja-JP", "zh-CN", JaJP},
		{"fr-FR", "zh-CN", EnUS},
		{"system", "zh-CN", ZhCN},
	}
	for _, c := range cases {
		if got := ResolvePreference(c.preference, c.system); got != c.want {
			t.Errorf("ResolvePreference(%q, %q) = %v, want %v", c.preference, c.system, got, c.want)
		}
	}
}

func TestResolvesSystemLocaleFamilies(t *testing.T) {
	cases := map[string]Locale{
		"zh-CN": ZhCN, "zh-Hans-CN": ZhCN, "zh_TW": ZhTW, "zh-Hant-HK": ZhTW,
		"en-US": EnUS, "ja": JaJP, "ja-JP": JaJP, "ja_JP": JaJP, "fr-FR": EnUS, "": EnUS,
	}
	for system, want := range cases {
		if got := ResolveSystemLocale(system); got != want {
			t.Errorf("ResolveSystemLocale(%q) = %v, want %v", system, got, want)
		}
	}
}

func TestReturnsLocalizedNativeChromeText(t *testing.T) {
	cases := []struct {
		locale Locale
		key    string
		want   string
	}{
		{ZhCN, "tray_open", "打开 Nomo"},
		{ZhTW, "tray_open", "開啟 Nomo"},
		{EnUS, "tray_open", "Open Nomo"},
		{JaJP, "tray_open", "Nomo を開く"},
		{ZhCN, "menu_chart_blank", "空白图表"},
		{ZhTW, "menu_chart_blank", "空白圖表"},
		{EnUS, "menu_chart_blank", "Blank Diagram"},
		{JaJP, "menu_chart_blank", "空白の図表"},
		{JaJP, "menu_file", "ファイル(&F)"},
		{JaJP, "settings_window_title", "環境設定 - Nomo"},
	}
	for _, c := range cases {
		if got := Text(c.locale, c.key); got != c.want {
			t.Errorf("Text(%v, %q) = %q, want %q", c.locale, c.key, got, c.want)
		}
	}
}

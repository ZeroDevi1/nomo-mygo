package shell

import (
	"fmt"
	"math"
	"runtime"
	"slices"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/i18n"
	"nomo/desktop/internal/imageasset"
)

// settingsWindow 是原生设置窗口，分类与文案对应原版 SettingsWindow.svelte。
//
// 只列出原生外壳已生效的设置；尚未实现的功能（行高、排版、代码块行号等）不展示无效的开关。
// 读写与主窗口共享的 Shell.prefs，键与 WebView 版相同。
// 生命周期：openSettings 创建，关闭窗口即销毁（Shell.settings 置空）。
type settingsWindow struct {
	s   *Shell
	win *mygo.Window
	cat string
	// pending 是拖动滑块期间暂存、松开后再写盘的设置键，避免每帧写 config.json。
	pending map[string]bool
	// drafts 是文本设置的编辑草稿（按设置键），失焦或回车时校验并提交；focused 记录上一帧焦点。
	drafts  map[string]*string
	focused map[string]bool
	invalid map[string]bool
	// confirmTheme 为删除用户主题的确认框；testing/testResult 为图床连接测试状态。
	confirmTheme bool
	testing      bool
	testResult   string
	// updating/update 为“关于”页检查更新的状态与结果文案；updateAvailable 时显示查看更新。
	updating        bool
	update          string
	updateAvailable bool
}

type settingsCategory struct{ id, label, icon string }

func (s *Shell) settingsCategories() []settingsCategory {
	return []settingsCategory{
		{"general", "settingsCategoryGeneral", "settings-2"},
		{"editor", "settingsCategoryEditor", "book-open-text"},
		{"appearance", "settingsCategoryAppearance", "palette"},
		{"files", "settingsCategoryFiles", "folder-open"},
		{"images", "settingsCategoryImages", "image"},
		{"stats", "settingsCategoryStats", "chart-column"},
		{"advanced", "settingsCategoryAdvanced", "sliders-horizontal"},
		{"about", "settingsCategoryAboutTitle", "info"},
	}
}

func newSettingsWindow(s *Shell) *settingsWindow {
	return &settingsWindow{s: s, cat: "general", pending: map[string]bool{}, drafts: map[string]*string{},
		focused: map[string]bool{}, invalid: map[string]bool{}}
}

// openSettings 打开设置窗口；已打开时前置。
func (s *Shell) openSettings() {
	if s.settings != nil {
		s.settings.win.Show()
		s.settings.win.Focus()
		return
	}
	sw := newSettingsWindow(s)
	sw.win = mygo.NewWindow(mygo.WindowOptions{
		Title: s.tr("settingsWindowTitle"), Width: 780, Height: 560, MinWidth: 640, MinHeight: 420,
		StateKey: "native-settings", TitleBarStyle: mygo.TitleBarHidden, Vibrancy: s.pf.vibrancy, Content: ui.View(sw.view),
	})
	sw.win.OnClosed(func() {
		if s.win != nil {
			s.win.Update(func() {
				sw.flush()
				s.settings = nil
			})
		}
	})
	s.settings = sw
}

func (sw *settingsWindow) view(c *ui.Context) {
	s := sw.s
	s.applyTheme(c)
	ch := s.pf.chrome(c.Theme(), c.Vibrancy())
	if ch.material {
		c.Root().Background(ui.Transparent)
	}
	bar := c.TitleBar()
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		// 左侧分类：macOS 为 vibrancy 源列表（交通灯在其顶部），Windows 为 Mica 上的导航列表。
		ui.Column(c).Width(200).FillHeight().Background(ch.paint(ch.sidebar, true)).Children(func() {
			ui.Row(c).Height(max(bar.Height, s.pf.titleH)).FillWidth().Padding(0, 12, 0, bar.Left+12).AlignItems(ui.Center).DragWindow().Children(func() {
				if !s.pf.mac {
					ui.Text(c, s.tr("settingsTitle")).FontSize(12).Bold()
				}
			})
			ui.Sidebar(c, &sw.cat, func() {
				for _, cat := range s.settingsCategories() {
					ui.SidebarItem(c, cat.id, s.icon(cat.icon), s.tr(cat.label))
				}
			}).Grow(1).FillWidth().Background(ui.Transparent)
		})
		ui.Box(c).Width(1).FillHeight().Background(ch.separator)
		ui.Column(c).Grow(1).Fill().Background(ch.content).Children(func() {
			ui.Row(c).Height(max(bar.Height, s.pf.titleH)).FillWidth().Padding(0, bar.Right+16, 0, 24).AlignItems(ui.Center).DragWindow().Children(func() {
				for _, cat := range s.settingsCategories() {
					if cat.id == sw.cat {
						ui.Text(c, s.tr(cat.label)).FontSize(15).Bold()
					}
				}
			})
			ui.Scroll(c).Grow(1).FillWidth().Children(func() {
				ui.Column(c).FillWidth().Padding(4, 28, 28, 28).Gap(20).Children(func() { sw.page(c) })
			})
		})
	})
	if !c.Root().Pressed() {
		sw.flush()
	}
}

// flush 写回拖动期间暂存的设置。
func (sw *settingsWindow) flush() {
	for key := range sw.pending {
		sw.s.setPref(key)
		delete(sw.pending, key)
	}
}

func (sw *settingsWindow) page(c *ui.Context) {
	s, p := sw.s, &sw.s.prefs
	switch sw.cat {
	case "general":
		sw.section(c, "basicBehavior", func() {
			sw.choice(c, "editorModeDefault", "editorMode", &p.EditorMode,
				[]string{modeSemantic, modeSource}, []string{s.tr("semanticEditing"), s.tr("sourceMode")})
			sw.toggle(c, "autoSave", "autoSaveEnabled", &p.AutoSaveEnabled)
			sw.slider(c, "autoSaveDelay", "autoSaveDelayMs", &p.AutoSaveDelayMs, 500, 5000, 100, "%.0f ms", !p.AutoSaveEnabled)
			sw.toggle(c, "createSnapshotBeforeSave", "createSnapshotBeforeSave", &p.CreateSnapshotBeforeSave)
			sw.choice(c, "interfaceLanguage", "interfaceLanguage", &p.InterfaceLanguage,
				[]string{"system", "zh-CN", "zh-TW", "en-US"}, []string{s.tr("themeSystem"), "简体中文", "繁體中文", "English"})
		})
	case "editor":
		sw.section(c, "editorScale", func() {
			sw.slider(c, "fontSize", "fontSize", &p.FontSize, 14, 22, 1, "%.0f px", false)
			sw.slider(c, "contentWidth", "contentWidthPercent", &p.ContentWidthPercent, 45, 90, 1, "%.0f%%", false)
			sw.choice(c, "calloutStyle", "documentStyleId", &p.DocumentStyleID,
				[]string{"nomo-modern", "nomo-classic"}, []string{s.tr("modern"), s.tr("classic")})
		})
		sw.section(c, "quickInsert", func() {
			sw.toggle(c, "quickInsertEnabled", "quickInsertEnabled", &p.QuickInsertEnabled)
			sw.text(c, "quickInsertTrigger", "quickInsertTrigger", &p.QuickInsertTrigger, "/", "quickInsertTriggerInvalid", !p.QuickInsertEnabled,
				func(v string) (string, bool) { v = strings.TrimSpace(v); return v, isQuickInsertTrigger(v) })
		})
		sw.section(c, "markdownLint", func() {
			sw.toggle(c, "markdownLintEnabled", "markdownLintEnabled", &p.MarkdownLintEnabled)
			sw.choiceDisabled(c, "markdownLintRules", "markdownLintRuleSet", &p.MarkdownLintRuleSet,
				[]string{"relaxed", "default"}, []string{s.tr("markdownLintRelaxed"), s.tr("markdownLintDefault")}, !p.MarkdownLintEnabled)
		})
	case "appearance":
		sw.section(c, "theme", func() {
			sw.choice(c, "themeMode", "themeMode", &p.ThemeMode,
				[]string{"system", "light", "dark"}, []string{s.tr("themeSystem"), s.tr("themeLight"), s.tr("themeDark")})
			sw.colorTheme(c)
		})
		sw.section(c, "fonts", func() {
			font := func(v string) (string, bool) {
				n := normalizeFontFamily(v)
				return n, n != "" || strings.TrimSpace(v) == ""
			}
			sw.text(c, "uiFontFamily", "uiFontFamily", &p.UIFontFamily, s.tr("fontFamilyPlaceholder"), "fontFamilyInvalid", false, font)
			sw.text(c, "documentFontFamily", "documentFontFamily", &p.DocumentFontFamily, s.tr("fontFamilyPlaceholder"), "fontFamilyInvalid", false, font)
			sw.text(c, "monoFontFamily", "monoFontFamily", &p.MonoFontFamily, s.tr("fontFamilyPlaceholder"), "fontFamilyInvalid", false, font)
		})
		sw.section(c, "visualZoom", func() {
			sw.slider(c, "zoomLevel", "zoomPercent", &p.ZoomPercent, 80, 160, 10, "%.0f%%", false)
		})
	case "files":
		sw.section(c, "filesAndWindows", func() {
			sw.toggle(c, "hideExplorerOnLaunch", "sidebarHidden", &p.SidebarHidden)
			sw.choice(c, "externalFileChangeBehavior", "externalFileChangeBehavior", &p.ExternalFileChangeBehavior,
				[]string{"reload-external", "overwrite-external", "ignore"},
				[]string{s.tr("externalFileChangeBehaviorReload"), s.tr("externalFileChangeBehaviorOverwrite"), s.tr("externalFileChangeBehaviorIgnore")})
		})
	case "images":
		img := &p.ImageHandlingSettings
		sw.section(c, "imageImport", func() {
			sw.choice(c, "imageHandlingStrategy", "imageHandlingSettings", &img.ImageInsertStrategy,
				[]string{"copy-current-folder", "copy-assets", "copy-document-assets", "upload"},
				[]string{s.tr("currentFolder"), s.tr("assetsFolder"), s.tr("documentAssetsFolder"), s.tr("upload")})
			if img.ImageInsertStrategy == "upload" {
				sw.choice(c, "uploadProvider", "imageHandlingSettings", &img.UploadProvider,
					[]string{"picgo", "picgo-core"}, []string{"PicGo", "PicGo-Core"})
				if img.UploadProvider == "picgo-core" {
					sw.text(c, "picgoCoreCommand", "imageHandlingSettings", &img.PicgoCoreCommand, "picgo", "", false, trimmed)
					sw.text(c, "picgoCoreConfigPath", "imageHandlingSettings", &img.PicgoCoreConfigPath, "", "", false, trimmed)
				} else {
					sw.text(c, "picgoServerUrl", "imageHandlingSettings", &img.PicgoServerURL, "http://127.0.0.1:36677/upload", "", false, trimmed)
				}
				sw.connectionTest(c)
			}
			sw.text(c, "imageDefaultWidth", "imageHandlingSettings", &img.DefaultImageWidth, s.tr("emptyPlaceholder"), "", false, trimmed)
			sw.choice(c, "imageDefaultAlign", "imageHandlingSettings", &img.DefaultImageAlign,
				[]string{"none", "left", "center", "right"},
				[]string{s.tr("followText"), s.tr("alignLeft"), s.tr("alignCenter"), s.tr("alignRight")})
		})
	case "advanced":
		sw.section(c, "renderingMode", func() {
			sw.choice(c, "renderMode", "renderMode", &p.RenderMode,
				[]string{"hardware", "software"}, []string{s.tr("renderModeHardware"), s.tr("renderModeSoftware")})
		})
		sw.section(c, "customShortcuts", func() { sw.shortcuts(c) })
	case "stats":
		sw.section(c, "statsAndNavigation", func() {
			sw.toggle(c, "showDocumentOutline", "outlineVisible", &p.OutlineVisible)
			sw.toggle(c, "showDocumentStats", "writingStatsVisible", &p.WritingStatsVisible)
			sw.choice(c, "defaultStatsMetric", "writingStatsMetric", &p.WritingStatsMetric,
				[]string{"words", "chars", "lines"}, []string{s.tr("words"), s.tr("chars"), s.tr("lines")})
		})
	case "about":
		ui.Column(c).Gap(6).Children(func() {
			icon := s.appIcon[0]
			if c.Theme().Dark && s.appIcon[1] != nil {
				icon = s.appIcon[1]
			}
			if icon != nil {
				ui.Image(c, icon).Size(72, 72)
			}
			ui.Text(c, "Nomo").FontSize(20).Bold()
			ui.Text(c, fmt.Sprintf("%s %s · %s/%s", s.tr("version"), s.version, runtime.GOOS, runtime.GOARCH)).TextColor(c.Theme().TextMuted)
			ui.Text(c, s.tr("positioningDescription")).Wrap().TextColor(c.Theme().TextMuted)
		})
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, s.tr("githubRepository")).Clicked() {
				c.Services().OpenURL("https://github.com/nomo-md/nomo")
			}
			if ui.Button(c, s.tr("reportIssue")).Clicked() {
				c.Services().OpenURL("https://github.com/nomo-md/nomo/issues")
			}
		})
		if s.checkUpdate != nil {
			sw.updateRow(c)
		}
	}
}

// section 是一组设置：分组标题 + macOS 风格的两列表单（标签右对齐）。
func (sw *settingsWindow) section(c *ui.Context, title string, fn func()) {
	ui.Column(c).FillWidth().Gap(10).Children(func() {
		ui.Text(c, sw.s.tr(title)).FontSize(12).Bold().TextColor(c.Theme().TextMuted)
		ui.Form(c, fn)
	})
}

func (sw *settingsWindow) describe(f ui.Element, label string) {
	if d := sw.s.tr(label + "Description"); d != label+"Description" {
		f.Description(d)
	}
}

func (sw *settingsWindow) toggle(c *ui.Context, label, key string, v *bool) {
	f := ui.Field(c.Key(key), sw.s.tr(label), func() {
		if ui.Switch(c, v).Changed() {
			sw.s.setPref(key)
		}
	})
	sw.describe(f, label)
}

// choice 是枚举设置：values 为存储值，labels 为显示文案。
func (sw *settingsWindow) choice(c *ui.Context, label, key string, v *string, values, labels []string) {
	sw.choiceDisabled(c, label, key, v, values, labels, false)
}

func (sw *settingsWindow) choiceDisabled(c *ui.Context, label, key string, v *string, values, labels []string, disabled bool) {
	cur := labels[0]
	for i, val := range values {
		if val == *v {
			cur = labels[i]
		}
	}
	f := ui.Field(c.Key(label), sw.s.tr(label), func() {
		if ui.Select(c, &cur, labels).Width(220).Disabled(disabled).Changed() {
			for i, l := range labels {
				if l == cur {
					*v = values[i]
				}
			}
			sw.s.setPref(key)
			if key == "interfaceLanguage" {
				sw.s.locale = i18n.ResolvePreference(*v, sw.s.systemLocale)
				sw.s.installMenu()
			}
		}
	})
	sw.describe(f, label)
}

// slider 是数值设置，按 step 取整；拖动中只改内存，松开后写盘（见 flush）。
func (sw *settingsWindow) slider(c *ui.Context, label, key string, v *float64, lo, hi, step float64, format string, disabled bool) {
	f := ui.Field(c.Key(key), sw.s.tr(label), func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			if ui.Slider(c, v, lo, hi).Width(220).Disabled(disabled).Changed() {
				*v = math.Round(*v/step) * step
				sw.pending[key] = true
				if sw.s.win != nil {
					sw.s.win.Update(func() {})
				}
			}
			ui.Text(c, fmt.Sprintf(format, *v)).FontSize(12).TextColor(c.Theme().TextMuted).Width(64)
		})
	})
	sw.describe(f, label)
}

func trimmed(v string) (string, bool) { return strings.TrimSpace(v), true }

// text 是文本设置：编辑草稿，失焦或回车时用 validate 规范化并提交；不合法时保留草稿并显示
// invalidKey 对应的错误文案，不写盘。
func (sw *settingsWindow) text(c *ui.Context, label, key string, v *string, placeholder, invalidKey string, disabled bool,
	validate func(string) (string, bool)) {
	id := label
	d, ok := sw.drafts[id]
	if !ok {
		d = new(string)
		*d = *v
		sw.drafts[id] = d
	}
	f := ui.Field(c.Key(id), sw.s.tr(label), func() {
		in := ui.TextInput(c, d).Width(260).Placeholder(placeholder).Disabled(disabled)
		focused := in.Focused()
		if !focused && !sw.focused[id] && *d != *v && !sw.invalid[id] {
			*d = *v // 外部修改（如导入设置）时同步草稿
		}
		if in.Submitted() || (sw.focused[id] && !focused) {
			if n, ok := validate(*d); ok {
				sw.invalid[id] = false
				*d = n
				if n != *v {
					*v = n
					sw.s.setPref(key)
				}
			} else {
				sw.invalid[id] = true
			}
		}
		sw.focused[id] = focused
	})
	sw.describe(f, label)
	if sw.invalid[id] && invalidKey != "" {
		f.Error(sw.s.tr(invalidKey))
	}
}

// colorTheme 是颜色主题选择与用户主题的导入、导出、删除。
func (sw *settingsWindow) colorTheme(c *ui.Context) {
	s := sw.s
	names := make([]string, len(s.themes))
	cur := s.currentTheme()
	sel := ""
	for i, t := range s.themes {
		names[i] = t.Name(s.locale.Tag())
		if t == cur {
			sel = names[i]
		}
	}
	ui.Field(c.Key("colorTheme"), s.tr("colorTheme"), func() {
		ui.Column(c).Gap(8).Children(func() {
			if ui.Select(c, &sel, names).Width(220).Changed() {
				if i := slices.Index(names, sel); i >= 0 {
					s.prefs.ColorThemeID = s.themes[i].ID
					s.setPref("colorThemeId")
				}
			}
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, s.tr("importUserTheme")).Clicked() {
					s.importTheme()
				}
				if ui.Button(c, s.tr("exportThemeTemplate")).Clicked() {
					s.exportTheme()
				}
				if !cur.Builtin && ui.Button(c, s.tr("deleteUserTheme")).Clicked() {
					sw.confirmTheme = true
				}
			})
		})
	})
	if ui.AlertDialog(c, &sw.confirmTheme, s.tr("confirmDelete"), s.tr("deleteUserTheme")+"："+cur.Name(s.locale.Tag()),
		s.tr("cancel"), s.tr("delete")) == 1 {
		s.deleteTheme()
	}
}

// connectionTest 测试图床连接（后台运行，结果显示在按钮旁）。
func (sw *settingsWindow) connectionTest(c *ui.Context) {
	s := sw.s
	f := ui.Field(c.Key("connectionTest"), s.tr("connectionTest"), func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			label := s.tr("testConnection")
			if sw.testing {
				label = s.tr("testing")
			}
			if ui.Button(c, label).Disabled(sw.testing).Clicked() {
				sw.testing, sw.testResult = true, ""
				img := s.prefs.ImageHandlingSettings
				go func() {
					msg, err := imageasset.TestConnection(img.UploadProvider, img.PicgoServerURL, img.PicgoCoreCommand)
					if err != nil {
						msg = err.Error()
					}
					s.postToSettings(func() {
						sw.testing, sw.testResult = false, msg
						if sw.win != nil {
							sw.win.Update(func() {})
						}
					})
				}()
			}
			if sw.testResult != "" {
				ui.Text(c, sw.testResult).FontSize(12).Wrap().TextColor(c.Theme().TextMuted).Shrink(1)
			}
		})
	})
	sw.describe(f, "connectionTest")
}

// shortcuts 是可自定义快捷键列表（格式同原版，如 Ctrl+Shift+K，Ctrl 在 macOS 上为 Command）；
// 提交后重建菜单使新快捷键生效。
func (sw *settingsWindow) shortcuts(c *ui.Context) {
	s := sw.s
	rows := []struct{ id, label string }{
		{"new-file", "newMarkdown"}, {"open-file", "openFile"}, {"save-file", "save"},
		{"toggle-source", "toggleSourceMode"}, {"toggle-theme", "toggleThemeLightDark"},
		{"toggle-focus", "showHideExplorer"}, {"toggle-toolbar", "showHideToolbar"},
		{"insert-code-block", "insertCodeBlock"}, {"insert-table", "insertTable"},
		{"insert-math-block", "insertMathBlock"}, {"menu-link", "editLink"}, {"menu-clear-format", "clearStyle"},
	}
	for _, r := range rows {
		v := s.prefs.ShortcutPreferences[r.id]
		before := v
		sw.text(c, r.label, "shortcutPreferences", &v, defaultShortcuts[r.id], "", false, func(t string) (string, bool) {
			// 留空恢复默认；格式不合法时不提交。
			if t = strings.TrimSpace(t); t == "" {
				return defaultShortcuts[r.id], true
			}
			return normalizeShortcuts(map[string]string{r.id: t})[r.id], shortcutPattern.MatchString(t)
		})
		if v != before {
			s.prefs.ShortcutPreferences[r.id] = v
			s.setPref("shortcutPreferences")
			s.installMenu()
		}
	}
}

// updateRow 是“关于”页的检查更新：后台检查，有新版本时提供打开发布页的按钮（原生外壳暂不内置
// 下载安装，安装包从发布页获取）。
func (sw *settingsWindow) updateRow(c *ui.Context) {
	s := sw.s
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		label := s.tr("softwareUpdateCheckNow")
		if sw.updating {
			label = s.tr("softwareUpdateCheckingShort")
		}
		if ui.Button(c, label).Disabled(sw.updating).Clicked() {
			sw.updating, sw.update, sw.updateAvailable = true, "", false
			go func() {
				info, err := s.checkUpdate()
				s.postToSettings(func() {
					sw.updating = false
					switch {
					case err != nil:
						sw.update = s.tr("softwareUpdateFailed") + " " + err.Error()
					case !info.Supported:
						sw.update = s.tr("softwareUpdateUnsupported")
					case info.Available:
						sw.update, sw.updateAvailable = s.tr("softwareUpdateReleaseTitle", "version", strings.TrimPrefix(info.Version, "v")), true
					default:
						sw.update = s.tr("softwareUpdateUpToDate")
					}
					if sw.win != nil {
						sw.win.Update(func() {})
					}
				})
			}()
		}
		if sw.updateAvailable && ui.Button(c, s.tr("softwareUpdateViewDetails")).Clicked() {
			c.Services().OpenURL("https://github.com/nomo-md/nomo/releases/latest")
		}
		if sw.update != "" {
			ui.Text(c, sw.update).FontSize(12).Wrap().TextColor(c.Theme().TextMuted).Shrink(1)
		}
	})
}

package shell

import (
	"fmt"
	"math"
	"nomo/desktop/providers"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/config"
	"nomo/desktop/internal/i18n"
	"nomo/desktop/internal/imageasset"
)

// codeLanguagePattern 限制代码块默认语言为信息串可用的字符（空为不标语言）。
var codeLanguagePattern = regexp.MustCompile(`^[A-Za-z0-9_+#.-]{0,32}$`)

// settingsWindow 是原生设置窗口，分类与文案对应原版 SettingsWindow.svelte。
//
// 只列出原生外壳已生效的设置；未实现的功能不展示无效的开关。
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
	win32        winState // “Windows 集成”分组，见 winintegration.go
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

// openSettings 在 Windows Scholar 中打开设置页，其他窗口使用独立的设置窗口。
func (s *Shell) openSettings() {
	if p := s.scholarForTab(s.activeTab()); s.pf.windows && p != nil {
		p.accountOpen, p.winRailPopup, p.winCompactProject = false, false, false
		if p.winSettings == nil {
			p.winSettings = newSettingsWindow(s)
			p.winSettings.win = s.win
		}
		return
	}
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
	ch := s.pf.chrome(c.Theme(), c.Vibrancy() && !(s.pf.windows && c.Preferences().HighContrast))
	if ch.material {
		c.Root().Background(ui.Transparent)
	}
	bar := c.TitleBar()
	if s.pf.windows {
		sw.windowsView(c, ch)
	} else {
		ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
			// macOS 的分类侧栏顶部预留交通灯位置。
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
	}
	if !c.Root().Pressed() {
		sw.flush()
	}
}

func (sw *settingsWindow) windowsView(c *ui.Context, ch chrome) {
	s := sw.s
	bar := c.TitleBar()
	ui.Column(c).Fill().Background(ch.paint(ch.titlebar, true)).Children(func() {
		ui.Row(c).Height(max(bar.Height, s.pf.titleH)).FillWidth().Padding(0, bar.Right+8, 0, bar.Left+16).
			AlignItems(ui.Center).DragWindow().Children(func() {
			ui.Text(c, s.tr("settingsTitle")).FontSize(12).SingleLine()
		})
		ui.Row(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
			ui.Column(c).Width(196).Shrink(0).FillHeight().Padding(8).Gap(4).Children(func() {
				for _, cat := range s.settingsCategories() {
					if ch.windowsNavButton(c.Key(cat.id), s.icon(cat.icon), s.tr(cat.label), sw.cat == cat.id, true).Clicked() {
						sw.cat = cat.id
					}
				}
			})
			ui.Column(c).Grow(1).FillHeight().MinWidth(0).MinHeight(0).Radius(8, 0, 0, 0).
				Border(1, ch.separator).BorderWidth(1, 0, 0, 1).Clip().Background(ch.content).Children(func() {
				sw.windowsContent(c, func() {
					for _, cat := range s.settingsCategories() {
						if cat.id == sw.cat {
							ui.Text(c, s.tr(cat.label)).FontSize(28).FontWeight(600).SingleLine()
						}
					}
				})
			})
		})
	})
}

func (sw *settingsWindow) windowsContent(c *ui.Context, header func()) {
	const maxWidth = 1000
	ui.Column(c).Grow(1).FillWidth().MinHeight(0).AlignItems(ui.Center).Background(c.Theme().Background).Children(func() {
		ui.Column(c).MaxWidth(maxWidth).FillWidth().Padding(20, 24, 16, 24).Children(header)
		ui.Scroll(c.Key(sw.cat)).Grow(1).FillWidth().MinHeight(0).Children(func() {
			ui.Column(c).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Column(c).MaxWidth(maxWidth).FillWidth().Padding(4, 24, 24, 24).Gap(24).Children(func() { sw.page(c) })
			})
		})
	})
}

// flush 写回拖动期间暂存的设置。
func (sw *settingsWindow) flush() {
	for key := range sw.pending {
		sw.s.setPref(key)
		delete(sw.pending, key)
	}
}

func (sw *settingsWindow) page(c *ui.Context) {
	s, p := sw.s, sw.s.prefs
	switch sw.cat {
	case "general":
		sw.section(c, "basicBehavior", func() {
			sw.choice(c, "editorModeDefault", "editorMode", &p.EditorMode,
				[]string{modeSemantic, modeSource}, []string{s.tr("semanticEditing"), s.tr("sourceMode")})
			sw.toggle(c, "autoSave", "autoSaveEnabled", &p.AutoSaveEnabled)
			sw.slider(c, "autoSaveDelay", "autoSaveDelayMs", &p.AutoSaveDelayMs, 500, 5000, 100, "%.0f ms", !p.AutoSaveEnabled)
			sw.toggle(c, "createSnapshotBeforeSave", "createSnapshotBeforeSave", &p.CreateSnapshotBeforeSave)
			sw.choice(c, "interfaceLanguage", "interfaceLanguage", &p.InterfaceLanguage,
				[]string{"system", "zh-CN", "zh-TW", "en-US", "ja-JP"}, []string{s.tr("themeSystem"), "简体中文", "繁體中文", "English", "日本語"})
		})
	case "editor":
		sw.section(c, "typesetProviders", func() {
			for pr := range providers.All() {
				enabled := !p.DisabledProviders[pr.ID]
				sw.setting(c.Key("provider."+pr.ID), pr.Name, "", func() ui.Element {
					toggle := ui.Switch(c, &enabled)
					if toggle.Changed() {
						if enabled {
							delete(p.DisabledProviders, pr.ID)
						} else {
							if p.DisabledProviders == nil {
								p.DisabledProviders = make(map[string]bool)
							}
							p.DisabledProviders[pr.ID] = true
						}
						s.setPref("disabledProviders")
					}
					return toggle
				})
			}
			sw.toggle(c, "providerSyntax", "providerSyntax", &p.ProviderSyntax)
			sw.toggle(c, "providerPreview", "providerPreview", &p.ProviderPreview)
		})
		sw.section(c, "typographySettings", func() {
			sw.toggle(c, "typographyEnabled", "typographyEnabled", &p.TypographyEnabled)
		})
		sw.section(c, "editorScale", func() {
			sw.slider(c, "fontSize", "fontSize", &p.FontSize, 14, 22, 1, "%.0f px", false)
			sw.slider(c, "lineHeight", "lineHeight", &p.LineHeight, 1.4, 2.1, 0.05, "%.2f", false)
			sw.choice(c, "defaultIndent", "codeBlockIndent", &p.CodeBlockIndent,
				[]string{"spaces-2", "spaces-4", "tab"}, []string{s.tr("twoSpaces"), s.tr("fourSpaces"), "Tab"})
			sw.toggle(c, "codeBlockLineNumbers", "codeBlockLineNumbersVisible", &p.CodeBlockLineNumbersVisible)
			sw.toggle(c, "inlineCodeRendering", "inlineCodeRenderingEnabled", &p.InlineCodeRenderingEnabled)
			sw.toggle(c, "copyMarkdownSyntax", "copyMarkdownSyntaxEnabled", &p.CopyMarkdownSyntaxEnabled)
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
			sw.toggle(c, "ctrlWheelZoom", "ctrlWheelZoomEnabled", &p.CtrlWheelZoomEnabled)
		})
	case "files":
		sw.section(c, "filesAndWindows", func() {
			sw.toggle(c, "hideExplorerOnLaunch", "sidebarHidden", &p.SidebarHidden)
			sw.toggle(c, "filePreviewTab", "filePreviewEnabled", &p.FilePreviewEnabled)
			sw.choice(c, "externalFileChangeBehavior", "externalFileChangeBehavior", &p.ExternalFileChangeBehavior,
				[]string{"reload-external", "overwrite-external", "ignore"},
				[]string{s.tr("externalFileChangeBehaviorReload"), s.tr("externalFileChangeBehaviorOverwrite"), s.tr("externalFileChangeBehaviorIgnore")})
		})
		sw.section(c, "pdfExportSettings", func() {
			sw.choice(c, "pdfPaperSize", "pdfPaperSize", &p.PDFPaperSize, []string{"A4", "A5", "Letter", "Legal"}, []string{"A4", "A5", "Letter", "Legal"})
			sw.choice(c, "pdfOrientation", "pdfOrientation", &p.PDFOrientation, []string{"portrait", "landscape"},
				[]string{s.tr("pdfPortrait"), s.tr("pdfLandscape")})
			sw.slider(c, "pdfMargin", "pdfMarginMm", &p.PDFMarginMm, 10, 40, 1, "%.0f mm", false)
			sw.text(c, "pdfHeader", "pdfHeader", &p.PDFHeader, "{title}", "", false, trimmed)
			sw.text(c, "pdfFooter", "pdfFooter", &p.PDFFooter, "{date}", "", false, trimmed)
			sw.choice(c, "pdfPageNumbers", "pdfPageNumbers", &p.PDFPageNumbers, []string{"none", "center", "right"},
				[]string{s.tr("pdfPageNumbersNone"), s.tr("pdfPageNumbersCenter"), s.tr("pdfPageNumbersRight")})
			sw.toggle(c, "pdfBreakBeforeH1", "pdfBreakBeforeH1", &p.PDFBreakBeforeH1)
		})
		sw.windowsIntegration(c)
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
		sw.section(c, "defaultInsertBehavior", func() {
			sw.text(c, "defaultCodeBlockLanguage", "defaultCodeBlockLanguage", &p.DefaultCodeBlockLanguage, "ts", "", false,
				func(v string) (string, bool) { v = strings.TrimSpace(v); return v, codeLanguagePattern.MatchString(v) })
			labels := make([]string, len(diagramLabels))
			for k, key := range diagramLabels {
				labels[k] = s.tr(key)
			}
			sw.choice(c, "defaultDiagramType", "defaultDiagramType", &p.DefaultDiagramType, diagramTypes, labels)
		})
		sw.section(c, "customShortcuts", func() { sw.shortcuts(c) })
	case "stats":
		sw.section(c, "statsAndNavigation", func() {
			sw.toggle(c, "showDocumentOutline", "outlineVisible", &p.OutlineVisible)
			sw.toggle(c, "showDocumentStats", "writingStatsVisible", &p.WritingStatsVisible)
			sw.choice(c, "defaultStatsMetric", "writingStatsMetric", &p.WritingStatsMetric,
				[]string{"words", "chars", "lines"}, []string{s.tr("words"), s.tr("chars"), s.tr("lines")})
			sw.toggle(c, "readingTime", "readingTimeVisible", &p.ReadingTimeVisible)
			sw.slider(c, "outlineDefaultExpandLevel", "outlineDefaultExpandLevel", &p.OutlineDefaultExpandLevel, 1, 6, 1, "H%.0f", false)
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
			ui.Text(c, config.AppName).FontSize(20).Bold()
			ui.Text(c, fmt.Sprintf("%s %s · %s/%s", s.tr("version"), s.version, runtime.GOOS, runtime.GOARCH)).TextColor(c.Theme().TextMuted)
			ui.Text(c, s.tr("positioningDescription")).Wrap().TextColor(c.Theme().TextMuted)
			ui.Text(c, s.tr("mygoEditionDescription")).Wrap().TextColor(c.Theme().TextMuted)
		})
		// 本版本（MyGo 重写）的仓库与问题反馈，以及原版 Nomo（Tauri）的仓库。
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, s.tr("mygoRepository")).Clicked() {
				c.Services().OpenURL(mygoRepoURL)
			}
			if ui.Button(c, s.tr("originalRepository")).Clicked() {
				c.Services().OpenURL(originalRepoURL)
			}
			if ui.Button(c, s.tr("reportIssue")).Clicked() {
				c.Services().OpenURL(mygoRepoURL + "/issues")
			}
		})
		if s.checkUpdate != nil {
			sw.updateRow(c)
		}
	}
}

// 关于页的仓库地址：本版本（Go + MyGo 重写）与原版 Nomo（Tauri）。
const (
	mygoRepoURL     = "https://github.com/ZeroDevi1/nomo-mygo"
	originalRepoURL = "https://github.com/nomo-md/nomo"
)

// section 在 Windows 上用分组标题和独立设置卡片，其他平台用两列表单。
func (sw *settingsWindow) section(c *ui.Context, title string, fn func()) {
	if sw.s.pf.windows {
		ui.Column(c).FillWidth().Gap(4).Children(func() {
			ui.Text(c, sw.s.tr(title)).FontWeight(600).Margin(0, 0, 4, 0)
			fn()
		})
		return
	}
	ui.Column(c).FillWidth().Gap(10).Children(func() {
		ui.Text(c, sw.s.tr(title)).FontSize(12).Bold().TextColor(c.Theme().TextMuted)
		ui.Form(c, fn)
	})
}

// setting 在宽卡片内把操作放在右侧，空间不足时随说明换行。
func (sw *settingsWindow) setting(c *ui.Context, label, description string, control func() ui.Element) ui.Element {
	if !sw.s.pf.windows {
		return ui.Field(c, label, func() { control() }).Description(description)
	}
	t := c.Theme()
	var input ui.Element
	ui.Row(c).FillWidth().MinHeight(64).Wrap().Padding(12, 16).GapX(20).GapY(8).
		AlignItems(ui.Center).Radius(4).Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Column(c).Basis(240).Grow(1).MinWidth(0).Gap(4).Children(func() {
			ui.Text(c, label).Wrap()
			if description != "" {
				ui.Text(c, description).FontSize(12).TextColor(t.TextMuted).Wrap()
			}
		})
		ui.Column(c).Shrink(0).Gap(4).Children(func() {
			input = control().Label(label).Description(description)
		})
	})
	return input
}

func (sw *settingsWindow) settingsPanel(c *ui.Context, label string, controls func()) ui.Element {
	field := ui.Field(c, label, controls)
	if sw.s.pf.windows {
		field.FillWidth().Padding(12, 16).Radius(4).Background(c.Theme().Surface).Border(1, c.Theme().Border)
	}
	return field
}

// descriptionKeys 是说明文案键不等于“标题键 + Description”的设置（沿用原版文案键）。
var descriptionKeys = map[string]string{"typographyEnabled": "typographyDescription"}

func (sw *settingsWindow) description(label string) string {
	key := label + "Description"
	if k, ok := descriptionKeys[label]; ok {
		key = k
	}
	if d := sw.s.tr(key); d != key {
		return d
	}
	return ""
}

func (sw *settingsWindow) toggle(c *ui.Context, label, key string, v *bool) {
	sw.setting(c.Key(key), sw.s.tr(label), sw.description(label), func() ui.Element {
		toggle := ui.Switch(c, v)
		if toggle.Changed() {
			sw.s.setPref(key)
		}
		return toggle
	})
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
	sw.setting(c.Key(label), sw.s.tr(label), sw.description(label), func() ui.Element {
		selectBox := ui.Select(c, &cur, labels).Width(220).Disabled(disabled)
		if selectBox.Changed() {
			for i, l := range labels {
				if l == cur {
					*v = values[i]
				}
			}
			sw.s.setPref(key)
			if key == "interfaceLanguage" {
				sw.s.locale = i18n.ResolvePreference(*v, sw.s.systemLocale)
				applyAppLocale(*v)
				sw.s.installMenu()
			}
		}
		return selectBox
	})
}

// slider 是数值设置，按 step 取整；拖动中只改内存，松开后写盘（见 flush）。
func (sw *settingsWindow) slider(c *ui.Context, label, key string, v *float64, lo, hi, step float64, format string, disabled bool) {
	sw.setting(c.Key(key), sw.s.tr(label), sw.description(label), func() ui.Element {
		width, valueWidth := float32(220), float32(64)
		if sw.s.pf.windows {
			width, valueWidth = 154, 56
		}
		var slider ui.Element
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			slider = ui.Slider(c, v, lo, hi).Width(width).Disabled(disabled)
			if slider.Changed() {
				*v = math.Round(*v/step) * step
				sw.pending[key] = true
				if sw.s.win != nil {
					sw.s.win.Update(func() {})
				}
			}
			value := ui.Text(c, fmt.Sprintf(format, *v)).FontSize(12).TextColor(c.Theme().TextMuted).Width(valueWidth)
			if sw.s.pf.windows {
				value.TextAlign(ui.End)
			}
		})
		return slider
	})
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
	f := sw.setting(c.Key(id), sw.s.tr(label), sw.description(label), func() ui.Element {
		width := float32(260)
		if sw.s.pf.windows {
			width = 220
		}
		in := ui.TextInput(c, d).Width(width).Placeholder(placeholder).Disabled(disabled)
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
		if sw.s.pf.windows && sw.invalid[id] && invalidKey != "" {
			ui.Text(c, sw.s.tr(invalidKey)).FontSize(12).TextColor(c.Theme().Danger).MaxWidth(width).Wrap()
		}
		return in
	})
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
	sw.settingsPanel(c.Key("colorTheme"), s.tr("colorTheme"), func() {
		ui.Column(c).Gap(8).Children(func() {
			if ui.Select(c, &sel, names).Width(220).Changed() {
				if i := slices.Index(names, sel); i >= 0 {
					s.prefs.ColorThemeID = s.themes[i].ID
					s.setPref("colorThemeId")
				}
			}
			actions := ui.Row(c).Gap(8)
			if s.pf.windows {
				actions.FillWidth().Wrap()
			}
			actions.Children(func() {
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
	f := sw.settingsPanel(c.Key("connectionTest"), s.tr("connectionTest"), func() {
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
	f.Description(sw.description("connectionTest"))
}

// shortcuts 是可自定义快捷键列表；提交后重建菜单使新快捷键生效。存储格式同原版（如 Ctrl+Shift+K，Ctrl 在 macOS 上
// 即 Command），设置页按平台显示与输入：macOS 上显示为 Cmd+Shift+K、Option，也接受 ⌘⌥⇧ 符号（见 shortcutDisplay）。
func (sw *settingsWindow) shortcuts(c *ui.Context) {
	s := sw.s
	rows := []struct{ id, label string }{
		{"new-file", "newMarkdown"}, {"open-file", "openFile"}, {"save-file", "save"},
		{"toggle-source", "toggleSourceMode"}, {"toggle-theme", "toggleThemeLightDark"},
		{"toggle-focus", "showHideExplorer"}, {"toggle-toolbar", "showHideToolbar"},
		{"insert-code-block", "insertCodeBlock"}, {"insert-table", "insertTable"},
		{"insert-math-block", "insertMathBlock"}, {"menu-link", "editLink"}, {"menu-clear-format", "clearStyle"},
		{"apply-text-color", "applyTextColor"},
	}
	for _, r := range rows {
		v := shortcutDisplay(s.prefs.ShortcutPreferences[r.id], runtime.GOOS)
		before := v
		sw.text(c, r.label, "shortcutPreferences", &v, shortcutDisplay(defaultShortcuts[r.id], runtime.GOOS), "", false, func(t string) (string, bool) {
			// 留空恢复默认；格式不合法时不提交。
			if t = strings.TrimSpace(t); t == "" {
				return shortcutDisplay(defaultShortcuts[r.id], runtime.GOOS), true
			}
			t = shortcutStored(t)
			return shortcutDisplay(normalizeShortcuts(map[string]string{r.id: t})[r.id], runtime.GOOS), shortcutPattern.MatchString(t)
		})
		if v != before {
			s.prefs.ShortcutPreferences[r.id] = shortcutStored(v)
			s.setPref("shortcutPreferences")
			s.installMenu()
		}
	}
}

// shortcutDisplay 把存储的快捷键转为 goos 平台上设置页显示的文字：macOS 上 Ctrl 显示为 Cmd、Alt 显示为 Option
// （存储格式同原版，Ctrl 在 macOS 上即 Command）；其他平台原样显示。
func shortcutDisplay(v, goos string) string {
	if goos != "darwin" {
		return v
	}
	parts := strings.Split(v, "+")
	for i, p := range parts {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "ctrl":
			parts[i] = "Cmd"
		case "alt":
			parts[i] = "Option"
		}
	}
	return strings.Join(parts, "+")
}

// shortcutStored 把设置页输入的快捷键转回存储格式：Cmd / Command / ⌘ / Meta / CmdOrCtrl 记为 Ctrl，Option / ⌥ 记为 Alt，
// ⇧ 记为 Shift；符号可连写（如 ⌘⇧K）。
func shortcutStored(v string) string {
	v = strings.NewReplacer("⌘", "+Cmd+", "⌥", "+Option+", "⇧", "+Shift+", "⌃", "+Ctrl+").Replace(v)
	var parts []string
	for _, p := range strings.Split(v, "+") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		switch strings.ToLower(p) {
		case "cmd", "command", "meta", "cmdorctrl", "ctrl", "control":
			p = "Ctrl"
		case "option", "opt", "alt":
			p = "Alt"
		case "shift":
			p = "Shift"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "+")
}

// updateRow 是“关于”页的检查更新：后台检查；Windows 安装版可下载并重启安装，其他形态打开发布页。状态与启动检查
// 共用（Shell.upd，见 update.go）。
func (sw *settingsWindow) updateRow(c *ui.Context) {
	s := sw.s
	u := &s.upd
	ui.Column(c).Gap(8).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			label := s.tr("softwareUpdateCheckNow")
			if u.checking {
				label = s.tr("softwareUpdateCheckingShort")
			}
			if ui.Button(c, label).Disabled(u.checking || u.downloading || u.installing).Clicked() {
				s.runUpdateCheck(false)
			}
			s.updateActions(c)
		})
		if t := s.updateStatusText(); t != "" {
			ui.Text(c, t).FontSize(12).Wrap().TextColor(c.Theme().TextMuted)
		}
		if u.downloading && u.total > 0 {
			ui.Progress(c, float64(u.done)/float64(u.total)).Width(320)
		}
	})
}

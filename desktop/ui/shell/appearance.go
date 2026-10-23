package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/config"
	"nomo/desktop/ui/mdedit"
	"nomo/desktop/ui/themes"
)

// 颜色主题与字体：内置主题来自原版主题注册表（desktop/ui/themes），用户主题读写数据目录
// themes/<id>.json（与 WebView 版共用）。颜色主题作用于文档区（正文、标题、链接、引用、提示块、
// 代码、表格、选区与内容背景）；窗口框架保持各平台原生外观，只取主题的强调色。

func (s *Shell) themesDir() string { return filepath.Join(s.cfg.DataDir(), config.ThemesDirName) }

// loadThemes 读取内置与用户主题；无效的用户主题文件跳过并记日志，与 WebView 版一致。
func (s *Shell) loadThemes() {
	s.themes = append([]*themes.Theme(nil), themes.Builtin()...)
	files, err := config.ListUserThemes(s.themesDir())
	if err != nil {
		applog.Warn("Native", err.Error())
		return
	}
	for _, f := range files {
		t, err := themes.ParseUser([]byte(f.Content))
		if err != nil || t.ID != f.ID {
			applog.Warn("Native", "跳过主题文件 "+f.ID)
			continue
		}
		s.themes = append(s.themes, t)
	}
}

// currentTheme 返回 colorThemeId 对应的主题；主题已不存在（如被删除）时回退默认。
func (s *Shell) currentTheme() *themes.Theme { return themes.Find(s.themes, s.prefs.ColorThemeID) }

// editorColors 把主题令牌映射为编辑器配色；classic 为经典块样式（引用、提示块使用 *ClassicBackground）。
// renderThemeOf 返回公式与图表渲染使用的配色：正文前景、文档背景与 Mermaid 主题名。
func renderThemeOf(t *themes.Theme, dark bool) renderTheme {
	tok := t.Tokens(dark)
	css := func(c ui.Color) string { return fmt.Sprintf("rgba(%d,%d,%d,%.3f)", c.R, c.G, c.B, float32(c.A)/255) }
	i := 0
	if dark {
		i = 1
	}
	return renderTheme{fg: css(tok("foreground")), bg: css(tok("documentBackground")), mermaid: t.Variants[i].Mermaid}
}

func editorColors(t *themes.Theme, dark, classic bool) *mdedit.Colors {
	tok := t.Tokens(dark)
	bg := func(modern, classicName string) ui.Color {
		if classic {
			return tok(classicName)
		}
		return tok(modern)
	}
	c := &mdedit.Colors{
		Background: tok("documentBackground"), Text: tok("foreground"), Heading: tok("heading"), Muted: tok("mutedForeground"),
		Link: tok("link"), Selection: tok("selection"),
		QuoteBar: tok("blockquoteBorder"), QuoteText: tok("blockquoteForeground"), QuoteBackground: bg("blockquoteBackground", "blockquoteClassicBackground"),
		CodeBackground: tok("codeBackground"), CodeText: tok("codeForeground"), CodeBorder: tok("codeBorder"),
		TableBorder: tok("tableBorder"), TableHeader: tok("tableHeaderBackground"),
		Border: tok("border"), Surface: tok("surface"), AccentFill: tok("accentFill"),
	}
	for i, name := range []string{"Note", "Tip", "Important", "Warning", "Caution"} {
		c.ToneBorder[i] = tok("callout" + name + "Border")
		c.ToneText[i] = tok("callout" + name + "Foreground")
		c.ToneBackground[i] = bg("callout"+name+"Background", "callout"+name+"ClassicBackground")
	}
	return c
}

// applyAppearance 在每帧开始时把主题与字体交给控件主题、外壳配色和当前编辑器。
// applyEditorStyle 把字号、缩放、内容宽度与行高设置交给 tab 的语义编辑器；值未变时编辑器不做任何事。
func (s *Shell) applyEditorStyle(tab *Tab) {
	tab.Ed.SetStyle(float32(s.prefs.FontSize), float32(s.prefs.ZoomPercent), float32(s.prefs.ContentWidthPercent))
	tab.Ed.SetLineHeight(float32(s.prefs.LineHeight))
	tab.Ed.SetOptions(s.editorOptions())
	if tab.Ed.CtrlWheel == nil {
		tab.Ed.CtrlWheel = s.ctrlWheel
	}
}

func (s *Shell) applyDocumentAppearance(c *ui.Context, tab *Tab) {
	t := s.currentTheme()
	dark := c.Theme().Dark
	if (dark != s.dark || !s.iconSynced) && s.opts.SetIconTheme != nil {
		s.iconSynced = true
		go s.opts.SetIconTheme(dark)
	}
	s.dark = dark
	tok := t.Tokens(dark)
	s.ch.content = tok("documentBackground")
	if s.prefs.UIFontFamily != "" {
		th := *c.Theme()
		th.Font = s.prefs.UIFontFamily
		c.SetTheme(&th)
	}
	if tab != nil {
		tab.Ed.SetColors(editorColors(t, dark, s.prefs.DocumentStyleID == "nomo-classic"), s.prefs.DocumentStyleID == "nomo-classic")
		tab.Ed.SetFonts(s.prefs.DocumentFontFamily, s.prefs.MonoFontFamily)
		dir := ""
		if tab.Path != "" {
			dir = filepath.Dir(tab.Path)
		}
		tab.Ed.SetImages(s.images, dir)
		// 公式与图表按当前主题渲染；主题变化后丢弃旧的渲染结果。
		if s.rend.setTheme(renderThemeOf(t, false), renderThemeOf(t, true)) {
			s.images.InvalidateRenders()
		}
		tab.Ed.SetLabels(mdedit.Labels{Comment: s.tr("comment"), Toc: s.tr("toc"), NoHeadings: s.tr("documentHasNoHeadings"),
			DetailsTitle: s.tr("detailsTitlePlaceholder"), InlineComment: s.tr("inlineComment")})
		if tab.source != nil {
			mono := s.prefs.MonoFontFamily
			if mono == "" {
				mono = "monospace"
			}
			tab.source.Font = ui.Font{Family: mono, Size: float32(13 * s.prefs.ZoomPercent / 100)}
		}
	}
}

// importTheme 选择 JSON 文件导入为用户主题（同 ID 覆盖），校验规则与 WebView 版一致；成功后选用。
func (s *Shell) importTheme() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Filters: []mygo.FileFilter{{Name: "JSON", Extensions: []string{"json"}}}})
		if err != nil || len(paths) == 0 {
			return
		}
		data, err := config.ReadThemeText(paths[0])
		s.postToSettings(func() {
			if err != nil {
				s.status = err.Error()
				return
			}
			t, err := themes.ParseUser([]byte(data))
			if err != nil {
				s.status = s.tr("userThemeLoadFailed") + "：" + err.Error()
				return
			}
			if err := config.SaveUserTheme(s.themesDir(), t.ID, data); err != nil {
				s.status = err.Error()
				return
			}
			s.loadThemes()
			s.prefs.ColorThemeID = t.ID
			s.setPref("colorThemeId")
		})
	}()
}

// exportTheme 把当前主题导出为可编辑模板（内置主题换成 user-…-custom ID）。
func (s *Shell) exportTheme() {
	t := s.currentTheme()
	data, err := themes.Serialize(t)
	if err != nil {
		s.status = err.Error()
		return
	}
	name := strings.TrimPrefix(t.ID, "nomo-") + ".json"
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{DefaultPath: name, Filters: []mygo.FileFilter{{Name: "JSON", Extensions: []string{"json"}}}})
		if err != nil || path == "" {
			return
		}
		err = config.WriteThemeAtomically(path, string(data))
		s.postToSettings(func() {
			if err != nil {
				s.status = err.Error()
			}
		})
	}()
}

// deleteTheme 删除当前用户主题文件（内置主题不可删除），随后回退默认主题。只删除应用数据目录
// themes/ 下的主题文件，由设置页的确认对话框触发。
func (s *Shell) deleteTheme() {
	t := s.currentTheme()
	if t.Builtin {
		return
	}
	if err := config.DeleteUserTheme(s.themesDir(), t.ID); err != nil && !os.IsNotExist(err) {
		s.status = err.Error()
		return
	}
	s.loadThemes()
	s.prefs.ColorThemeID = themes.DefaultThemeID
	s.setPref("colorThemeId")
}

// postToSettings 把后台结果交回 UI 线程（设置窗口与主窗口在同一 UI 线程上）。
func (s *Shell) postToSettings(fn func()) {
	if s.post != nil {
		s.post(fn)
		return
	}
	fn()
}

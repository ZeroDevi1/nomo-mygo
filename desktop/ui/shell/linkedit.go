package shell

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// 链接快速编辑（原版 LinkQuickEditor）：工具栏、菜单、⌘K 与右键“编辑链接”在光标下方弹出浮层，编辑显示文字与
// 地址。光标或选区在已有链接内时编辑该链接（可移除，保留文字），否则以选中文字为显示文字插入新链接。
// 应用为一次独立的撤销步。打开期间文档被改动（版本变化）或换了标签时，替换范围失效，浮层关闭。

type linkEditor struct {
	open     bool
	tab      int    // 标签 ID
	ver      uint64 // 打开时的文档版本
	a, z     int    // 被替换的原文范围
	title    string // 已有链接的标题，原样保留
	existing bool
	text     string
	href     string
	err      string
	focus    bool // 下一帧聚焦地址输入框
}

// openLinkEditor 为当前语义模式标签打开链接浮层。
func (s *Shell) openLinkEditor() {
	t := s.activeTab()
	if t == nil || s.mode() != modeSemantic {
		return
	}
	src := t.Doc.Source()
	a, z := t.Doc.Sel.Range()
	le := linkEditor{open: true, tab: t.ID, ver: t.Doc.Version(), a: a, z: z, focus: true}
	if l, ok := t.Ed.LinkAt(a); ok && z <= l.End {
		le.a, le.z, le.existing = l.Start, l.End, true
		le.text, le.href, le.title = l.Label, l.Dest, l.Title
	} else {
		le.text = src[a:z]
		if isLinkLike(le.text) {
			le.href = le.text
		}
	}
	s.link = le
}

// isLinkLike 报告文字是否像一个地址（选中网址后插入链接时直接作为地址）。
func isLinkLike(v string) bool {
	l := strings.ToLower(v)
	return !strings.ContainsAny(v, " \n\t") &&
		(strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "mailto:"))
}

// linkHrefOK 与原版 link.ts 一致：拒绝脚本类协议，其余（http(s)、mailto、锚点、相对路径等）放行。
func linkHrefOK(href string) bool {
	l := strings.ToLower(strings.TrimSpace(href))
	for _, p := range []string{"javascript:", "vbscript:", "data:"} {
		if strings.HasPrefix(l, p) {
			return false
		}
	}
	return !strings.ContainsAny(href, "\n\r")
}

// linkMarkdown 拼出行内链接原文：地址含空白或括号时用尖括号包裹；方括号文字为空时显示地址。
func linkMarkdown(text, href, title string) string {
	if text == "" {
		text = href
	}
	dest := href
	if strings.ContainsAny(href, " ()<>") {
		dest = "<" + strings.ReplaceAll(strings.ReplaceAll(href, "<", "%3C"), ">", "%3E") + ">"
	}
	if title != "" {
		dest += ` "` + strings.ReplaceAll(title, `"`, `\"`) + `"`
	}
	return "[" + strings.ReplaceAll(text, "\n", " ") + "](" + dest + ")"
}

// applyLink 写入链接；remove 为 true 时移除已有链接、保留显示文字。
func (s *Shell) applyLink(t *Tab, remove bool) {
	le := &s.link
	var md string
	switch {
	case remove:
		md = le.text
	case strings.TrimSpace(le.href) == "":
		le.err = s.tr("linkHrefRequired")
		return
	case !linkHrefOK(le.href):
		le.err = s.tr("linkHrefInvalid")
		return
	default:
		md = linkMarkdown(le.text, strings.TrimSpace(le.href), le.title)
	}
	t.Ed.Replace(le.a, le.z, md)
	le.open = false
	s.focusEditor = true
}

// linkPopover 画链接浮层：位于光标下方（editor 为语义编辑区元素）。
func (s *Shell) linkPopover(c *ui.Context, t *Tab, editor ui.Element) {
	le := &s.link
	if !le.open {
		return
	}
	if t == nil || t.ID != le.tab || t.Doc.Version() != le.ver || s.mode() != modeSemantic {
		le.open = false
		return
	}
	caret, ok := t.Ed.CaretRect()
	if !ok {
		return
	}
	origin := editor.Bounds()
	w, _ := c.Size()
	const panelW = 340
	x := min(max(8, origin.X+caret.X-24), w-panelW-8)
	y := origin.Y + caret.Y + caret.H + 6
	th := c.Theme()
	ui.Overlay(c, func() {
		panel := ui.Column(c).Absolute().Left(x).Top(y).Width(panelW).Padding(12).Gap(8).Radius(th.Radius+4).
			Background(th.Background).Border(1, s.ch.separator).Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.2)).
			Role(ui.RoleDialog).Label(s.tr("editLink"))
		panel.Children(func() {
			ui.Text(c, s.tr("linkTitle")).FontSize(11).TextColor(s.ch.muted)
			text := ui.TextInput(c, &le.text).FillWidth().Placeholder(s.tr("linkTitlePlaceholder"))
			ui.Text(c, s.tr("linkHref")).FontSize(11).TextColor(s.ch.muted)
			href := ui.TextInput(c, &le.href).FillWidth().Placeholder("https://")
			if le.focus {
				href.Focus()
				le.focus = false
			}
			if href.Changed() {
				le.err = ""
			}
			if le.err != "" {
				ui.Text(c, le.err).FontSize(12).TextColor(th.Danger).Wrap()
			}
			ui.Row(c).FillWidth().Gap(8).Padding(4, 0, 0, 0).Children(func() {
				if le.existing && ui.Button(c, s.tr("removeLink")).Clicked() {
					s.applyLink(t, true)
				}
				ui.Spacer(c)
				if ui.Button(c, s.tr("cancel")).Clicked() {
					le.open = false
					s.focusEditor = true
				}
				if ui.PrimaryButton(c, s.tr("applyLink")).Clicked() {
					s.applyLink(t, false)
				}
			})
			if text.Submitted() || href.Submitted() {
				s.applyLink(t, false)
			}
		})
		if panel.Shortcut(0, ui.KeyEscape) {
			le.open = false
			s.focusEditor = true
		}
		if panel.PressedOutside() {
			le.open = false
		}
	})
}

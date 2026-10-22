package shell

import (
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"nomo/desktop/ui/mdlint"
)

// Markdown 格式检查（对应原版 markdownLintController + StatusBar 的检查弹层）：只检查当前标签，
// 在后台 goroutine 运行；同一标签与规则集下修改后延迟 500ms 再查，切换标签或规则集立即查。
// 结果按原文版本记录，返回时版本已变化则丢弃（不会显示过期结果）。超过大文件阈值时跳过。

// lintDelay 与原版 App.svelte 的防抖间隔一致。
const lintDelay = 500 * time.Millisecond

// lintState 是一个标签的检查结果；ver/set 为结果对应的原文版本与规则集。
type lintState struct {
	ver     uint64
	set     string
	running bool
	status  string // checking / clean / issues / skipped
	issues  []mdlint.Issue
	total   int
	at      time.Time
	// editVer/editAt 记录最近一次版本变化，用于防抖。
	editVer uint64
	editAt  time.Time
	open    bool // 状态栏弹层是否展开
}

// scheduleLint 在每帧检查当前标签是否需要（重新）检查。
func (s *Shell) scheduleLint(c *ui.Context, tab *Tab) {
	if tab == nil || !s.prefs.MarkdownLintEnabled || tab.Kind() != kindMarkdown {
		return
	}
	l := &tab.lint
	ver, set := tab.Doc.Version(), s.prefs.MarkdownLintRuleSet
	if l.running || (l.status != "" && l.ver == ver && l.set == set) {
		return
	}
	if len(tab.Doc.Source()) > int(s.prefs.LargeDocumentLimit) {
		l.ver, l.set, l.status, l.issues, l.total = ver, set, "skipped", nil, 0
		return
	}
	now := c.Now()
	// 首次检查或换了规则集：立即；否则在最后一次修改后 lintDelay 再查。
	if l.status != "" && l.set == set {
		if l.editVer != ver {
			l.editVer, l.editAt = ver, now
		}
		if due := l.editAt.Add(lintDelay); now.Before(due) {
			c.After(due.Sub(now))
			return
		}
	}
	l.running = true
	if l.status == "" {
		l.status = "checking"
	}
	src, id := tab.Doc.Source(), tab.ID
	go func() {
		issues, total := mdlint.Lint(src, set)
		s.postToSettings(func() {
			t := s.tabWithID(id)
			if t == nil {
				return
			}
			t.lint.running = false
			if t.Doc.Version() != ver {
				return // 已过期：下一帧按新版本重新调度
			}
			t.lint.ver, t.lint.set, t.lint.issues, t.lint.total, t.lint.at = ver, set, issues, total, time.Now()
			t.lint.status = "clean"
			if total > 0 {
				t.lint.status = "issues"
			}
		})
	}()
}

// lintButton 是状态栏的检查状态按钮与问题弹层。
func (s *Shell) lintButton(c *ui.Context, tab *Tab) {
	if !s.prefs.MarkdownLintEnabled || tab.Kind() != kindMarkdown {
		return
	}
	l := &tab.lint
	label, icon := s.tr("markdownLintStatus"), "minus"
	switch l.status {
	case "checking":
		label, icon = s.tr("markdownLintChecking"), "loader-circle"
	case "clean":
		label, icon = s.tr("markdownLintClean"), "check"
	case "issues":
		label, icon = s.tr("markdownLintIssues", "count", strconv.Itoa(l.total)), "triangle-alert"
	case "skipped":
		label = s.tr("markdownLintSkipped")
	}
	btn := ui.ButtonBase(c).Padding(1, 6).Gap(4).Radius(c.Theme().Radius).Label(label).Tooltip(label).Shrink(0)
	if btn.Hovered() || l.open {
		btn.Background(s.ch.hover)
	}
	btn.Children(func() {
		color := s.ch.muted
		if l.status == "issues" {
			color = ui.RGB(0xc2, 0x7a, 0x0e)
		}
		ui.Icon(c, s.icon(icon)).FontSize(12).TextColor(color)
		if l.status == "issues" {
			ui.Text(c, strconv.Itoa(l.total)).FontSize(11).TextColor(color)
		}
	})
	if btn.Clicked() {
		l.open = !l.open
	}
	ui.Popover(c, btn, &l.open, func() {
		ui.Column(c).Width(360).MaxHeight(420).Padding(12).Gap(8).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).Gap(2).Children(func() {
					ui.Text(c, s.tr("markdownLintStatus")).Bold()
					ui.Text(c, label).FontSize(12).TextColor(s.ch.muted)
				})
				if ui.Button(c, s.tr("markdownLintRetry")).Clicked() {
					l.status, l.ver = "checking", 0
				}
			})
			set := s.tr("markdownLintDefault")
			if l.set == "relaxed" {
				set = s.tr("markdownLintRelaxed")
			}
			at := s.tr("markdownLintNotCheckedYet")
			if !l.at.IsZero() {
				at = l.at.Format("15:04:05")
			}
			ui.Text(c, s.tr("markdownLintRuleMode")+"："+set+" · "+s.tr("markdownLintCheckedAt")+"："+at).FontSize(11).TextColor(s.ch.muted)
			switch l.status {
			case "clean":
				ui.Text(c, s.tr("markdownLintNoIssues")).TextColor(s.ch.muted)
			case "skipped":
				ui.Text(c, s.tr("markdownLintSkippedDescription")).Wrap().TextColor(s.ch.muted)
			case "checking":
				ui.Text(c, s.tr("markdownLintChecking")).TextColor(s.ch.muted)
			case "issues":
				ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
					ui.Column(c).FillWidth().Gap(2).Children(func() {
						for i, is := range l.issues {
							s.lintIssueRow(c.Key(i), tab, is)
						}
						if l.total > len(l.issues) {
							ui.Text(c, s.tr("markdownLintRemaining", "shown", strconv.Itoa(len(l.issues)),
								"remaining", strconv.Itoa(l.total-len(l.issues)))).FontSize(11).Wrap().TextColor(s.ch.muted)
						}
					})
				})
			}
		})
	})
}

func (s *Shell) lintIssueRow(c *ui.Context, tab *Tab, is mdlint.Issue) {
	pos := s.tr("markdownLintLine", "line", strconv.Itoa(is.Line))
	if is.Column > 0 {
		pos = s.tr("markdownLintLocation", "line", strconv.Itoa(is.Line), "column", strconv.Itoa(is.Column))
	}
	title := s.tr("lintRule." + is.Rule)
	row := ui.ButtonBase(c).FillWidth().Padding(6, 8).Gap(2).Radius(c.Theme().Radius).Justify(ui.Start).Label(is.Rule + " " + title)
	if row.Hovered() {
		row.Background(s.ch.hover)
	}
	row.Children(func() {
		ui.Column(c).FillWidth().Gap(2).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, is.Rule).FontSize(11).Bold().TextColor(s.ch.muted)
				ui.Text(c, title).FontSize(12).SingleLine().Shrink(1)
			})
			ui.Text(c, pos).FontSize(11).TextColor(s.ch.muted)
			if is.Context != "" {
				ui.Text(c, is.Context).FontSize(11).Font("monospace").SingleLine().TextColor(s.ch.muted)
			}
		})
	})
	if row.Clicked() {
		s.gotoLine(tab, is.Line, is.Column)
		tab.lint.open = false
	}
}

// gotoLine 把光标移到第 line 行（从 1 起）第 col 列（0 为行首），语义与源码模式都适用。
func (s *Shell) gotoLine(tab *Tab, line, col int) {
	src := tab.Doc.Source()
	pos := 0
	for range line - 1 {
		i := strings.IndexByte(src[pos:], '\n')
		if i < 0 {
			break
		}
		pos += i + 1
	}
	if col > 1 {
		end := strings.IndexByte(src[pos:], '\n')
		if end < 0 {
			end = len(src) - pos
		}
		n := 0
		for i := range src[pos : pos+end] {
			if n == col-1 {
				pos += i
				break
			}
			n++
		}
	}
	if s.mode() == modeSource {
		tab.Source().SetSelections(editor.Selection{Anchor: pos, Head: pos})
	} else {
		tab.Ed.JumpTo(pos)
	}
	s.focusEditor = true
}

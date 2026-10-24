package shell

import (
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/config"
)

// 历史快照：列出当前文件的保存前快照（config.ListSnapshots，新的在前，至多 SnapshotLimit 份），右侧预览所选版本，
// “恢复此版本”把原文换成快照内容（未保存、可撤销），不直接写盘。快照正文在后台读取。

type snapshotsState struct {
	open    bool
	loading bool
	tab     int // 标签 ID
	list    []config.SnapshotRecord
	sel     int
	err     string
	rows    ui.ListState // 快照列表的滚动位置
}

// snapshotPreviewLimit 是预览显示的原文字节上限：大文档的快照只预览开头，恢复时仍用完整内容。
const snapshotPreviewLimit = 24 << 10

// openSnapshots 打开当前文档的历史快照窗口。
func (s *Shell) openSnapshots() {
	t := s.activeTab()
	if t == nil {
		return
	}
	if t.Path == "" {
		s.status = s.tr("snapshotsRequireSavedFile")
		return
	}
	s.snaps = snapshotsState{open: true, loading: true, tab: t.ID}
	path, cfg, id := t.Path, s.cfg, t.ID
	go func() {
		list, err := cfg.ListSnapshots(path)
		s.post(func() {
			if !s.snaps.open || s.snaps.tab != id {
				return // 已关闭或换了文档：丢弃过期结果
			}
			s.snaps.loading, s.snaps.list = false, list
			if err != nil {
				s.snaps.err = s.tr("snapshotLoadFailed", "error", err.Error())
			}
		})
	}()
}

func (s *Shell) snapshotReason(r string) string {
	if r == "before-save" {
		return s.tr("snapshotReasonBeforeSave")
	}
	return r
}

// snapshotsDialog 画历史快照窗口。
func (s *Shell) snapshotsDialog(c *ui.Context) {
	st := &s.snaps
	if !st.open {
		return
	}
	i := s.findTabIndex(st.tab)
	if i < 0 {
		st.open = false
		return
	}
	t := s.tabs[i]
	th := c.Theme()
	ui.Modal(c, &st.open, func() {
		ui.Column(c).Width(760).Height(480).Gap(12).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
				ui.Text(c, s.tr("documentSnapshots")).Bold()
				ui.Text(c, t.Name(s.untitledName)).FontSize(12).TextColor(s.ch.muted).SingleLine().Shrink(1)
				ui.Spacer(c)
				if len(st.list) > 0 {
					ui.Text(c, s.tr("snapshotCount", "count", strconv.Itoa(len(st.list)))).FontSize(12).TextColor(s.ch.muted)
				}
			})
			switch {
			case st.loading:
				ui.Column(c).Grow(1).FillWidth().Center().Children(func() { ui.Spinner(c) })
			case st.err != "":
				ui.Text(c, st.err).TextColor(th.Danger).Wrap()
				ui.Spacer(c)
			case len(st.list) == 0:
				ui.Column(c).Grow(1).FillWidth().Center().Gap(10).Children(func() {
					ui.Icon(c, s.icon("history")).FontSize(32).TextColor(s.ch.muted)
					ui.Text(c, s.tr("snapshotsEmpty")).TextColor(s.ch.muted).Wrap().MaxWidth(420)
				})
			default:
				st.sel = clampInt(st.sel, 0, len(st.list)-1)
				s.snapshotBody(c, t)
			}
			ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, s.tr("cancel")).Clicked() {
					st.open = false
				}
				restore := ui.PrimaryButton(c, s.tr("restoreSnapshot")).Disabled(len(st.list) == 0 || st.loading)
				if restore.Clicked() && len(st.list) > 0 {
					t.replaceText(t.adoptSnapshotText(st.list[st.sel].Markdown))
					st.open = false
					s.focusEditor = true
					s.status = s.tr("snapshotRestored")
				}
			})
		})
	})
}

// snapshotBody 是快照列表与所选快照的预览。
func (s *Shell) snapshotBody(c *ui.Context, t *Tab) {
	st := &s.snaps
	current := t.text()
	ui.Row(c).Grow(1).FillWidth().MinHeight(0).Gap(12).AlignItems(ui.Stretch).Children(func() {
		ui.List(c, &st.rows, len(st.list), func(i int) {
			r := st.list[i]
			ck := c.Key(r.ID)
			b := ui.ButtonBase(ck).FillWidth().Padding(7, 10).Radius(c.Theme().Radius).Justify(ui.Start)
			switch {
			case i == st.sel:
				b.Background(s.ch.pressed)
			case b.Hovered():
				b.Background(s.ch.hover)
			}
			b.Children(func() {
				ui.Column(ck).Gap(2).Grow(1).Children(func() {
					ui.Text(ck, time.Unix(r.CreatedAt, 0).Format("2006-01-02 15:04:05")).FontSize(13)
					sub := s.snapshotReason(r.Reason) + " · " + s.statsChars(r.Markdown)
					if r.Markdown == current {
						sub += " · " + s.tr("snapshotSameAsCurrent")
					}
					ui.Text(ck, sub).FontSize(11).TextColor(s.ch.muted).SingleLine()
				})
			})
			if b.Clicked() {
				st.sel = i
			}
		}).Width(240).FillHeight()
		ui.Box(c).Width(1).FillHeight().Background(s.ch.separator)
		preview := st.list[st.sel].Markdown
		if len(preview) > snapshotPreviewLimit {
			cut := snapshotPreviewLimit
			for cut > 0 && !utf8.RuneStart(preview[cut]) {
				cut--
			}
			preview = preview[:cut] + "\n…"
		}
		ui.Scroll(c).Grow(1).FillHeight().Children(func() {
			mono := s.prefs.MonoFontFamily
			if mono == "" {
				mono = "monospace"
			}
			ui.Text(c, preview).Font(mono).FontSize(12).Wrap().Selectable().Padding(4, 8, 8, 4)
		})
	})
}

// statsChars 返回原文字符数的简短说明（如“1234 字符”）。
func (s *Shell) statsChars(text string) string {
	return strconv.Itoa(utf8.RuneCountInString(text)) + " " + s.tr("charUnit")
}

// adoptSnapshotText 把快照原文统一为 LF 换行（编辑器内一律 LF）；不改变标签记录的文件换行风格。
func (t *Tab) adoptSnapshotText(raw string) string {
	eol, mixed := t.eol, t.mixedRaw
	text := t.adoptLineEndings(raw)
	t.eol, t.mixedRaw = eol, mixed
	return text
}

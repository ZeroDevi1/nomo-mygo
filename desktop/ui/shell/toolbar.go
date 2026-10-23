package shell

import (
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"
)

// 格式工具栏（原版 EditorToolbar）：按钮、分组与顺序同原版，另保留原生版已有的折叠块按钮；源码模式下禁用。
// 表格按钮弹出尺寸网格（原版 table-picker，5 行 × 6 列），公式与图表按钮弹出菜单，右侧为表情与内容宽度。

// toolbarState 是工具栏与状态栏弹层的界面状态；不写盘。
type toolbarState struct {
	tableOpen            bool
	tableRows, tableCols int // 网格中悬停预览的尺寸；打开时复位为 3 × 3（同原版）
	widthOpen            bool
	width                float64 // 内容宽度滑块的界面值，拖动时写回 prefs.ContentWidthPercent
	zoomOpen             bool
	zoom                 float64 // 缩放滑块的界面值，拖动时按 5% 取整写回 prefs.ZoomPercent
}

const (
	tablePickerRows = 5 // 原版 tableRows = [1..5]
	tablePickerCols = 6 // 原版 tableColumns = [1..6]
)

// 工具栏各部分的估算宽度（DIP），用于按可用宽度收起分组（原版 app-responsive.css 的容器断点：先收起插入组，
// 再收起中间组，最后收起核心格式组）。toolBtnW 为图标按钮（16 图标 + 内边距 10 + 间隔 1），segmentedW 为三段
// 模式切换（中文标签 12 号字实测约 180）。调整按钮或字号时同步修改。
const (
	toolBtnW   = 27
	toolDivW   = 13
	widthBtnW  = 64
	segmentedW = 186
)

// toolbarFit 返回在宽度 w 内显示哪些分组；w 为 0（首帧尚未布局）时全部显示。tools 为右侧是否含模式切换、查找与大纲。
func toolbarFit(w float32, emoji, tools bool) (core, medium, low bool) {
	if w <= 0 {
		return true, true, true
	}
	right := float32(widthBtnW + 20) // 内容宽度按钮与左右内边距
	if emoji {
		right += toolBtnW
	}
	if tools {
		right += toolDivW + segmentedW + 6 + 2*toolBtnW
	}
	coreW := float32(9 * toolBtnW)
	mediumW := float32(5*toolBtnW + toolDivW)
	lowW := float32(7*toolBtnW + toolDivW)
	core = right+coreW <= w
	medium = core && right+coreW+mediumW <= w
	low = medium && right+coreW+mediumW+lowW <= w
	return
}

func (s *Shell) toolbar(c *ui.Context) {
	disabled := s.mode() != modeSemantic
	button := func(icon, tip string, on bool) ui.Element {
		b := s.toolButton(c, icon, tip, on).Disabled(disabled)
		if disabled {
			b.Opacity(0.4)
		}
		return b
	}
	row := ui.Row(c).Height(toolbarH).FillWidth().Padding(0, 10).Gap(1).AlignItems(ui.Center)
	// 按上一帧的工具栏宽度决定显示哪些分组；收起的命令仍可从菜单、快捷键与快捷插入使用。
	showCore, showMedium, showLow := toolbarFit(row.Bounds().W, s.loadEmoji != nil, !s.pf.mac)
	row.Children(func() {
		group := func(items ...[3]string) {
			for _, it := range items {
				if button(it[0], it[1], false).Clicked() {
					s.run(it[2])
				}
			}
		}
		divider := func() { ui.Box(c).Size(1, 16).Background(s.ch.separator).Margin(0, 6) }
		if showCore {
			s.toolbarCore(c, group)
		}
		if showMedium {
			divider()
			group([3]string{"message-square", s.tr("insertInlineComment"), "inlineComment"},
				[3]string{"quote", s.tr("toggleQuote"), "quote"}, [3]string{"info", s.tr("insertCallout"), "callout"},
				[3]string{"list", s.tr("toggleList"), "unorderedList"}, [3]string{"square-check", s.tr("toggleTaskList"), "taskList"})
		}
		if showLow {
			divider()
			s.toolbarInserts(c, button, group)
		}
		// 表情、内容宽度与文档工具与原版一样位于工具栏右侧。
		ui.Box(c).Grow(1)
		if s.loadEmoji != nil {
			if b := button("smile", s.tr("emojiInsert"), s.emo.picker); b.Clicked() {
				r := b.Bounds()
				s.openEmojiPicker(ui.Point{X: r.X + r.W - emojiPickerW, Y: r.Y + r.H + 6})
			}
		}
		s.widthControl(c)
		if !s.pf.mac {
			divider()
			s.documentTools(c)
		}
	})
	ui.Box(c).Height(1).FillWidth().Background(s.ch.separator)
}

// toolbarCore 是核心格式组（原版 toolbar-group-core）。
func (s *Shell) toolbarCore(c *ui.Context, group func(items ...[3]string)) {
	group([3]string{"heading-1", s.tr("setHeadingOne"), "heading1"},
		[3]string{"bold", s.tr("toggleBold"), "bold"}, [3]string{"italic", s.tr("toggleItalic"), "italic"},
		[3]string{"strikethrough", s.tr("toggleStrikethrough"), "strikethrough"}, [3]string{"underline", s.tr("toggleUnderline"), "underline"},
		[3]string{"highlighter", s.tr("toggleHighlight"), "highlight"},
		[3]string{"superscript", s.tr("toggleSuperscript"), "superscript"}, [3]string{"subscript", s.tr("toggleSubscript"), "subscript"},
		[3]string{"link", s.tr("editLink"), "link"})
}

// toolbarInserts 是插入组（原版 toolbar-group-low）：表格、代码块、公式、图表、脚注、目录与折叠块。
func (s *Shell) toolbarInserts(c *ui.Context, button func(icon, tip string, on bool) ui.Element, group func(items ...[3]string)) {
	s.tablePicker(c, button("table-2", s.tr("insertTable"), s.tbar.tableOpen))
	group([3]string{"code-xml", s.tr("insertCodeBlock"), "codeBlock"})
	button("sigma", s.tr("insertMathFormula"), false).Menu(func(m *ui.Menu) {
		if m.Item(s.tr("mathInline") + "  $…$").Chosen() {
			s.run("mathInline")
		}
		if m.Item(s.tr("mathBlock") + "  $$…$$").Chosen() {
			s.run("mathBlock")
		}
	})
	button("braces", s.tr("insertDiagram"), false).Menu(func(m *ui.Menu) {
		if m.Item(s.tr("blankDiagram")).Chosen() {
			s.insertDiagram("")
		}
		m.Separator()
		for i, typ := range diagramTypes {
			if m.Item(s.tr(diagramLabels[i])).Chosen() {
				s.insertDiagram(diagramTemplates[typ])
			}
		}
	})
	group([3]string{"superscript", s.tr("insertFootnote"), "footnote"}, [3]string{"table-of-contents", s.tr("insertToc"), "toc"},
		[3]string{"panel-top-close", s.tr("insertDetails"), "details"})
}

// insertDiagram 插入 Mermaid 图表：code 为空时插入空白图表并把光标放进代码块（原版 insertMermaidBlock），否则插入模板。
func (s *Shell) insertDiagram(code string) {
	t := s.activeTab()
	if t == nil || s.mode() != modeSemantic {
		return
	}
	t.Ed.InsertMermaid(code)
	s.focusEditor = true
}

// tablePicker 在表格按钮下方弹出尺寸网格：悬停预览行列数，单击插入（行数为表头以外的正文行，同原版）。
func (s *Shell) tablePicker(c *ui.Context, anchor ui.Element) {
	st := &s.tbar
	if anchor.Clicked() {
		st.tableOpen = !st.tableOpen
		st.tableRows, st.tableCols = 3, 3
	}
	ui.Popover(c, anchor, &st.tableOpen, func() {
		ui.Column(c).Padding(10).Gap(8).Role(ui.RoleDialog).Label(s.tr("chooseTableSize")).Children(func() {
			ui.Row(c).FillWidth().Children(func() {
				ui.Text(c, s.tr("table")).FontSize(12).TextColor(s.ch.muted).Grow(1)
				ui.Text(c, fmt.Sprintf("%d × %d", st.tableRows, st.tableCols)).FontSize(12).Bold()
			})
			ui.Column(c).Gap(3).Children(func() {
				for r := 1; r <= tablePickerRows; r++ {
					ui.Row(c).Gap(3).Children(func() {
						for col := 1; col <= tablePickerCols; col++ {
							cell := ui.ButtonBase(c).Size(18, 18).Radius(3).Border(1, s.ch.separator).
								Label(s.tr("insertTableSize", "rows", strconv.Itoa(r), "columns", strconv.Itoa(col)))
							if r <= st.tableRows && col <= st.tableCols {
								cell.Background(s.ch.accent).Opacity(0.7)
							}
							if cell.Hovered() || cell.Focused() {
								st.tableRows, st.tableCols = r, col
							}
							if cell.Clicked() {
								st.tableOpen = false
								if t := s.activeTab(); t != nil && s.mode() == modeSemantic {
									t.Ed.InsertTable(r, col)
									s.focusEditor = true
								}
							}
						}
					})
				}
			})
		})
	})
}

// widthControl 是内容宽度按钮（原版 width-control-compact）：显示当前百分比，单击弹出 45–90% 滑块；
// 双栏模式下宽度自适应，按钮禁用（原版 splitAdaptiveWidth）。
func (s *Shell) widthControl(c *ui.Context) {
	st := &s.tbar
	split := s.split()
	tip := s.tr("contentWidth")
	if split {
		tip = s.tr("splitAdaptiveWidth")
	}
	pct := fmt.Sprintf("%.0f%%", s.prefs.ContentWidthPercent)
	b := ui.ButtonBase(c).Padding(4, 6).Radius(c.Theme().Radius).Tooltip(tip).Label(tip).Disabled(split)
	switch {
	case st.widthOpen:
		b.Background(s.ch.pressed)
	case b.Hovered():
		b.Background(s.ch.hover)
	}
	if split {
		b.Opacity(0.4)
	}
	b.Children(func() {
		ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, s.icon("align-horizontal-space-around")).FontSize(15).TextColor(s.ch.muted)
			ui.Text(c, pct).FontSize(12).TextColor(s.ch.muted)
		})
	})
	if b.Clicked() {
		st.widthOpen = !st.widthOpen
		st.width = s.prefs.ContentWidthPercent
	}
	ui.Popover(c, b, &st.widthOpen, func() {
		ui.Column(c).Padding(12).Gap(6).Width(220).Role(ui.RoleDialog).Label(s.tr("contentWidth")).Children(func() {
			ui.Row(c).FillWidth().Children(func() {
				ui.Text(c, s.tr("contentWidth")).FontSize(12).Bold().Grow(1)
				ui.Text(c, pct).FontSize(12).TextColor(s.ch.muted)
			})
			if ui.Slider(c, &st.width, 45, 90).FillWidth().Label(s.tr("contentWidth")).Changed() {
				s.prefs.ContentWidthPercent = float64(int(st.width + 0.5))
				s.setPref("contentWidthPercent")
			}
		})
	})
}

// zoomControl 是状态栏右端的缩放按钮（原版 statusbar-zoom）：显示当前百分比，单击弹出 −/+（步长 5%）与 80–160% 滑块。
func (s *Shell) zoomControl(c *ui.Context) {
	st := &s.tbar
	pct := fmt.Sprintf("%.0f%%", s.prefs.ZoomPercent)
	b := ui.ButtonBase(c).Padding(1, 6).Radius(c.Theme().Radius).Tooltip(s.tr("zoomLevel")).Label(s.tr("zoomLevel")).Shrink(0)
	switch {
	case st.zoomOpen:
		b.Background(s.ch.pressed)
	case b.Hovered():
		b.Background(s.ch.hover)
	}
	b.Children(func() { ui.Text(c, pct).FontSize(11).TextColor(s.ch.muted) })
	if b.Clicked() {
		st.zoomOpen = !st.zoomOpen
	}
	set := func(v float64) {
		v = max(80, min(160, float64(int(v/5+0.5)*5)))
		if v != s.prefs.ZoomPercent {
			s.prefs.ZoomPercent = v
			s.setPref("zoomPercent")
		}
	}
	ui.Popover(c, b, &st.zoomOpen, func() {
		ui.Column(c).Padding(10).Gap(8).Width(200).Role(ui.RoleDialog).Label(s.tr("zoomLevel")).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				if ui.Button(c, "−").Label(s.tr("decreaseZoom")).Disabled(s.prefs.ZoomPercent <= 80).Clicked() {
					set(s.prefs.ZoomPercent - 5)
				}
				ui.Row(c).Grow(1).Justify(ui.Center).Children(func() { ui.Text(c, pct).FontSize(13).Bold() })
				if ui.Button(c, "+").Label(s.tr("increaseZoom")).Disabled(s.prefs.ZoomPercent >= 160).Clicked() {
					set(s.prefs.ZoomPercent + 5)
				}
			})
			st.zoom = s.prefs.ZoomPercent
			if ui.Slider(c, &st.zoom, 80, 160).FillWidth().Label(s.tr("zoomLevel")).Changed() {
				set(st.zoom)
			}
		})
	})
}

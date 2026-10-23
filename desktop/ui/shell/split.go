package shell

import (
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// 双栏模式（原版 EditorWorkspace 的 split）：语义编辑与源码编辑并排显示同一文档，按 splitViewLayout 决定左右，
// 中间分隔条拖动调整比例（25–75%，写入 splitLeftPercent）。两侧原文每帧按版本双向同步（Tab.syncFromSource /
// syncToSource，只在有变化时做最小差异）；活动一侧随焦点切换，命令与插入作用于活动一侧（Shell.mode）。
// 滚动同步：活动一侧的视口顶部（语义侧为行的原文偏移，源码侧为首个可见行）变化时，另一侧滚到对应位置。

// splitScroll 记录上次同步时两侧的位置，只在活动一侧移动时同步，避免两侧互相触发。
type splitScroll struct {
	tab        int
	semTop     int // 语义侧视口顶部的原文偏移
	srcTopLine int // 源码侧首个可见行
}

// splitSaveDelay 是拖动分隔条后写盘前的等待：拖动期间每帧都在变，停下后再写一次。
const splitSaveDelay = 400 * time.Millisecond

// splitView 画双栏工作区，返回语义侧元素（快捷插入、表情建议等以它定位）。
func (s *Shell) splitView(c *ui.Context, tab *Tab, ck *ui.Context) (semantic ui.Element) {
	if s.splitPct == 0 {
		s.splitPct = float32(s.prefs.SplitLeftPercent)
	}
	// 两侧原文双向同步：先收源码侧的修改，再把语义侧的修改推给源码侧。
	tab.syncFromSource()
	tab.syncToSource()
	st := tab.Source()
	if st.OnCompletion == nil {
		st.OnCompletion = func(req editor.CompletionRequest) { s.sourceEmojiCompletion(tab, req) }
	}
	semPane := func() {
		semantic = tab.Ed.View(ck)
	}
	srcPane := func() {
		ui.Row(ck).Fill().Padding(16, 0, 0, 12).Children(func() {
			editor.View(ck, st).Fill()
		})
	}
	first, second := semPane, srcPane
	if s.prefs.SplitViewLayout == "source-semantic" {
		first, second = srcPane, semPane
	}
	sp := ui.SplitPercent(c, &s.splitPct, first, second).Fill()
	if sp.Changed() {
		s.splitPct = float32(clampF64(float64(s.splitPct), 25, 75))
		s.scheduleSplitSave()
	}
	// 活动一侧随焦点切换。
	switch {
	case st.Focused():
		s.splitActive = modeSource
	case tab.Ed.Focused():
		s.splitActive = modeSemantic
	}
	s.syncSplitScroll(tab)
	return semantic
}

// syncSplitScroll 让非活动一侧跟随活动一侧滚动到对应的原文位置。
func (s *Shell) syncSplitScroll(tab *Tab) {
	st := tab.Source()
	sync := &s.splitSync
	semTop := tab.Ed.TopSourceOffset()
	srcTop, _ := st.VisibleLines()
	if sync.tab != tab.ID {
		*sync = splitScroll{tab: tab.ID, semTop: semTop, srcTopLine: srcTop}
		return
	}
	switch {
	case s.splitActive == modeSource && srcTop != sync.srcTopLine:
		off := st.Buffer.Offset(editor.Pos{Line: srcTop})
		tab.Ed.ScrollToSource(off)
		sync.srcTopLine, sync.semTop = srcTop, off
	case s.splitActive != modeSource && semTop != sync.semTop:
		line := st.Buffer.Pos(min(semTop, st.Buffer.Len())).Line
		st.ScrollTo(line)
		sync.semTop, sync.srcTopLine = semTop, line
	}
}

// scheduleSplitSave 在拖动停下 splitSaveDelay 后把比例写入设置。
func (s *Shell) scheduleSplitSave() {
	if s.splitSave != nil {
		s.splitSave.Stop()
	}
	s.splitSave = time.AfterFunc(splitSaveDelay, func() {
		if s.post == nil {
			return
		}
		s.post(func() {
			s.prefs.SplitLeftPercent = float64(s.splitPct)
			s.setPref("splitLeftPercent")
		})
	})
}

func clampF64(v, lo, hi float64) float64 { return min(max(v, lo), hi) }

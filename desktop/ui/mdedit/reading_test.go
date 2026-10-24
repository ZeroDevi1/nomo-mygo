package mdedit

import (
	"strconv"
	"strings"
	"testing"
)

// TestReadingLine：大纲跳转把标题文字放在阅读参考线（视口 20%，64–180 DIP），参考线处的原文落在该标题的章节内；
// 文档顶部时参考线落在第一节。
func TestReadingLine(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 30; i++ {
		b.WriteString("## 第 " + strconv.Itoa(i) + " 节\n\n" + strings.Repeat("正文段落，用于撑开章节高度。", 12) + "\n\n")
	}
	ed, tt := newTester(t, b.String(), 600, 800)
	if got := ed.readingLine(); got != 160 {
		t.Fatalf("参考线 %v，应为 160（800 × 20%%）", got)
	}
	hs := ed.Doc.Headings()
	if off, ok := ed.ReadingSourceOffset(); !ok || off >= hs[1].Pos {
		t.Fatalf("顶部时参考线应在第一节：%d（第二节起点 %d）", off, hs[1].Pos)
	}
	for _, k := range []int{5, 17, 28} {
		ed.JumpTo(hs[k].Pos)
		tt.Frame()
		off, ok := ed.ReadingSourceOffset()
		if !ok || off < hs[k].Pos || k+1 < len(hs) && off >= hs[k+1].Pos {
			t.Errorf("跳到第 %d 节后参考线处偏移 %d，应在 [%d, %d)", k+1, off, hs[k].Pos, hs[min(k+1, len(hs)-1)].Pos)
		}
	}
}

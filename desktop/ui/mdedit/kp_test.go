package mdedit

import (
	"math"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

const kpDoc = "研究团队需要离线整理现场记录、图片和结构化数据。本文为虚构技术方案，用于模拟日常文档，而非均匀重复节点。" +
	"一份技术文档往往同时包含需求背景、设计理由、接口说明和运行记录（English words 与 typography hyphenation 混排），" +
	"阅读者需要迅速找到关键结论，也需要在细节中核对约束。\n\n尾段\n"

func kpRow(t *testing.T, ed *Editor) (seg, *row) {
	t.Helper()
	s := ed.segOf(0, 0)
	r := &ed.entries[0].rows[0]
	if s.layout == nil || s.layout.kp == nil || len(s.layout.kp) < 3 {
		t.Fatalf("段落应完成精细排版（%d 行）", func() int {
			if s.layout == nil {
				return -1
			}
			return len(s.layout.kp)
		}())
	}
	return s, r
}

func TestKPLayoutGeometryAndMapping(t *testing.T) {
	ed, tt := newTester(t, kpDoc, 520, 800)
	ed.SetOptions(Options{Typography: true})
	tt.Frame()
	s, _ := kpRow(t, ed)
	ll := s.layout
	_, w := ed.rowBox(&ed.entries[0].rows[0])
	// 两端对齐：非末行右缘贴齐行宽（允许 1.5 DIP 误差：行末标点悬挂与舍入）。
	for i, k := range ll.kp[:len(ll.kp)-1] {
		lw, _ := k.l.Size()
		if right := k.x + lw; math.Abs(float64(right-w)) > 1.5+float64(ed.scale*bodySize*0.5) {
			t.Errorf("第 %d 行右缘 %.1f，行宽 %.1f", i, right, w)
		}
		if k.u1 != ll.kp[i+1].u0 {
			t.Fatalf("行 %d 与下一行不相接：%d / %d", i, k.u1, ll.kp[i+1].u0)
		}
	}
	// 光标与命中往返；行首不出现句读标点（禁则）。
	n := s.proj.u16
	for u := 0; u <= n; u = ll.NextBoundary(u) {
		c := ll.Caret(u)
		if got := ll.IndexAt(ui0(c.X+0.3, c.Y+c.H/2)); got != u && ll.Caret(got) != c {
			t.Fatalf("位置 %d 往返得到 %d", u, got)
		}
		if u == n {
			break
		}
	}
	for _, k := range ll.kp[1:] {
		first := []rune(s.proj.text[byteAt(s.proj.text, k.u0):])[0]
		if strings.ContainsRune("，。、；：！？）》」』", first) {
			t.Fatalf("行首出现标点 %q", first)
		}
	}
}

func TestKPEditingKeepsSource(t *testing.T) {
	ed, tt := newTester(t, kpDoc, 520, 800)
	ed.SetOptions(Options{Typography: true})
	tt.Frame()
	d := ed.Doc
	// 方向键逐字素右移走完整段，每步前进且不跳过。
	d.SetSelection(Selection{})
	tt.Frame()
	end := d.blocks[0].End
	steps := 0
	for d.Sel.Head < end && steps < 2000 {
		before := d.Sel.Head
		ed.setHead(ed.moveH(d.Sel.Head, 1, false, false), false)
		if d.Sel.Head <= before {
			t.Fatalf("右移卡在 %d", before)
		}
		steps++
	}
	// 下移经过每个视觉行。
	d.SetSelection(Selection{})
	tt.Frame()
	lines := 0
	for prev := -1; d.Sel.Head < end && lines < 50; lines++ {
		prev = d.Sel.Head
		ed.setHead(ed.moveV(d.Sel.Head, 1), false)
		tt.Frame()
		if d.Sel.Head <= prev {
			break
		}
	}
	s, _ := kpRow(t, ed)
	if lines < len(s.layout.kp)-1 {
		t.Fatalf("下移 %d 次，段落有 %d 行", lines, len(s.layout.kp))
	}
	// 段中键入与撤销：原文正确恢复，段落仍精排。
	src := d.Source()
	at := strings.Index(src, "关键结论")
	d.SetSelection(Selection{Anchor: at, Head: at})
	tt.Type("新")
	tt.Frame()
	if !strings.Contains(d.Source(), "新关键结论") {
		t.Fatal("键入未写入原文")
	}
	ed.Run("undo")
	tt.Frame()
	if d.Source() != src {
		t.Fatal("撤销后原文应恢复")
	}
	kpRow(t, ed)
}

func ui0(x, y float32) ui.Point { return ui.Point{X: x, Y: y} }

func byteAt(s string, u16 int) int {
	n := 0
	for i, c := range s {
		if n >= u16 {
			return i
		}
		n += utf16Len(string(c))
	}
	return len(s)
}

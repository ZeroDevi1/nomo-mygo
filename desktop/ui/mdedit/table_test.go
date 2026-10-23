package mdedit

import (
	"fmt"
	"strings"
	"testing"
)

func wideTable(cols, rows int) string {
	var b strings.Builder
	for c := range cols {
		fmt.Fprintf(&b, "| 字段%02d ", c)
	}
	b.WriteString("|\n" + strings.Repeat("| --- ", cols) + "|\n")
	for r := range rows {
		for c := range cols {
			fmt.Fprintf(&b, "| V-%04d-%02d ", r, c)
		}
		b.WriteString("|\n")
	}
	return b.String()
}

func TestTableColumnsFitOrScroll(t *testing.T) {
	ed, tt := newTester(t, "| 名称 | 说明 |\n| --- | --- |\n| a | 一段较长的说明文字 |\n\n"+wideTable(64, 20), 1000, 700)
	small := tableFrame(ed.ensure(0), 0)
	if small == nil || small.tableW > small.viewW || len(small.cols) != 2 {
		t.Fatalf("小表格应放得下：%+v", small)
	}
	en := ed.ensure(1)
	wide := tableFrame(en, 0)
	if wide.tableW <= wide.viewW {
		t.Fatalf("64 列表格应溢出：tableW=%v viewW=%v", wide.tableW, wide.viewW)
	}
	// 横向滚轮滚动表格，单元格随之平移；滚到最右后不再超出。
	tt.Frame()
	var y float32
	for _, v := range ed.vsegs {
		if v.i == 1 {
			y = v.y + 5
			break
		}
	}
	x0, _ := ed.rowBox(&en.rows[1])
	tt.Scroll(400, y, 300, 0)
	tt.Frame()
	en = &ed.entries[1]
	if x1, _ := ed.rowBox(&en.rows[1]); x1 >= x0 || tableFrame(en, 0).scrollX <= 0 {
		t.Fatalf("横向滚动后单元格应左移：%v → %v", x0, x1)
	}
	ed.scrollTable(en, tableFrame(en, 0), 1e6)
	if f := tableFrame(en, 0); f.scrollX != f.tableW-f.viewW {
		t.Fatalf("滚动应夹到末端：%v", f.scrollX)
	}
	// 光标移到第一列：表格滚回使该列可见。
	ed.Doc.SetSelection(Selection{ed.Doc.blocks[1].Start + en.rows[0].a + 1, ed.Doc.blocks[1].Start + en.rows[0].a + 1})
	ed.reveal, ed.dirty = true, true
	tt.Frame()
	if f := tableFrame(&ed.entries[1], 0); f.scrollX != 0 {
		t.Fatalf("光标进入第一列后应滚回：%v", f.scrollX)
	}
}

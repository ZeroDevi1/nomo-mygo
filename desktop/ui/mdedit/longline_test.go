package mdedit

import (
	"github.com/egoist/mygo/ui"
	"strings"
	"testing"
	"time"
)

func TestLongLinesAreChunked(t *testing.T) {
	long := strings.Repeat("x", 20000)
	src := "```\n    " + long + "\n```\n\n" + strings.Repeat("段落文字", 2000) + "\n"
	ed, tt := newTester(t, src, 1000, 800)
	code := ed.ensure(0)
	chunks := 0
	for _, r := range code.rows {
		if r.b-r.a > maxRowBytes {
			t.Fatalf("行片段 %d 字节超过上限", r.b-r.a)
		}
		if r.cont {
			chunks++
		}
	}
	if chunks < 4 {
		t.Fatalf("2 万字节的行应分为至少 5 段，后续片段 %d", chunks)
	}
	if para := ed.ensure(1); para.rows[0].kind != rowLine || len(para.rows) < 6 {
		t.Fatalf("超过 %d 字节的段落应按原样行分段显示，得到 %d 行 kind=%d", maxLeafBytes, len(para.rows), para.rows[0].kind)
	}
	// 在长行中段键入：只重排所在片段，耗时应远低于整行排版。
	mid := strings.Index(src, long) + 10000
	ed.Doc.SetSelection(Selection{mid, mid})
	ed.dirty = true
	tt.Frame()
	start := time.Now()
	tt.Type("y")
	if d := time.Since(start); d > 60*time.Millisecond {
		t.Fatalf("长行内键入耗时 %v", d)
	}
	// 续行片段中回车：保留原文行（首片段）的缩进 4 个空格。
	tt.Key(0, ui.KeyEnter)
	if got := ed.Doc.Source()[mid+1 : mid+6]; got != "\n    " {
		t.Fatalf("回车后 %q", got)
	}
}

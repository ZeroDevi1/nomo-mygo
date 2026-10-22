package mdedit

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestBigBlockLatency 测量超大单块（3000 行代码块、2 万字无换行段落）中按键到一帧
// 的耗时，覆盖整块重排的最坏情况。设 NOMO_PERF=1 时运行。
func TestBigBlockLatency(t *testing.T) {
	if os.Getenv("NOMO_PERF") == "" {
		t.Skip("设 NOMO_PERF=1 运行性能测量")
	}
	for _, c := range []struct {
		name string
		src  string
	}{
		{"code block 3000 lines", "```go\n" + strings.Repeat("fmt.Println(\"hello world 你好世界\")\n", 3000) + "```\n"},
		{"paragraph 20k chars", strings.Repeat("这是一个很长的段落，没有空行分隔。", 1300) + "\n"},
	} {
		ed, tt := newTester(t, c.src, 1200, 900)
		p := strings.Index(c.src, "\n") + 1
		if c.name[0] == 'p' {
			p = 30
		}
		ed.Doc.SetSelection(Selection{p, p})
		ed.dirty = true
		tt.Frame()
		d := make([]time.Duration, 50)
		for i := range d {
			s := time.Now()
			tt.Type("x")
			tt.Frame()
			d[i] = time.Since(s)
		}
		slices.Sort(d)
		fmt.Printf("%-24s p50=%v p95=%v\n", c.name, d[25], d[47])
	}
}

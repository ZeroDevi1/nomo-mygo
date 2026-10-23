package mdedit

import (
	"math/rand"
	"strings"
	"testing"
)

// TestPatchRowEditMatchesRebuild 在大表格与长代码块内随机编辑（含会改变结构的字符），修补后的行记录与重新展开一致。
func TestPatchRowEditMatchesRebuild(t *testing.T) {
	var b strings.Builder
	b.WriteString("| a | b | c |\n|---|---|---|\n")
	for i := range 80 {
		b.WriteString("| r" + itoa(i) + " | **x** | `y` |\n")
	}
	b.WriteString("\n```go\n")
	for i := range 120 {
		b.WriteString("x := " + itoa(i) + " /* c */\n")
	}
	b.WriteString("```\n")
	ed, tt := newTester(t, b.String(), 900, 700)
	d := ed.Doc
	rng := rand.New(rand.NewSource(5))
	inserts := []string{"z", "中", "", "|", "\n", "`", "a b"}
	for n := 0; n < 600; n++ {
		s := d.Source()
		bi := rng.Intn(len(d.blocks))
		blk := d.blocks[bi]
		at := runeStartBefore(s, blk.Start+1+rng.Intn(max(1, blk.End-blk.Start-1)))
		to := at
		if rng.Intn(3) == 0 {
			to = runeStartBefore(s, min(blk.End, at+rng.Intn(3)))
		}
		ed.ensure(bi)
		d.SetSelection(Selection{Anchor: at, Head: to})
		ed.Replace(min(at, to), max(at, to), inserts[rng.Intn(len(inserts))])
		tt.Frame()
		fresh := NewEditor(NewDocument(d.Source()))
		fresh.contentW, fresh.viewW = ed.contentW, ed.viewW
		if len(fresh.entries) != len(ed.entries) {
			t.Fatalf("第 %d 次编辑：块数 %d / %d", n, len(ed.entries), len(fresh.entries))
		}
		for i := range ed.entries {
			if !ed.entries[i].split {
				continue
			}
			got, want := ed.ensure(i).rows, fresh.ensure(i).rows
			if len(got) != len(want) {
				t.Fatalf("第 %d 次编辑：块 %d 行数 %d / %d", n, i, len(got), len(want))
			}
			for k := range want {
				g, w := got[k], want[k]
				if g.a != w.a || g.b != w.b || g.rel != w.rel || g.kind != w.kind || g.fence != w.fence || g.blockB != w.blockB || g.lineNo != w.lineNo || g.col != w.col {
					t.Fatalf("第 %d 次编辑：块 %d 行 %d 为 a=%d b=%d rel=%d，重新展开 a=%d b=%d rel=%d", n, i, k, g.a, g.b, g.rel, w.a, w.b, w.rel)
				}
			}
		}
	}
}

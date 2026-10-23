package mdedit

import (
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

func repoFile(t testing.TB, name string) string {
	_, file, _, _ := runtime.Caller(0)
	b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../..", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// checkIndex 比较增量维护的块索引与从头解析的结果。
func checkIndex(t *testing.T, d *Document) {
	t.Helper()
	fresh := NewDocument(d.Source())
	if len(fresh.blocks) != len(d.blocks) {
		t.Fatalf("块数 %d，全量解析 %d", len(d.blocks), len(fresh.blocks))
	}
	for i := range fresh.blocks {
		a, b := d.blocks[i], fresh.blocks[i]
		a.Index, b.Index = 0, 0
		if a != b {
			t.Fatalf("块 %d 不一致：增量 %+v，全量 %+v", i, a, b)
		}
	}
	if d.u16Total != fresh.u16Total {
		t.Fatalf("u16Total %d，期望 %d", d.u16Total, fresh.u16Total)
	}
}

func TestIncrementalIndexMatchesFullParse(t *testing.T) {
	src := repoFile(t, "sample.md")
	d := NewDocument(src)
	rng := rand.New(rand.NewSource(1))
	inserts := []string{"a", "中", "😀", "\n", "\n\n", "**", "# ", "- ", "```\n", "`", "[x](y)"}
	for i := 0; i < 400; i++ {
		s := d.Source()
		from := runeStartBefore(s, rng.Intn(len(s)+1))
		to := runeStartBefore(s, min(len(s), from+rng.Intn(8)))
		if to < from {
			to = from
		}
		d.Replace(from, to, inserts[rng.Intn(len(inserts))], editOther)
		checkIndex(t, d)
	}
}

func TestOpenSaveIsByteIdentical(t *testing.T) {
	for _, name := range []string{"sample.md", "README.md"} {
		src := repoFile(t, name)
		d := NewDocument(src)
		if d.Source() != src {
			t.Fatalf("%s 打开后原文变化", name)
		}
	}
}

func TestU16RoundTrip(t *testing.T) {
	d := NewDocument("# 标题😀\n\n段落 **加粗** 😀x\n\n```\n代码\n```\n")
	src := d.Source()
	for b := 0; b <= len(src); b++ {
		if b < len(src) && src[b]&0xC0 == 0x80 {
			continue
		}
		u := d.ByteToU16(b)
		if u != u16Len(src[:b]) {
			t.Fatalf("ByteToU16(%d)=%d，期望 %d", b, u, u16Len(src[:b]))
		}
		if got := d.U16ToByte(u); got != b {
			t.Fatalf("U16ToByte(%d)=%d，期望 %d", u, got, b)
		}
	}
}

func TestTypingAndCompositionUndo(t *testing.T) {
	d := NewDocument("你好\n")
	d.SetSelection(Selection{6, 6})
	for _, s := range []string{"a", "b", "c"} {
		p := d.Sel.Head
		d.Replace(p, p, s, editTyping)
	}
	// 组字：sh → shi → 提交“是”，应与被替换内容合为一步。
	p := d.Sel.Head
	d.SetMarked(p, p, "sh", 2, 2)
	m, _ := d.Marked()
	d.SetMarked(m.Anchor, m.Head, "shi", 3, 3)
	m, _ = d.Marked()
	d.CommitMarked(m.Anchor, m.Head, "是")
	if got := d.Source(); got != "你好abc是\n" {
		t.Fatalf("原文 %q", got)
	}
	if _, ok := d.Marked(); ok {
		t.Fatal("提交后仍有组字")
	}
	d.Undo()
	if got := d.Source(); got != "你好abc\n" {
		t.Fatalf("撤销组字后 %q", got)
	}
	d.Undo()
	if got := d.Source(); got != "你好\n" {
		t.Fatalf("撤销键入后 %q", got)
	}
	d.Redo()
	d.Redo()
	if got := d.Source(); got != "你好abc是\n" {
		t.Fatalf("重做后 %q", got)
	}
	checkIndex(t, d)
}

func TestProjectionHidesMarkersOutsideCaret(t *testing.T) {
	d := NewDocument("## 标题 **粗** *斜* `c` [链](u) \\*\n")
	b := d.Blocks()[0]
	p := projectLeaf(d, b.Index, b.Start, b.End, expansion{}, leafOpts{prefix: true})
	if p.text != "标题 粗 斜 c 链 *" {
		t.Fatalf("折叠投影 %q", p.text)
	}
	// 光标在“粗”里：展开 **，标题前缀也显示。
	caret := strings.Index(d.Source(), "粗") - b.Start
	p = projectLeaf(d, b.Index, b.Start, b.End, expansion{inBlock: true, caret: caret}, leafOpts{prefix: true})
	if p.text != "## 标题 **粗** 斜 c 链 \\*" {
		t.Fatalf("展开投影 %q", p.text)
	}
	// 映射往返：可见位置原样往返。
	for _, r := range p.runs {
		for off := r.srcA; off <= r.srcB; off++ {
			if off < r.srcB && !utf8.RuneStart(d.Source()[b.Start+off]) {
				continue
			}
			u := p.toDisp(off)
			if got := p.toSrc(u, off == r.srcB); got != off {
				t.Fatalf("往返 %d → %d → %d", off, u, got)
			}
		}
	}
}

func TestCRLineEndingsDoNotOverlapBlocks(t *testing.T) {
	for _, src := range []string{"# 标题\r\r段落一\r段落续\r\r- 列表\r", "# 标题\r\n\r\n段落\r\n"} {
		d := NewDocument(src)
		bs := d.Blocks()
		for i := 1; i < len(bs); i++ {
			if bs[i-1].End > bs[i].Start {
				t.Fatalf("%q 块 %d 与 %d 重叠：%+v %+v", src, i-1, i, bs[i-1], bs[i])
			}
		}
		if d.Source() != src {
			t.Fatal("原文应不变")
		}
	}
}

package mdedit

import (
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/egoist/mygo/ui/markdown/parser"
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
	src := repoFile(t, "desktop/packaging/samples/sample.md")
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
	for _, name := range []string{"desktop/packaging/samples/sample.md", "README.md"} {
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

// TestRecollectMatchesFullParse 用容易改变块结构的片段（懒惰续行、Setext、列表续行、围栏、目录标记、front matter）
// 随机编辑，逐块对照全量解析，并检查块序号指向当前解析结果中的同一块。
func TestRecollectMatchesFullParse(t *testing.T) {
	inserts := []string{"a", "中", "\n", "\n\n", "# ", "- ", "1. ", "> ", "    ", "===\n", "---\n", "```\n", "~~~",
		"<!-- toc -->\n", "<!-- /toc -->\n", "|a|b|\n|-|-|\n", "  continued", "<div>\n", "[^1]: note\n", "$$\n"}
	for seed := int64(1); seed <= 6; seed++ {
		d := NewDocument(repoFile(t, "desktop/packaging/samples/sample.md"))
		rng := rand.New(rand.NewSource(seed))
		for i := 0; i < 300; i++ {
			s := d.Source()
			from := runeStartBefore(s, rng.Intn(len(s)+1))
			to := runeStartBefore(s, min(len(s), from+rng.Intn(12)))
			if to < from || rng.Intn(3) > 0 {
				to = from
			}
			d.Replace(from, to, inserts[rng.Intn(len(inserts))], editOther)
			checkIndex(t, d)
			for k, b := range d.blocks {
				if b.Kind == KindFrontMatter || b.Kind == KindToc {
					continue
				}
				if pb := d.md.Block(b.Index); pb.Parent != 0 || pb.Start != b.Start || pb.Kind != b.Kind {
					t.Fatalf("种子 %d 第 %d 次编辑：块 %d 的序号 %d 指向 %+v", seed, i, k, b.Index, pb)
				}
			}
		}
	}
}

// TestChunkedTreeMatchesWholeParse 把文档切成很小的段，随机编辑（含会跨段延续的围栏、HTML、公式与引用定义），
// 逐块、逐行内节点对照整篇解析。
func TestChunkedTreeMatchesWholeParse(t *testing.T) {
	ext := parser.Default | parser.ExtDollarMath
	inserts := []string{"a", "中", "\n", "\n\n", "# ", "- ", "1. ", "> ", "    ", "===\n", "```\n", "~~~\n", "<!-- ",
		" -->", "<script>\n", "$$\n", "|a|b|\n|-|-|\n", "  continued", "**b**", "[x](y)", "[r]: /u\n", "[^1]: n\n"}
	var kept []string
	for _, line := range strings.Split(repoFile(t, "desktop/packaging/samples/sample.md"), "\n") {
		if !hasDefinitions(line) { // 含定义的文档不分段
			kept = append(kept, line)
		}
	}
	sample := strings.Repeat(strings.Join(kept, "\n"), 2)
	for seed := int64(1); seed <= 6; seed++ {
		tr := newTree(ext)
		tr.min, tr.size = 4<<10, 2<<10
		tr.Set(sample)
		if seed == 1 && len(tr.chunks) < 8 {
			t.Fatalf("应切成多段，得到 %d 段", len(tr.chunks))
		}
		rng := rand.New(rand.NewSource(seed))
		for i := 0; i < 250; i++ {
			s := tr.Source()
			from := runeStartBefore(s, rng.Intn(len(s)+1))
			to := from
			if rng.Intn(3) == 0 {
				to = runeStartBefore(s, min(len(s), from+rng.Intn(40)))
			}
			text := inserts[rng.Intn(len(inserts))]
			if seed > 3 && strings.HasPrefix(text, "[") && strings.Contains(text, "]:") {
				text = "x" // 一半种子不引入定义，保持多段，覆盖段内与跨段编辑
			}
			tr.Edit(from, to, text)
			whole := parser.New(ext)
			whole.Set(tr.Source())
			if tr.Len() != whole.Len() {
				t.Fatalf("种子 %d 第 %d 次编辑（%d 段）：块数 %d，整篇 %d", seed, i, len(tr.chunks), tr.Len(), whole.Len())
			}
			for g := 1; g < whole.Len(); g++ {
				a, b := tr.Block(g), whole.Block(g)
				if a.Kind != b.Kind || a.Start != b.Start || a.End != b.End || a.Parent != b.Parent || a.Next != b.Next || a.Open != b.Open {
					t.Fatalf("种子 %d 第 %d 次编辑（%d 段）：块 %d 为 %v [%d,%d) p%d n%d，整篇 %v [%d,%d) p%d n%d",
						seed, i, len(tr.chunks), g, a.Kind, a.Start, a.End, a.Parent, a.Next, b.Kind, b.Start, b.End, b.Parent, b.Next)
				}
				ia, ib := tr.Inlines(g), whole.Inlines(g)
				if len(ia) != len(ib) {
					t.Fatalf("种子 %d 第 %d 次编辑：块 %d 行内节点 %d 个，整篇 %d 个", seed, i, g, len(ia), len(ib))
				}
				for x := range ia {
					if ia[x].Kind != ib[x].Kind || ia[x].Start != ib[x].Start || ia[x].End != ib[x].End || ia[x].Dest != ib[x].Dest {
						t.Fatalf("种子 %d 第 %d 次编辑：块 %d 行内节点 %d 为 %+v，整篇 %+v", seed, i, g, x, ia[x], ib[x])
					}
				}
			}
		}
		t.Logf("种子 %d：结束时 %d 段", seed, len(tr.chunks))
	}
}

// TestStatsIncremental 随机编辑后，增量维护的写作统计与整篇重算一致。
func TestStatsIncremental(t *testing.T) {
	inserts := []string{"a", "word", " ", "\n", "中文", "かな", "x1", "-", "😀", "", "foo bar\nbaz"}
	d := NewDocument(repoFile(t, "desktop/packaging/samples/sample.md"))
	_ = d.Stats()
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 2000; i++ {
		s := d.Source()
		from := runeStartBefore(s, rng.Intn(len(s)+1))
		to := runeStartBefore(s, min(len(s), from+rng.Intn(6)))
		if to < from {
			to = from
		}
		d.Replace(from, to, inserts[rng.Intn(len(inserts))], editOther)
		if got, want := d.Stats(), TextStats(d.Source()); got != want {
			t.Fatalf("第 %d 次编辑后统计 %+v，整篇 %+v", i, got, want)
		}
	}
}

// TestU16CheckpointsInLargeBlock 大块内的字节与 UTF-16 换算（检查点路径）与直接计数一致，编辑后同样一致。
func TestU16CheckpointsInLargeBlock(t *testing.T) {
	src := "开头\n\n```\n" + strings.Repeat("ab中😀c\n", 6000) + "```\n\n结尾\n"
	d := NewDocument(src)
	rng := rand.New(rand.NewSource(9))
	for round := 0; round < 20; round++ {
		s := d.Source()
		for n := 0; n < 200; n++ {
			b := runeStartBefore(s, rng.Intn(len(s)+1))
			if got, want := d.ByteToU16(b), u16Len(s[:b]); got != want {
				t.Fatalf("ByteToU16(%d) = %d，期望 %d", b, got, want)
			}
			u := rng.Intn(d.U16Len() + 1)
			if got, want := d.U16ToByte(u), u16ToByte(s, u); got != want {
				t.Fatalf("U16ToByte(%d) = %d，期望 %d", u, got, want)
			}
		}
		at := runeStartBefore(s, len("开头\n\n```\n")+rng.Intn(len(s)-40))
		d.Replace(at, at, []string{"x", "中", "😀"}[rng.Intn(3)], editOther)
		checkIndex(t, d)
	}
}

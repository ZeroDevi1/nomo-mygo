package mdedit

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

func newTester(t testing.TB, src string, w, h int) (*Editor, *ui.Tester) {
	ed := NewEditor(NewDocument(src))
	tt := ui.NewTester(func(c *ui.Context) { ed.View(c).AutoFocus() }, w, h)
	tt.Frame()
	return ed, tt
}

// largeDoc 重复 sample.md 直到至少 runes 个字符，模拟 10 万字文档。
func largeDoc(t testing.TB, runes int) string {
	sample := repoFile(t, "desktop/packaging/samples/sample.md")
	var b strings.Builder
	for utf8.RuneCountInString(b.String()) < runes {
		b.WriteString(sample)
		b.WriteString("\n\n")
	}
	return b.String()
}

func TestTypingComposeAndUndoThroughTester(t *testing.T) {
	ed, tt := newTester(t, "# 标题\n\n第一段 **加粗** 文本\n", 800, 600)
	// 光标放到“文本”之后再输入。
	end := strings.Index(ed.Doc.Source(), "文本") + len("文本")
	ed.Doc.SetSelection(Selection{end, end})
	tt.Type("ab")
	tt.Compose("ni", 2)
	tt.Compose("nih", 3)
	tt.Type("你好")
	tt.Frame()
	if got := ed.Doc.Source(); got != "# 标题\n\n第一段 **加粗** 文本ab你好\n" {
		t.Fatalf("原文 %q", got)
	}
	tt.Command("undo")
	if got := ed.Doc.Source(); got != "# 标题\n\n第一段 **加粗** 文本ab\n" {
		t.Fatalf("撤销组字 %q", got)
	}
	tt.Command("undo")
	if got := ed.Doc.Source(); got != "# 标题\n\n第一段 **加粗** 文本\n" {
		t.Fatalf("撤销键入 %q", got)
	}
	checkIndex(t, ed.Doc)
}

func TestEnterBackspaceAndArrows(t *testing.T) {
	ed, tt := newTester(t, "abc def\n", 800, 600)
	ed.Doc.SetSelection(Selection{3, 3})
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if got := ed.Doc.Source(); got != "abc\n\n def\n" {
		t.Fatalf("分段 %q", got)
	}
	tt.Key(0, ui.KeyBackspace) // 段首退格：与上一段合并
	tt.Frame()
	if got := ed.Doc.Source(); got != "abc def\n" {
		t.Fatalf("合并 %q", got)
	}
	tt.Key(0, ui.KeyRight)
	tt.Key(ui.Shift, ui.KeyRight)
	tt.Key(ui.Shift, ui.KeyRight)
	tt.Frame()
	if a, z := ed.Doc.Sel.Range(); ed.Doc.Source()[a:z] != "de" {
		t.Fatalf("选区 %q", ed.Doc.Source()[a:z])
	}
	tt.Type("X")
	tt.Frame()
	if got := ed.Doc.Source(); got != "abc Xf\n" {
		t.Fatalf("替换选区 %q", got)
	}
}

func TestGapCaretStartsNewParagraph(t *testing.T) {
	ed, tt := newTester(t, "abc", 800, 600)
	ed.Doc.SetSelection(Selection{3, 3})
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if _, ok := ed.gapCaret(); !ok {
		t.Fatal("段尾回车后光标应在块间隙")
	}
	if !ed.caretOK {
		t.Fatal("间隙光标应可见")
	}
	tt.Type("d")
	tt.Frame()
	if got := ed.Doc.Source(); got != "abc\n\nd" || len(ed.Doc.Blocks()) != 2 {
		t.Fatalf("新段落 %q，块数 %d", got, len(ed.Doc.Blocks()))
	}
}

func TestBoldCommandWrapsAndUnwraps(t *testing.T) {
	ed, tt := newTester(t, "hello world\n", 800, 600)
	ed.Doc.SetSelection(Selection{0, 5})
	tt.Command("bold")
	if got := ed.Doc.Source(); got != "**hello** world\n" {
		t.Fatalf("加粗 %q", got)
	}
	tt.Command("bold")
	if got := ed.Doc.Source(); got != "hello world\n" {
		t.Fatalf("取消加粗 %q", got)
	}
}

func TestClickPlacesCaretInCollapsedBlock(t *testing.T) {
	ed, tt := newTester(t, "**粗体**在前\n\n第二段\n", 800, 600)
	tt.Frame()
	v := ed.vsegs[1]
	// 点击第二段第一个字的左半边，光标应位于“第”之前。
	tt.ClickAt(ed.left()+2, v.y+8)
	tt.Frame()
	if got, want := ed.Doc.Sel.Head, strings.Index(ed.Doc.Source(), "第二段"); got != want {
		t.Fatalf("光标 %d，期望 %d", got, want)
	}
}

func TestViewportOnlyLaysOutVisibleBlocks(t *testing.T) {
	ed, tt := newTester(t, largeDoc(t, 100_000), 1000, 800)
	tt.Frame()
	// 布局只出现在视口及其上下各一屏（空闲预排版）范围内，其余块保持未排版。
	first, last := ed.vblocks[0].i, ed.vblocks[len(ed.vblocks)-1].i
	below := float32(0)
	limit := last
	for limit+1 < len(ed.entries) && below < ed.viewH {
		limit++
		below += ed.entries[limit].height
	}
	for i, en := range ed.entries {
		for _, r := range en.rows {
			if r.layout != nil && (i < first || i > limit) {
				t.Fatalf("块 %d 超出视口与预排版范围（可见 %d–%d，预排到 %d）却已布局", i, first, last, limit)
			}
		}
	}
	if limit >= len(ed.entries)-1 {
		t.Fatal("测试文档太短，无法验证只排版局部")
	}
	// 一路滚到底，缓存应保持在预算内。
	ed.CacheBudget = 1 << 20
	for i := 0; i < 400; i++ {
		tt.Scroll(500, 400, 0, 800)
		tt.Frame()
	}
	if ed.cacheBytes > 1<<20+256<<10 {
		t.Fatalf("缓存 %d 字节超出预算", ed.cacheBytes)
	}
	ed.ReleaseLayouts()
	if ed.cacheBytes != 0 {
		t.Fatal("释放后缓存不为 0")
	}
}

func TestCodeBlockEditReusesLineLayouts(t *testing.T) {
	src := "```go\n" + strings.Repeat("fmt.Println(\"hello\")\n", 3000) + "```\n"
	ed, tt := newTester(t, src, 1000, 800)
	en := ed.entries[0]
	laid := 0
	for _, ln := range en.rows {
		if ln.layout != nil {
			laid++
		}
	}
	if laid == 0 || laid > 80 {
		t.Fatalf("3002 行代码块中布局了 %d 行，应只布局可见行", laid)
	}
	keep := en.rows[5].layout
	p := len("```go\n") + 2
	ed.Doc.SetSelection(Selection{p, p})
	tt.Type("x")
	tt.Frame()
	en = ed.entries[0]
	if en.rows[5].layout != keep {
		t.Fatal("未编辑的行应复用原布局")
	}
	if got := en.rows[1].b - en.rows[1].a; got != len("fxmt.Println(\"hello\")") {
		t.Fatalf("被编辑行长度 %d", got)
	}
	// 在代码块中回车只插入换行，块仍是一个。
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if len(ed.Doc.Blocks()) != 1 || len(ed.entries[0].rows) != 3003 {
		t.Fatalf("块数 %d 行数 %d", len(ed.Doc.Blocks()), len(ed.entries[0].rows))
	}
}

// TestKeystrokeLatency100k 测量 10 万字文档中“按键 → 编辑 → 重建并绘制一帧”的耗时
// 分布（无头 CPU 渲染，比 GPU 窗口更保守）。设 NOMO_PERF=1 时输出报告。
func TestKeystrokeLatency100k(t *testing.T) {
	if os.Getenv("NOMO_PERF") == "" {
		t.Skip("设 NOMO_PERF=1 运行性能测量")
	}
	src := largeDoc(t, 100_000)
	start := time.Now()
	ed, tt := newTester(t, src, 1200, 900)
	open := time.Since(start)
	// 光标放在文档中部的段落里，接近真实编辑。
	mid := len(src) / 2
	for _, b := range ed.Doc.Blocks() {
		if b.Start > mid && b.Kind == 1 {
			ed.Doc.SetSelection(Selection{b.End, b.End})
			break
		}
	}
	ed.reveal, ed.dirty = true, true
	tt.Frame()
	measure := func(name string, n int, act func(i int)) {
		d := make([]time.Duration, n)
		for i := range n {
			s := time.Now()
			act(i)
			tt.Frame()
			d[i] = time.Since(s)
		}
		slices.Sort(d)
		fmt.Printf("%-22s n=%d p50=%v p95=%v max=%v\n", name, n, d[n/2], d[n*95/100], d[n-1])
	}
	measure("type ascii", 300, func(int) { tt.Type("a") })
	measure("type 中文", 300, func(int) { tt.Type("字") })
	measure("compose 拼音", 100, func(i int) {
		if i%4 == 3 {
			tt.Type("是")
		} else {
			tt.Compose("shi"[:i%4+1], i%4+1)
		}
	})
	measure("backspace", 200, func(int) { tt.Key(0, ui.KeyBackspace) })
	measure("enter", 50, func(int) { tt.Key(0, ui.KeyEnter); tt.Type("x") })
	measure("arrow", 200, func(i int) {
		k := ui.KeyLeft
		if i%2 == 0 {
			k = ui.KeyDown
		}
		tt.Key(0, k)
	})
	measure("scroll", 200, func(int) { tt.Scroll(600, 450, 0, 120) })
	// 大跨度滚动：每次滚一整屏，两次之间留出空闲时间让预排版运行（模拟阅读后翻页）。
	measure("page jump (idle)", 60, func(int) {
		tt.Advance(300 * time.Millisecond)
		tt.Scroll(600, 450, 0, 900)
	})
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	fmt.Printf("doc runes=%d bytes=%d blocks=%d open+first frame=%v\n", utf8.RuneCountInString(src), len(src), len(ed.Doc.Blocks()), open)
	fmt.Printf("go heap in use=%.1f MB, layout cache≈%.1f MB\n", float64(ms.HeapInuse)/(1<<20), float64(ed.cacheBytes)/(1<<20))
	runtime.KeepAlive(ed)
}

// TestSnapshot 把 sample.md 首屏渲染为 PNG，供人工检查投影效果。
// 设 NOMO_SNAPSHOT=<输出路径> 时运行。
func TestSnapshot(t *testing.T) {
	out := os.Getenv("NOMO_SNAPSHOT")
	if out == "" {
		t.Skip("设 NOMO_SNAPSHOT=<png 路径> 运行")
	}
	src := repoFile(t, "desktop/packaging/samples/sample.md")
	if f := os.Getenv("NOMO_SNAPSHOT_DOC"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src = string(b)
	}
	ed, tt := newTester(t, src, 1000, 900)
	// 加载图片（相对路径按文档目录解析），等待已排队的加载完成后再截图。
	tp := &testPost{}
	im := NewImages(tp.post, DefaultImageBudget)
	defer im.Close()
	dir, _ := os.Getwd()
	if f := os.Getenv("NOMO_SNAPSHOT_DOC"); f != "" {
		dir = filepath.Dir(f)
	} else {
		dir = filepath.Join(dir, "..", "..", "..")
	}
	ed.SetImages(im, dir)
	tt.Frame()
	for range 50 {
		time.Sleep(100 * time.Millisecond)
		tp.mu.Lock()
		fns := tp.fns
		tp.fns = nil
		tp.mu.Unlock()
		for _, fn := range fns {
			fn()
		}
		tt.Frame()
		if im.idle() && len(fns) == 0 {
			break
		}
	}
	// 光标放进第一个含加粗的段落，展示就地展开。
	if i := strings.Index(ed.Doc.Source(), "**"); i >= 0 {
		ed.Doc.SetSelection(Selection{i + 3, i + 3})
		ed.dirty = true
	}
	if y := os.Getenv("NOMO_SNAPSHOT_SCROLL"); y != "" {
		var dy float32
		fmt.Sscan(y, &dy)
		ed.scrollBy(dy)
	}
	tt.Frame()
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

func TestDoubleClickWordStaysInBlock(t *testing.T) {
	src := "## 科学问题\n\n选取 MEG 数据\n"
	// 标题末尾双击：只选标题末段汉字，不越过换行。
	if a, z := wordAt(src, strings.Index(src, "\n"), 0, strings.Index(src, "\n")); src[a:z] != "科学问题" {
		t.Fatalf("块尾双击选中 %q", src[a:z])
	}
	p := strings.Index(src, "MEG") + 1
	b := strings.Index(src, "选")
	if a, z := wordAt(src, p, b, len(src)-1); src[a:z] != "MEG" {
		t.Fatalf("双击英文词选中 %q", src[a:z])
	}
}

func TestBoldAcrossBlocksWrapsEachBlock(t *testing.T) {
	ed, tt := newTester(t, "## 标题\n\n正文段落\n", 800, 600)
	src := ed.Doc.Source()
	ed.Doc.SetSelection(Selection{strings.Index(src, "题"), strings.Index(src, "正") + len("正")})
	tt.Command("bold")
	if got := ed.Doc.Source(); got != "## 标**题**\n\n**正**文段落\n" {
		t.Fatalf("跨块加粗 %q", got)
	}
	tt.Command("undo")
	if got := ed.Doc.Source(); got != src {
		t.Fatalf("撤销后 %q", got)
	}
}

func TestClickInParagraphGapPicksNearestBlock(t *testing.T) {
	ed, tt := newTester(t, "## 标题\n\n正文\n", 800, 600)
	tt.Frame()
	head, body := ed.vsegs[0], ed.vsegs[1]
	lh := ed.entries[0].rows[0].bandH
	// 段间距中靠近下一块的位置：应落入正文块。
	tt.ClickAt(ed.left()+2, body.y-1)
	tt.Frame()
	if i, _ := ed.Doc.BlockAt(ed.Doc.Sel.Head); i != 1 {
		t.Fatalf("靠近正文的间距点击落在块 %d", i)
	}
	tt.ClickAt(ed.left()+2, head.y+lh+1)
	tt.Frame()
	if i, _ := ed.Doc.BlockAt(ed.Doc.Sel.Head); i != 0 {
		t.Fatalf("靠近标题的间距点击落在块 %d", i)
	}
}

// TestPageJumpPrefetch 比较空闲预排版开、关时“整屏翻页”那一帧的耗时（只计翻页帧本身）。
func TestPageJumpPrefetch(t *testing.T) {
	if os.Getenv("NOMO_PERF") == "" {
		t.Skip("设 NOMO_PERF=1 运行性能测量")
	}
	src := largeDoc(t, 100_000)
	for _, budget := range []time.Duration{0, 4 * time.Millisecond} {
		prefetchBudget = budget
		ed, tt := newTester(t, src, 1200, 900)
		d := make([]time.Duration, 40)
		for i := range d {
			if budget > 0 {
				tt.Advance(400 * time.Millisecond) // 留出空闲帧
			} else {
				ed.prefetchedTick = ed.tick // 关闭：视为已完成
			}
			s := time.Now()
			tt.Scroll(600, 450, 0, 900)
			tt.Frame()
			d[i] = time.Since(s)
		}
		slices.Sort(d)
		fmt.Printf("page jump prefetch=%v p50=%v p95=%v max=%v\n", budget > 0, d[20], d[38], d[39])
	}
	prefetchBudget = 4 * time.Millisecond
}

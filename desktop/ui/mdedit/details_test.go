package mdedit

import (
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

const detailsDoc = "# 标题\n\n<details>\n<summary>折叠 <strong>标题</strong></summary>\n\n正文一\n\n正文二\n\n</details>\n\n尾段\n"

func TestParseDetailsMarker(t *testing.T) {
	closes, opens, ok := parseDetailsMarker("<details open>\n<summary>A <b>B</b></summary>")
	if !ok || len(closes) != 0 || len(opens) != 1 || !opens[0].defaultOpen || opens[0].summary != "A <b>B</b>" {
		t.Fatalf("开始标记：%v %+v %v", closes, opens, ok)
	}
	if closes, opens, ok = parseDetailsMarker("</details>\n</DETAILS>\n<details><summary></summary>"); !ok || len(closes) != 2 || len(opens) != 1 || opens[0].defaultOpen {
		t.Fatalf("结束后接开始：%v %+v %v", closes, opens, ok)
	}
	for _, bad := range []string{"<details class=x>\n<summary>A</summary>", "<details>\n<p>x</p>", "<details>\n<summary>A</summary>\ntext", "<details><summary>A</summary></details>"} {
		if _, _, ok := parseDetailsMarker(bad); ok {
			t.Errorf("%q 不应识别为标记", bad)
		}
	}
}

func TestDetailsFramesFromCorpusSamples(t *testing.T) {
	root := os.Getenv("NOMO_CORPUS")
	if root == "" {
		t.Skip("设 NOMO_CORPUS=<测试集目录> 运行")
	}
	read := func(rel string) *Document {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		return NewDocument(string(b))
	}
	x := read("01_基础与节点/06_200折叠块与12层嵌套.md").details()
	maxDepth := 0
	for _, f := range x.frames {
		maxDepth = max(maxDepth, f.depth)
	}
	if len(x.frames) != 212 || maxDepth != 11 {
		t.Fatalf("框架 %d 个、最深 %d 层，期望 212、11", len(x.frames), maxDepth)
	}
	if x := read("08_边界与兼容/异常_未闭合折叠标签.md").details(); len(x.frames) != 0 {
		t.Fatalf("未闭合的折叠块不应构成框架：%d", len(x.frames))
	}
}

func TestDetailsFoldNavigateAndExpand(t *testing.T) {
	ed, tt := newTester(t, detailsDoc, 800, 600)
	d := ed.Doc
	body := strings.Index(d.Source(), "正文二")
	bi, _ := d.BlockAt(body)
	if !ed.detailsHidden(bi) || ed.height(bi) != 0 {
		t.Fatal("默认折叠时正文应隐藏、高度为 0")
	}
	// 方向键从标题行向下越过折叠的正文，落到尾段。
	p := strings.Index(d.Source(), "</summary>") + len("</summary>")
	d.SetSelection(Selection{Anchor: p, Head: p})
	ed.dirty = true
	tt.Frame()
	ed.setHead(ed.moveV(d.Sel.Head, 1), false)
	tt.Frame()
	if i, _ := d.BlockAt(d.Sel.Head); i < 0 || !strings.HasPrefix(d.Source()[d.blocks[i].Start:], "尾段") {
		t.Fatalf("下移应越过折叠正文，落在 %q", d.Source()[d.Sel.Head:min(len(d.Source()), d.Sel.Head+12)])
	}
	// 点击三角展开。
	var hit ui.Point
	found := false
	for y := float32(0); y < 300 && !found; y += 2 {
		for x := float32(0); x < 200; x += 2 {
			if _, ok := ed.detailsToggleHit(ui.Point{X: x, Y: y}); ok {
				hit, found = ui.Point{X: x, Y: y}, true
				break
			}
		}
	}
	if !found {
		t.Fatal("找不到标题行三角")
	}
	tt.ClickAt(hit.X+2, hit.Y+2)
	tt.Frame()
	if ed.detailsHidden(bi) {
		t.Fatal("点击三角后应展开")
	}
	tt.ClickAt(hit.X+2, hit.Y+2)
	tt.Frame()
	if !ed.detailsHidden(bi) {
		t.Fatal("再次点击应折叠")
	}
	// 程序定位（查找、大纲跳转）进入折叠区时自动展开；原文不变。
	before := d.Source()
	ed.JumpTo(body)
	tt.Frame()
	if ed.detailsHidden(bi) || d.Source() != before {
		t.Fatal("跳转到折叠正文应自动展开且不改原文")
	}
	if out := os.Getenv("NOMO_DETAILS_PNG"); out != "" {
		f, err := os.Create(out)
		if err == nil {
			_ = png.Encode(f, tt.Image())
			f.Close()
		}
	}
}

func TestDetailsCommands(t *testing.T) {
	ed, tt := newTester(t, "段落\n", 800, 600)
	d := ed.Doc
	d.SetSelection(Selection{Anchor: 1, Head: 1})
	ed.InsertDetails()
	tt.Frame()
	if got, want := d.Source(), "段落\n\n<details>\n<summary></summary>\n\n</details>\n"; got != want {
		t.Fatalf("插入后 %q", got)
	}
	if !strings.HasSuffix(d.Source()[:d.Sel.Head], "<summary>") {
		t.Fatal("光标应在标题中")
	}
	tt.Type("标题")
	tt.Frame()
	key := strings.Index(d.Source(), "<details")
	if !ed.SetDetailsDefaultOpen(key, true) || !strings.Contains(d.Source(), "<details open>\n<summary>标题</summary>") {
		t.Fatalf("默认展开：%q", d.Source())
	}
	ed.Run("undo")
	if strings.Contains(d.Source(), "open") {
		t.Fatalf("撤销后应恢复：%q", d.Source())
	}
	at := len(d.Source()) - len("</details>\n")
	ed.Replace(at, at, "正文\n\n")
	tt.Frame()
	if !ed.UnwrapDetails(strings.Index(d.Source(), "<details")) {
		t.Fatal("取消折叠失败")
	}
	if got, want := d.Source(), "段落\n\n标题\n\n正文\n"; got != want {
		t.Fatalf("取消折叠后 %q，期望 %q", got, want)
	}
}

func TestDetailsContextTarget(t *testing.T) {
	ed, tt := newTester(t, detailsDoc, 800, 600)
	for y := float32(0); y < 300; y += 2 {
		if f, ok := ed.detailsHeaderHit(ui.Point{X: 300, Y: y}); ok {
			tt.RightClickAt(300, y+1)
			if tg := ed.ContextTarget(); tg.Kind != "details" || tg.DetailsKey != f.key || tg.DetailsOpen {
				t.Fatalf("右键标题行的目标 %+v", tg)
			}
			return
		}
	}
	t.Fatal("找不到标题行")
}

// TestDetailsIndexShiftMatchesRebuild 随机编辑（含插入、删除折叠标记）后，就地平移的折叠索引与全量重建一致。
func TestDetailsIndexShiftMatchesRebuild(t *testing.T) {
	base := strings.Repeat(detailsDoc+"\n<details open>\n<summary>B</summary>\n\n<details>\n<summary>C</summary>\n\nx\n\n</details>\n\n</details>\n\n", 6)
	inserts := []string{"a", "中", "\n", "\n\n", "<details>\n<summary>N</summary>\n\n", "\n\n</details>\n\n", "</details>", " open", "# "}
	rng := rand.New(rand.NewSource(3))
	d := NewDocument(base)
	_ = d.details()
	for i := 0; i < 1500; i++ {
		s := d.Source()
		from := runeStartBefore(s, rng.Intn(len(s)+1))
		to := from
		if rng.Intn(4) == 0 {
			to = runeStartBefore(s, min(len(s), from+rng.Intn(20)))
		}
		d.Replace(from, to, inserts[rng.Intn(len(inserts))], editOther)
		got := d.details()
		d.detailsIdx = nil
		want := d.details()
		if len(got.frames) != len(want.frames) || len(got.markers) != len(want.markers) {
			t.Fatalf("第 %d 次编辑：框架 %d/%d，标记块 %d/%d", i, len(got.frames), len(want.frames), len(got.markers), len(want.markers))
		}
		for f := range want.frames {
			if got.frames[f] != want.frames[f] {
				t.Fatalf("第 %d 次编辑：框架 %d 为 %+v，重建 %+v", i, f, got.frames[f], want.frames[f])
			}
		}
		for k := range want.markers {
			g, w := got.markers[k], want.markers[k]
			if g.block != w.block || !slices.Equal(g.closes, w.closes) || !slices.Equal(g.opens, w.opens) {
				t.Fatalf("第 %d 次编辑：标记块 %d 为 %+v，重建 %+v", i, k, g, w)
			}
		}
	}
}

// TestNestedDetailsMarkers：列表中的折叠块与未配对的顶层标记显示为标题行 / 分隔线预览，不再显示原文。
func TestNestedDetailsMarkers(t *testing.T) {
	src := "- item\n\n  <details>\n  <summary>内层 <b>标题</b></summary>\n\n  body\n\n  </details>\n\n<details>\n<summary>未配对</summary>\n"
	ed, tt := newTester(t, src, 800, 600)
	tt.Frame()
	var kinds []uint8
	for i := range ed.entries {
		for _, r := range ed.ensure(i).rows {
			if r.htmlKind == previewDetailsMark {
				kinds = append(kinds, r.htmlKind)
			}
		}
	}
	if len(kinds) != 3 {
		t.Fatalf("标记预览行 %d 个，应为 3（列表内开始、结束与未配对的开始）", len(kinds))
	}
	if got := detailsMarkHTML("<details open>\n<summary>A <code>x</code></summary>"); got != "<p><strong>▾ A <code>x</code></strong></p>" {
		t.Fatalf("开始标记预览 %q", got)
	}
	if got := detailsMarkHTML("</details>"); got != "<hr>" {
		t.Fatalf("结束标记预览 %q", got)
	}
}

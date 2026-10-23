package mdedit

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// testPost 收集后台结果，由测试在主 goroutine 中执行（对应外壳的 Window.Update）。
type testPost struct {
	mu  sync.Mutex
	fns []func()
}

func (tp *testPost) post(fn func()) {
	tp.mu.Lock()
	tp.fns = append(tp.fns, fn)
	tp.mu.Unlock()
}

// drain 等待至少 n 个结果并执行。
func (tp *testPost) drain(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		tp.mu.Lock()
		if len(tp.fns) >= n {
			fns := tp.fns
			tp.fns = nil
			tp.mu.Unlock()
			for _, fn := range fns {
				fn()
			}
			return
		}
		tp.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("等待图片加载超时")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func pngBytes(t testing.TB, w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 200, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestImageProjectsAsObjectUnlessExpanded(t *testing.T) {
	src := "前 ![图](a.png) 后 <img src=\"b.png\" width=\"50%\" alt='B'>"
	d := NewDocument(src)
	p := projectLeaf(d, d.blocks[0].Index, 0, len(src), expansion{}, leafOpts{})
	if len(p.objs) != 2 || p.objs[0].src != "a.png" || p.objs[0].alt != "图" {
		t.Fatalf("objs = %+v", p.objs)
	}
	if o := p.objs[1]; o.src != "b.png" || o.width != "50%" || o.alt != "B" {
		t.Fatalf("img 标签 = %+v", o)
	}
	if want := "前 " + objChar + " 后 " + objChar; p.text != want {
		t.Fatalf("显示文本 %q", p.text)
	}
	// 光标落在 Markdown 图片内：展开原文，不再是对象。
	in := expansion{inBlock: true, caret: len("前 ![")}
	p = projectLeaf(d, d.blocks[0].Index, 0, len(src), in, leafOpts{})
	if len(p.objs) != 1 || p.objs[0].src != "b.png" {
		t.Fatalf("展开后 objs = %+v", p.objs)
	}
}

func TestHTMLAttr(t *testing.T) {
	tag := `<img data-x=1 SRC='a b.png' width=300 alt="&lt;x&gt;" />`
	if got := htmlAttr(tag, "src"); got != "a b.png" {
		t.Fatalf("src = %q", got)
	}
	if got := htmlAttr(tag, "width"); got != "300" {
		t.Fatalf("width = %q", got)
	}
	if got := htmlAttr(tag, "alt"); got != "<x>" {
		t.Fatalf("alt = %q", got)
	}
	if htmlAttr(tag, "title") != "" {
		t.Fatal("不存在的属性应为空")
	}
}

func TestImageLoadsAndRelayouts(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pic.png"), pngBytes(t, 40, 20), 0o644); err != nil {
		t.Fatal(err)
	}
	src := "![a](pic.png)\n\n<img src=\"pic.png\" width=\"80\">\n\n![m](missing.png)\n"
	ed, tt := newTester(t, src, 800, 600)
	tp := &testPost{}
	im := NewImages(tp.post, DefaultImageBudget)
	defer im.Close()
	ed.SetImages(im, dir)
	tt.Frame()
	r := &ed.entries[0].rows[0]
	if len(r.objSizes) != 1 || r.objSizes[0] != [2]float32{imagePendingW, imagePendingH} {
		t.Fatalf("加载中尺寸 %v", r.objSizes)
	}
	tp.drain(t, 2) // pic.png 与 missing.png（同一路径只加载一次）
	tt.Frame()
	r = &ed.entries[0].rows[0]
	if r.objSizes[0] != [2]float32{40, 20} || r.objRects[0].W != 40 {
		t.Fatalf("加载后尺寸 %v 位置 %v", r.objSizes, r.objRects)
	}
	if got := ed.entries[1].rows[0].objSizes[0]; got != [2]float32{80, 40} {
		t.Fatalf("width=80 的尺寸 %v", got)
	}
	if en := im.m[filepath.Join(dir, "missing.png")]; en == nil || en.state != imageFailed {
		t.Fatal("缺失的图片应标记为失败")
	}
	if len(im.m) != 2 || im.Bytes() != 40*20*4*4/3 {
		t.Fatalf("缓存项 %d 字节 %d", len(im.m), im.Bytes())
	}
}

func TestDecodeImageDownscalesLargeBitmaps(t *testing.T) {
	res := decodeImage(pngBytes(t, 3000, 300), false)
	if res.err != nil {
		t.Fatal(res.err)
	}
	if res.w != 3000 || res.h != 300 {
		t.Fatalf("原始尺寸 %v×%v", res.w, res.h)
	}
	if w, h := res.bmp.Size(); w != maxDecodeDim || h != 240 {
		t.Fatalf("保留像素 %d×%d", w, h)
	}
	svg := decodeImage([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="64" height="32"><rect width="64" height="32"/></svg>`), false)
	if svg.err != nil || svg.svg == nil || svg.w != 64 || svg.h != 32 {
		t.Fatalf("svg = %+v", svg)
	}
	if bad := decodeImage([]byte("not an image"), false); bad.err == nil {
		t.Fatal("无效数据应报错")
	}
}

func TestImagesEvictLeastRecentlyUsedOutsideFrame(t *testing.T) {
	im := NewImages(func(func()) {}, 100)
	defer im.Close()
	add := func(key string, used uint64) *imageEntry {
		en := &imageEntry{key: key, state: imageReady, bytes: 60, used: used}
		im.m[key], im.bytes = en, im.bytes+60
		return en
	}
	im.clock = 5
	add("old", 1)
	add("mid", 3)
	add("now", 5) // 本帧可见
	im.evict()
	if _, ok := im.m["old"]; ok {
		t.Fatal("最久未用的应被淘汰")
	}
	if _, ok := im.m["now"]; !ok {
		t.Fatal("本帧使用的不应淘汰")
	}
	if _, ok := im.m["mid"]; ok || im.bytes != 60 {
		t.Fatalf("超出预算时继续淘汰，剩余 %d 字节", im.bytes)
	}
}

func TestResolveImageAndDataURI(t *testing.T) {
	im := NewImages(func(func()) {}, 1)
	defer im.Close()
	dir := filepath.FromSlash("/docs")
	cases := map[string]string{
		"a%20b.png":           filepath.Join(dir, "a b.png"),
		"file:///tmp/x.png":   "/tmp/x.png",
		"https://e.com/x.png": "https://e.com/x.png",
		"ftp://e.com/x.png":   "",
		"":                    "",
	}
	for src, want := range cases {
		if key, _ := im.resolveImage(src, dir); key != want {
			t.Errorf("%q → %q，期望 %q", src, key, want)
		}
	}
	if key, _ := im.resolveImage("rel.png", ""); key != "" {
		t.Error("未保存文档的相对路径不应解析")
	}
	data := pngBytes(t, 2, 2)
	got, err := decodeDataURI("data:image/png;base64," + base64.StdEncoding.EncodeToString(data))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("data uri 解码失败：%v", err)
	}
}

func TestOrientRGBA(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	src.Set(0, 0, color.RGBA{255, 0, 0, 255})
	dst := orientRGBA(src, 6) // 顺时针 90°：左上角移到右上角
	if b := dst.Bounds(); b.Dx() != 2 || b.Dy() != 3 {
		t.Fatalf("尺寸 %v", b)
	}
	if c := dst.RGBAAt(1, 0); c.R != 255 {
		t.Fatalf("像素位置错误 %v", c)
	}
}

func TestHTMLImageBlockAndClickToExpand(t *testing.T) {
	if _, ok := htmlImagesOnly("<p align=\"center\">\n  <a href=\"x\"><img src=\"a.png\"></a>\n</p>"); !ok {
		t.Fatal("链接包裹的图片块应识别为图片块")
	}
	if align, _ := htmlImagesOnly(`<p align="center"><img src="a.png"></p>`); align != ui.Center {
		t.Fatal("align=center 应居中")
	}
	for _, s := range []string{`<p align="center"><strong>x</strong></p>`, `<div>text <img src="a.png"></div>`, `<img alt="x">`} {
		if _, ok := htmlImagesOnly(s); ok {
			t.Fatalf("%q 不是图片块", s)
		}
	}
	p := projectHTMLImages("<p>\n  <img src=\"a.png\">\n  <img src=\"b.png\"><br><img src=\"c.png\">\n</p>")
	if p.text != objChar+" "+objChar+"\n"+objChar || len(p.objs) != 3 {
		t.Fatalf("投影 %q objs %d", p.text, len(p.objs))
	}

	src := "正文\n\n<p align=\"center\"><img src=\"a.png\" width=\"100\"></p>\n"
	ed, tt := newTester(t, src, 800, 600)
	ed.Doc.SetSelection(Selection{0, 0})
	tt.Frame()
	r := &ed.entries[1].rows[0]
	if !r.htmlImg || len(r.objRects) != 1 {
		t.Fatalf("图片块行 htmlImg=%v objs=%d", r.htmlImg, len(r.objRects))
	}
	// 点击图片：光标进入原文，图片块展开为源码（不再有对象）。
	var pt ui.Point
	for _, v := range ed.vsegs {
		if v.i == 1 {
			s := ed.segOf(v.i, v.k)
			rc := r.objRects[0]
			pt = ui.Point{X: ed.left() + s.x + rc.X + rc.W/2, Y: v.y + s.padTop + rc.Y + rc.H/2}
		}
	}
	tt.ClickAt(pt.X, pt.Y)
	tt.Frame()
	if head := ed.Doc.Sel.Head; head != strings.Index(src, "<img")+1 {
		t.Fatalf("光标 %d", head)
	}
	if r := &ed.entries[1].rows[0]; len(r.objRects) != 0 || !strings.Contains(r.proj.text, "<img") {
		t.Fatalf("点击后应显示原文：%q", r.proj.text)
	}
}

func TestImagesRevalidateDropsChangedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.png")
	if err := os.WriteFile(path, pngBytes(t, 4, 4), 0o644); err != nil {
		t.Fatal(err)
	}
	tp := &testPost{}
	im := NewImages(tp.post, DefaultImageBudget)
	defer im.Close()
	im.request("a.png", dir)
	tp.drain(t, 1)
	if en := im.m[path]; en == nil || en.state != imageReady || en.modTime.IsZero() {
		t.Fatal("应已加载并记录修改时间")
	}
	im.Revalidate() // 未修改：不应回调
	if err := os.Chtimes(path, time.Now(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	im.Revalidate()
	tp.drain(t, 1)
	if _, ok := im.m[path]; ok || im.Bytes() != 0 {
		t.Fatal("外部修改的图片应移出缓存")
	}
}

// TestImageHeavyScroll 测量图片较多的文档中滚动的帧耗时与缓存占用（无头 CPU 渲染）。
// 30 张 1200×800 的不同图片约 150 MB 解码后像素，超过默认预算，用于检查淘汰。设 NOMO_PERF=1 时运行。
func TestImageHeavyScroll(t *testing.T) {
	if os.Getenv("NOMO_PERF") == "" {
		t.Skip("设 NOMO_PERF=1 运行性能测量")
	}
	dir := t.TempDir()
	var src strings.Builder
	data := pngBytes(t, 1200, 800)
	for k := range 30 {
		name := filepath.Join(dir, "p"+strconv.Itoa(k)+".png")
		if err := os.WriteFile(name, data, 0o644); err != nil {
			t.Fatal(err)
		}
		src.WriteString("段落文字 " + strconv.Itoa(k) + "\n\n![p](p" + strconv.Itoa(k) + ".png)\n\n")
	}
	ed, tt := newTester(t, src.String(), 1000, 800)
	tp := &testPost{}
	im := NewImages(tp.post, DefaultImageBudget)
	defer im.Close()
	ed.SetImages(im, dir)
	tt.Frame()
	var times []time.Duration
	maxBytes := 0
	for range 200 {
		tp.mu.Lock()
		fns := tp.fns
		tp.fns = nil
		tp.mu.Unlock()
		for _, fn := range fns {
			fn()
		}
		start := time.Now()
		tt.Scroll(500, 400, 0, 120)
		tt.Frame()
		times = append(times, time.Since(start))
		maxBytes = max(maxBytes, im.Bytes())
		time.Sleep(15 * time.Millisecond) // 留出解码时间，模拟连续阅读

	}
	// 往返两轮：第一轮把屏幕外加载完成的行修正为实际尺寸；第二轮中被淘汰后重新加载的图片按记住的
	// 尺寸占位，总高度应保持不变。
	total, pos := ed.scrollMetrics()
	roundTrip := func() float32 {
		for range 40 {
			tt.Scroll(500, 400, 0, -600)
			tt.Frame()
		}
		for range 40 {
			tt.Scroll(500, 400, 0, 600)
			tt.Frame()
		}
		tot, _ := ed.scrollMetrics()
		return tot
	}
	t1 := roundTrip()
	t2 := roundTrip()
	t.Logf("scroll total=%.0f final pos=%.0f after round trips=%.0f, %.0f", total, pos, t1, t2)
	if t2 != t1 {
		t.Fatalf("往返后总高度 %v → %v", t1, t2)
	}
	slices.Sort(times)
	t.Logf("scroll with images n=%d p50=%v p95=%v max=%v", len(times), times[len(times)/2], times[len(times)*95/100], times[len(times)-1])
	t.Logf("image cache max=%.1f MB entries=%d final=%.1f MB", float64(maxBytes)/(1<<20), len(im.m), float64(im.Bytes())/(1<<20))
	if maxBytes > DefaultImageBudget+3*(1200*800*4*4/3) {
		t.Fatalf("缓存 %d 字节，超出预算加一屏图片", maxBytes)
	}
}

func TestAnimatedGIFFrames(t *testing.T) {
	data, err := os.ReadFile("/Users/zhangweihao/Downloads/Nomo性能测试集_2026-10-22/assets/formats/animated.gif")
	if err != nil {
		t.Skip("缺少测试集 GIF")
	}
	res := decodeImage(data, false)
	if res.err != nil || len(res.frames) < 2 || len(res.delays) != len(res.frames) {
		t.Fatalf("应解码出多帧：%d 帧 err=%v", len(res.frames), res.err)
	}
	en := &imageEntry{bmp: res.bmp, frames: res.frames, delays: res.delays}
	for _, d := range res.delays {
		en.period += d
	}
	a, next := en.frameAt(time.Unix(0, 0))
	b, _ := en.frameAt(time.Unix(0, 0).Add(next))
	if a == b || next <= 0 {
		t.Fatal("经过一帧停留时间后应切到下一帧")
	}
}

func TestRenderObjectsMathAndMermaid(t *testing.T) {
	src := "行内 $a^2$ 公式。\n\n$$\nx=1\n$$\n\n```mermaid\nflowchart TD\n  A-->B\n```\n"
	ed, tt := newTester(t, src, 900, 700)
	tp := &testPost{}
	im := NewImages(tp.post, DefaultImageBudget)
	defer im.Close()
	var reqs []RenderRequest
	var mu sync.Mutex
	im.SetRenderer(func(req RenderRequest) (image.Image, float32, float32, float32, error) {
		mu.Lock()
		reqs = append(reqs, req)
		mu.Unlock()
		img := image.NewRGBA(image.Rect(0, 0, 80, 40))
		return img, 40, 20, 5, nil
	})
	ed.SetImages(im, "")
	ed.Doc.SetSelection(Selection{0, 0})
	tt.Frame()
	tp.drain(t, 3)
	tt.Frame()
	mu.Lock()
	kinds := map[string]string{}
	for _, r := range reqs {
		kinds[r.Kind] = r.Src
	}
	mu.Unlock()
	if kinds[RenderMathInline] != "a^2" || kinds[RenderMathDisplay] != "x=1" || kinds[RenderMermaid] != "flowchart TD\n  A-->B" {
		t.Fatalf("渲染请求 %v", kinds)
	}
	// 行内公式对象只算基线以上的高度（20 − 深度 5）。
	if got := ed.entries[0].rows[0].objSizes[0]; got != [2]float32{40, 15} {
		t.Fatalf("行内公式尺寸 %v", got)
	}
	if r := &ed.entries[1].rows[0]; r.htmlKind != previewMath || r.objSizes[0] != [2]float32{40, 20} {
		t.Fatalf("公式块 %+v", r.objSizes)
	}
	// 光标进入公式块原文：显示原文。
	b := ed.Doc.Blocks()[1]
	ed.Doc.SetSelection(Selection{b.Start + 3, b.Start + 3})
	ed.dirty = true
	tt.Frame()
	if r := &ed.entries[1].rows[0]; len(r.proj.objs) != 0 {
		t.Fatal("光标在公式块内应显示原文")
	}
	// 没有渲染函数时按原文框显示，不崩溃。
	ed2, tt2 := newTester(t, "$x$\n", 400, 300)
	ed2.SetImages(NewImages(func(func()) {}, DefaultImageBudget), "")
	ed2.Doc.SetSelection(Selection{4, 4})
	tt2.Frame()
	if en := ed2.imageEntry(ed2.entries[0].rows[0].proj.objs[0]); en == nil || en.state != imageFailed {
		t.Fatal("无渲染函数时应标记失败")
	}
}

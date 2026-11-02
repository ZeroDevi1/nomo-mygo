package shell

import (
	"github.com/egoist/mygo/ui"
	"image"
	"image/draw"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// NOMO_THESIS_PREVIEW 指向讲稿目录；只编辑临时副本。每轮等待正式页校正后重新测量。
func TestThesisContinuousPreview(t *testing.T) {
	root := os.Getenv("NOMO_THESIS_PREVIEW")
	if root == "" {
		t.Skip("set NOMO_THESIS_PREVIEW to the math-talk.tex directory")
	}
	src, e := os.ReadFile(filepath.Join(root, "math-talk.tex"))
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "math-talk.tex")
	if e = os.WriteFile(path, src, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(filepath.Join(root, "figures"), filepath.Join(dir, "figures")); e != nil {
		t.Fatal(e)
	}
	s, tt := newTestShell(t, path)
	posted := make(chan func(), 8)
	s.post = func(f func()) { posted <- f }
	defer s.closeCompile()
	if err := tt.Click(s.tr("splitMode")); err != nil {
		t.Fatal(err)
	}
	p := s.compile
	buf := s.activeTab().Source().Buffer
	wait := func(check func() bool) {
		t.Helper()
		limit := time.After(60 * time.Second)
		for !check() {
			select {
			case f := <-posted:
				f()
				tt.Frame()
				if p.err != "" {
					t.Fatal(p.err)
				}
			case <-limit:
				t.Fatal("preview timeout")
			}
		}
	}
	wait(func() bool { return p.image != nil && !p.busy })
	off := strings.Index(string(src), "这份讲稿从矩阵乘法开始解释") + len("这份讲稿")
	if off < len("这份讲稿") {
		t.Fatal("sample paragraph missing")
	}
	s.focusEditor = true
	tt.Frame()
	s.activeTab().Source().Select(off, off)
	tt.Frame()
	var warm, final []time.Duration
	for round := range 3 {
		timestamps := map[uint64]time.Time{}
		var shown uint64
		var rect ui.Rect
		pageBox, _ := tt.Find(s.tr("compiledPage"))
		page := p.page
		count := 0
		var lag time.Duration
		screen := tt.Image()
		baseline := image.NewRGBA(screen.Bounds())
		draw.Draw(baseline, baseline.Bounds(), screen, image.Point{}, draw.Src)
		check := func() {
			t.Helper()
			if r, ok := tt.Find(s.tr("compiledPage")); !ok || r != pageBox || p.page != page {
				t.Fatalf("page moved: %v => %v", pageBox, r)
			}
			if p.paragraph == nil && p.inlineText == "" {
				return
			}
			r, ok := tt.Find(s.tr("inlineDraft"))
			if !ok || r.W <= 0 || r.H <= 0 || p.anchor.Page != page {
				t.Fatalf("draft not anchored: %+v %+v", p.anchor, r)
			}
			if rect.W == 0 {
				rect = r
			} else if rect != r {
				t.Fatalf("draft moved: %+v -> %+v", rect, r)
			}
			if t0, ok := timestamps[p.shown]; ok && shown != p.shown {
				lag = time.Since(t0)
				if count != 0 || round != 0 {
					warm = append(warm, lag)
				}
				shown = p.shown
				count++
				if count == 1 {
					t.Logf("round %d first frame %v", round+1, lag)
				}
			}
		}
		for range 24 {
			start := time.Now()
			tt.Type("新")
			off += len("新")
			if s.activeTab().Source().Cursor() != off {
				t.Fatal("input did not reach editor")
			}
			timestamps[buf.Version()] = start
			tt.Frame()
			check()
			tick := time.NewTimer(max(0, 60*time.Millisecond-time.Since(start)))
		drain:
			for {
				select {
				case f := <-posted:
					f()
					tt.Frame()
					check()
				case <-tick.C:
					break drain
				}
			}
		}
		during := count
		wait(func() bool { check(); return shown == buf.Version() })
		lat := lag
		final = append(final, lat)
		if during < 4 {
			t.Fatal("input starved preview", during)
		}
		// 直接检查渲染像素，排除元素坐标未变但整张页面仍闪烁的情况。
		screen = tt.Image()
		inside := 0
		for y := int(pageBox.Y) + 1; y < int(pageBox.Y+pageBox.H)-1; y++ {
			for x := int(pageBox.X) + 1; x < int(pageBox.X+pageBox.W)-1; x++ {
				if baseline.RGBAAt(x, y) == screen.RGBAAt(x, y) {
					continue
				}
				if float32(x) < rect.X-2 || float32(x) > rect.X+rect.W+2 || float32(y) < rect.Y-2 || float32(y) > rect.Y+rect.H+2 {
					t.Fatalf("changed page pixel outside paragraph: %d,%d", x, y)
				}
				inside++
			}
		}
		if inside == 0 {
			t.Fatal("draft did not change page pixels")
		}
		snapshot(t, tt, "thesis-inline")
		wait(func() bool { return !p.busy && p.paragraph == nil })
		check()
		t.Logf("round %d: %d updates during input, last key to frame %v; page %d; box %+v", round+1, during, lat, page, rect)
	}
	slices.Sort(warm)
	p95 := warm[int(math.Ceil(.95*float64(len(warm))))-1]
	t.Logf("warm n=%d P50=%v P95=%v final=%v", len(warm), warm[len(warm)/2], p95, final)
	if p95 > 250*time.Millisecond || slices.Max(final) > 250*time.Millisecond {
		t.Fatal("latency gate")
	}
}

package mdedit

import (
	"bytes"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/docfs"
)

// TestCorpus 用外部测试集（如“Nomo 性能测试集”）逐文件检查原生语义编辑器：
//
//   - 保真：解码 → 编码字节不变；打开后原文不变；输入一个字再撤销后原文不变。
//   - 耗时：解析 + 首帧、滚动到 25/50/75/100% 每次的帧、查找 NOMO_END 与跳转、键入一个中文字的帧。
//   - 内存：处理完该文件后的 Go 堆与布局缓存估算。
//
// 设 NOMO_CORPUS=<目录> 运行；NOMO_CORPUS_OUT=<目录> 输出 report.md 与首屏截图；NOMO_CORPUS_MATCH 只跑路径
// 含该子串的文件；NOMO_CORPUS_MAX=<字节> 跳过更大的文件（默认不限）；NOMO_CORPUS_KP=1 开启精细排版。无头 CPU 渲染，不代表 GPU 窗口实测。
func TestCorpus(t *testing.T) {
	root := os.Getenv("NOMO_CORPUS")
	if root == "" {
		t.Skip("设 NOMO_CORPUS=<测试集目录> 运行")
	}
	out := os.Getenv("NOMO_CORPUS_OUT")
	if out != "" {
		_ = os.MkdirAll(filepath.Join(out, "shots"), 0o755)
	}
	var maxBytes int64
	fmt.Sscan(os.Getenv("NOMO_CORPUS_MAX"), &maxBytes)
	var files []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".md") && strings.Contains(p, os.Getenv("NOMO_CORPUS_MATCH")) {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	var report strings.Builder
	report.WriteString("| 文件 | 字节 | 块 | 首帧 | 滚动最大 | 查找+跳转 | 键入 | 堆 MB | 缓存 MB | 问题 |\n|---|---:|---:|---:|---:|---:|---:|---:|---:|---|\n")
	for _, p := range files {
		rel, _ := filepath.Rel(root, p)
		data, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if maxBytes > 0 && int64(len(data)) > maxBytes {
			continue
		}
		row := corpusFile(t, p, rel, data, out)
		report.WriteString(row)
		runtime.GC()
	}
	if out != "" {
		_ = os.WriteFile(filepath.Join(out, "report.md"), []byte(report.String()), 0o644)
	}
	t.Log("\n" + report.String())
}

func corpusFile(t *testing.T, path, rel string, data []byte, out string) string {
	var issues []string
	text, enc, err := docfs.Decode(data)
	if err != nil {
		return fmt.Sprintf("| %s | %d | | | | | | | | 解码失败：%v |\n", rel, len(data), err)
	}
	if back, err := docfs.Encode(text, enc); err != nil || !bytes.Equal(back, data) {
		issues = append(issues, "编码往返不一致")
	}

	// 打开：解析 + 首帧。
	start := time.Now()
	doc := NewDocument(text)
	ed := NewEditor(doc)
	if os.Getenv("NOMO_CORPUS_KP") != "" {
		ed.SetOptions(Options{Typography: true})
	}
	tt := ui.NewTester(func(c *ui.Context) { ed.View(c).AutoFocus() }, 1200, 800)
	tp := &testPost{}
	im := NewImages(tp.post, DefaultImageBudget)
	defer im.Close()
	ed.SetImages(im, filepath.Dir(path))
	tt.Frame()
	first := time.Since(start)
	// 等本屏图片加载（至多 3 秒），再截首屏。
	for k := 0; k < 60 && !im.idle(); k++ {
		time.Sleep(50 * time.Millisecond)
		drainPost(tp)
		tt.Frame()
	}
	drainPost(tp)
	tt.Frame()
	if out != "" && !strings.HasPrefix(rel, "09_") { // 目录树 256 份小文件不逐一截图
		name := strings.NewReplacer("/", "__", " ", "_").Replace(strings.TrimSuffix(rel, ".md")) + ".png"
		if f, err := os.Create(filepath.Join(out, "shots", name)); err == nil {
			_ = png.Encode(f, tt.Image())
			f.Close()
		}
	}
	if doc.Source() != text {
		issues = append(issues, "打开后原文变化")
	}

	// 滚动到 25/50/75/100%。
	var worst time.Duration
	for _, f := range []float32{0.25, 0.5, 0.75, 1} {
		total, _ := ed.scrollMetrics()
		ed.scrollTo(total * f)
		s := time.Now()
		tt.Scroll(600, 400, 0, 1) // 触发一帧（scrollTo 不经过输入）
		tt.Frame()
		worst = max(worst, time.Since(s))
	}

	// 查找 NOMO_END 并跳转。
	s := time.Now()
	found := doc.Find("NOMO_END", true, false)
	if len(found) > 0 {
		ed.JumpTo(found[len(found)-1][0])
		ed.dirty = true
		tt.Frame()
	}
	find := time.Since(s)
	if len(found) == 0 && strings.Contains(text, "NOMO_START") {
		issues = append(issues, "未找到 NOMO_END")
	}

	// 回到开头键入一个中文字，再撤销。
	doc.SetSelection(Selection{0, 0})
	ed.dirty = true
	tt.Frame()
	s = time.Now()
	tt.Type("中")
	typing := time.Since(s)
	if !strings.HasPrefix(doc.Source(), "中") {
		issues = append(issues, "键入未生效")
	}
	ed.Run("undo")
	tt.Frame()
	if doc.Source() != text {
		issues = append(issues, "撤销后原文不一致")
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	ms2 := func(d time.Duration) string { return fmt.Sprintf("%.1f ms", float64(d.Microseconds())/1000) }
	return fmt.Sprintf("| %s | %d | %d | %s | %s | %s | %s | %.1f | %.1f | %s |\n", rel, len(data), len(doc.blocks), ms2(first), ms2(worst), ms2(find), ms2(typing),
		float64(ms.HeapInuse)/(1<<20), float64(ed.CacheBytes()+im.Bytes())/(1<<20), strings.Join(issues, "；"))
}

func drainPost(tp *testPost) {
	tp.mu.Lock()
	fns := tp.fns
	tp.fns = nil
	tp.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

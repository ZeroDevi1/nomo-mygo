package shell

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"
	"sync"
	"time"

	"github.com/egoist/mygo"

	"nomo/desktop/internal/applog"
	"nomo/desktop/ui/mdedit"
)

// 公式与 Mermaid 图表的离屏渲染服务（计划 D3 方案 ②：按需 WebView）：第一次有渲染请求时创建隐藏窗口加载
// 前端的 render.html（KaTeX 与 mermaid，与原版编辑器同一套渲染调用），逐个渲染到舞台、截图并裁剪为位图；
// 最后一次请求 renderIdle 之后销毁窗口，不常驻 WebView 进程。请求由图片缓存的后台 worker 调用（串行化），
// 结果作为图片缓存条目参与字节预算与淘汰（见 mdedit.Images）。

const (
	// renderViewport 是隐藏窗口的尺寸（CSS 像素）：更大的图表在页面内按比例缩小到此范围再截图。
	renderViewport = 2400
	// renderIdle 是最后一次渲染后保留窗口的时间：连续滚动时复用，空闲后释放 WebView 进程的内存。
	renderIdle = 30 * time.Second
	// renderTimeout 是单次渲染（含首次加载页面）的上限；超时按渲染失败处理，正文显示原文。
	renderTimeout = 20 * time.Second
)

// renderTheme 是渲染配色：由 UI 线程按当前主题更新，渲染时读取。
type renderTheme struct {
	fg, bg, mermaid string
}

// renderService 的两把锁分工：mu 串行化渲染并保护窗口与空闲计时器，只在后台 goroutine（图片 worker、计时器）
// 中持有——渲染期间要等主线程执行 JS 与截图，UI 线程若等这把锁会死锁；themeMu 只保护配色，持有时间极短，
// UI 线程可以拿。
type renderService struct {
	mu   sync.Mutex
	win  *mygo.Window
	idle *time.Timer
	// kpRuntime 缓存精细排版运行时脚本（typographyRuntime），受 mu 保护。
	kpRuntime string

	themeMu sync.Mutex
	theme   [2]renderTheme // [0] 浅色、[1] 深色
}

// setTheme 更新配色，返回是否变化（变化时调用方丢弃已有渲染结果）。在 UI 线程调用。
func (r *renderService) setTheme(light, dark renderTheme) bool {
	r.themeMu.Lock()
	defer r.themeMu.Unlock()
	if r.theme == [2]renderTheme{light, dark} {
		return false
	}
	r.theme = [2]renderTheme{light, dark}
	return true
}

// close 销毁隐藏窗口（主窗口关闭时，在 UI 线程调用）：放到后台等进行中的渲染结束，不阻塞主线程。
func (r *renderService) close() {
	go func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.destroyLocked()
	}()
}

func (r *renderService) destroyLocked() {
	if r.idle != nil {
		r.idle.Stop()
		r.idle = nil
	}
	if r.win != nil {
		r.win.Destroy()
		r.win = nil
	}
}

// ensureLocked 创建并加载隐藏窗口（已存在时直接返回）。
func (r *renderService) ensureLocked(ctx context.Context) error {
	if r.win != nil {
		return nil
	}
	loaded := make(chan error, 1)
	win := mygo.NewWindow(mygo.WindowOptions{
		Title: "Nomo Render", URL: "/render.html", Width: renderViewport, Height: renderViewport,
		Hidden: true, Frameless: true, SkipTaskbar: true,
	})
	page := win.Page()
	page.OnDidFinishLoad(func() {
		select {
		case loaded <- nil:
		default:
		}
	})
	page.OnDidFailLoad(func(e *mygo.LoadError) {
		select {
		case loaded <- fmt.Errorf("渲染页加载失败：%v", e):
		default:
		}
	})
	select {
	case err := <-loaded:
		if err != nil {
			win.Destroy()
			return err
		}
	case <-ctx.Done():
		win.Destroy()
		return errors.New("渲染页加载超时")
	}
	r.win = win
	return nil
}

// render 批量渲染（mdedit.RenderFunc），在图片缓存的后台 worker 中调用：页面把一批请求纵向排布并逐个测量，
// 这里把窗口缩到整批的包围尺寸后截图一次，再按各项位置裁剪。
func (r *renderService) render(reqs []mdedit.RenderRequest) []mdedit.RenderResult {
	out := make([]mdedit.RenderResult, len(reqs))
	fail := func(err error) []mdedit.RenderResult {
		for k := range out {
			out[k].Err = err
		}
		return out
	}
	if len(reqs) == 0 {
		return out
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), renderTimeout)
	defer cancel()
	if err := r.ensureLocked(ctx); err != nil {
		applog.Warn("Native", "公式/图表渲染窗口不可用："+err.Error())
		return fail(err)
	}
	if r.idle != nil {
		r.idle.Stop()
	}
	r.idle = time.AfterFunc(renderIdle, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.destroyLocked()
	})
	r.themeMu.Lock()
	theme := r.theme
	r.themeMu.Unlock()
	items := make([]map[string]any, len(reqs))
	for k, req := range reqs {
		th := theme[0]
		if req.Dark {
			th = theme[1]
		}
		items[k] = map[string]any{"kind": req.Kind, "src": req.Src, "fg": th.fg, "bg": th.bg, "fontSize": req.FontSize, "mermaidTheme": th.mermaid, "w": req.W, "h": req.H}
	}
	arg, _ := json.Marshal(items)
	res, err := r.evalLocked(ctx, fmt.Sprintf("return await window.nomoRenderBatch(%s, %d)", arg, renderViewport))
	if err != nil {
		return fail(err)
	}
	batch, _ := res.(map[string]any)
	dpr, _ := batch["dpr"].(float64)
	dpr = max(1, dpr)
	list, _ := batch["items"].([]any)
	type box struct{ x, y, w, h, depth float64 }
	boxes := make([]box, len(reqs))
	var maxX, maxY float64
	for k := range reqs {
		it, _ := func() (map[string]any, bool) {
			if k < len(list) {
				m, ok := list[k].(map[string]any)
				return m, ok
			}
			return nil, false
		}()
		num := func(name string) float64 { v, _ := it[name].(float64); return v }
		switch {
		case it == nil:
			out[k].Err = errors.New("缺少渲染结果")
		case it["skipped"] == true:
			out[k].Skipped = true
		case it["error"] != nil:
			msg, _ := it["error"].(string)
			out[k].Err = errors.New(msg)
		case num("w") <= 0 || num("h") <= 0:
			out[k].Err = errors.New("渲染结果为空")
		default:
			boxes[k] = box{num("x"), num("y"), num("w"), num("h"), num("depth")}
			maxX, maxY = max(maxX, num("x")+num("w")), max(maxY, num("y")+num("h"))
		}
	}
	if maxX <= 0 {
		return out
	}
	// 截图取整个可见页面：先把窗口缩到整批的包围尺寸，避免编解码整块离屏视口（2 倍屏上可达 4800² 像素）。
	r.win.SetContentSize(int(math.Ceil(maxX))+2, int(math.Ceil(maxY))+2)
	// 等页面按新尺寸重绘（隐藏窗口里 rAF 被暂停，用定时器）。
	if _, err := r.evalLocked(ctx, "await new Promise((r) => setTimeout(r, 50)); return true"); err != nil {
		return fail(err)
	}
	shot, err := r.captureLocked(ctx)
	// 下一批渲染前恢复完整视口，让大图表按完整宽度排版。
	if r.win != nil {
		defer r.win.SetContentSize(renderViewport, renderViewport)
	}
	if err != nil {
		return fail(err)
	}
	full, err := png.Decode(bytes.NewReader(shot))
	if err != nil {
		return fail(err)
	}
	// 截图按设备像素，各项按 CSS 位置 × dpr 裁剪并复制出独立的位图。
	b := full.Bounds()
	for k, bx := range boxes {
		if out[k].Err != nil || out[k].Skipped || bx.w <= 0 {
			continue
		}
		crop := image.Rect(b.Min.X+int(bx.x*dpr), b.Min.Y+int(bx.y*dpr),
			b.Min.X+int(math.Ceil((bx.x+bx.w)*dpr)), b.Min.Y+int(math.Ceil((bx.y+bx.h)*dpr))).Intersect(b)
		img := image.NewRGBA(image.Rect(0, 0, crop.Dx(), crop.Dy()))
		draw.Draw(img, img.Bounds(), full, crop.Min, draw.Src)
		out[k] = mdedit.RenderResult{Img: img, W: float32(bx.w), H: float32(bx.h), Depth: float32(bx.depth)}
	}
	return out
}

// evalLocked 执行脚本并等待结果，超时则销毁窗口（下次请求重建）并返回错误：页面卡住时不让串行队列一直等。
// MyGo 的 Eval 等待结果时不响应 ctx，所以在单独的 goroutine 中调用。
func (r *renderService) evalLocked(ctx context.Context, js string) (any, error) {
	type result struct {
		v   any
		err error
	}
	ch := make(chan result, 1)
	page := r.win.Page()
	go func() {
		v, err := page.EvalContext(ctx, js)
		ch <- result{v, err}
	}()
	select {
	case res := <-ch:
		return res.v, res.err
	case <-ctx.Done():
		r.destroyLocked()
		return nil, errors.New("渲染超时")
	}
}

// captureLocked 截取离屏窗口页面，超时处理同 evalLocked。
func (r *renderService) captureLocked(ctx context.Context) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	win := r.win
	go func() {
		data, err := win.CapturePage()
		ch <- result{data, err}
	}()
	select {
	case res := <-ch:
		return res.data, res.err
	case <-ctx.Done():
		r.destroyLocked()
		return nil, errors.New("渲染截图超时")
	}
}

// markupItem 是导出用的渲染结果：html 为 KaTeX HTML 或 Mermaid SVG，失败时 err 非空。
type markupItem struct {
	html, err string
}

// markup 为导出渲染公式与图表的标记（不截图），返回各项结果与按需附带的 KaTeX 样式（含内联字体）。在后台调用。
func (r *renderService) markup(reqs []mdedit.RenderRequest, dark bool) ([]markupItem, string, error) {
	if len(reqs) == 0 {
		return nil, "", nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), renderTimeout*3)
	defer cancel()
	if err := r.ensureLocked(ctx); err != nil {
		return nil, "", err
	}
	r.themeMu.Lock()
	th := r.theme[0]
	if dark {
		th = r.theme[1]
	}
	r.themeMu.Unlock()
	items := make([]map[string]any, len(reqs))
	for k, req := range reqs {
		items[k] = map[string]any{"kind": req.Kind, "src": req.Src, "mermaidTheme": th.mermaid}
	}
	arg, _ := json.Marshal(items)
	res, err := r.evalLocked(ctx, "return await window.nomoRenderMarkup("+string(arg)+")")
	if err != nil {
		return nil, "", err
	}
	m, _ := res.(map[string]any)
	css, _ := m["css"].(string)
	list, _ := m["items"].([]any)
	out := make([]markupItem, len(reqs))
	for k := range out {
		if k >= len(list) {
			out[k].err = "缺少渲染结果"
			continue
		}
		it, _ := list[k].(map[string]any)
		out[k].html, _ = it["html"].(string)
		out[k].err, _ = it["error"].(string)
	}
	return out, css, nil
}

// word 在离屏页面中生成 .docx（复用原版 Word 导出），返回文件字节与降级说明。在后台调用，大文档可能耗时数十秒。
// typographyRuntime 返回原版精细排版的离线运行时脚本（导出时内联）；首次取得后缓存（单个字符串，约百 KB）。
func (r *renderService) typographyRuntime() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.kpRuntime != "" {
		return r.kpRuntime, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), renderTimeout*3)
	defer cancel()
	if err := r.ensureLocked(ctx); err != nil {
		return "", err
	}
	res, err := r.evalLocked(ctx, "return await window.nomoTypographyRuntime()")
	if err != nil {
		return "", err
	}
	s, _ := res.(string)
	if s == "" {
		return "", errors.New("精细排版运行时为空")
	}
	r.kpRuntime = s
	return s, nil
}

func (r *renderService) word(markdown, title string) ([]byte, []string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := r.ensureLocked(ctx); err != nil {
		return nil, nil, err
	}
	md, _ := json.Marshal(markdown)
	tt, _ := json.Marshal(title)
	res, err := r.evalLocked(ctx, "return await window.nomoExportWord("+string(md)+", "+string(tt)+")")
	if err != nil {
		return nil, nil, err
	}
	m, _ := res.(map[string]any)
	data, _ := m["data"].(string)
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(raw) == 0 {
		return nil, nil, errors.New("Word 导出结果为空")
	}
	var warnings []string
	if list, ok := m["warnings"].([]any); ok {
		for _, w := range list {
			if s, ok := w.(string); ok {
				warnings = append(warnings, s)
			}
		}
	}
	return raw, warnings, nil
}

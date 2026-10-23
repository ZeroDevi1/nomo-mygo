package shell

import (
	"bytes"
	"context"
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

// render 渲染一个请求（mdedit.RenderFunc）。在图片缓存的后台 worker 中调用。
func (r *renderService) render(req mdedit.RenderRequest) (image.Image, float32, float32, float32, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), renderTimeout)
	defer cancel()
	if err := r.ensureLocked(ctx); err != nil {
		applog.Warn("Native", "公式/图表渲染窗口不可用："+err.Error())
		return nil, 0, 0, 0, err
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
	th := r.theme[0]
	if req.Dark {
		th = r.theme[1]
	}
	r.themeMu.Unlock()
	arg, _ := json.Marshal(map[string]any{"kind": req.Kind, "src": req.Src, "fg": th.fg, "bg": th.bg,
		"fontSize": req.FontSize, "mermaidTheme": th.mermaid, "viewport": renderViewport})
	out, err := r.evalLocked(ctx, "return await window.nomoRender("+string(arg)+")")
	if err != nil {
		return nil, 0, 0, 0, err
	}
	res, _ := out.(map[string]any)
	num := func(k string) float64 { v, _ := res[k].(float64); return v }
	if msg, _ := res["error"].(string); msg != "" {
		return nil, 0, 0, 0, errors.New(msg)
	}
	w, h, depth, dpr := num("w"), num("h"), num("depth"), max(1, num("dpr"))
	if w <= 0 || h <= 0 {
		return nil, 0, 0, 0, errors.New("渲染结果为空")
	}
	// 截图取整个可见页面：先把窗口缩到舞台大小，避免每次编解码整块离屏视口（2 倍屏上可达 4800² 像素）。
	r.win.SetContentSize(int(math.Ceil(w))+2, int(math.Ceil(h))+2)
	// 等页面按新尺寸重绘（隐藏窗口里 rAF 被暂停，用定时器）。
	if _, err := r.evalLocked(ctx, "await new Promise((r) => setTimeout(r, 50)); return true"); err != nil {
		return nil, 0, 0, 0, err
	}
	shot, err := r.captureLocked(ctx)
	// 下一次渲染前恢复完整视口，让大图表按完整宽度排版。
	if r.win != nil {
		defer r.win.SetContentSize(renderViewport, renderViewport)
	}
	if err != nil {
		return nil, 0, 0, 0, err
	}
	full, err := png.Decode(bytes.NewReader(shot))
	if err != nil {
		return nil, 0, 0, 0, err
	}
	// 截图按设备像素；舞台位于页面左上角，按尺寸 × dpr 裁剪并复制出独立的位图。
	b := full.Bounds()
	crop := image.Rect(b.Min.X, b.Min.Y, b.Min.X+int(math.Ceil(w*dpr)), b.Min.Y+int(math.Ceil(h*dpr))).Intersect(b)
	img := image.NewRGBA(image.Rect(0, 0, crop.Dx(), crop.Dy()))
	draw.Draw(img, img.Bounds(), full, crop.Min, draw.Src)
	return img, float32(w), float32(h), float32(depth), nil
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

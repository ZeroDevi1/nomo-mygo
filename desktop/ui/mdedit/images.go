package mdedit

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/maphash"
	"image"
	"image/draw"
	"image/gif"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	xdraw "golang.org/x/image/draw"
)

// Images 是语义模式图片的按需加载缓存，可由多个编辑器共用（外壳每个窗口一个）。
//
// 生命周期：NewImages 创建；编辑器在布局可见行时 request，读取与解码在后台 worker 中进行
// （本地与远程各一条队列，各至多 imageWorkers 个 worker，队列空时退出；远程下载慢时不阻塞本地
// 图片），结果经 post 交回 UI 线程写入；Close 取消进行中的
// 远程下载并丢弃之后到达的结果。除 queue 外的字段只在 UI 线程访问。
//
// 容量：按解码后像素估算字节（宽 × 高 × 4，再加 1/3 给缩小绘制时生成的 mipmap），总量超过 limit
// 时按最近使用时间淘汰本帧未用到的图片；本帧可见的图片不淘汰，所以单屏图片总量可暂时超出预算。
// 超过 maxDecodeDim / maxDecodePixels 的图片解码后先缩小再保留，单张不超过约 21 MB。
type Images struct {
	post  func(func())
	limit int

	m     map[string]*imageEntry
	bytes int
	clock uint64 // 绘制帧序号，编辑器每次绘制推进，用于 LRU
	// gen 在任一图片加载完成时递增；含图片的行据此重新计算对象尺寸并重排。
	gen  uint64
	seed maphash.Seed
	// dims 记住加载过的图片原始尺寸，被淘汰后重新加载期间仍按真实尺寸占位，行高不变、滚动不跳。
	// 每项约 100 字节，超过 maxImageDims 项时清空重建。
	dims map[string][2]float32

	mu     sync.Mutex    // 保护 queues
	queues [3]imageQueue // 0 本地文件与 data URI，1 远程下载，2 公式与图表渲染（串行，见 enqueue）

	ctx    context.Context
	cancel context.CancelFunc
	closed bool
	// fetch 读取远程图片；测试中可替换。
	fetch func(ctx context.Context, u string) ([]byte, error)
	// render 渲染公式与图表（外壳经 SetRenderer 注入，离屏 WebView 实现）；为空时渲染对象按加载失败显示原文。
	render RenderFunc
}

// 渲染对象的种类（RenderRequest.Kind）。
const (
	RenderMathInline  = "math-inline"
	RenderMathDisplay = "math-display"
	RenderMermaid     = "mermaid"
)

// RenderRequest 是一个公式或 Mermaid 图表的渲染请求：Src 为 TeX 或 Mermaid 源码，Dark 与 FontSize（CSS 像素）
// 决定配色与字号，参与缓存键。
type RenderRequest struct {
	Kind, Src string
	Dark      bool
	FontSize  float32
}

// RenderResult 是一个渲染请求的结果：Img 与显示尺寸 W、H（CSS 像素，按 DIP 显示），Depth 为基线以下的深度
// （行内公式用于与正文基线对齐，其余为 0）；Err 为渲染失败（如 Mermaid 语法错误）；Skipped 表示本批未处理
// （如超出离屏视口），稍后重新排队。
type RenderResult struct {
	Img         image.Image
	W, H, Depth float32
	Err         error
	Skipped     bool
}

// RenderFunc 批量渲染请求，结果与 reqs 一一对应。在后台 worker 中调用，可能耗时数百毫秒；批量是为了让多个
// 公式共用一次截图。
type RenderFunc func(reqs []RenderRequest) []RenderResult

// renderBatch 是一次交给 RenderFunc 的请求数上限：一屏公式通常不超过此数。
const renderBatch = 16

// renderCoalesce 是取批前的等待：一帧内为可见公式排入的请求在此期间到齐，合为一批（经验值，远小于单次渲染耗时）。
const renderCoalesce = 8 * time.Millisecond

// SetRenderer 设置公式与图表的渲染函数；在 UI 线程调用。
func (im *Images) SetRenderer(f RenderFunc) { im.render = f }

// InvalidateRenders 丢弃全部渲染结果（配色主题变化后调用），下次绘制时按新主题重新渲染；在 UI 线程调用。
func (im *Images) InvalidateRenders() {
	for k, en := range im.m {
		if en.req != nil && en.state != imageLoading {
			delete(im.m, k)
			im.bytes -= en.bytes
		}
	}
	im.gen++
}

// requestRender 返回渲染请求的缓存项并标记本帧使用；不存在时创建并排队渲染。
func (im *Images) requestRender(req RenderRequest) *imageEntry {
	key := fmt.Sprintf("render:%s:%t:%.1f:%x:%d", req.Kind, req.Dark, req.FontSize, maphash.String(im.seed, req.Src), len(req.Src))
	en := im.m[key]
	if en == nil || en.state == imageFailed && time.Since(en.failedAt) > imageRetryAfter && im.render != nil {
		if en != nil {
			im.bytes -= en.bytes
		}
		en = &imageEntry{key: key, req: &req}
		im.m[key] = en
		if im.render == nil {
			en.state, en.failedAt = imageFailed, time.Now()
		} else {
			im.enqueue(en)
		}
	}
	en.used = im.clock
	return en
}

type imageQueue struct {
	items   []*imageEntry
	workers int
}

type imageState uint8

const (
	imageLoading imageState = iota
	imageReady
	imageFailed
)

// imageEntry 是一张图片的缓存项。key 为解析后的绝对路径、URL 或 data URI 摘要；src 为加载来源
// （路径、URL 或 data URI 本身），创建后不变，worker 只读它。
type imageEntry struct {
	key, src string
	state    imageState
	bmp      *ui.Bitmap
	svg      *ui.SVG
	// frames/delays 为动画 GIF 的各帧（已按处置方式合成）与停留时间，len(frames) > 1 时按时间播放；
	// period 为一轮总时长。静态图片为空。
	frames   []*ui.Bitmap
	delays   []time.Duration
	period   time.Duration
	w, h     float32 // 原始尺寸（像素，按 DIP 显示，与原版 CSS 像素一致）；EXIF 旋转后的方向。加载中为 dims 中记住的尺寸，未知为 0
	bytes    int
	used     uint64
	failedAt time.Time
	// req 非空表示公式或图表的渲染结果（见 RenderRequest），depth 为其基线以下的深度（CSS 像素）。
	req     *RenderRequest
	depth   float32
	modTime time.Time // 本地文件加载时的修改时间，Revalidate 据此发现外部改动；其他来源为零
}

const (
	// imageWorkers 限制并发读取解码数，避免打开图片很多的文档时同时解码大量大图造成内存尖峰。
	imageWorkers = 2
	// maxDecodeDim / maxDecodePixels 是保留像素的上限：内容区最宽约 1200 DIP，2 倍屏下 2400 像素已足够清晰。
	maxDecodeDim    = 2400
	maxDecodePixels = 4 << 20
	// maxImageSource 是读取的单个图片文件或下载上限；更大的按加载失败处理。
	maxImageSource = 40 << 20
	// maxSourcePixels 拒绝声明尺寸过大的图片（解码炸弹），约 1 亿像素。
	maxSourcePixels = 100_000_000
	// imageRetryAfter 后再次请求失败的图片时重试（如文件稍后才写入）。
	imageRetryAfter = 5 * time.Second
	// maxImageDims 是 dims 的项数上限（约 400 KB）。
	maxImageDims = 4096
	// DefaultImageBudget 是图片缓存的默认字节预算，约容纳 4 张 2400×1600 的截图。
	DefaultImageBudget = 64 << 20
)

// NewImages 创建图片缓存。post 把函数交给 UI 线程执行（外壳的 Window.Update）；limit 为字节预算。
func NewImages(post func(func()), limit int) *Images {
	ctx, cancel := context.WithCancel(context.Background())
	return &Images{post: post, limit: limit, m: map[string]*imageEntry{}, dims: map[string][2]float32{}, seed: maphash.MakeSeed(), ctx: ctx, cancel: cancel, fetch: fetchImage}
}

// Close 取消进行中的下载，之后到达的结果被丢弃；可重复调用。
func (im *Images) Close() {
	im.mu.Lock()
	im.closed = true
	for q := range im.queues {
		im.queues[q].items = nil
	}
	im.mu.Unlock()
	im.cancel()
}

// Bytes 返回缓存的估算字节数（诊断用）。
func (im *Images) Bytes() int { return im.bytes }

// frame 推进绘制帧序号；编辑器每次绘制开始时调用。
func (im *Images) frame() { im.clock++ }

// resolveImage 把图片地址解析为缓存键与加载来源；不支持的地址（空、其他协议、无文档目录的相对路径）
// 返回空串。只做字符串处理，不访问文件系统。
func (im *Images) resolveImage(src, dir string) (key, from string) {
	src = strings.TrimSpace(src)
	switch {
	case src == "":
		return "", ""
	case strings.HasPrefix(src, "data:"):
		return fmt.Sprintf("data:%x:%d", maphash.String(im.seed, src), len(src)), src
	case isRemote(src):
		return src, src
	}
	p := src
	if u, err := url.Parse(src); err == nil && u.Scheme == "file" {
		p = u.Path
	} else if strings.Contains(src, "://") {
		return "", ""
	} else if dec, err := url.PathUnescape(src); err == nil {
		p = dec
	}
	if !filepath.IsAbs(p) {
		if dir == "" {
			return "", ""
		}
		p = filepath.Join(dir, p)
	}
	return p, p
}

// request 返回图片的缓存项并标记本帧使用；不存在时创建并排队加载。地址不受支持时返回 nil。
func (im *Images) request(src, dir string) *imageEntry {
	key, from := im.resolveImage(src, dir)
	if key == "" {
		return nil
	}
	en := im.m[key]
	if en == nil || en.state == imageFailed && time.Since(en.failedAt) > imageRetryAfter {
		if en != nil {
			im.bytes -= en.bytes
		}
		d := im.dims[key]
		en = &imageEntry{key: key, src: from, w: d[0], h: d[1]}
		im.m[key] = en
		im.enqueue(en)
	}
	en.used = im.clock
	return en
}

func (im *Images) enqueue(en *imageEntry) {
	im.mu.Lock()
	defer im.mu.Unlock()
	if im.closed {
		return
	}
	k, limit := 0, imageWorkers
	switch {
	case en.req != nil:
		// 渲染在同一个离屏窗口里串行进行，多开 worker 只会排队等锁；单独一条队列，慢渲染不拖住图片。
		k, limit = 2, 1
	case isRemote(en.src):
		k = 1
	}
	q := &im.queues[k]
	q.items = append(q.items, en)
	if q.workers < limit {
		q.workers++
		go im.work(q)
	}
}

func isRemote(src string) bool {
	return strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://")
}

// idle 报告是否没有排队或进行中的加载（测试与诊断用）。
func (im *Images) idle() bool {
	im.mu.Lock()
	defer im.mu.Unlock()
	for _, q := range im.queues {
		if len(q.items) > 0 || q.workers > 0 {
			return false
		}
	}
	return true
}

// work 依次加载队列 q 中的图片，队列为空时退出。
func (im *Images) work(q *imageQueue) {
	for {
		im.mu.Lock()
		if len(q.items) == 0 || im.closed {
			q.workers--
			im.mu.Unlock()
			return
		}
		if q.items[0].req != nil {
			// 渲染队列：先等 renderCoalesce 收集同一帧排入的请求，再一次取出至多 renderBatch 个批量渲染。
			if len(q.items) < renderBatch {
				im.mu.Unlock()
				time.Sleep(renderCoalesce)
				im.mu.Lock()
				if len(q.items) == 0 || im.closed {
					im.mu.Unlock() // 回到循环开头重新加锁判断退出
					continue
				}
			}
			n := min(len(q.items), renderBatch)
			batch := append([]*imageEntry(nil), q.items[:n]...)
			q.items = q.items[n:]
			im.mu.Unlock()
			im.renderEntries(q, batch)
			continue
		}
		en := q.items[0]
		q.items = q.items[1:]
		im.mu.Unlock()

		res := im.load(en.src)
		im.post(func() { im.finish(en, res) })
	}
}

// imageResult 是后台加载的结果。
type imageResult struct {
	bmp     *ui.Bitmap
	svg     *ui.SVG
	frames  []*ui.Bitmap
	delays  []time.Duration
	w, h    float32
	bytes   int
	modTime time.Time
	depth   float32
	err     error
}

// renderEntries 批量渲染并把结果转为位图（在 worker 中运行）；未处理的请求放回队首。
func (im *Images) renderEntries(q *imageQueue, batch []*imageEntry) {
	reqs := make([]RenderRequest, len(batch))
	for k, en := range batch {
		reqs[k] = *en.req
	}
	results := im.render(reqs)
	var skipped []*imageEntry
	for k, en := range batch {
		var r RenderResult
		if k < len(results) {
			r = results[k]
		} else {
			r.Err = errors.New("missing render result")
		}
		if r.Skipped {
			skipped = append(skipped, en)
			continue
		}
		var res imageResult
		switch {
		case r.Err != nil:
			res.err = r.Err
		case r.Img == nil || r.W <= 0 || r.H <= 0:
			res.err = errors.New("empty render")
		default:
			b := r.Img.Bounds()
			res = imageResult{bmp: ui.NewBitmap(r.Img), w: r.W, h: r.H, depth: r.Depth, bytes: b.Dx() * b.Dy() * 4 * 4 / 3}
		}
		im.post(func() { im.finish(en, res) })
	}
	if len(skipped) > 0 {
		if len(skipped) == len(batch) {
			// 一个都没渲染（如单个超大图表）：逐个失败，避免无限重排。
			for _, en := range skipped {
				im.post(func() { im.finish(en, imageResult{err: errors.New("render skipped")}) })
			}
			return
		}
		im.mu.Lock()
		q.items = append(skipped, q.items...)
		im.mu.Unlock()
	}
}

// finish 在 UI 线程写入加载结果；缓存项已被替换或缓存已关闭时丢弃。
func (im *Images) finish(en *imageEntry, res imageResult) {
	if im.closed || im.m[en.key] != en {
		return
	}
	if res.err != nil {
		en.state, en.failedAt = imageFailed, time.Now()
	} else {
		en.state, en.bmp, en.svg, en.w, en.h, en.bytes, en.modTime = imageReady, res.bmp, res.svg, res.w, res.h, res.bytes, res.modTime
		en.frames, en.delays, en.depth = res.frames, res.delays, res.depth
		for _, d := range res.delays {
			en.period += d
		}
		im.bytes += res.bytes
		if len(im.dims) >= maxImageDims {
			clear(im.dims)
		}
		im.dims[en.key] = [2]float32{res.w, res.h}
	}
	im.gen++
	im.evict()
}

// evict 在超出预算时按最近使用时间淘汰本帧未用到的图片。
func (im *Images) evict() {
	if im.bytes <= im.limit {
		return
	}
	var old []*imageEntry
	for _, en := range im.m {
		if en.used < im.clock && en.state != imageLoading {
			old = append(old, en)
		}
	}
	sort.Slice(old, func(a, b int) bool { return old[a].used < old[b].used })
	for _, en := range old {
		if im.bytes <= im.limit {
			break
		}
		delete(im.m, en.key)
		im.bytes -= en.bytes
	}
}

// load 读取并解码图片（在 worker 中运行）。
func (im *Images) load(from string) imageResult {
	var data []byte
	var err error
	var mod time.Time
	switch {
	case strings.HasPrefix(from, "data:"):
		data, err = decodeDataURI(from)
	case isRemote(from):
		data, err = im.fetch(im.ctx, from)
	default:
		data, mod, err = readLimited(from)
	}
	if err != nil {
		return imageResult{err: err}
	}
	res := decodeImage(data, strings.EqualFold(filepath.Ext(strings.SplitN(from, "?", 2)[0]), ".svg"))
	res.modTime = mod
	return res
}

func readLimited(path string) ([]byte, time.Time, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	if fi.IsDir() || fi.Size() > maxImageSource {
		return nil, time.Time{}, errors.New("not an image file or too large")
	}
	data, err := os.ReadFile(path)
	return data, fi.ModTime(), err
}

// Revalidate 在后台检查已加载的本地图片是否被外部修改或删除，变化的移出缓存，下次绘制时重新加载；
// 外壳在窗口获得焦点时调用（与文档的外部变更检查同时机）。
func (im *Images) Revalidate() {
	type item struct {
		en  *imageEntry
		mod time.Time
	}
	var items []item
	for _, en := range im.m {
		if en.state == imageReady && !en.modTime.IsZero() {
			items = append(items, item{en, en.modTime})
		}
	}
	if len(items) == 0 || im.closed {
		return
	}
	go func() {
		var changed []*imageEntry
		for _, it := range items {
			if fi, err := os.Stat(it.en.src); err != nil || !fi.ModTime().Equal(it.mod) {
				changed = append(changed, it.en)
			}
		}
		if len(changed) == 0 {
			return
		}
		im.post(func() {
			for _, en := range changed {
				if im.m[en.key] == en {
					delete(im.m, en.key)
					im.bytes -= en.bytes
				}
			}
			im.gen++
		})
	}()
}

// fetchImage 下载远程图片：20 秒超时、不发送 Referer（与原版避免防盗链的处理一致）、限制大小。
func fetchImage(ctx context.Context, u string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageSource+1))
	if err == nil && len(data) > maxImageSource {
		err = errors.New("image too large")
	}
	return data, err
}

// decodeDataURI 解码 data:[<mime>][;base64],<data>。
func decodeDataURI(s string) ([]byte, error) {
	comma := strings.IndexByte(s, ',')
	if comma < 0 {
		return nil, errors.New("bad data uri")
	}
	meta, payload := s[len("data:"):comma], s[comma+1:]
	if strings.HasSuffix(meta, ";base64") {
		payload = strings.Join(strings.Fields(payload), "")
		if b, err := base64.StdEncoding.DecodeString(payload); err == nil {
			return b, nil
		}
		return base64.RawStdEncoding.DecodeString(strings.TrimRight(payload, "="))
	}
	dec, err := url.PathUnescape(payload)
	return []byte(dec), err
}

// decodeImage 解码位图或 SVG。位图超过保留上限时先缩小再转换为 Bitmap，并按 JPEG 的 EXIF 方向摆正。
func decodeImage(data []byte, svgExt bool) imageResult {
	head := data[:min(len(data), 512)]
	if svgExt || bytes.Contains(head, []byte("<svg")) {
		s, err := ui.ParseSVG(data)
		if err != nil {
			return imageResult{err: err}
		}
		w, h := s.Size()
		if w <= 0 || h <= 0 {
			w, h = 300, 150 // 与浏览器对无尺寸 SVG 的默认值一致
		}
		return imageResult{svg: s, w: w, h: h, bytes: len(data) + 16<<10}
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return imageResult{err: err}
	}
	if format == "gif" {
		if res, ok := decodeAnimatedGIF(data); ok {
			return res
		}
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxSourcePixels {
		return imageResult{err: errors.New("image dimensions out of range")}
	}
	if cfg.Width <= maxDecodeDim && cfg.Height <= maxDecodeDim && cfg.Width*cfg.Height <= maxDecodePixels {
		b, err := ui.DecodeBitmap(data)
		if err != nil {
			return imageResult{err: err}
		}
		w, h := b.Size()
		return imageResult{bmp: b, w: float32(w), h: float32(h), bytes: w * h * 4 * 4 / 3}
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return imageResult{err: err}
	}
	sw, sh := cfg.Width, cfg.Height
	f := min(float64(maxDecodeDim)/float64(max(sw, sh)), math.Sqrt(float64(maxDecodePixels)/float64(sw*sh)))
	dw, dh := max(1, int(float64(sw)*f)), max(1, int(float64(sh)*f))
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
	nw, nh := float32(sw), float32(sh)
	if o := jpegOrientation(data); o > 1 {
		dst = orientRGBA(dst, o)
		if o >= 5 {
			nw, nh = nh, nw
		}
	}
	b := dst.Bounds()
	return imageResult{bmp: ui.NewBitmap(dst), w: nw, h: nh, bytes: b.Dx() * b.Dy() * 4 * 4 / 3}
}

// maxGIFBytes 是动画 GIF 全部帧解码后的像素字节上限；超出时只保留首帧（静态显示），避免长动画占满缓存。
const maxGIFBytes = 32 << 20

// decodeAnimatedGIF 解码多帧 GIF：按各帧的处置方式（保留、清除为透明、恢复上一帧）合成完整画面。停留时间
// 小于等于 10 ms 的帧按 100 ms 处理（与浏览器一致）。单帧、超出 maxGIFBytes 或解码失败时返回 false。
func decodeAnimatedGIF(data []byte) (imageResult, bool) {
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil || len(g.Image) < 2 {
		return imageResult{}, false
	}
	w, h := g.Config.Width, g.Config.Height
	if w <= 0 || h <= 0 || len(g.Image)*w*h*4 > maxGIFBytes || w > maxDecodeDim || h > maxDecodeDim {
		return imageResult{}, false
	}
	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	res := imageResult{w: float32(w), h: float32(h)}
	for k, fr := range g.Image {
		var prev *image.RGBA
		disposal := byte(0)
		if k < len(g.Disposal) {
			disposal = g.Disposal[k]
		}
		if disposal == gif.DisposalPrevious {
			prev = image.NewRGBA(canvas.Bounds())
			copy(prev.Pix, canvas.Pix)
		}
		draw.Draw(canvas, fr.Bounds(), fr, fr.Bounds().Min, draw.Over)
		snap := image.NewRGBA(canvas.Bounds())
		copy(snap.Pix, canvas.Pix)
		res.frames = append(res.frames, ui.NewBitmap(snap))
		d := 100 * time.Millisecond
		if k < len(g.Delay) && g.Delay[k] > 1 {
			d = time.Duration(g.Delay[k]) * 10 * time.Millisecond
		}
		res.delays = append(res.delays, d)
		switch disposal {
		case gif.DisposalBackground:
			draw.Draw(canvas, fr.Bounds(), image.Transparent, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			canvas = prev
		}
	}
	res.bmp = res.frames[0]
	res.bytes = len(res.frames) * w * h * 4
	return res, true
}

// frameAt 返回动画在 now 时刻应显示的帧及距下一帧的时间；静态图片返回 bmp 与 0。所有动画共用墙钟相位，
// 同一张图片的多处引用同步播放。
func (en *imageEntry) frameAt(now time.Time) (*ui.Bitmap, time.Duration) {
	if len(en.frames) < 2 || en.period <= 0 {
		return en.bmp, 0
	}
	t := time.Duration(now.UnixNano()) % en.period
	for k, d := range en.delays {
		if t < d {
			return en.frames[k], d - t
		}
		t -= d
	}
	return en.frames[0], en.delays[0]
}

// jpegOrientation 返回 JPEG EXIF 的方向标签（1–8），没有时返回 0。
func jpegOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xff || b[1] != 0xd8 {
		return 0
	}
	for i := 2; i+4 <= len(b); {
		if b[i] != 0xff {
			return 0
		}
		marker := b[i+1]
		n := int(binary.BigEndian.Uint16(b[i+2:]))
		if marker == 0xda || n < 2 || i+2+n > len(b) {
			return 0
		}
		seg := b[i+4 : i+2+n]
		if marker == 0xe1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + n
	}
	return 0
}

func tiffOrientation(t []byte) int {
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	off := int(bo.Uint32(t[4:]))
	if off+2 > len(t) {
		return 0
	}
	n := int(bo.Uint16(t[off:]))
	for k := range n {
		e := off + 2 + 12*k
		if e+12 > len(t) {
			return 0
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			return int(bo.Uint16(t[e+8:]))
		}
	}
	return 0
}

// orientRGBA 按 EXIF 方向 o（2–8）变换图像，使其正向显示。
func orientRGBA(src *image.RGBA, o int) *image.RGBA {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range h {
		for x := range w {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			default:
				dx, dy = x, y
			}
			si, di := src.PixOffset(x, y), dst.PixOffset(dx, dy)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
	return dst
}

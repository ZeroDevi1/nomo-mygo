package shell

import (
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"time"

	"nomo/desktop/internal/applog"
	"nomo/desktop/ui/mdedit"
)

// 离屏渲染转储（诊断用，默认关闭）：环境变量 NOMO_RENDER_DUMP 指向一个目录时，每批渲染把整页截图、各项裁剪后的
// 位图与测量数据写到该目录，用于排查偶发的公式、图表显示异常（对比截图本身与编辑区显示）。
//
// 文件：<批次>-page.png（整页截图）、<批次>-<序号>.png（裁剪结果）、<批次>.json（时间、dpr、截图尺寸、截图前的
// 页面滚动与视口、各项请求与测量框）。批次名为纳秒时间戳。在图片缓存的后台 worker 中写，不在 UI 线程。
// 上限 renderDumpLimit 批，达到后不再写并记一条日志，避免长时间开着时占满磁盘。

const (
	renderDumpEnv   = "NOMO_RENDER_DUMP"
	renderDumpLimit = 200
)

// renderDumpDir 在首次使用时读取环境变量；为空表示关闭。
var renderDumpDir = sync.OnceValue(func() string { return os.Getenv(renderDumpEnv) })

var renderDumpCount struct {
	sync.Mutex
	n int
}

// renderDumpItem 是转储 JSON 中的一项：请求（公式与图表原文较短，原样记录便于复现）与测量框（CSS 像素）。
type renderDumpItem struct {
	Kind     string  `json:"kind"`
	Src      string  `json:"src"`
	Dark     bool    `json:"dark"`
	FontSize float32 `json:"fontSize"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	W        float64 `json:"w"`
	H        float64 `json:"h"`
	Depth    float64 `json:"depth"`
	Err      string  `json:"err,omitempty"`
	Skipped  bool    `json:"skipped,omitempty"`
}

type renderDump struct {
	Time     string           `json:"time"`
	DPR      float64          `json:"dpr"`
	Shot     [2]int           `json:"shot"`     // 截图像素尺寸
	Viewport any              `json:"viewport"` // 截图前页面的 [scrollX, scrollY, innerWidth, innerHeight]
	Items    []renderDumpItem `json:"items"`
}

// dumpRender 写一批渲染的转储；未开启或已达上限时什么也不做。写盘失败只记日志，不影响渲染结果。
func dumpRender(shot []byte, full image.Image, dpr float64, viewport any, reqs []mdedit.RenderRequest,
	boxes []renderBox, out []mdedit.RenderResult) {
	dir := renderDumpDir()
	if dir == "" {
		return
	}
	renderDumpCount.Lock()
	n := renderDumpCount.n
	renderDumpCount.n++
	renderDumpCount.Unlock()
	if n == renderDumpLimit {
		applog.Warn("Native", fmt.Sprintf("离屏渲染转储已达 %d 批上限，之后不再写入", renderDumpLimit))
	}
	if n >= renderDumpLimit {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		applog.Warn("Native", "离屏渲染转储目录不可用："+err.Error())
		return
	}
	now := time.Now()
	base := filepath.Join(dir, fmt.Sprintf("%d", now.UnixNano()))
	d := renderDump{Time: now.Format(time.RFC3339Nano), DPR: dpr, Shot: [2]int{full.Bounds().Dx(), full.Bounds().Dy()}, Viewport: viewport}
	for k, req := range reqs {
		it := renderDumpItem{Kind: req.Kind, Src: req.Src, Dark: req.Dark, FontSize: req.FontSize,
			X: boxes[k].x, Y: boxes[k].y, W: boxes[k].w, H: boxes[k].h, Depth: boxes[k].depth, Skipped: out[k].Skipped}
		if out[k].Err != nil {
			it.Err = out[k].Err.Error()
		}
		d.Items = append(d.Items, it)
	}
	write := func(path string, data []byte) {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			applog.Warn("Native", "写入离屏渲染转储失败："+err.Error())
		}
	}
	write(base+"-page.png", shot)
	for k, res := range out {
		if res.Img == nil {
			continue
		}
		f, err := os.Create(fmt.Sprintf("%s-%d.png", base, k))
		if err != nil {
			applog.Warn("Native", "写入离屏渲染转储失败："+err.Error())
			continue
		}
		_ = png.Encode(f, res.Img)
		f.Close()
	}
	if raw, err := json.MarshalIndent(d, "", "  "); err == nil {
		write(base+".json", raw)
	}
}

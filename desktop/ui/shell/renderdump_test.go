package shell

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nomo/desktop/ui/mdedit"
)

// TestRenderDump：开启 NOMO_RENDER_DUMP 后，一批渲染写出整页截图、各项裁剪位图与测量数据。
func TestRenderDump(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(renderDumpEnv, dir)
	full := image.NewRGBA(image.Rect(0, 0, 20, 10))
	var buf bytes.Buffer
	_ = png.Encode(&buf, full)
	reqs := []mdedit.RenderRequest{{Kind: "math-display", Src: "E=mc^2", FontSize: 16}, {Kind: "mermaid", Src: "graph TD"}}
	boxes := []renderBox{{x: 0, y: 0, w: 10, h: 5}, {}}
	out := []mdedit.RenderResult{{Img: image.NewRGBA(image.Rect(0, 0, 20, 10)), W: 10, H: 5}, {Skipped: true}}
	dumpRender(buf.Bytes(), full, 2, []any{0.0, 0.0, 12.0, 7.0}, reqs, boxes, out)

	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	var page, item, meta int
	var js string
	for _, f := range files {
		switch {
		case strings.HasSuffix(f, "-page.png"):
			page++
		case strings.HasSuffix(f, "-0.png"):
			item++
		case strings.HasSuffix(f, ".json"):
			meta++
			js = f
		}
	}
	if page != 1 || item != 1 || meta != 1 || len(files) != 3 {
		t.Fatalf("转储文件 %v", files)
	}
	raw, _ := os.ReadFile(js)
	var d renderDump
	if err := json.Unmarshal(raw, &d); err != nil || d.DPR != 2 || len(d.Items) != 2 || d.Items[0].Src != "E=mc^2" || !d.Items[1].Skipped {
		t.Fatalf("转储数据 %s", raw)
	}
}

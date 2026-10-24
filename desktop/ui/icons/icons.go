// Package icons 提供原生界面使用的图标：与原版界面相同的 Lucide 图标（ISC 许可，
// 图标表见 lucide.go），供外壳与编辑器共用。
package icons

import (
	"sync"

	"github.com/egoist/mygo/ui"
)

var (
	mu     sync.Mutex
	parsed = map[string]*ui.SVG{}
)

// Get 返回名为 name（Lucide 文件名，如 "file-text"）的图标；首次使用时解析并缓存，
// 进程内常驻（共约 60 个小 SVG）。未知名称返回 nil，调用方据此不画图标。
func Get(name string) *ui.SVG {
	mu.Lock()
	defer mu.Unlock()
	if svg, ok := parsed[name]; ok {
		return svg
	}
	src, ok := svgs[name]
	if !ok {
		return nil
	}
	svg := ui.MustParseSVG([]byte(src))
	parsed[name] = svg
	return svg
}

// ForFile 返回文件类型图标，与原版 ExplorerSidebar / DocumentTabs 一致：
// JSON 为 FileJson2，TXT 为 FileType2，图片为 Image，其余（Markdown）为 FileText。
func ForFile(name string) *ui.SVG {
	switch ext := lowerExt(name); ext {
	case ".json":
		return Get("file-json-2")
	case ".txt":
		return Get("file-type-2")
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".bmp":
		return Get("image")
	}
	return Get("file-text")
}

func lowerExt(name string) string {
	for i := len(name) - 1; i >= 0 && name[i] != '/' && name[i] != '\\'; i-- {
		if name[i] == '.' {
			b := []byte(name[i:])
			for k, c := range b {
				if c >= 'A' && c <= 'Z' {
					b[k] = c + 32
				}
			}
			return string(b)
		}
	}
	return ""
}

// calloutIcons 是提示块类型（0 note … 4 caution）的图标名，对应原版 calloutTypes.ts。
var calloutIcons = [...]string{"info", "lightbulb", "star", "triangle-alert", "octagon-alert"}

// Callout 返回提示块类型（0 note … 4 caution）的图标。
func Callout(tone int) *ui.SVG {
	if tone < 0 || tone >= len(calloutIcons) {
		tone = 0
	}
	return Get(calloutIcons[tone])
}

// CalloutSource 返回提示块类型图标的 SVG 原文（导出 HTML 内联用）。
func CalloutSource(tone int) string {
	if tone < 0 || tone >= len(calloutIcons) {
		tone = 0
	}
	return svgs[calloutIcons[tone]]
}

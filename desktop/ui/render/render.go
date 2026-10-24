// Package render 提供公式与 Mermaid 图表的离屏渲染页（render.html + render.js）及其随附的 KaTeX 0.17.0、
// Mermaid 11.15.0 发布文件（原样取自 npm 发布包，sha512 与原版锁文件一致，许可见各目录的 LICENSE 与
// THIRD_PARTY_NOTICES.md）。页面经 mygo.SetFrontend 在 mygo://localhost/ 提供，由外壳按需创建的隐藏窗口加载。
package render

import (
	"embed"
	"encoding/base64"
	"io/fs"
	"regexp"
	"sync"
)

//go:embed assets
var assets embed.FS

// FS 返回渲染页的文件（render.html 位于根目录）。
func FS() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err) // 嵌入目录在编译期确定，不会失败
	}
	return sub
}

// PagePath 是渲染页在前端中的路径（窗口 URL）。
const PagePath = "/render.html"

var (
	katexCSSOnce sync.Once
	katexCSS     string
	// fontSrcRe 匹配 KaTeX 样式中一个 @font-face 的 src 列表（woff2、woff、ttf 三种格式）。
	fontSrcRe = regexp.MustCompile(`src:url\(fonts/([A-Za-z0-9_-]+)\.woff2\) format\("woff2"\)[^;}]*`)
)

// KatexCSS 返回导出用的 KaTeX 样式：字体改为内联的 woff2 data URL（只保留 woff2，导出文件自包含）。首次调用时生成
// 并缓存（约 400 KB），只在导出含公式的文档时调用。
func KatexCSS() string {
	katexCSSOnce.Do(func() {
		raw, err := assets.ReadFile("assets/katex/katex.min.css")
		if err != nil {
			return
		}
		katexCSS = fontSrcRe.ReplaceAllStringFunc(string(raw), func(m string) string {
			name := fontSrcRe.FindStringSubmatch(m)[1]
			font, err := assets.ReadFile("assets/katex/fonts/" + name + ".woff2")
			if err != nil {
				return m
			}
			return `src:url(data:font/woff2;base64,` + base64.StdEncoding.EncodeToString(font) + `) format("woff2")`
		})
	})
	return katexCSS
}

// Package kpexport 提供导出 HTML / PDF 的精细排版脚本：排版算法（desktop/ui/typography）编译为 WebAssembly，
// 连同 Go 的 wasm_exec.js 与页面脚本 kp.js 内联到导出文档，页面按自身字体测量后计算断行（见 assets/kp.js）。
//
// WebAssembly 与 wasm_exec.js 是构建产物：mygo build 前由 mygo.json 的 buildCommand（scripts/kpwasm）生成到 gen/，
// 不纳入版本库。未生成时（如直接 go run / go test）Script 返回 ErrUnavailable，导出不含精细排版。
package kpexport

import (
	"embed"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
)

//go:embed assets/kp.js
var kpJS string

// gen 目录在版本库中只有占位文件 .gitkeep，构建时写入 typography.wasm.gz 与 wasm_exec.js。
//
//go:embed all:gen
var gen embed.FS

// ErrUnavailable 表示构建时没有生成排版 WebAssembly。
var ErrUnavailable = errors.New("精细排版 WebAssembly 未生成（构建时运行 go run ./scripts/kpwasm）")

var (
	once   sync.Once
	script string
	err    error
)

// Script 返回内联到导出文档 </body> 前的脚本（约 1.8 MB，首次调用时生成并缓存）：选项标记
// #nomo-typography-options（PDF 打印前据此调用 __NOMO_PREPARE_PRINT__）、压缩的 WebAssembly、wasm_exec.js 与 kp.js。
func Script() (string, error) {
	once.Do(func() {
		wasm, e1 := gen.ReadFile("gen/typography.wasm.gz")
		exec, e2 := gen.ReadFile("gen/wasm_exec.js")
		if e1 != nil || e2 != nil {
			err = ErrUnavailable
			return
		}
		var b strings.Builder
		b.WriteString(`<script id="nomo-typography-options" type="application/json">{"enabled":true,"profile":"zh-CN","hanging":true}</script>` + "\n")
		b.WriteString(`<script id="nomo-kp-wasm" type="application/octet-stream">` + base64.StdEncoding.EncodeToString(wasm) + "</script>\n")
		b.WriteString("<script>" + safe(string(exec)) + "</script>\n")
		b.WriteString("<script>" + safe(kpJS) + "</script>\n")
		script = b.String()
	})
	return script, err
}

// safe 防止脚本中的 "</script" 提前结束内联的 script 元素。
func safe(js string) string { return strings.ReplaceAll(js, "</script", `<\/script`) }

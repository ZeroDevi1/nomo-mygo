//go:build js && wasm

// Command wasm 把精细排版（desktop/ui/typography）编译为 WebAssembly，供导出的 HTML / PDF 页面使用：页面中的
// 排版脚本（desktop/ui/kpexport/assets/kp.js）测量各片段宽度后调用 globalThis.nomoKPLayout，按返回的断行与间距
// 调整重排段落，算法与编辑区完全相同（mdedit/kp.go 的单元构造规则）。由 scripts/kpwasm 在构建时编译。
package main

import (
	"encoding/json"
	"syscall/js"
)

func main() {
	js.Global().Set("nomoKPLayout", js.FuncOf(func(this js.Value, args []js.Value) any {
		var req request
		if len(args) == 0 || json.Unmarshal([]byte(args[0].String()), &req) != nil {
			return `{"ok":false}`
		}
		out, _ := json.Marshal(layout(req))
		return string(out)
	}))
	select {} // 保持运行，供页面反复调用
}

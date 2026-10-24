package kpexport

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// TestScript：生成了 WebAssembly 时脚本包含选项标记、WebAssembly、wasm_exec.js 与 kp.js，且不会提前结束 script 元素；
// 未生成时返回 ErrUnavailable。设 NOMO_KP_FIXTURE=<html 路径> 时写出带排版脚本的测试页（供浏览器中人工检查）。
func TestScript(t *testing.T) {
	s, err := Script()
	if errors.Is(err, ErrUnavailable) {
		t.Skip("未生成 WebAssembly（go run ./scripts/kpwasm）")
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`id="nomo-typography-options"`, `id="nomo-kp-wasm"`, "globalThis.Go", "__NOMO_PREPARE_PRINT__"} {
		if !strings.Contains(s, want) {
			t.Errorf("脚本缺少 %s", want)
		}
	}
	if n := strings.Count(s, "</script>"); n != 4 {
		t.Errorf("</script> 出现 %d 次，应为 4（每个 script 元素一次）", n)
	}
	if out := os.Getenv("NOMO_KP_FIXTURE"); out != "" {
		para := strings.Repeat("精细排版让中文段落两端对齐、标点挤压与行末悬挂更自然，English words like typography and hyphenation are broken at proper points。", 4)
		page := `<!doctype html><html><head><meta charset="utf-8"><style>.nomo-export{width:520px;font:16px/1.75 -apple-system,"PingFang SC",sans-serif}` +
			`p{margin:0 0 1em}</style></head><body><article class="nomo-export"><p id="kp">` + para + `</p><p id="zh">` + strings.Repeat("精细排版让中文段落两端对齐，标点挤压与行末悬挂更自然。好的排版让阅读更舒适，段落右边缘应当整齐，「引号」与（括号）在行首行末也会压缩。", 3) +
			`</p><p id="plain" style="text-align:center">居中段落不排版</p></article>` + s + `</body></html>`
		if err := os.WriteFile(out, []byte(page), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

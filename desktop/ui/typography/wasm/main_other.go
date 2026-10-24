//go:build !(js && wasm)

package main

import (
	"fmt"
	"os"
)

// main 在 js/wasm 以外的平台只提示用法：本包只用于编译导出排版的 WebAssembly（见 main.go 与 scripts/kpwasm），
// 这里保证 go build ./... 在普通平台上也能通过。
func main() {
	fmt.Fprintln(os.Stderr, "本程序只用于 GOOS=js GOARCH=wasm，请运行 go run ./scripts/kpwasm")
	os.Exit(2)
}

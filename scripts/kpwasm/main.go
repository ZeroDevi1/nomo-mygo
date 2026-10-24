// Command kpwasm 生成导出精细排版所需的构建产物（desktop/ui/kpexport/gen，不纳入版本库）：把排版算法编译为
// WebAssembly 并 gzip 压缩，复制当前 Go 工具链的 wasm_exec.js（须与编译所用的 Go 版本一致）。
// mygo build 前由 mygo.json 的 buildCommand 调用；也可手动在仓库根目录运行 go run ./scripts/kpwasm。
package main

import (
	"bytes"
	"compress/gzip"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const out = "desktop/ui/kpexport/gen"

func main() {
	log.SetFlags(0)
	tmp, err := os.MkdirTemp("", "nomo-kpwasm")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	wasm := filepath.Join(tmp, "typography.wasm")
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", wasm, "./desktop/ui/typography/wasm")
	cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("编译排版 WebAssembly 失败：%v", err)
	}
	data, err := os.ReadFile(wasm)
	if err != nil {
		log.Fatal(err)
	}
	var gz bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&gz, gzip.BestCompression)
	if _, err := zw.Write(data); err != nil {
		log.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "typography.wasm.gz"), gz.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	root, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		log.Fatal(err)
	}
	execJS, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(root)), "lib", "wasm", "wasm_exec.js"))
	if err != nil {
		log.Fatalf("读取 wasm_exec.js 失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(out, "wasm_exec.js"), execJS, 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("已生成精细排版 WebAssembly（%d KB，压缩后 %d KB）", len(data)>>10, gz.Len()>>10)
}

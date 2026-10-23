package mdedit

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// TestTypingLatency 在大文档中部连续键入，报告每键耗时（含一帧绘制）与其中原文替换（解析与块索引）的耗时。
// 设 NOMO_TYPE_DOC=<文件> 运行；无头 CPU 渲染，只用于同环境前后对照。
func TestTypingLatency(t *testing.T) {
	path := os.Getenv("NOMO_TYPE_DOC")
	if path == "" {
		t.Skip("设 NOMO_TYPE_DOC=<文件> 运行")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := NewDocument(string(data))
	ed := NewEditor(doc)
	tt := ui.NewTester(func(c *ui.Context) { ed.View(c).AutoFocus() }, 1200, 800)
	tt.Frame()
	ed.JumpTo(len(data) / 2)
	doc.SetSelection(Selection{ed.Doc.Sel.Head, ed.Doc.Sel.Head})
	ed.dirty = true
	tt.Frame()
	tt.Type("预")
	tt.Frame()
	var keys, edits []time.Duration
	for range 60 {
		s := time.Now()
		tt.Type("字")
		keys = append(keys, time.Since(s))
		at := doc.Sel.Head
		s = time.Now()
		doc.replace(at, at, "")
		doc.replace(at, at, "x")
		doc.replace(at, at+1, "")
		edits = append(edits, time.Since(s)/2)
	}
	slices.Sort(keys)
	slices.Sort(edits)
	t.Logf("%s: 每键 p50 %v max %v；原文替换 p50 %v max %v", path, keys[len(keys)/2], keys[len(keys)-1], edits[len(edits)/2], edits[len(edits)-1])
}

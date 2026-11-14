package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/asuka/sdk/document"
	"github.com/egoist/asuka/ui"
	"github.com/egoist/asuka/uitest"

	"nomo/desktop/internal/textcodec"
)

func TestMarkdownDocumentWorkflow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ASUKA_DATA", filepath.Join(dir, "app-data"))

	const encoding = textcodec.EncodingUTF16LEBOM
	const original = "# 原文\n\n待编辑。\n"
	const edited = "# 修改后 😀\n\n带有 **加粗** 的正文。\n"
	path := filepath.Join(dir, "fixture.md")
	data, err := textcodec.Encode(original, encoding)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	docs := newDocuments()
	a := uitest.Mount(t, docs.Root, uitest.Platform(ui.PlatformMacOS))
	// 测试宿主会搜索所有窗口，先关掉启动时创建的空白文档。
	a.Until("启动窗口打开", func() bool { return len(docs.Documents().Get()) == 1 })
	docs.Documents().Get()[0].Close()
	a.Until("启动窗口关闭", func() bool { return len(docs.Documents().Get()) == 0 })
	open := func() *document.Document[source] {
		t.Helper()
		var doc *document.Document[source]
		var openErr error
		docs.Open(path, func(result *document.Document[source], err error) {
			doc, openErr = result, err
		})
		a.Until("文档打开", func() bool { return doc != nil || openErr != nil })
		if openErr != nil {
			t.Fatal(openErr)
		}
		return doc
	}

	doc := open()
	a.WaitFor("原文")
	a.Type("Markdown 源码", edited)
	a.WaitFor("修改后 😀")
	a.Expect("未保存 · " + string(encoding))
	if got := doc.Content().Peek(); got != (source{Text: edited, Encoding: encoding}) {
		t.Fatalf("编辑后文档内容为 %+v", got)
	}
	a.Tap("隐藏预览")
	a.ExpectNo("修改后 😀")
	a.Tap("显示预览")
	a.Expect("修改后 😀")

	// 桌面实验使用显式保存，输入后磁盘仍为原文。
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("显式保存前磁盘内容已变化")
	}
	saved := false
	var saveErr error
	doc.Save(func(err error) {
		saveErr, saved = err, true
	})
	a.Until("文档保存", func() bool { return saved })
	if saveErr != nil {
		t.Fatal(saveErr)
	}
	a.Expect(string(encoding))
	doc.Close()
	a.Until("文档关闭", func() bool {
		for _, current := range docs.Documents().Get() {
			if current == doc {
				return false
			}
		}
		return true
	})
	reopened := open()
	a.WaitFor("修改后 😀")
	if got := reopened.Content().Peek(); got != (source{Text: edited, Encoding: encoding}) {
		t.Fatalf("重开后文档内容为 %+v", got)
	}
}

func TestGBKSourceSavesAsUTF8(t *testing.T) {
	gbk, err := textcodec.Encode("原文", textcodec.EncodingGBK)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := readSource(bytes.NewReader(gbk))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Encoding != textcodec.EncodingGBK || doc.Text != "原文" {
		t.Fatalf("读入 GBK 文档为 %+v", doc)
	}
	doc.Text += " 😀"
	var saved bytes.Buffer
	if err := writeSource(&saved, doc); err != nil {
		t.Fatal(err)
	}
	if saved.String() != doc.Text {
		t.Fatalf("保存后的 UTF-8 内容为 %q", saved.String())
	}
}

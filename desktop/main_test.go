package main

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nomo/desktop/internal/docfs"
)

func TestWebView2BrowserArgs(t *testing.T) {
	for _, c := range []struct {
		existing string
		software bool
		want     string
	}{
		{"", false, webview2DefaultArgs},
		{"", true, webview2DefaultArgs + " --disable-gpu --disable-gpu-compositing"},
		{" --remote-debugging-port=9222 ", false, "--remote-debugging-port=9222"},
		{"--remote-debugging-port=9222", true, "--remote-debugging-port=9222 --disable-gpu --disable-gpu-compositing"},
	} {
		if got := webview2BrowserArgs(c.existing, c.software); got != c.want {
			t.Errorf("webview2BrowserArgs(%q, %v) = %q，应为 %q", c.existing, c.software, got, c.want)
		}
	}
}

// TestLargeDocumentContentByProtocol 超过阈值的正文移出调用结果，页面凭令牌经协议读取一次。
func TestLargeDocumentContentByProtocol(t *testing.T) {
	small := deferLargeContent(docfs.DocumentPayload{Path: "/a.md", Markdown: "短文"})
	if small.ContentToken != "" || small.Markdown != "短文" {
		t.Fatalf("小文档应直接返回正文：%+v", small)
	}
	text := strings.Repeat("大文档正文\n", docContentThreshold/16+1)
	res := deferLargeContent(docfs.DocumentPayload{Path: "/b.md", FileName: "b.md", Markdown: text, SizeBytes: int64(len(text))})
	encoded, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatal(err)
	}
	if back["file_name"] != "b.md" || back["markdown"] != "" || back["content_token"] != res.ContentToken || res.ContentToken == "" {
		t.Fatalf("结果应内联文档字段并带令牌：%s", encoded[:min(len(encoded), 200)])
	}
	get := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		serveDocumentContent(w, httptest.NewRequest(http.MethodGet, "/"+res.ContentToken, nil))
		return w
	}
	if w := get(); w.Code != http.StatusOK || w.Body.String() != text || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain; charset=utf-8") {
		t.Fatalf("第一次读取：%d %d 字节", w.Code, w.Body.Len())
	}
	if w := get(); w.Code != http.StatusNotFound {
		t.Fatalf("令牌只能使用一次：%d", w.Code)
	}
}

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

func TestNativeShellRequested(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	none := env(nil)
	for _, c := range []struct {
		name    string
		args    []string
		getenv  func(string) string
		setting string
		want    bool
	}{
		{"默认原生", nil, none, "", true},
		{"设置为 WebView", nil, none, `"webview"`, false},
		{"设置为原生", nil, none, `"native"`, true},
		{"命令行优先于设置", []string{"--native"}, none, `"webview"`, true},
		{"命令行回退 WebView", []string{"a.md", "--webview"}, none, "", false},
		{"环境变量优先于设置", nil, env(map[string]string{"NOMO_SHELL": "native"}), `"webview"`, true},
		{"环境变量回退 WebView", nil, env(map[string]string{"NOMO_SHELL": "webview"}), "", false},
		{"兼容 NOMO_NATIVE=0", nil, env(map[string]string{"NOMO_NATIVE": "0"}), "", false},
		{"命令行优先于环境变量", []string{"--webview"}, env(map[string]string{"NOMO_SHELL": "native"}), "", false},
	} {
		if got := nativeShellRequested(c.args, c.getenv, c.setting); got != c.want {
			t.Errorf("%s：得到 %v", c.name, got)
		}
	}
	if got := stripShellFlags([]string{"--webview", "a.md", "--native"}); len(got) != 1 || got[0] != "a.md" {
		t.Errorf("stripShellFlags：%v", got)
	}
}

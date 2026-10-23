package main

import "testing"

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

// TestWithFrameShim 动画帧替换脚本放在 <head> 开头（先于页面脚本），没有 <head> 时放在最前。
func TestWithFrameShim(t *testing.T) {
	got := withFrameShim("<!doctype html><html><head><script>run()</script></head></html>")
	if want := "<!doctype html><html><head>" + frameShim + "<script>run()</script>"; got[:len(want)] != want {
		t.Fatalf("应插在 <head> 之后：%s", got)
	}
	if got := withFrameShim("<p>正文</p>"); got != frameShim+"<p>正文</p>" {
		t.Fatalf("无 <head> 时应放在最前：%s", got)
	}
}

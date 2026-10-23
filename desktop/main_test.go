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

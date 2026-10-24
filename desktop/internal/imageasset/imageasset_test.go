package imageasset

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestPicgoCoreTimeout：命令超过时限时结束进程并返回超时错误，不一直等待。
func TestPicgoCoreTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("用 sleep 模拟卡住的命令")
	}
	start := time.Now()
	_, stderr, err := runPicgoCore(200*time.Millisecond, "sleep", "10")
	if err == nil || !strings.Contains(err.Error(), "超时") || stderr != "" {
		t.Fatalf("应返回超时错误：err=%v stderr=%q", err, stderr)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("超时后仍等待了 %v", d)
	}
	if out, _, err := runPicgoCore(5*time.Second, "echo", "ok"); err != nil || strings.TrimSpace(out) != "ok" {
		t.Fatalf("正常命令：%q %v", out, err)
	}
}

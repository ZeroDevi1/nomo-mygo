// Package applog 是桌面端的进程级日志：默认关闭，开发者模式下同时写 stderr 与按天滚动的日志文件。
//
// 全局状态在 Init 前即可使用（此时只写 stderr 之外的文件路径无法确定，写文件会失败并降级）；
// 所有导出函数并发安全。
package applog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// maxLogFileBytes 是单个日志文件的轮转阈值；超过后改名为带时间戳的文件，再新建当天文件。
const maxLogFileBytes = 5 * 1024 * 1024

var (
	mu      sync.Mutex
	enabled bool
	// logsDir 在 Init 时确定；为空表示尚未初始化，此时只写 stderr。
	logsDir string
)

// Init 设置日志目录并按启动配置决定是否开启日志。
func Init(dir string, enable bool) {
	mu.Lock()
	logsDir = dir
	mu.Unlock()
	SetEnabled(enable)
	Info("App", "日志系统初始化")
}

// SetEnabled 开关日志输出；开启时强制写一条提示，便于在日志中定位开关时间。
func SetEnabled(value bool) {
	mu.Lock()
	enabled = value
	mu.Unlock()
	if value {
		writeForce("INFO", "Logger", "日志输出已开启")
	}
}

// Enabled 返回当前是否输出日志。
func Enabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return enabled
}

func Debug(tag, message string) { Write("DEBUG", tag, message) }
func Info(tag, message string)  { Write("INFO", tag, message) }
func Warn(tag, message string)  { Write("WARN", tag, message) }
func Error(tag, message string) { Write("ERROR", tag, message) }

// Perf 记录一次操作耗时（毫秒，两位小数）。
func Perf(tag, operation string, elapsed time.Duration) {
	Write("PERF", tag, fmt.Sprintf("%s 耗时 %.2fms", operation, float64(elapsed.Microseconds())/1000))
}

// Write 在日志开启时写入一行；level 不区分大小写。
func Write(level, tag, message string) {
	if !Enabled() {
		return
	}
	writeForce(level, tag, message)
}

func writeForce(level, tag, message string) {
	line := formatLine(level, tag, message)
	fmt.Fprintln(os.Stderr, line)
	if err := appendToFile(line); err != nil {
		fmt.Fprintln(os.Stderr, formatLine("WARN", "Logger", "写入日志文件失败："+err.Error()))
	}
}

func formatLine(level, tag, message string) string {
	return fmt.Sprintf("[%s][%s][%s] %s", time.Now().Format("2006-01-02 15:04:05.000"), strings.ToUpper(level), tag, message)
}

func appendToFile(line string) error {
	mu.Lock()
	dir := logsDir
	mu.Unlock()
	if dir == "" {
		return fmt.Errorf("日志目录未初始化")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建日志目录失败：%w", err)
	}
	now := time.Now()
	path := filepath.Join(dir, now.Format("2006-01-02")+".log")
	if info, err := os.Stat(path); err == nil && info.Size() >= maxLogFileBytes {
		rotated := filepath.Join(dir, now.Format("2006-01-02")+"-"+now.Format("150405")+".log")
		if err := os.Rename(path, rotated); err != nil {
			return fmt.Errorf("轮转日志文件失败：%w", err)
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("打开日志文件失败：%w", err)
	}
	defer file.Close()
	if _, err := file.WriteString(line + "\n"); err != nil {
		return fmt.Errorf("追加日志失败：%w", err)
	}
	return nil
}

// Package memtrim 在大文档关闭后把 Go 堆中已空闲的内存尽快交还操作系统。
//
// Go 的后台回收器按约 1% CPU 的速度归还空闲页：关闭 50 MiB 文档后约 300 MB 空闲堆要很久才归还，期间仍计入
// 进程提交内存（Windows 任务管理器显示的“内存”）。关闭大文档后合并请求、延时执行一次 debug.FreeOSMemory；
// 此时存活堆很小，强制回收的停顿短。
package memtrim

import (
	"runtime/debug"
	"sync"
	"time"
)

// Threshold 是值得主动归还内存的文档字节数：更小的文档留给后台回收器，避免频繁强制回收。
const Threshold = 4 << 20

// delay 等待关闭后的收尾（界面释放布局、会话后台任务退出）完成再回收；期间的多次请求合并为一次。
const delay = 2 * time.Second

var (
	mu    sync.Mutex
	timer *time.Timer
)

// Request 预约一次内存归还：delay 后在后台 goroutine 执行 debug.FreeOSMemory。可在任意 goroutine 调用。
func Request() {
	mu.Lock()
	defer mu.Unlock()
	if timer != nil {
		timer.Reset(delay)
		return
	}
	timer = time.AfterFunc(delay, func() {
		mu.Lock()
		timer = nil
		mu.Unlock()
		debug.FreeOSMemory()
	})
}

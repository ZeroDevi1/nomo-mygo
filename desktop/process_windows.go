//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// createNoWindow 让控制台子进程不弹出黑窗口。
const createNoWindow = 0x08000000

func hideConsoleWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

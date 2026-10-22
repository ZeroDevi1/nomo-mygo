//go:build !windows

package imageasset

import "os/exec"

func hideConsoleWindow(cmd *exec.Cmd) {}

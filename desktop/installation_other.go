//go:build !windows

package main

// 非 Windows 平台不区分安装形态，更新检查只提示新版本（见 software_update.go）。
func currentInstallationKind() (installationKind, error) { return installationUnsupported, nil }

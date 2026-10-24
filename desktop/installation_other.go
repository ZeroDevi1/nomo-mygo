//go:build !windows

package main

import (
	"errors"

	"github.com/egoist/mygo"
)

// 非 Windows 平台：正式构建检查并提示新版本（手动从发布页或 Homebrew 升级），不在应用内安装；开发构建不检查。
func currentInstallationKind() (installationKind, error) {
	if mygo.IsDev() {
		return installationUnsupported, nil
	}
	return installationManual, nil
}

func launchInstallerAndExit(string) error {
	return errors.New("当前平台不支持应用内安装更新")
}

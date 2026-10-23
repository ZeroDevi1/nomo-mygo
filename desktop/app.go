package main

import (
	"path/filepath"

	"nomo/desktop/internal/config"
)

// appIdentifier 决定应用数据目录（与 Tauri 版本一致），升级后沿用已有配置与恢复数据。
const appIdentifier = "com.nomo.desktop"

// desktopApp 持有进程级服务；在 main 中 App.Run 之前初始化一次，此后只读。
type desktopApp struct {
	dataDir string
	config  *config.Manager
}

var app *desktopApp

func newDesktopApp(dataDir string) (*desktopApp, error) {
	manager, err := config.Load(filepath.Join(dataDir, config.FileName))
	if err != nil {
		return nil, err
	}
	return &desktopApp{dataDir: dataDir, config: manager}, nil
}

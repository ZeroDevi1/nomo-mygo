package main

import (
	"path/filepath"

	"nomo/desktop/internal/config"
)

// appDataIdentifier 保留与旧版共享的配置、恢复数据与缓存路径，独立于 mygo.json 的打包身份。
const appDataIdentifier = "com.nomo.desktop"

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

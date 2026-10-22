package main

import (
	"errors"
	"path/filepath"

	"nomo/desktop/internal/config"
	"nomo/desktop/internal/textdoc"
)

// appIdentifier 决定应用数据目录（与 Tauri 版本一致），升级后沿用已有配置与恢复数据。
const appIdentifier = "com.nomo.desktop"

// desktopApp 持有进程级服务；在 main 中 App.Run 之前初始化一次，此后只读。
type desktopApp struct {
	dataDir   string
	config    *config.Manager
	documents *textdoc.Manager
}

var app *desktopApp

func newDesktopApp(dataDir string) (*desktopApp, error) {
	manager, err := config.Load(filepath.Join(dataDir, config.FileName))
	if err != nil {
		return nil, err
	}
	return &desktopApp{
		dataDir:   dataDir,
		config:    manager,
		documents: textdoc.NewManager(filepath.Join(dataDir, "segmented-documents")),
	}, nil
}

// contentStoreDir 返回应用数据目录下的子目录（草稿、快照、主题等）。
func contentStoreDir(name string) string {
	return filepath.Join(app.dataDir, name)
}

// settingValue 返回设置的 JSON 文本；不存在时 ok 为 false。
func settingValue(key string) (string, bool) {
	return app.config.Setting(key)
}

var errConfigUnavailable = errors.New("配置管理器尚未初始化")

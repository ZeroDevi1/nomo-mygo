package main

import _ "embed"

// 应用图标，与 mygo.config.ts 的 icon 同源（desktop/packaging/icons/nomo/source）。
// 原生外壳用于窗口图标（Windows / Linux）与“关于”页；macOS Dock 图标见 tray.go 的 setDesktopIconTheme。
var (
	//go:embed packaging/icons/nomo/source/nomo-app-light-256.png
	appIconLight []byte
	//go:embed packaging/icons/nomo/source/nomo-app-dark-256.png
	appIconDark []byte
)

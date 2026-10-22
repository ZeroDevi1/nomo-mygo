package main

import _ "embed"

// 应用图标，与 mygo.config.ts 的 icon 同源（desktop/packaging/icons/nomo/source）。
// 原生外壳用于窗口图标（Windows / Linux）、开发运行时的 Dock 图标与“关于”页。
var (
	//go:embed packaging/icons/nomo/source/nomo-app-light-256.png
	appIconLight []byte
	//go:embed packaging/icons/nomo/source/nomo-app-dark-256.png
	appIconDark []byte
	//go:embed packaging/icons/nomo/source/nomo-app-light-512.png
	appIconDock []byte
)

// Package nomo 汇集需要编译进桌面程序的仓库级文件；go:embed 只能引用本包目录之下的文件，
// 因此放在仓库根目录。
package nomo

import "embed"

// ReleaseNotes 是各版本的更新日志（.github/release-notes/v<版本>.md），
// 桌面程序据此在升级后展示当前版本说明。
//
//go:embed .github/release-notes
var ReleaseNotes embed.FS

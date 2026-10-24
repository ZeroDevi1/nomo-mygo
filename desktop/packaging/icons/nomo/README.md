# Nomo 图标资源

本目录存放 Nomo 的 SVG 母版及从矢量生成的浅深色、托盘、Windows、macOS 图标。

## 修改与生成

应用造型只修改 `source/nomo-app-light.svg`。深色 SVG 共用相同几何结构，配色、桌面描边、平台边距与托盘造型在 `scripts/icons/main.go` 中维护。PNG 都是生成产物，不要直接编辑位图。

应用内 Logo 的 SVG 和 `source/` PNG 保持原有造型。生成程序仅为桌面、任务栏 / Dock 和系统文件关联资源派生描边：浅色使用 `#c8cdd4`，深色使用 `#7b8594`。描边沿底板内侧绘制，保持原有外轮廓、圆角和平台边距；256px 画布的 8px 描边在 32px 图标中为 1px。托盘继续使用原有造型及配色。

```bash
go run ./scripts/icons              # 更新全部图标
go run ./scripts/icons -macos-only  # 只更新 macOS 图标
go run ./scripts/icons -out <目录>  # 写到其他目录，与现有资源对比
```

栅格化使用 MyGo 的无头渲染，每个尺寸直接从矢量渲染，不放大或缩小位图。`.icns` 与 `.ico` 由 `go tool mygo build` 从 `mygo.json` 的 `icon`（`source/nomo-app-light-1024.png`）生成，不在仓库中保存。

## Windows

- `windows/` 包含 15 档 `targetsize` 的默认、深色 `altform-unplated`、浅色 `altform-lightunplated` 资源，以及 125%、150%、200%、400% 的基础 Logo 缩放资源；上级目录的 `StoreLogo.png`、`Square44x44Logo.png`、`Square71x71Logo.png`、`Square150x150Logo.png` 是 100% 基础资源。
- MSIX 构建通过 `scripts/msix/Build-IconResources.ps1` 复制资源并生成 `resources.pri`，让系统按实际尺寸和外观选择图标。无限定符的基础 PNG 作为 100% 缩放回退，不额外生成重复的 `scale-100` 候选。

## macOS 两套图标（重要）

macOS 上 Dock / 启动台 / Cmd+Tab 看到的图标来自以下资源：

| 资源 | 用途 |
|------|------|
| `macos/AppIcon.icon`（Icon Composer 文档，`mygo.json` 的 `macos.icon`） | `go tool mygo build` 用 actool 编译为 `Assets.car`，应用**未运行**时 Finder / Dock / 启动台按系统浅色/深色选图；需要 Xcode 26 及以上，缺少时 MyGo 给出警告并回退到 `.icns` |
| `macos/nomo-app-light-1024.png` / `dark` | 应用**运行后**由 `desktop/tray.go` 设置 Dock / Cmd+Tab 图标 |

`AppIcon.icon/Assets/*.png` 从 SVG 的底板边界直接导出满幅 1024px 图层，由 Icon Composer 应用平台遮罩。运行时 PNG 的底板宽度占画布的 `218/256`（约 85.2%）。`scripts/build-macos.sh` 构建后由 `scripts/verify-macos-bundle.sh` 检查 `Assets.car` 已打进 `.app`；实际 Dock 外观仍需在 macOS 上查看。

## 托盘

浅深色各提供激活、非激活状态。SVG 使用不透明的圆角底板、对比边框和清晰的 N 与状态横线：浅色为白底深色 N，深色为深底浅色 N。应用主题与系统任务栏外观可能不同，底板和边框让图标在两种任务栏背景上都能辨认；未激活状态（所有窗口隐藏到托盘时）保留底板，仅将 N 和横线改为灰色。运行时嵌入 48px PNG，供高 DPI 缩放使用，同时保留 24px 预览。

## 目录说明

- `source/`: 浅色 SVG 母版、生成的深色 SVG，以及 light/dark × 128/256/512/1024 PNG。
- `macos/`: 应用平台边距的 256/512/1024 PNG，以及 Icon Composer 文档 `AppIcon.icon`。
- `tray/`: light/dark × active/inactive SVG、24px 预览和 48px 运行时 PNG。
- `windows/`: MSIX 的目标尺寸、主题和缩放限定资源。

## 命名规则

- 文件名前缀统一使用 `nomo-`。
- `app-light` / `app-dark` 表示应用 logo 浅色或深色版本。
- `tray-light` / `tray-dark` 表示跟随应用浅色或深色主题的托盘图标；两者均带底板，兼容系统栏的浅深色背景。
- `active` / `inactive` 表示激活态或未激活态。
- 数字后缀表示导出像素尺寸。

<p align="center">
  <img src="./src-tauri/icons/128x128.png" alt="Nomo 图标" width="60">
</p>

<h1 align="center">Nomo</h1>

<p align="center">
  <a href="https://github.com/nomo-md/nomo/releases/latest">
    <img src="https://img.shields.io/github/v/release/nomo-md/nomo?label=release" alt="GitHub Release">
  </a>
  <img src="https://img.shields.io/badge/platform-macOS%20%7C%20Windows-lightgrey" alt="支持 macOS 与 Windows">
  <a href="./LICENSE">
    <img src="https://img.shields.io/badge/license-AGPL--3.0--or--later-blue" alt="AGPL-3.0-or-later">
  </a>
</p>

<p align="center">
  <a href="./README.md"><strong>简体中文</strong></a>
  ·
  <a href="./README.en.md">English</a>
</p>

<p align="center">
  <strong>一款本地优先的 Markdown 编辑器，让写作保持简单。</strong>
</p>

<p align="center">
  <img src="./assets/nomo-editor-zh.gif" alt="Nomo 中文写作演示" width="800">
</p>

## 下载与安装

从 [GitHub Releases](https://github.com/nomo-md/nomo/releases/latest) 下载最新版本。

| 系统 | 要求 | 安装方式 |
| :--- | :--- | :--- |
| Windows | Windows 10 / 11，x64 | `.exe` 安装版 / `.zip` 免安装版 |
| macOS | macOS 12.0+，Apple Silicon | `.dmg` / Homebrew |

macOS 也可通过 Homebrew 安装：

```bash
brew tap nomo-md/nomo https://github.com/nomo-md/nomo
brew install --cask nomo
```

升级：`brew upgrade --cask nomo`。

首次打开可能出现 SmartScreen / Gatekeeper 提示，请从本项目 Releases 下载。

## 核心功能

- **Markdown 编辑**：即时预览、源码与双栏模式，输入 `/` 快速插入内容。
- **丰富内容**：支持表格、代码高亮、提示块与文字颜色；数学公式与 Mermaid 图表以源码显示。
- **文件管理**：文件树键入定位、多标签，以及 TXT / JSON 大文件分段编辑。
- **保存与恢复**：可启用自动保存和 Markdown 本地快照，恢复工作区与阅读位置。
- **舒适写作**：浅深色主题、[自定义配色主题与字体](docs/custom-themes.md)、可手动开启的精细排版，以及可置顶的 Markdown 小窗。
- **导出与预览**：导出 HTML / PDF（可设纸张、边距、页码与页眉页脚）；macOS 支持 Quick Look 快速预览。

版本变化请查看[更新记录](https://github.com/nomo-md/nomo/releases)。

## 开发

界面由 Go 与 [MyGo](https://github.com/egoist/mygo) 原生 UI 构建（`desktop/`），不含 Web 前端代码。需要 Go 1.27；Node.js 22 与 pnpm 11.5.1 只用于 MyGo 命令行、图标生成与格式化。

- macOS：Xcode Command Line Tools；构建 Quick Look 扩展需要完整 Xcode。

```bash
pnpm install
pnpm desktop:dev
```

检查与测试：`pnpm check`（`go vet`）、`pnpm test`（`go test`）。

## 参与项目

欢迎提交 [Issue](https://github.com/nomo-md/nomo/issues) 或 [Pull Request](https://github.com/nomo-md/nomo/pulls)；反馈问题时请附系统版本、复现步骤与样例。

喜欢 Nomo，欢迎 [Star 项目](https://github.com/nomo-md/nomo) 或关注作者 [LIXianSenQwQ](https://github.com/LIXianSenQwQ)。社区：[linux.do](https://linux.do/)。

采用 [AGPL-3.0-or-later](./LICENSE) 许可证；商业授权请联系维护者，第三方组件遵循各自许可证。

感谢 MyGo、pdfcpu、Lucide 等开源项目。

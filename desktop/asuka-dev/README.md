# Nomo Asuka 开发实验

独立的 Markdown 编辑器，用于验证 [Asuka v0.1.0](https://github.com/egoist/asuka/releases/tag/v0.1.0) 的原生输入、Markdown 预览和文件生命周期。此目录使用独立 Go 模块，通过本地模块引用复用 Nomo 的文档编码逻辑。

从仓库根目录运行以下命令。Go 版本与 Nomo 主模块一致，Asuka CLI 由实验模块锁定。

```sh
cd desktop/asuka-dev
go tool asuka dev --platform mac
```

在「File → Open」打开 `.md` 或 `.markdown`。左侧编辑源码，右侧预览。窄窗口上下排列。桌面版使用显式保存。新文档默认 UTF-8。已有 UTF-8、UTF-8 BOM 或 UTF-16 BOM 文档保留原编码。GBK 文档可打开，状态栏显示「GBK → UTF-8」，保存时会转换为 UTF-8。

```sh
go test -race ./...
go tool asuka build --platform mac
go tool asuka build --platform web
```

macOS 应用包在 `dist/Nomo Asuka Dev.app`。Web 产物在 `dist/web`，可通过 HTTP 静态服务器打开。自动化测试使用临时目录和 Asuka 测试宿主，覆盖打开、编辑、预览切换、显式保存和 UTF-16 BOM 重开。

实验按窗口读入整份文档。预览合并连续输入，隐藏时释放预览视图并取消待处理的更新。公式、Mermaid、相对路径附件、Nomo 的语义编辑和学术工作区属于后续集成范围。

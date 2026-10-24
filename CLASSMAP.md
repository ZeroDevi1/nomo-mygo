# CLASSMAP.md

## Purpose

本文件将 Nomo Markdown 编辑器的功能映射到具体代码单元，帮助后续 AI 编码会话快速定位文件，避免大范围搜索。

## How to Use

- 修改行为前先查本文件。
- 用 **Feature Index** 按功能找候选代码。
- 用 **Code Unit Entries** 了解各模块的职责边界与非职责。
- 若本文件与代码冲突，**以代码为准**并更新本文件。

---

## Feature Index

### 应用启动与窗口生命周期

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| 程序入口（数据目录、日志、WebView2 参数、单实例） | `desktop/main.go` | `desktop/app.go`, `desktop/native.go` | 修改启动顺序、单实例或隐藏 WebView 的浏览器参数 |
| 原生界面启动、外部打开投递、托盘菜单、PDF 与更新检查接入 | `desktop/native.go` | `desktop/external_open.go`, `desktop/tray.go`, `desktop/export.go`, `desktop/update_check.go` | 修改外壳选项、第二实例与 macOS 打开文件事件的投递、托盘菜单 |
| 启动参数识别 | `desktop/external_open.go` | `desktop/native.go` | 修改受支持文档与文件夹的识别、去重与路径规范化 |

### 原生语义编辑器原型（Go，P0）

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| 原文、块索引、选区与撤销 | `desktop/ui/mdedit/document.go` | `edit.go`, `history.go`, `text.go` | 修改增量块索引、UTF-16/字节换算、组字与撤销合并规则 |
| 分段解析（大文档按键成本） | `desktop/ui/mdedit/chunks.go` | `document.go`（`recollect` 增量块收集） | 修改分段阈值、切分安全条件、编辑后的合并与重新切分 |
| 正文行高（行距排版） | `desktop/ui/mdedit/lines.go` | `view.go`（`ensureRow`）, `blocks.go`（`rowLead`）, `draw.go` | 修改行距规则、加行距后的命中测试、选区与逐行绘制 |
| 折叠块（details） | `desktop/ui/mdedit/details.go`（识别与配对）、`detailsview.go`（视图与命令） | `blocks.go`（标记行）、`htmlpreview.go`（`previewDetails`）、`desktop/ui/shell/editormenu.go` | 修改折叠标记语法、配对与增量平移、卡片绘制、折叠隐藏与自动展开、插入/默认展开/取消折叠 |
| 编辑区选项（代码块行号、Tab 缩进、行内代码、复制语法、默认插入） | `desktop/ui/mdedit/options.go` | `desktop/ui/shell/editoroptions.go`（设置到选项、图表模板、Ctrl 滚轮） | 修改原版 editor.updateOptions 对应的显示与插入行为 |
| 精细排版（Knuth–Plass 断行，纯计算） | `desktop/ui/typography`（`knuthplass.go`、`rules.go`、`hyphenation.go`） | `hyphen_en_us.go`（hyphenation.en-us 0.2.1 原样数据）、`testdata/crosscheck.json`（原版依赖对照数据） | 修改 KP 求解、标点禁则/压缩/悬挂、中西文空白、英文断词 |
| 精细排版接入编辑区 | `desktop/ui/mdedit/kp.go`（测量、规则输入、逐行排版）、`kplines.go`（多行布局映射） | `view.go`（`ensureRow`）、`lines.go`（`adjacentY`） | 修改精排适用范围、测量与绘制 |
| 单行修补（大表格/长代码块按键） | `desktop/ui/mdedit/patch.go` | `view.go`（`applyEdit`）、`document.go`（`checkpoints`） | 修改块内编辑的增量路径与 UTF-16 检查点 |
| 原文到显示的投影 | `desktop/ui/mdedit/projection.go` | `view.go` | 修改行内标记就地展开/隐藏、行内 HTML 标签样式、实体解码、提示块标记、显示与原文偏移映射 |
| 块组件（列表、任务、引用、提示块、表格、代码块、分割线、front matter） | `desktop/ui/mdedit/blocks.go` | `draw.go`, `view.go`, `document.go` | 修改块展开为行的规则、容器前缀、缩进与间距、装饰范围 |
| 可见区布局与缓存 | `desktop/ui/mdedit/view.go` | `blocks.go`, `input.go` | 修改行虚拟化、阅读锚点、布局缓存预算与淘汰、命中测试、主题配色 |
| 绘制 | `desktop/ui/mdedit/draw.go` | `view.go` | 修改装饰（引用条、提示块标题、代码卡片、表格网格）、列表符号与任务框绘制 |
| 代码块语法高亮 | `desktop/ui/mdedit/highlight.go` | `view.go`, `blocks.go` | 修改代码块着色、Tokenizer 跨行状态缓存 |
| 语义模式图片（行内对象、加载缓存） | `desktop/ui/mdedit/imageview.go` | `images.go`, `projection.go`, `desktop/ui/shell/appearance.go` | 修改图片显示尺寸、点击展开、只含图片的 HTML 块、图片缓存预算与淘汰、后台加载、GIF 动画 |
| 表格列宽与横向滚动 | `desktop/ui/mdedit/tablelayout.go` | `view.go`（`rowBox`）, `draw.go`, `input.go` | 修改列宽策略（原版 widths.ts）、超宽表格横向滚动、单元格裁剪 |
| HTML 块预览、注释卡片、目录卡片 | `desktop/ui/mdedit/htmlpreview.go` | `blocks.go`, `document.go`（`KindToc`）, `imageview.go` | 修改可预览的 HTML 白名单与排版、注释/目录卡片样式、目录点击跳转 |
| 公式与 Mermaid 渲染 | `desktop/ui/shell/render.go`（离屏 WebView 渲染服务） | `desktop/ui/render`（渲染页 render.html / render.js 与随附的 KaTeX 0.17.0、Mermaid 11.15.0）, `desktop/ui/mdedit/images.go`（`RenderFunc`、`rasterizeSVG`）, `export.go`（`renderExportMarkup`） | 修改离屏渲染窗口的生命周期、截图裁剪、渲染配色、行内公式基线；升级 KaTeX / Mermaid 时替换 assets 并更新 THIRD_PARTY_NOTICES.md |
| Word（.docx）导出 | `desktop/ui/word`（`Build`：docx.go、media.go、parts.go） | `desktop/ui/shell/export.go`（`exportWord`）, `mdedit.HeadingIDs`（目录锚点） | 修改 Word 版式与样式、块与行内的转换、图片与公式嵌入、资源预算与降级说明 |
| 图片编辑（拖边缩放、拖动移位、右键菜单、预览） | `desktop/ui/mdedit/imageedit.go` | `input.go`（拖动与双击）, `desktop/ui/shell/imagemenu.go`（菜单与全屏预览） | 修改宽度/对齐写回原文的规则、拖动命中与绘制、图片菜单项 |
| 文字颜色 | `desktop/ui/mdedit/textcolor.go` | `projection.go`（`style` 高位颜色索引）, `desktop/ui/shell/toolbar.go`（`colorPicker`） | 修改颜色解析、`<span style="color">` 写回、调色板 |
| 提示块标题与折叠 | `desktop/ui/mdedit/calloutfold.go` | `projection.go`（`parseCalloutHeader`）, `blocks.go`, `draw.go`（`drawFrame`） | 修改提示块别名、自定义标题、`+`/`-` 折叠语法与点击展开 |
| 上下标语法（^上标^、~下标~） | `desktop/ui/mdedit/script.go` | `projection.go`（`text`、删除线改判）, `api.go`（`toggleScript`）, `desktop/ui/shell/export.go`（`ExpandScripts`） | 修改上下标与 GFM 删除线的分工规则、工具栏写法、导出改写 |
| 内嵌 SVG 显示 | `desktop/ui/mdedit/imageview.go`（`svgDataURI`、`svgElementEnd`） | `projection.go`（`svgClose`）, `images.go`（data URI 解码） | 修改 HTML 块与段落内 `<svg>` 作为图片显示的识别规则 |
| 导出 HTML 安全清理 | `desktop/ui/shell/sanitize.go` | `export.go`（`exportDocument`） | 修改导出时去掉的元素、属性与 URL 协议 |
| 源码模式 HTML 补全 | `desktop/ui/shell/htmlcomplete.go` | `desktop/ui/mdedit/api.go`（`Document.CodeAt`） | 修改标签/属性/CSS 补全项与触发条件 |
| 双栏模式 | `desktop/ui/shell/split.go` | `tab.go`（双向同步）, `desktop/ui/mdedit/api.go`（`TopSourceOffset`、`ScrollToSource`） | 修改双栏布局、比例保存、活动一侧与滚动同步 |
| 组合标签 | `desktop/ui/shell/compare.go` | `view.go`（标签栏、右键菜单）, `tab.go`（`partner`） | 修改组合/解散/交换、并排显示与活动文档 |
| 多窗口、托盘、小窗、标签拖动 | `desktop/ui/shell/windows.go`, `tray.go`, `mini.go`, `view.go`（`tabDragTarget`） | `desktop/native.go`（`installNativeTray`）, `workspace.go`（多窗口恢复） | 修改窗口注册与菜单分发、关闭到托盘与托盘图标状态、Markdown 小窗、标签拖动排序/跨窗口/拖出成新窗口、中键关闭 |
| 测试集跑测 | `desktop/ui/mdedit/corpus_test.go`, `desktop/ui/shell/corpus_test.go` | `NOMO_CORPUS` 环境变量 | 修改测试集逐文件检查项（保真、耗时、内存）与外壳场景（编码、四标签、目录树） |
| 输入法、键盘与命令 | `desktop/ui/mdedit/input.go` | `view.go`, `edit.go`, `structure.go` | 修改 TextInputClient、光标移动、删除、剪贴板与加粗等命令 |
| 结构编辑 | `desktop/ui/mdedit/structure.go` | `input.go`, `blocks.go` | 修改列表/引用内回车与退出、行首退格提出容器、Tab 缩进与表格单元格切换、任务勾选 |
| 编辑器对外接口（格式命令、查找、大纲、统计、源码同步） | `desktop/ui/mdedit/api.go` | `structure.go`, `edit.go` | 修改工具栏/菜单命令的原文事务、查找匹配、大纲与写作统计 |
| 工具栏插入命令（表格尺寸、脚注、正文目录、行内注释、行内公式） | `desktop/ui/mdedit/inserts.go` | `history.go`（`linkLast` 相连撤销）, `api.go`（`Run`） | 修改插入的原文格式、脚注编号、目录锚点规则 |

### 原生外壳（Go，唯一界面，见 `desktop/native.go`）

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| 入口、外部打开与退出 | `desktop/native.go` | `desktop/main.go`, `desktop/external_open.go` | 修改原生模式的启动条件、第二实例与 macOS 打开文件事件、退出 |
| 外壳状态、偏好、文件操作 | `desktop/ui/shell/shell.go` | `tab.go`, `desktop/internal/config`, `desktop/internal/docfs` | 修改新建/打开/保存/关闭确认、最近打开、外部修改检测、偏好持久化 |
| 文档标签与源码模式同步 | `desktop/ui/shell/tab.go` | `desktop/ui/mdedit` | 修改语义/源码模式的原文同步、未保存判定、统计与大纲缓存 |
| 布局（标题栏、侧栏、标签、工具栏、查找栏、大纲、状态栏、空白工作区、确认框） | `desktop/ui/shell/view.go` | `explorer.go`, `menu.go` | 修改外壳布局与外观 |
| 格式工具栏与状态栏缩放 | `desktop/ui/shell/toolbar.go` | `view.go`（`toolButton`、`statusBar`）, `editoroptions.go`（图表模板） | 修改工具栏按钮与分组、表格尺寸网格、公式/图表菜单、内容宽度与缩放弹层 |
| 菜单与快捷键 | `desktop/ui/shell/menu.go` | `view.go` | 增删菜单命令或快捷键（标题栏菜单、macOS 菜单栏共用一张表） |
| 平台原生风格（macOS / Windows 11 / Linux 的布局结构、材质、配色） | `desktop/ui/shell/platform.go` | `view.go`, `settings.go` | 修改各平台主题、vibrancy / Mica、外壳配色 |
| 偏好（与原版 AppPreferences 同键、同默认、同取值范围） | `desktop/ui/shell/prefs.go` | `settings.go`, `shell.go` | 新增设置项或修改默认值、规范化规则 |
| 设置窗口 | `desktop/ui/shell/settings.go` | `prefs.go`, `platform.go` | 增删设置分类与设置项 |
| 资源管理器与标签的文件操作 | `desktop/ui/shell/fileops.go` | `explorer.go`, `view.go` | 修改新建、重命名、复制路径、在文件夹中显示 |
| 资源管理器目录树 | `desktop/ui/shell/explorer.go` | `desktop/internal/docfs/tree.go` | 修改目录懒加载、刷新与点击打开 |
| 颜色主题、字体与用户主题导入导出 | `desktop/ui/shell/appearance.go` | `desktop/ui/themes`, `settings.go` | 修改主题到编辑器配色的映射、字体应用、用户主题文件读写 |
| 编辑区右键菜单 | `desktop/ui/shell/editormenu.go` | `desktop/ui/mdedit/api.go`（`ContextTarget`）, `desktop/ui/mdedit/blockedit.go` | 修改右键目标判断与各目标的菜单项（原版 buildEditorContextMenuItems），含表格、代码块、公式与图表菜单 |
| 块级命令（表格增删行列与对齐、代码块语言、复制块内容、删除块） | `desktop/ui/mdedit/blockedit.go` | `editormenu.go`（`blockMenu`） | 修改表格原文重写规则、围栏内容范围、公式块闭合行 |
| 链接快速编辑浮层 | `desktop/ui/shell/linkedit.go` | `desktop/ui/mdedit/api.go`（`LinkAt`） | 修改链接插入/编辑/移除、地址校验与浮层位置 |
| 历史快照窗口 | `desktop/ui/shell/snapshots.go` | `desktop/internal/config/snapshot.go`（`ListSnapshots`） | 修改快照列表、预览与恢复 |
| 工作区恢复与恢复副本、首次示例文档 | `desktop/ui/shell/workspace.go` | `shell.go`（`New`、关闭流程）, `desktop/native.go`（`resolveSampleDocument`） | 修改设置 `nativeWorkspace` 的结构、副本写入时机与冲突处理、示例文档条件 |
| Windows 文件关联与右键菜单 | `desktop/windows_integration_windows.go` | `desktop/windows_integration.go`（接入外壳）, `desktop/ui/shell/winintegration.go`（设置页） | 修改注册表写入、MSIX 包内右键菜单开关、设置页按钮 |
| 大纲面板（可折叠树、默认展开层级） | `desktop/ui/shell/outline.go` | `view.go`（`outline`） | 修改大纲可见项、展开箭头与默认层级 |
| 快捷插入菜单 | `desktop/ui/shell/quickinsert.go` | `desktop/ui/mdedit/api.go`（`CaretRect`、`Delete`、`Intercept`） | 修改触发条件、菜单项与键盘操作 |
| 原生表情建议与选择器 | `desktop/ui/shell/emoji.go` | `desktop/internal/emoji`, `desktop/ui/mdedit/api.go`（`CaretContext`、`Replace`）, `Document.InputVersion` | 修改冒号建议的弹出与排除条件、键盘操作、选择器分类/网格/搜索、插入目标校验 |
| 图片粘贴、拖放与插入 | `desktop/ui/shell/images.go` | `desktop/internal/imageasset`, `mdedit` `PasteData` | 修改图片导入策略、PicGo 上传、插入的 Markdown / HTML |
| Markdown 格式检查（状态栏与问题列表） | `desktop/ui/shell/lint.go` | `desktop/ui/mdlint` | 修改检查调度、防抖、问题弹层与跳转 |
| 导出 HTML / PDF | `desktop/ui/shell/export.go` | `desktop/ui/themes/export.go`, `desktop/native.go`（`exportNativePDF`）, `desktop/export.go` | 修改导出文档结构、图片内联、提示块卡片、分页标记、PDF 书签标记与版式（`PDFLayout`） |
| 导出精细排版（HTML / PDF） | `desktop/ui/kpexport`（`Script`、assets/kp.js） | `desktop/ui/typography/wasm`（编译为 WebAssembly 的排版入口）, `scripts/kpwasm`（构建时生成 gen/，mygo.json 的 buildCommand）, `desktop/export.go`（`__NOMO_PREPARE_PRINT__`） | 修改导出页面的段落选择、测量、断行应用或打印前重排 |
| 界面文案 | `desktop/ui/shell/messages.go`（直接维护，简体 / 繁体 / 英文 / 日文） | `i18n.go`（语言选择、MyGo 内置文案语言包）, `i18n_test.go`（日文与英文键一一对应） | 新增或修改界面文案（四种语言同步） |
| 界面图标（Lucide） | `desktop/ui/icons/lucide.go`（直接维护） | `icons.go` | 增删界面所用图标 |
| MyGo 应用配置（名称、版本、图标、随包资源、文件关联、Info.plist） | `mygo.json` | `desktop/packaging/samples`（示例文档）, `go.mod` 的 `tool`（`go tool mygo`） | 修改版本号、打包资源或平台配置；版本号唯一来源 |
| 应用与界面图标 | `desktop/ui/icons/icons.go` | `desktop/appicon.go`, `desktop/native.go` | 修改文件类型图标、提示块图标、应用图标（窗口 / Dock / 关于页） |
| 内置主题、导出样式与 Quick Look 主题表 | `desktop/ui/themes/builtin.go`（直接维护） | `export_document.css`, `desktop/packaging/macos/NomoQuickLookPreview/BuiltinThemes.swift` | 修改内置配色或导出样式；改内置主题时同步 Quick Look 主题表 |
| 主题解析、校验与导出 | `desktop/ui/themes/themes.go` | `export.go` | 修改用户主题 JSON 规则（与 userThemeDefinition.ts 一致）、颜色解析、导出 CSS 变量 |
| Markdown 检查规则（markdownlint 子集） | `desktop/ui/mdlint/mdlint.go` | `mdlint_test.go`（`MDLINT_DUMP` 与 markdownlint 对照） | 新增规则或调整宽松规则集 |
| 图片资源导入与图床上传 | `desktop/internal/imageasset/imageasset.go` | — | 修改资源目录策略、同内容复用、PicGo 上传与连接测试 |
| 保存前快照 | `desktop/internal/config/snapshot.go` | `desktop/ui/shell/shell.go` | 修改快照存储与保留数量 |

### 编辑器插件

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| Emoji 搜索与联网缓存 | `desktop/internal/emoji` | `desktop/ui/shell/emoji.go` | 固定版本元数据、双语搜索索引、三源错峰获取、SHA-256 校验、4 MiB 原子缓存与共享加载 |

### 文件系统与文档操作

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| 关闭大文档后归还内存 | `desktop/internal/memtrim` | `desktop/ui/shell/shell.go`（`removeTab`） | 修改主动归还的阈值与延时 |
| Go 文件系统 | `desktop/internal/docfs/docfs.go` | `desktop/internal/config/config.go` | 后端文件读写、Markdown 编码转换/保持、短句柄目录快照与按需枚举 |

### 设置与偏好

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| Go 配置管理 | `desktop/internal/config/config.go` | `desktop/internal/config/config.go` | 应用配置 JSON 持久化、设置读写、启动前读取 |
| 用户主题文件存储 | `desktop/internal/config/themes.go` | `desktop/ui/shell/appearance.go` | `themes/<id>.json` 列出/保存/删除、外部主题读取与模板写出、ID/大小/数量上限 |

### 日志与导出

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| 后端日志系统 | `desktop/internal/applog/applog.go` | — | 日志文件落盘/轮转/终端输出 |
| PDF 导出（隐藏 WebView 原生打印、书签、页码与页眉页脚） | `desktop/export.go` | `desktop/pdf_outline.go`（`stampPageNumbers`、`stampHeaderFooter`）, `desktop/ui/shell/export.go` | 修改纸张与边距、书签、页码与页眉页脚盖印 |

### 本地化

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| 后端本地化 | `desktop/internal/i18n/i18n.go` | — | 菜单/托盘/系统集成文案 |

### Quick Look 预览（macOS）

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| macOS 扩展入口 | `desktop/packaging/macos/NomoQuickLookPreview/PreviewViewController.swift` | `MarkdownRenderer.swift`, `BuiltinThemes.swift` | 读取文件、Nomo 设置与用户主题，用 NSTextView 显示原生渲染结果 |
| Quick Look 原生 Markdown 渲染 | `desktop/packaging/macos/NomoQuickLookPreview/MarkdownRenderer.swift` | `PreviewViewController.swift` | 修改 PresentationIntent 到富文本的映射（标题、列表、引用 / 提示块、代码、表格、图片） |

### 软件更新

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| 版本发布与 Pages 同步 | `.github/workflows/release.yml` | `.github/workflows/backfill-linux-release.yml`, `.github/workflows/sync-pages-notes.yml`, `.github/release-notes/` | 安装包发布、已有稳定版的 Linux 补发、MD5 生成、发布说明维护、Cloudflare/GitHub Pages 重建与线上校验 |

### 原生系统集成

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| 托盘与 Dock 图标深浅色、托盘删除、主程序文案语言 | `desktop/tray.go` | `desktop/native.go`（`installNativeTray`） | 修改托盘与 Dock 图标随外观替换、退出前删除托盘（`destroyAppTray`） |
| 软件更新检查、下载与安装 | `desktop/update_check.go`, `desktop/update_install.go` | `desktop/installation_windows.go`（静默安装并重启、旧版安装器检测）, `desktop/installation_other.go`, `desktop/ui/shell/update.go`（提示、进度、更新日志） | 修改 GitHub Release 检查、gh-proxy 回退、安装包下载与 MD5 校验、Windows 安装形态判断 |

### 发布与打包

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| macOS 构建、Quick Look 嵌入与 DMG | `scripts/build-macos.sh` | `scripts/build-macos-quicklook.sh`, `scripts/verify-macos-bundle.sh`, `desktop/packaging/macos/NomoQuickLookPreview/`, `mygo.json` | 修改 MyGo 产物后处理、扩展架构/版本同步、由内向外签名（扩展保留沙盒 entitlements，主应用不带 `--deep`）或 DMG 生成 |
| SVG 图标与平台资源生成 | `scripts/icons/main.go`（`go run ./scripts/icons`） | `desktop/packaging/icons/nomo/source/nomo-app-light.svg`, `desktop/packaging/icons/nomo/macos/AppIcon.icon`（mygo.json 的 `macos.icon`）, `desktop/tray.go` | 修改应用 Logo、浅深色配色、平台边距、多尺寸位图或托盘图标；浅色 SVG 为造型母版，桌面与系统文件图标单独派生描边，应用内 Logo 保持母版造型 |
| Windows Store MSIX 构建与校验 | `scripts/msix/Build-Msix.ps1` | `scripts/msix/Build-IconResources.ps1`, `.github/workflows/msix.yml`, `desktop/packaging/msix/`, `mygo.json` | 修改 Store 包身份、版本映射、MyGo 构建与产物复制、图标 PRI 生成、Shell 扩展封装或上传产物 |

## Code Unit Entries

### `desktop/ui/mdedit`

**Kind:** package

**Owns:**
- 以 Markdown 原文为唯一事实源的语义编辑器原型（迁移计划 P0）：`Document` 持有原文、顶层块索引、选区、组字区间与完整撤销历史，不持有视图对象
- 块组件：顶层块展开为行（叶子段落、原文行、表格单元格）与跨行装饰，对齐原版 WebView 的列表、任务、引用、五类提示块、表格、代码块、分割线与 front matter 显示；行内 HTML 标签、实体与注释按原版渲染
- `Editor` 视图：只布局可见行，块级阅读锚点，按估算字节的 LRU 布局缓存；`ReleaseLayouts` 供后台标签释放
- `TextInputClient` 实现（原文 UTF-16 坐标）、键盘导航、删除/回车、剪贴板、加粗/斜体/删除线命令与结构编辑（列表/引用续写与退出、Tab 缩进、表格单元格切换、任务勾选）
- 代码块语法高亮（MyGo editor Tokenizer）
- 图片：Markdown 图片与 `<img>` 作为行内对象排版，只含图片的 HTML 块整块显示；`Images` 为窗口共用的按需加载缓存（本地、data URI、远程，后台解码、超大图缩小、字节预算 LRU、GIF 动画）
- 公式与 Mermaid：外壳注入 `RenderFunc` 时作为渲染对象生成位图；当前未注入，按源码显示
- HTML 块静态预览（白名单与原版 sanitizeHtml 一致，含合并单元格表格）、注释卡片、目录卡片（`KindToc` 合成块）、脚注引用标签与跳转
- 表格列宽（原版 widths.ts 策略）与超宽表格横向滚动；超长行分段排版

**Does not own:**
- 精细排版与行高（P0 第 3 项）
- 文件读写（由调用方经 `desktop/internal/docfs` 完成）

**Called by:** `desktop/ui/shell`

**Depends on:** `github.com/egoist/mygo/ui`, `github.com/egoist/mygo/ui/editor`（Tokenizer 与语法配色）, `github.com/egoist/mygo/ui/markdown/parser`, `golang.org/x/image/draw`, `desktop/ui/icons`

**Change this when:**
- 调整原生语义编辑的投影、输入、布局或缓存策略

**Do not change this when:**
- 修改外壳布局、菜单或文件操作（在 `desktop/ui/shell`）

**Related tests:** `desktop/ui/mdedit/*_test.go`（`NOMO_PERF=1` 运行延迟测量，`NOMO_SNAPSHOT=<png>` 输出首屏截图）

**Confidence:** high

---

### `desktop/main.go`

**Kind:** entry

**Owns：**
- 定位应用数据目录、初始化日志、设置隐藏 WebView 的 WebView2 浏览器参数（Windows）
- 单实例锁、加载配置，交给 `runNativeShell`（`desktop/native.go`）

**Does not own：**
- 不拥有界面（`desktop/ui/shell`）、托盘与外部打开投递（`native.go`、`tray.go`）

**Called by:** —

**Depends on:** `desktop/app.go`, `desktop/native.go`, `desktop/internal/config/`, `desktop/internal/applog/`

**Change this when：**
- 修改启动顺序、单实例或 WebView2 参数

**Related tests:** `desktop/main_test.go`

**Confidence:** high

---

### `desktop/internal/config/config.go`

**Kind:** model

**Owns：**
- 跨端序列化数据结构：DocumentPayload、RecentEntry、SnapshotRecord、SettingRecord、WindowStateInput、FileTreeEntry、ImageAssetPayload、DesktopActionPayload 等
- Markdown 文件编码枚举及其 IPC 序列化值
- 窗口事件通信 payload（如 `WindowLabelPayload`）

**Does not own：**
- 不拥有具体业务逻辑

**Called by:** 前后端各模块

**Depends on:** `serde`

**Change this when：**
- 新增/修改 IPC 参数或返回值结构
- 新增前后端通信数据结构

**Do not change this when：**
- 修改业务逻辑

**Related tests:** —

**Confidence:** high

---

### `desktop/internal/config/config.go`

**Kind:** service / data store

**Owns：**
- 应用配置 JSON 持久化：`config.json` 的读取、写入、备份
- `ConfigManager`：线程安全的配置管理器（`Arc<RwLock<AppConfig>>`）
- `AppConfig` 结构定义：app（设置）、editor（编辑器设置）、window（窗口状态）、recent（最近打开）、workspace（工作区标签元数据）、snapshots（文档快照索引）
- 草稿/快照正文仓库路径规则，以及旧 snapshot 正文迁移到独立内容文件
- 设置键值存储：原生外壳偏好的读写
- 设置路由：按 key 前缀将设置分发到对应 section（`windowState:`、`workspaceTabs:`、`pendingFolder:` 等）
- 启动前设置读取：在 `AppHandle` 可用前从磁盘读取配置（渲染模式、开发者模式）

**Does not own：**
- 不拥有偏好模型与归一化（在 `desktop/ui/shell/prefs.go` 中）

**Called by:** `desktop/main.go`（setup 中初始化）, 启动流程

**Depends on:** `desktop/internal/config/config.go`, `desktop/internal/applog/applog.go`, MyGo path API

**Change this when：**
- 新增/修改 AppConfig section 或字段
- 新增启动前需要读取的设置项
- 修改配置备份/恢复逻辑
- 修改草稿/快照正文文件目录或旧配置迁移规则
- 修改设置路由规则

**Do not change this when：**
- 修改具体业务逻辑

**Related tests:** `desktop/internal/config/config.go` 模块内测试

**Confidence:** high

---

### `desktop/internal/docfs/docfs.go`

**Kind:** service

**Owns：**
- 后端文件系统：读写 Markdown、创建/重命名/删除文件夹和文件
- Markdown 的 UTF-8/BOM、UTF-16 BOM、GBK 解码与按源编码安全写回
- 使用短生命周期目录快照按需枚举根目录与已加载子目录
- 路径存在性检查
- 示例文档安装
- Markdown-like 文件过滤、忽略规则、`.gitignore`、目录优先排序

**Does not own：**
- 不拥有图片资源处理（在 file_system/image_assets.rs 中）

**Called by:** `desktop/main.go`（注册为 IPC）, `desktop/ui/shell`

**Depends on:** `desktop/internal/config/config.go`, `encoding_rs`

**Change this when：**
- 修改文件读写逻辑
- 修改目录扫描规则
- 修改文件过滤/忽略规则

**Do not change this when：**

**Related tests:** `desktop/internal/docfs/docfs.go` 内联测试

**Confidence:** high

---

### `desktop/tray.go`

**Kind:** service

**Owns：**
- 托盘与 macOS Dock 图标随应用深浅色替换（`setDesktopIconTheme`）、退出前删除托盘（`destroyAppTray`）
- 主程序文案的语言（`effectiveLocale`、`tr`）

**Does not own：**
- 不拥有托盘的创建与菜单（`desktop/native.go` 的 `installNativeTray`）、关闭到托盘行为（`desktop/ui/shell/tray.go`）

**Called by:** `desktop/native.go`

**Depends on:** `desktop/internal/i18n/i18n.go`

**Change this when：**
- 修改托盘或 Dock 图标资源、深浅色切换

**Related tests:** —

**Confidence:** high

---

### `desktop/external_open.go`

**Kind:** helper

**Owns：**
- 从启动参数与第二实例参数中识别受支持的文档与文件夹（去重、忽略选项与可执行文件自身、规范化路径）

**Does not own：**
- 不拥有投递与打开（`desktop/native.go` 的 `deliver`、`desktop/ui/shell` 的 `OpenExternal`）

**Called by:** `desktop/native.go`

**Depends on:** `desktop/internal/docfs/`

**Change this when：**
- 修改启动参数解析逻辑

**Related tests:** —

**Confidence:** high

---

### `desktop/export.go`

**Kind:** service

**Owns：**
- PDF 导出：隐藏窗口加载原生外壳生成的导出 HTML，等排版运行时、字体与图片就绪后原生打印；校验后写入书签并原子写盘

**Does not own：**
- 不拥有导出 HTML 的生成（`desktop/ui/shell/export.go`）、书签解析（`desktop/pdf_outline.go`）

**Called by:** `desktop/native.go`（`exportNativePDF`）

**Depends on:** `desktop/pdf_outline.go`, `desktop/internal/docfs/`, `desktop/internal/applog/`

**Change this when：**
- 修改打印纸张、边距或就绪检查

**Related tests:** —

**Confidence:** high

---


### `desktop/internal/applog/applog.go`

**Kind:** service

**Owns:**
- 后端日志系统：文件落盘（`./logs/`）、5MB 轮转、终端输出
- 日志开关 IPC（`set_logger_enabled`、`get_logger_enabled`、`log_message`）

**Called by:** `desktop/main.go`（注册为 IPC）, 后端各模块（通过 `info`/`debug`/`error` 等）

**Depends on:** `chrono`, `desktop/internal/config/config.go`

**Change this when:**
- 修改日志文件策略
- 修改日志轮转规则

**Related tests:** —

**Confidence:** high

---

## 文件与目录速查

| 目录 | 用途 |
|---|---|
| `desktop/` | Go 桌面程序入口：启动、托盘、外部打开、PDF 导出、更新检查（`main.go`、`native.go`） |
| `desktop/ui/shell/` | 原生界面（MyGo UI）：窗口、标签、工具栏、设置、导出、更新 |
| `desktop/ui/mdedit/` | 原生语义编辑器 |
| `desktop/ui/typography/` | 精细排版（Knuth–Plass）纯计算 |
| `desktop/internal/` | 配置、文件读写、日志、本地化、表情目录、图片资源、内存归还 |

---

## 置信度与漂移标记

- **high**: 基于代码扫描确认的职责划分。
- `desktop/internal/docfs/docfs.go` 未来拆分后需要更新条目。
- `desktop/internal/config/config.go` 承担了原 SQLite 数据库的全部职责（最近打开、快照、设置），未来若拆分需更新条目。


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

### 前端 Markdown 库（Word 导出与离屏渲染页使用）

界面已全部原生化；`src/` 只剩离屏渲染页（`render.html` → `src/render/render-entry.ts`：KaTeX、Mermaid、Word 导出）与其依赖，以及生成脚本读取的文案、主题与导出样式源文件。下列模块中提到的编辑器交互属于已移除的 WebView 界面，仅作历史说明。

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| 精细段落排版 | `src/lib/typography/knuthPlass.ts` | `types.ts`, `rules.ts`, `hyphenation.ts`, `measure.ts`, `renderPlan.ts`, `positions.ts`, `solver.ts`, `typography.worker.ts` | 修改 KP 间距质量评分、两端/左对齐选择、中文规则、英文 Liang 断词、可取消分批/有界同步测量、目标几何快照、文字测量 LRU 或位置映射 |
| 只读与离线排版 | `src/lib/typography/dom.ts` | `renderPlan.ts`, `selectionPaint.ts`, `exportRuntime.ts`, `exportPrint.ts`, `dom.test.ts`, `scripts/typography-runtime-plugin.ts` | 修改只读布局、图片克隆的加载失效、字距与连续选区背景、离线运行时或打印就绪契约 |
| Schema 定义 | `src/lib/editor-core/schema.ts` | `src/lib/editor-core/callout/calloutSchema.ts` | 新增/修改节点或 mark 类型 |
| Markdown 解析与序列化 | `src/lib/editor-core/markdown.ts` | `markdownTokenizer.ts`, `markdownSerialization.ts`, `src/lib/editor-core/callout/`, `html/` | Markdown 与 ProseMirror doc 互转规则变更；Word 可选解析临时放行图片来源，默认编辑与链接校验保持不变；纯 tokenizer 与序列化可供 Worker 复用 |
| Markdown 源码行到语义块导航 | `src/lib/editor-core/markdown.ts`,  | — | 诊断、大纲等功能需要从源码行定位到最近的语义顶层块 |
| HTML 安全策略 | `src/lib/editor-core/html/htmlPolicy.ts` | `src/lib/editor-core/html/htmlClassifier.ts` | 可编辑 HTML 标签/属性白名单变更 |
| HTML 块分类 | `src/lib/editor-core/html/htmlClassifier.ts` | `src/lib/editor-core/html/htmlPolicy.ts` | HTML 块可编辑性判断/属性提取规则变更 |
| HTML 锚点保留 | `src/lib/editor-core/html/htmlAnchor.ts` | `schema.ts`, `markdown.ts`, `html/htmlToPmLogic.ts`, `html/pmToHtml.ts`, `markdownSerialization.ts` | 空锚点节点、含文本锚点 mark、`id/name` 属性及安全 HTML 输出规则变更 |
| 列表创建、层级与类型切换 | `src/lib/editor-core/listCommands.ts` | `ProseMirrorEditorCore.ts`, `markdown.ts`, `editorCommands.ts` | 语义模式 Tab/Shift+Tab、列表符号输入与粘贴规则、选中项类型转换和任务列表退出清理；各入口共用局部事务 |
| 链接安全与规范化 | `src/lib/editor-core/link.ts` | — | 链接协议白名单/序列化规则变更 |

### 原生语义编辑器原型（Go，P0）

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| 原文、块索引、选区与撤销 | `desktop/ui/mdedit/document.go` | `edit.go`, `history.go`, `text.go` | 修改增量块索引、UTF-16/字节换算、组字与撤销合并规则 |
| 分段解析（大文档按键成本） | `desktop/ui/mdedit/chunks.go` | `document.go`（`recollect` 增量块收集） | 修改分段阈值、切分安全条件、编辑后的合并与重新切分 |
| 正文行高（行距排版） | `desktop/ui/mdedit/lines.go` | `view.go`（`ensureRow`）, `blocks.go`（`rowLead`）, `draw.go` | 修改行距规则、加行距后的命中测试、选区与逐行绘制 |
| 折叠块（details） | `desktop/ui/mdedit/details.go`（识别与配对）、`detailsview.go`（视图与命令） | `blocks.go`（标记行）、`htmlpreview.go`（`previewDetails`）、`desktop/ui/shell/editormenu.go` | 修改折叠标记语法、配对与增量平移、卡片绘制、折叠隐藏与自动展开、插入/默认展开/取消折叠 |
| 编辑区选项（代码块行号、Tab 缩进、行内代码、复制语法、默认插入） | `desktop/ui/mdedit/options.go` | `desktop/ui/shell/editoroptions.go`（设置到选项、图表模板、Ctrl 滚轮） | 修改原版 editor.updateOptions 对应的显示与插入行为 |
| 精细排版（Knuth–Plass 断行，纯计算） | `desktop/ui/typography`（`knuthplass.go`、`rules.go`、`hyphenation.go`） | `scripts/generate-native-hyphenation.mjs`（生成 `hyphen_en_us_gen.go`）、`testdata/crosscheck.json`（原版依赖对照数据） | 修改 KP 求解、标点禁则/压缩/悬挂、中西文空白、英文断词；移植自 `src/lib/typography` |
| 精细排版接入编辑区 | `desktop/ui/mdedit/kp.go`（测量、规则输入、逐行排版）、`kplines.go`（多行布局映射） | `view.go`（`ensureRow`）、`lines.go`（`adjacentY`）、`desktop/ui/shell/export.go`（`withTypography`） | 修改精排适用范围、测量与绘制、导出时内联原版运行时 |
| 单行修补（大表格/长代码块按键） | `desktop/ui/mdedit/patch.go` | `view.go`（`applyEdit`）、`document.go`（`checkpoints`） | 修改块内编辑的增量路径与 UTF-16 检查点 |
| 原文到显示的投影 | `desktop/ui/mdedit/projection.go` | `view.go` | 修改行内标记就地展开/隐藏、行内 HTML 标签样式、实体解码、提示块标记、显示与原文偏移映射 |
| 块组件（列表、任务、引用、提示块、表格、代码块、分割线、front matter） | `desktop/ui/mdedit/blocks.go` | `draw.go`, `view.go`, `document.go` | 修改块展开为行的规则、容器前缀、缩进与间距、装饰范围 |
| 可见区布局与缓存 | `desktop/ui/mdedit/view.go` | `blocks.go`, `input.go` | 修改行虚拟化、阅读锚点、布局缓存预算与淘汰、命中测试、主题配色 |
| 绘制 | `desktop/ui/mdedit/draw.go` | `view.go` | 修改装饰（引用条、提示块标题、代码卡片、表格网格）、列表符号与任务框绘制 |
| 代码块语法高亮 | `desktop/ui/mdedit/highlight.go` | `view.go`, `blocks.go` | 修改代码块着色、Tokenizer 跨行状态缓存 |
| 语义模式图片（行内对象、加载缓存） | `desktop/ui/mdedit/imageview.go` | `images.go`, `projection.go`, `desktop/ui/shell/appearance.go` | 修改图片显示尺寸、点击展开、只含图片的 HTML 块、图片缓存预算与淘汰、后台加载、GIF 动画 |
| 表格列宽与横向滚动 | `desktop/ui/mdedit/tablelayout.go` | `view.go`（`rowBox`）, `draw.go`, `input.go` | 修改列宽策略（原版 widths.ts）、超宽表格横向滚动、单元格裁剪 |
| HTML 块预览、注释卡片、目录卡片 | `desktop/ui/mdedit/htmlpreview.go` | `blocks.go`, `document.go`（`KindToc`）, `imageview.go` | 修改可预览的 HTML 白名单与排版、注释/目录卡片样式、目录点击跳转 |
| 公式与 Mermaid 渲染 | `desktop/ui/shell/render.go` | `render.html`, `src/render/render-entry.ts`, `desktop/ui/mdedit/images.go`（`RenderFunc`、`rasterizeSVG`）, `renderdump.go`（诊断转储） | 修改离屏渲染窗口的生命周期、截图裁剪、渲染配色、行内公式基线、含文字 SVG 的栅格化；`NOMO_RENDER_DUMP=<目录>` 开启每批截图与测量的转储 |
| 双栏模式 | `desktop/ui/shell/split.go` | `tab.go`（双向同步）, `desktop/ui/mdedit/api.go`（`TopSourceOffset`、`ScrollToSource`） | 修改双栏布局、比例保存、活动一侧与滚动同步 |
| 组合标签 | `desktop/ui/shell/compare.go` | `view.go`（标签栏、右键菜单）, `tab.go`（`partner`） | 修改组合/解散/交换、并排显示与活动文档 |
| 多窗口、托盘、小窗、标签拖动排序 | `desktop/ui/shell/windows.go`, `tray.go`, `mini.go`, `view.go`（`tabDragTarget`） | `desktop/native.go`（`installNativeTray`） | 修改窗口注册与菜单分发、关闭到托盘、Markdown 小窗、标签栏内拖动排序 |
| Word 导出 | `desktop/ui/shell/export.go`（`exportWord`） | `render.go`（`word`）, `src/render/render-entry.ts`（复用 `src/app/services/word`） | 修改 Word 导出流程与本地图片内联 |
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
| 导出 HTML / PDF | `desktop/ui/shell/export.go` | `desktop/ui/themes/export.go`, `desktop/native.go`（`exportNativePDF`）, `desktop/export.go` | 修改导出文档结构、图片内联、PDF 书签标记 |
| 界面文案与图标（生成） | `scripts/generate-native-messages.mjs`, `scripts/generate-native-icons.mjs` | `desktop/ui/shell/messages_gen.go`, `desktop/ui/icons/icons_gen.go`, `i18n.go` | 前端文案、检查规则标题或所用图标变化后重新生成 |
| 应用与界面图标 | `desktop/ui/icons/icons.go` | `desktop/appicon.go`, `desktop/native.go` | 修改文件类型图标、提示块图标、应用图标（窗口 / Dock / 关于页） |
| 内置主题、导出样式与 Quick Look 主题表（生成） | `scripts/generate-native-themes.mjs` | `desktop/ui/themes/builtin_gen.go`, `export_document.css`, `desktop/packaging/macos/NomoQuickLookPreview/BuiltinThemes.swift` | 前端主题注册表或导出样式变化后重新生成 |
| 主题解析、校验与导出 | `desktop/ui/themes/themes.go` | `export.go` | 修改用户主题 JSON 规则（与 userThemeDefinition.ts 一致）、颜色解析、导出 CSS 变量 |
| Markdown 检查规则（markdownlint 子集） | `desktop/ui/mdlint/mdlint.go` | `mdlint_test.go`（`MDLINT_DUMP` 与 markdownlint 对照） | 新增规则或调整宽松规则集 |
| 图片资源导入与图床上传 | `desktop/internal/imageasset/imageasset.go` | — | 修改资源目录策略、同内容复用、PicGo 上传与连接测试 |
| 保存前快照 | `desktop/internal/config/snapshot.go` | `desktop/ui/shell/shell.go` | 修改快照存储与保留数量 |

### 编辑器插件

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| 八类行内原文与投影 | `src/lib/editor-core/InlineSourceCodec.ts`, `inlineSourceCommands.ts` | `markdownTokenizer.ts`, `markdown.ts`, `markdownSerialization.ts`, `schema.ts`, `ProseMirrorEditorCore.ts` | 原文/格式与位置映射、上下标语法与投影、代码字面粘贴和清除、外层格式组合、HTML 导入与输出语义 |
| 表格自动列宽与局部滚动 | `src/lib/table-layout/widths.ts`, `src/lib/table-layout/dom.ts` | `src/lib/table-layout/export.ts`, `plugins/tableControls.ts`, `plugins/typography.ts` | 语义内容度量、合并格约束、按需列宽、视图级调度与缓存、精细排版失效、HTML/PDF 目标宽度重排；自动宽度不写入 Markdown |
| Emoji 搜索与联网缓存 | `desktop/internal/emoji`（ 注册命令） | `src/app/i18n.emoji.ts` | 固定版本元数据、双语搜索索引（Go 版 `catalog.go` 供原生外壳，规则与前端一致）、三源错峰获取、SHA-256 校验、4 MiB 原子缓存与共享加载 |
| 连续行选区绘制 | `src/lib/typography/selectionPaint.ts` | `dom.ts` | 合并可见行矩形、按实际行内内容限制背景宽度，保持首帧行高与像素对齐；选区折叠、转移或原生输入聚焦时同步清理本编辑区补色，释放监听与共享 CSSOM；不改正文内容或导出快照 |

### 文件系统与文档操作

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| 关闭大文档后归还内存 | `desktop/internal/memtrim` | `desktop/ui/shell/shell.go`（`removeTab`） | 修改主动归还的阈值与延时 |
| 渲染服务类型接口 | `src/lib/services/render.ts` | `katexMathRenderer.ts`, `mermaidDiagramRenderer.ts` | ImageLoader、CodeTokenizer、MathRenderer、DiagramRenderer 接口变更 |
| Go 文件系统 | `desktop/internal/docfs/docfs.go` | `desktop/internal/config/config.go` | 后端文件读写、Markdown 编码转换/保持、短句柄目录快照与按需枚举 |

### 大纲与导航

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| 大纲服务 | `src/lib/outline/outlineService.ts` | `src/lib/outline/writingStats.ts`, `src/lib/editor-core/InlineSourceCodec.ts` | 标题语义内容与 slug、八类行内符号排除、大纲与同步统计兼容入口 |
| 正文统计与范围索引 | `src/lib/outline/writingStats.ts` | `sourceTokenPositions.ts`, `writingStatsEngine.ts`, `writingStatsProtocol.ts` | 中英计数、可见字数、源码/语义选区上下文和索引查询 |
| TOC 服务 | `src/lib/toc/tocService.ts` | `src/lib/outline/outlineService.ts` | 生成 TOC Markdown/目录项数据，标题字面符号转义与统一跳转 |

### 渲染服务

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| 图表渲染 | `src/lib/services/mermaidDiagramRenderer.ts` | — | Mermaid 图表渲染 |
| 增强渲染调度与可见性 | `src/lib/services/renderTaskQueue.ts`,  | 代码块与 Mermaid NodeView、图表渲染器 | 有界并发、排队取消、视口及后台暂停、结果提交阅读锚点；导出主动补齐 |

### 设置与偏好

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| 主题公共契约 | `src/lib/theme/types.ts` | `src/app/services/themeRegistry.ts`, 编辑器渲染器 | 颜色/样式令牌、样式档案、文档样式或渲染配置契约变更 |
| 主题注册表 | `src/app/services/themeRegistry.ts` | `src/lib/theme/types.ts` | 内置配色、样式档案、文档样式、令牌映射或主题校验变更 |
| Go 配置管理 | `desktop/internal/config/config.go` | `desktop/internal/config/config.go` | 应用配置 JSON 持久化、设置读写、启动前读取 |
| 用户主题文件存储 | `desktop/internal/config/themes.go` | `desktop/ui/shell/appearance.go` | `themes/<id>.json` 列出/保存/删除、外部主题读取与模板写出、ID/大小/数量上限 |

### 日志与导出

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| 后端日志系统 | `desktop/internal/applog/applog.go` | — | 日志文件落盘/轮转/终端输出 |
| PDF 导出（隐藏 WebView 原生打印、文档书签） | `desktop/export.go` | `desktop/pdf_outline.go`, `desktop/ui/shell/export.go`, `src/lib/typography/exportRuntime.ts` | 修改打印纸张与边距、排版就绪检查、书签生成 |

### 导出

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| 共用正文样式与打印准备 | `src/lib/typography/exportPrint.ts` | `editor-document.css`, `export-document.css`, `src/lib/typography/exportRuntime.ts` | 编辑器与导出共用展示样式（含内嵌徽章及链接布局）、目标版心、跨页分类、安全表头提升及媒体尺寸约束 |

### 本地化

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| 前端本地化 | `src/app/i18n.ts` | `src/app/i18n.ja.ts`, `src/paraglide/` | 界面文案/语言切换 |
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
| 软件更新检查与安装形态 | `desktop/update_check.go` | `desktop/installation_windows.go`, `desktop/installation_other.go` | 修改 GitHub Release 检查、gh-proxy 回退与 Windows 安装形态判断 |

### 发布与打包

| Responsibility | Primary code | Related code | Change when |
|---|---|---|---|
| macOS 构建、Quick Look 嵌入与 DMG | `scripts/build-macos.sh` | `scripts/build-macos-quicklook.sh`, `scripts/verify-macos-bundle.sh`, `desktop/packaging/macos/NomoQuickLookPreview/`, `mygo.config.ts` | 修改 MyGo 产物后处理、扩展架构/版本同步、由内向外签名（扩展保留沙盒 entitlements，主应用不带 `--deep`）或 DMG 生成 |
| Linux 本地构建与安装 | `scripts/build-linux.sh` | `mygo.config.ts` | 修改 DEB/RPM 构建、产物定位或安装；DEB 优先通过 apt-get 解析依赖，失败时保留错误 |
| SVG 图标与平台资源生成 | `scripts/generate-icons.mjs` | `desktop/packaging/icons/nomo/source/nomo-app-light.svg`, `scripts/compile-macos-appicon.sh`, `desktop/tray.go` | 修改应用 Logo、浅深色配色、平台边距、多尺寸位图或托盘图标；浅色 SVG 为造型母版，桌面与系统文件图标单独派生描边，应用内 Logo 和移动端保持母版造型 |
| Windows Store MSIX 构建与校验 | `scripts/msix/Build-Msix.ps1` | `scripts/msix/Build-IconResources.ps1`, `.github/workflows/msix.yml`, `desktop/packaging/msix/`, `mygo.config.ts` | 修改 Store 包身份、版本映射、生产前端嵌入校验、图标 PRI 生成、Shell 扩展封装或上传产物 |

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
- 公式与 Mermaid：作为渲染对象，经外壳注入的 `RenderFunc` 生成位图，结果进入同一缓存
- HTML 块静态预览（白名单与原版 sanitizeHtml 一致，含合并单元格表格）、注释卡片、目录卡片（`KindToc` 合成块）、脚注引用标签与跳转
- 表格列宽（原版 widths.ts 策略）与超宽表格横向滚动；超长行分段排版

**Does not own:**
- 公式与图表的实际渲染（外壳 `render.go` 的离屏 WebView）
- 精细排版与行高（P0 第 3 项）
- 文件读写（由调用方经 `desktop/internal/docfs` 完成）

**Called by:** `desktop/ui/shell`

**Depends on:** `github.com/egoist/mygo/ui`, `github.com/egoist/mygo/ui/editor`（Tokenizer 与语法配色）, `github.com/egoist/mygo/ui/markdown/parser`, `golang.org/x/image/draw`, `desktop/ui/icons`

**Change this when:**
- 调整原生语义编辑的投影、输入、布局或缓存策略

**Do not change this when:**
- 修改现版本 ProseMirror 编辑器行为

**Related tests:** `desktop/ui/mdedit/*_test.go`（`NOMO_PERF=1` 运行延迟测量，`NOMO_SNAPSHOT=<png>` 输出首屏截图）

**Confidence:** high

---

### `src/lib/editor-core/scrollSyncMapping.ts`

**Kind:** parser instrumentation

**Owns:** 包装同一 MarkdownParser 的实际 token handler，记录真正创建的节点及嵌套源码来源，生成块边界和代码逻辑行锚点；定义只属于当前修订的只读同步数据。

**Does not own:** 不更改解析规则、持久化节点属性或历史，不测量 DOM。

**Called by:** `src/lib/editor-core/markdown.ts`

**Related tests:** —

**Confidence:** high

---

### `src/lib/theme/types.ts`

**Kind:** public contract

**Owns:**
- 明暗模式、有效配色、外观偏好、颜色令牌、主题变体和文档样式的公共类型
- Shiki 与 Mermaid 的主题渲染配置契约

**Does not own:**
- 不拥有具体主题色值或注册逻辑
- 不拥有主题运行时应用与持久化

**Called by:** 主题注册表、主题管理器、设置模型、EditorCore 和代码/图表渲染器

**Change this when:** 修改 v1 主题契约、必填令牌或渲染器主题配置

**Confidence:** high

---

### `src/app/services/themeRegistry.ts`

**Kind:** registry

**Owns:**
- `nomo-default`、`nomo-amber-paper`、`nomo-classic-gray`、`nomo-github` 的亮暗变体、样式档案和预览
- `nomo-classic`、`nomo-modern` 文档样式稳定 ID
- 必填颜色/样式令牌到 CSS 变量的映射与主题完整性校验
- 内置与用户主题的区分：用户主题可替换、注销，内置主题不可

**Does not own:**
- 不拥有用户当前选择或 DOM 应用
- 不拥有配置持久化

**Called by:** —

**Depends on:** `src/lib/theme/types.ts`

**Change this when:** 新增内置主题、调整主题令牌、文档样式或注册校验

**Related tests:** `src/app/services/themeRegistry.test.ts`

**Confidence:** high

---

### `src/lib/editor-core/markdown.ts`

**Kind:** service

**Owns：**
- 基于 `markdown-it` 和 `prosemirror-markdown` 的 Markdown 解析
- ProseMirror doc 序列化回 Markdown
- 表格文本的反斜杠保留与行内代码围栏，避免序列化丢字导致后续同步锚点偏移
- 合并格、无表头等保真表格 HTML 的完整校验与 schema 表格恢复；未知或不合法表格 HTML 保留原文
- 顶层 Front Matter 文档属性与正文的解析、序列化
- 表格、图片扩展属性、公式、脚注、HTML、Callout、TOC、Mermaid、注释等语法处理

**Does not own：**
- 不拥有基础 markdown-it 规则（依赖库提供）
- 不拥有 Callout 具体解析规则（在 callout/calloutParser.ts 中）
- 不拥有 HTML 转换逻辑（在 html/ 目录中）

**Called by:** —

**Depends on:** `src/lib/editor-core/schema.ts`, `src/lib/editor-core/callout/calloutParser.ts`, `src/lib/editor-core/callout/calloutSerializer.ts`, `src/lib/editor-core/html/htmlToPmLogic.ts`, `src/lib/editor-core/html/pmToHtml.ts`

**Change this when：**
- 修改 Markdown 解析规则
- 修改 Markdown 序列化输出格式
- 新增 Markdown 语法支持

**Do not change this when：**
- 修改编辑器 UI 行为
- 修改渲染样式

**Related tests:** `src/lib/editor-core/markdown.test.ts`

**Confidence:** high

---

### `src/lib/editor-core/schema.ts`

**Kind:** model

**Owns：**
- ProseMirror Schema 定义
- 扩展 Markdown 基础 schema：图片属性、表格、公式、HTML、注释、脚注、TOC、Mermaid、Callout 等节点/mark

**Does not own：**
- 不拥有解析/序列化规则（在 markdown.ts 中）
- 不拥有 Callout schema 细节（在 callout/calloutSchema.ts 中）

**Called by:** `src/lib/editor-core/markdown.ts`, nodeViews, plugins

**Depends on:** `src/lib/editor-core/callout/calloutSchema.ts`

**Change this when：**
- 新增/修改节点或 mark 类型
- 修改文档结构约束

**Do not change this when：**
- 修改解析规则
- 修改 UI 展示

**Related tests:** `src/lib/editor-core/*.test.ts`（涉及 schema 的测试）

**Confidence:** high

---

### `src/lib/services/mermaidDiagramRenderer.ts`

**Kind:** service

**Owns：**
- 调用 Mermaid 渲染图表
- 为 Mermaid NodeView 提供 SVG
- 按当前主题传入 Mermaid `theme` 与 `themeVariables`；初始化和渲染串行调度，允许取消过期的排队任务
- 首次加载 Mermaid 时注册本地 `logos` 图标包，按需加载品牌图标供主编辑器与 Quick Look 离线复用

**Does not own：**
- 不拥有图表代码编辑交互（在 MermaidBlockNodeView.ts 中）

**Called by:** —

**Depends on:** `mermaid`, `@iconify-json/logos`

**Change this when：**
- 修改 Mermaid 配置
- 修改图表渲染错误处理

**Do not change this when：**
- 修改图表插入流程

**Related tests:** —

**Confidence:** high

---

### `src/lib/outline/outlineService.ts`

**Kind:** service

**Owns：**
- 复用 `tocService.ts` 的真实 Markdown 标题集合、来源行号与统一 ID，提供大纲兼容入口
- 同步统计兼容入口；词数、字数与范围查询由 `writingStats.ts` 负责

**Does not own：**
- 不拥有大纲 UI 展示（在 EditorWorkspace.svelte 中）
- 不拥有大纲交互（在 outlineInteractionController.ts 中）

**Called by:** —

**Depends on:** `src/lib/outline/writingStats.ts`, `src/lib/toc/tocService.ts`

**Change this when：**
- 修改大纲提取算法
- 修改同步统计兼容入口（计数规则在 writingStats.ts）

**Do not change this when：**
- 修改大纲面板 UI

**Related tests:** `src/lib/outline/outlineService.test.ts`

**Confidence:** high

---

### `src/lib/toc/tocService.ts`

**Kind:** service

**Owns：**
- 生成 TOC Markdown 块
- 基于真实 Markdown 纯预处理与 token 生成目录项及来源行号，统一大纲与文档标题 ID
- 提供标题归一化与全局唯一 ID 分配，DOM 标题属性和目录导航复用文档位置映射

**Does not own：**
- 不拥有 TOC 块展示（在 TocBlockNodeView.ts 中）

**Called by:** `src/lib/outline/outlineService.ts`

**Depends on:** `src/lib/editor-core/markdownTokenizer.ts`, `src/lib/editor-core/markdownSerialization.ts`, `src/lib/editor-core/InlineSourceCodec.ts`, `src/lib/editor-core/markdown.ts`（纯 tokenizer 与来源行号 API）

**Change this when：**
- 修改 TOC 生成逻辑
- 修改目录项数据结构

**Do not change this when：**
- 修改 TOC 块渲染样式

**Related tests:** `src/lib/toc/tocService.test.ts`

**Confidence:** high

---

### `src/lib/markdown/frontMatter.ts`

**Kind:** utility

**Owns：**
- 识别 Markdown 开头的 YAML front matter
- 拆出元数据块和正文

**Does not own：**
- 不拥有 front matter UI（在 FrontMatterCard.svelte 中）

**Called by:** 编辑器/文档流程

**Depends on:** —

**Change this when：**
- 修改 front matter 解析规则
- 修改 YAML 边界检测

**Do not change this when：**
- 修改 front matter 展示 UI

**Related tests:** `src/lib/markdown/frontMatter.test.ts`

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
- 设置键值存储：为前端偏好设置提供后端读写
- 设置路由：按 key 前缀将设置分发到对应 section（`windowState:`、`workspaceTabs:`、`pendingFolder:` 等）
- 启动前设置读取：在 `AppHandle` 可用前从磁盘读取配置（渲染模式、开发者模式）

**Does not own：**
- 不拥有前端设置模型与归一化（在 `settings.ts` 中）

**Called by:** `desktop/main.go`（setup 中初始化）, 启动流程

**Depends on:** `desktop/internal/config/config.go`, `desktop/internal/applog/applog.go`, MyGo path API

**Change this when：**
- 新增/修改 AppConfig section 或字段
- 新增启动前需要读取的设置项
- 修改配置备份/恢复逻辑
- 修改草稿/快照正文文件目录或旧配置迁移规则
- 修改设置路由规则

**Do not change this when：**
- 修改前端设置 UI
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

**Called by:** `desktop/main.go`（注册为 IPC）, 前端 `documentFiles.ts`

**Depends on:** `desktop/internal/config/config.go`, `encoding_rs`

**Change this when：**
- 修改文件读写逻辑
- 修改目录扫描规则
- 修改文件过滤/忽略规则

**Do not change this when：**
- 修改前端文件操作封装

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

### `src/lib/editor-core/link.ts`

**Kind:** utility

**Owns:**
- 超链接安全校验与规范化：拒绝 `javascript:` 等脚本协议，允许 `https?`/`mailto`/相对路径
- 链接属性创建与序列化

**Does not own:**
- 不拥有链接交互 UI（在 linkInteraction.ts 和 LinkQuickEditor.svelte 中）

**Called by:** `src/lib/editor-core/markdown.ts`

**Depends on:** —

**Change this when:**
- 修改链接协议白名单
- 修改链接序列化规则

**Related tests:** —

**Confidence:** high

---

### `src/lib/editor-core/html/htmlPolicy.ts`

**Kind:** model

**Owns:**
- HTML 安全白名单：可编辑块级标签（section/div）、内联标签、允许属性
- 危险标签集合（script/iframe/form 等）
- 内联标签到 ProseMirror mark 的映射表

**Does not own:**
- 不拥有 HTML 分类逻辑（在 htmlClassifier.ts 中）

**Called by:** `src/lib/editor-core/html/htmlClassifier.ts`, `src/lib/editor-core/html/htmlToPmLogic.ts`

**Depends on:** —

**Change this when:**
- 新增可编辑 HTML 标签
- 修改安全白名单

**Related tests:** —

**Confidence:** high

---

### `src/lib/editor-core/html/htmlClassifier.ts`

**Kind:** utility

**Owns:**
- 无 DOM 的 HTML 块分类：仅单根及可保真的行内结构可编辑；其它结构/属性保留原文 fallback
- 按实际属性边界提取、实体解码与安全协议校验
- 与 HTML 导入共用现有行内原子节点的属性识别（图片、换行、数学、注释、脚注）

**Does not own:**
- 不拥有标签/mark 映射白名单（在 htmlPolicy.ts 中）
- 不拥有 DOM 导入与 HTML 输出（在 htmlToPmLogic.ts、pmToHtml.ts 中）

**Called by:** `src/lib/editor-core/markdown.ts`、`src/lib/editor-core/html/htmlToPmLogic.ts`、`src/lib/outline/writingStats.ts`

**Depends on:** `htmlPolicy.ts`、`../markdownTokenizer.ts`、`htmlAnchor.ts`、`../link.ts`

**Change this when:**
- 修改 HTML 块可编辑性判断规则
- 修改属性提取逻辑

**Related tests:** `src/lib/editor-core/html/__tests__/htmlClassifier.test.ts`

**Confidence:** high

---

### `src/lib/editor-core/callout/calloutTypes.ts`

**Kind:** model

**Owns:**
- Callout 类型定义：5 种固定类型（note/tip/important/warning/caution）
- 类型配置表（图标、默认标题、颜色后缀）
- 多语言标签映射（zh-CN/zh-TW/en-US）

**Does not own:**
- 不拥有 Callout 解析/序列化（在 calloutParser.ts/calloutSerializer.ts 中）

**Called by:** `src/lib/editor-core/callout/calloutSchema.ts`

**Depends on:** —

**Change this when:**
- 新增 Callout 类型
- 修改类型配置或标签

**Related tests:** —

**Confidence:** high

---

### `src/lib/services/render.ts`

**Kind:** model

**Owns:**
- 渲染服务类型接口：`ImageLoader`、`CodeTokenizer`、`MathRenderer`、`DiagramRenderer`
- 图片处理设置类型：`ImageHandlingSettings`、`ImageInsertStrategy`、`ImageUploadProvider`
- `ImageContext`、`ImageResolveResult`、`ImageImportResult` 等

**Does not own:**
- 不拥有具体渲染实现（在 shikiCodeTokenizer.ts、katexMathRenderer.ts、mermaidDiagramRenderer.ts 中）

**Called by:** `src/lib/services/mermaidDiagramRenderer.ts`

**Depends on:** —

**Change this when:**
- 新增渲染服务类型
- 修改图片处理设置结构

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
- 前端日志转发接收

**Does not own:**
- 不拥有前端日志生成（在 logger.ts 中）

**Called by:** `desktop/main.go`（注册为 IPC）, 后端各模块（通过 `info`/`debug`/`error` 等）

**Depends on:** `chrono`, `desktop/internal/config/config.go`

**Change this when:**
- 修改日志文件策略
- 修改日志轮转规则

**Related tests:** —

**Confidence:** high

---

### `src/lib/outline/writingStats.ts`

**Kind:** pure statistics / index

**Owns:** 共享 tokenizer 的源码正文位置映射、ProseMirror 正文投影、中英文词边界和 Unicode 可见字符边界、累计范围查询。

**Does not own:** DOM、选区修改、文件 IO 或 Worker 生命周期。

**Related code:** `sourceTokenPositions.ts`, `writingStatsEngine.ts`, `writingStatsProtocol.ts`；后两者管理 Worker 内的单文档缓存与消息契约。

**Related tests:** —

**Confidence:** high

---

### `src/lib/editor-core/markdownTokenizer.ts` / `markdownSerialization.ts`

**Kind:** pure Markdown syntax / serialization

**Owns:** 可复用的 markdown-it 规则工厂、表格/Callout/目录 token 归一化、无 DOM 的 Markdown 与选区/剪贴板纯文本序列化；普通 GFM 表格及不能保真的合并格、无表头表格 HTML 保存。

**Called by:** `markdown.ts`、正文统计与统计 Worker；原模块继续重导出序列化 API。

**Statistics path:** 语义选区的字符数和行数复用序列化规则流式计量，避免生成完整选区字符串；剪贴板和保存仍使用原有字符串序列化入口。

**Related tests:** `src/lib/editor-core/markdown.test.ts`

**Confidence:** high

---

## 文件与目录速查

| 目录 | 用途 |
|---|---|
| `desktop/` | Go 桌面程序入口：启动、托盘、外部打开、PDF 导出、更新检查（`main.go`、`native.go`） |
| `desktop/ui/shell/` | 原生界面（MyGo UI）：窗口、标签、工具栏、设置、导出、离屏渲染调用 |
| `desktop/ui/mdedit/` | 原生语义编辑器 |
| `desktop/ui/typography/` | 精细排版（Knuth–Plass）纯计算 |
| `desktop/internal/` | 配置、文件读写、日志、本地化、表情目录、图片资源、内存归还 |
| `render.html`, `src/render/` | 离屏渲染页：KaTeX、Mermaid、Word 导出（原生界面以隐藏窗口加载） |
| `src/app/services/word/` | Word 导出模型与文档生成（离屏渲染页按需加载） |
| `src/lib/` | 离屏渲染页与 Word 导出依赖的 Markdown 解析、排版运行时、Mermaid 渲染等 |
| `src/app/i18n*.ts`, `project.inlang/` | 文案源文件（`scripts/generate-native-messages.mjs` 生成 `desktop/ui/shell/messages_gen.go`） |
| `src/app/services/themeRegistry.ts`, `src/app/styles/export-document.css` | 主题与导出样式源文件（`scripts/generate-native-themes.mjs` 生成 Go 侧主题与样式） |
| `src/paraglide/` | Inlang/Paraglide 生成的本地化代码（**不要手改**） |

---

## 置信度与漂移标记

- **high**: 基于代码扫描确认的职责划分。
- `desktop/internal/docfs/docfs.go` 未来拆分后需要更新条目。
- `desktop/internal/config/config.go` 承担了原 SQLite 数据库的全部职责（最近打开、快照、设置），未来若拆分需更新条目。
- `src/app/i18n.ts` 和 `src/app/i18n.ja.ts` 未单独建条目（已在 Feature Index 本地化节覆盖）。


# Go Native 重写计划

把 Nomo 的界面与编辑器从 WebView（Svelte + ProseMirror + CodeMirror）迁到 MyGo 原生 UI（`github.com/egoist/mygo/ui`，Go 编写、GPU 绘制）。目的是性能：去掉常驻 WebView 进程及其内存，降低启动、输入和标签切换延迟。本文是计划，未开始实施；文中工作量均为估计，性能收益在 P0 实测前均未验证。

依据版本：MyGo v0.6.2（`go.mod`）、本仓库 `mygo-rewrite` 分支（提交 `6784fd5`）。

## 1. 目标与非目标

**目标**

- 文档窗口不再常驻 WebView。WebView 只在导出、Mermaid 等少数任务中按需创建，任务结束即销毁。
- 功能对齐当前版本（见第 5 节清单），Markdown 文件、`config.json`、`themes/<id>.json`、工作区与快照格式保持兼容，用户升级无迁移操作。
- 性能指标以 P0 实测基线为准，在同场景下全面不劣于现版本，并在稳定内存、冷启动、输入延迟三项上有可量化改善（具体门槛在 P0 结束时定稿）。

**非目标**

- 不改变 Markdown-first：`.md` 原文始终是唯一保存格式。
- 不重写 Quick Look 扩展：它仍用 `src/quicklook` 的 Web 渲染器，只读取同一份配置。
- 不在本计划内新增产品功能；迁移期间发现的行为差异按“保持现状”处理，确需调整的单独评审。

## 2. 现状与可复用能力

### 2.1 现有前端规模

| 范围 | 行数（TS/Svelte/CSS，不含测试） |
|---|---:|
| `src/lib/editor-core`（ProseMirror 核心、NodeView、插件） | ~25,000 |
| `src/lib` 其余（排版、渲染服务、分段编辑、大纲、主题等） | ~12,400 |
| `src/app`（外壳组件、服务、样式） | ~53,000 |
| 合计 | ~90,000 |

Go 侧 `desktop/`（文件、分段文档引擎 `textdoc`、配置、窗口生命周期、外部打开、标签移交、导出、图片资源、更新）已在本分支完成，原生化后可直接调用，不再经过 IPC 绑定。

### 2.2 MyGo 原生 UI 能力对照

| 能力 | MyGo 提供 | 对 Nomo 的可用度 |
|---|---|---|
| 窗口、自绘标题栏、vibrancy | `ui` + `TitleBarHidden`、`c.TitleBar()` | 直接用 |
| 标签、侧栏、树、分栏、工具栏、菜单、弹层、对话框、Toast、Tooltip | `ui` 组件库 | 直接用，按设计稿定制外观 |
| 代码编辑器（多光标、折叠、查找替换、只绘可见行） | `ui/editor` | 源码模式、TXT/JSON 分段编辑可用；内置 28 种语言高亮，少于 Shiki |
| 大文本缓冲 | `ui.TextBuffer`（rope、UTF-16/行索引、快照 O(1)） | 作为文档唯一事实源 |
| 自定义输入客户端 | `TextInputClient`（输入法组字、原生文本查询、无障碍） | 自研编辑器的输入基础 |
| 文字整形与绘制 | `ui.Shape`、`Painter.Glyphs`、`Font.Metrics` | 自研段落布局与 KP 排版的基础 |
| Markdown 解析 | `ui/markdown/parser`（GFM、脚注、数学；块带源码 `Start/End`；增量重解析） | 可作解析基础，需扩展 callout、front matter、HTML 块细节与八类行内语法 |
| Markdown 只读视图 | `ui/markdown` | 小窗/预览可参考；数学只显示 TeX 原文 |
| 富文本编辑器 | `ui/richtext` | **不采用**：扁平段落模型，表格存成制表符段落，导出丢对齐，无法保证源码保真 |
| SVG | `ui.ParseSVG` | 支持路径与基本形状；**不支持 text**，Mermaid 输出不能直接显示 |
| PDF | `Page.PrintToPDF` | 依赖 WebView 页面 |
| 嵌入 WebView | `ui.WebView` / `Window.NewWebView` | 用于按需渲染服务 |

结论：外壳与源码编辑可直接组合现有组件；语义编辑器必须自研；Mermaid、PDF/Word 导出短期内保留 Web 实现，以按需 WebView 运行。

## 3. 核心架构决策

### D1 Markdown 原文是唯一事实源

文档状态 = `ui.TextBuffer` 中的 Markdown 原文 + 增量块索引。语义视图是原文的**投影**：光标或选区所在的行内格式就地展开原文（沿用现有“八类行内显隐”的交互），其余部分按渲染结果显示。

- 不再有 ProseMirror 文档 ↔ Markdown 的序列化往返，保真由构造保证，现有的表格反斜杠、HTML 规范化等首次编辑差异问题随之消失。
- 源码模式、语义模式、双栏共享同一 buffer 与同一撤销历史；双栏同步按源码偏移映射，替代 `scrollSyncMapping.ts` 的节点映射。
- 语义命令（加粗、列表切换、表格增删列等）全部实现为对原文的区间替换事务。
- 代价：表格、嵌套列表、callout 等结构在投影中编辑时，需要把结构操作翻译成原文编辑，交互复杂度集中在这里（见风险 R2）。

### D2 自研语义编辑器组件 `desktop/ui/mdedit`

- 输入：`TextInputClient` 接 buffer；输入法组字作为虚拟插入，提交后与被替换内容合为一步撤销。
- 布局：块级布局结果按块缓存，只布局可见区附近，其余块用估算高度；缓存以估算字节计上限并 LRU 淘汰，标签转入后台时释放布局、保留 buffer。
- 排版：`src/lib/typography`（KP 断行、中文规则、Liang 断词，约 2,600 行纯算法）移植为 Go 包，基于 `ui.Shape` 取字形宽度；行内对象（公式、图片、代码）作为不可拆分盒子参与断行。
- 绘制：`Painter.Glyphs` + 自绘选区背景（连续行矩形合并，对应 `selectionPaint.ts`）。
- 解析：基于 `ui/markdown/parser` 扩展（或 fork 到本仓库），补齐 callout、front matter、Nomo 的八类行内语法、HTML 锚点；解析结果只描述区间，不复制正文。

### D3 增强渲染分级

| 内容 | 方案 | 说明 |
|---|---|---|
| 代码高亮 | `ui/editor` 的 Tokenizer | 内置语言不覆盖时显示为纯文本；常用缺失语言按需补 Tokenizer |
| 公式 | P0 二选一：① 嵌入 JS 引擎运行 MathJax 输出纯路径 SVG，交给 `ui.ParseSVG`；② 按需 WebView 用 KaTeX 渲染后取位图 | ① 引入新依赖，需确认；② 无新依赖但首个公式有 WebView 冷启动成本。结果按“源码哈希 + 字号 + 主题”缓存，有字节上限 |
| Mermaid | 按需 WebView 渲染，输出位图（或文字转路径的 SVG） | Mermaid 依赖 DOM 测量文字，不适合纯 JS 引擎 |
| HTML/PDF/Word 导出 | 按需 WebView 复用现有 `src/app/services/export`、`word/` 管线 | 导出入口改为把 Markdown 快照与主题交给离屏页面 |

渲染服务统一约束：单实例 WebView 按需创建，空闲超时（初定 30 s）后 `Destroy`；有界并发队列，任务可取消，标签切走或文档修订变化即作废结果。这样“有 Mermaid 的文档”才付出 WebView 成本，普通文档不付。

### D4 后端直接调用

`desktop/` 现有命令从 IPC 绑定方法改为 Go 包内调用；`bridge.go`、`bindings.gen.ts`、`src/lib/desktop/runtime` 随前端一并移除。`textdoc`、`docfs`、`config`、`applog`、`i18n` 保持不动。

### D5 主题与本地化

- `themeRegistry.ts` 的颜色/样式令牌转为 Go 结构，用户主题 JSON 格式不变，校验规则对齐 `userThemeDefinition.ts`。
- 界面文案从 `project.inlang/messages/*.json` 生成 Go 侧访问代码（沿用 inlang 作为唯一来源），Quick Look 仍用 paraglide。

### D6 生命周期

遵循 `AGENTS.md`：文档状态（buffer、撤销历史、阅读锚点）与视图（布局缓存、渲染结果、输入客户端）分离；后台标签只保留前者。所有 goroutine、定时器、WebView 都有明确的取消与释放路径。

## 4. 分阶段计划

工作量为单人估计，供排期参考，不作承诺；每阶段结束有门禁，未达标不进入下一阶段。

### P0 基线与可行性原型（3–4 周）

1. 按 `AGENTS.md`“性能验证基准”测量现版本：空白文档、纯文本、四标签、代码/图表/图片较多的文档，Release 构建、固定窗口尺寸与 DPI，每项三轮；记录私有驻留、私有提交、JS 堆、GPU，冷启动、输入延迟、标签/模式切换延迟。
2. 原型 `mdedit`：段落、标题、行内强调与链接的投影/原文展开、中文输入法、选区、撤销重做，打开 `sample.md` 与 10 万字文档。
3. 原型 KP 排版移植一段，验证 `ui.Shape` 精度与性能足够。
4. 公式方案 ①/② 各做一次实测。

**门禁（Go / No-Go）**：原型在 10 万字文档中单次按键到上屏 p95 ≤ 16 ms；中文输入法（macOS 拼音、Windows 微软拼音）组字、候选窗位置、撤销正确；同等内容下稳定内存明显低于基线。任一不满足则停止全量重写，回退到“只原生化设置窗口”的方案。

### P1 设置窗口原生化（2–3 周，可独立发布）

设置窗口已是独立小窗口，与编辑器无耦合，原生化后直接少一个 WebView。覆盖主题/字体/排版/更新/文件关联/图片配置全部页签，读写同一 `config.json`。这一步同时沉淀原生组件样式、主题令牌 Go 化和 i18n 生成，为后续阶段铺路。

### P2 原生外壳与编辑器 MVP（10–14 周，不发布）

在独立入口（构建标签或启动参数）中开发，现版本照常发布。

- 外壳：标题栏与菜单、标签、资源管理器、状态栏、工具栏、空白页与最近打开、确认/外部变更/未保存对话框。
- 编辑器：段落、标题、列表（含嵌套）、任务、引用、代码块、分割线、链接、图片、表格；源码模式（`ui/editor`）与模式切换；搜索替换；大纲与跳转；剪贴板（Markdown、HTML、图片粘贴落盘）；保存、自动保存、恢复草稿、外部变更。

**门禁**：用现有回归语料（`README.md`、`sample.md`、`src/lib/editor-core` 测试中的 Markdown 用例）做“打开 → 不编辑 → 保存”字节级不变；基础编辑命令行为与现版本一致。

### P3 增强内容（8–10 周）

KP 排版全量、公式、Mermaid、callout、脚注、HTML 块与锚点、注释块、TOC、emoji 补全与选择器、快捷插入菜单、行内代码高亮、标题层级角标、整段选择、三击选块、链接快速编辑。

### P4 工作区能力（6–8 周）

多窗口、组合标签与双栏对照、标签跨窗口移交与屏幕拖动、大纲拖拽重排、阅读位置、文档小窗（置顶/只读降级/过渡）、TXT/JSON 分段编辑（`ui/editor` + `textdoc`）、写作统计、Markdown 格式检查。

### P5 导出、切换与清理（4–6 周）

- 导出 HTML/PDF/Word 改走按需 WebView 渲染服务。
- 灰度：原生版作为可选开关随正式版发布一个版本周期，收集问题；默认切换后再保留一个版本的回退开关。
- 清理：移除 Svelte 应用、ProseMirror/CodeMirror 依赖、IPC 绑定；`vite` 只保留 Quick Look 渲染器与导出离屏页面的构建。
- 更新 `CLASSMAP.md`、`CONTEXT.md` 与构建/发布脚本。

**合计约 33–45 周（单人）**，不含设计稿与测试人力。

## 5. 功能迁移清单

| 功能 | 现实现 | 原生方案 | 阶段 |
|---|---|---|---|
| 标题栏/菜单/托盘 | `AppTitleBar.svelte`、`desktop/menu.go`、`tray.go` | `ui` 自绘标题栏；菜单托盘已在 Go | P2 |
| 标签与组合标签 | `DocumentTabs.svelte`、`workspaceItems.ts` | `ui` tabs + 自定义拖动 | P2/P4 |
| 资源管理器 | `ExplorerSidebar.svelte`、`folderExplorerController.ts` | `ui` tree + `docfs` 按需枚举 | P2 |
| 语义编辑 | ProseMirror `editor-core` | 自研 `mdedit`（D1/D2） | P2/P3 |
| 源码模式 | CodeMirror | `ui/editor` | P2 |
| 双栏内容同步 | `markdownScrollSync*.ts` | 共享 buffer，按源码偏移插值 | P4 |
| 精细排版 | `src/lib/typography` | Go 移植 | P0/P3 |
| 代码高亮 | Shiki | `ui/editor` Tokenizer | P3 |
| 公式 | KaTeX | D3 方案 ①/② | P3 |
| Mermaid | mermaid.js | 按需 WebView | P3 |
| 表格 | `TableNodeView`、`tableControls.ts` | `mdedit` 表格块 + 原文事务 | P2 |
| 搜索替换 | `searchReplace.ts` | buffer 上搜索，投影高亮 | P2 |
| 大纲/TOC/统计 | `src/lib/outline`、`toc` | 基于块索引，统计放后台 goroutine | P2/P4 |
| 设置窗口 | `SettingsWindow.svelte` | 原生 | P1 |
| 主题与自定义字体 | `themeRegistry.ts`、`fonts.ts` | Go 令牌结构，JSON 兼容 | P1 |
| TXT/JSON 分段编辑 | CodeMirror + `textdoc` | `ui/editor` + `textdoc` | P4 |
| 文档小窗 | `markdownMiniTransition.ts` | 原生窗口 + 只读投影 | P4 |
| HTML/PDF/Word 导出 | `export/`、`word/` | 按需 WebView 复用 | P5 |
| Quick Look | Swift + Web 渲染器 | 不变 | — |
| 软件更新 | `softwareUpdate.ts` + Go | 界面原生，逻辑已在 Go | P1 |

## 6. 性能预算与验收

- 每阶段结束按第 4 节 P0 的同一流程复测，与基线对照；切换与打开关闭各 20 轮，区分预热缓存与持续增长。
- 热路径约束：单次按键只重解析受影响块、只重排受影响段落；滚动不触发全文布局；后台标签不绘制、不排版。
- 缓存一律给出容量、估算字节与淘汰策略：块布局缓存、字形缓存、公式/图表结果缓存、图片解码缓存。
- 报告区分私有驻留、私有提交、Go 堆、GPU 资源，不简单相加；`MYGO_FRAME_STATS` 用于定位超过 8 ms 的帧。

## 7. 风险与缓解

| 编号 | 风险 | 缓解 |
|---|---|---|
| R1 | 自研编辑器的输入法、选区、无障碍边界情况多，Windows（TSF）尤甚 | P0 门禁先验输入法；每阶段在 macOS/Windows 实机回归 |
| R2 | 投影编辑下表格、嵌套列表、callout 的结构操作复杂 | 结构命令统一为原文事务，配套字节级保真回归；复杂块允许“块内临时展开原文”退路 |
| R3 | 公式/Mermaid 渲染仍需 WebView 或 JS 引擎 | 只在出现时按需创建并空闲销毁；结果缓存；首帧先显示原文占位 |
| R4 | 重写周期长，期间新功能需双份实现 | P2 起冻结非紧急新功能，或仅在原生版实现并延后上线 |
| R5 | MyGo 原生 UI 仍在快速演进（v0.x），API 可能变化 | 锁定版本；自研组件只依赖 `TextInputClient`、`Shape`、`Painter` 等底层接口 |
| R6 | 代码高亮语言覆盖少于 Shiki | 统计用户常见语言后按需补 Tokenizer；其余显示为纯文本 |
| R7 | 性能收益不及预期 | P0 门禁与每阶段复测；不达标时止损，保留 P1 成果 |

## 8. 待决策事项

1. 公式方案选 ①（引入 JS 引擎依赖）还是 ②（按需 WebView），P0 实测后定。
2. 是否接受 P2–P4 期间冻结非紧急新功能。
3. Linux 是否要求完全不依赖 WebKitGTK：若要求，导出与 Mermaid 需另寻纯 Go 方案，工作量显著增加。
4. 原生版是否继续支持 macOS 12（现 `minimumSystemVersion`）。

## 9. P0 进展（2026-10-22）

### 9.1 已完成：`mdedit` 原型（P0 第 2 项）

代码在 `desktop/ui/mdedit`。原型最初的独立入口 `desktop/cmd/mdedit-proto` 已由 9.7 的原生外壳取代并删除。

- 原文为唯一事实源；`parser.Document.Edit` 增量重解析，顶层块索引按前后缀匹配增量维护（随机 400 次编辑与全量解析逐块一致）。
- 段落与标题按行内语法投影：光标触及的强调、链接、行内代码标记就地展开，标题前缀与转义符在光标位于本块时显示；其余块原样显示（R2 退路）。
- 可见区虚拟化：只布局可见片段；原样块（代码块等）按源码行布局，编辑时按行哈希复用未变行；块级阅读锚点，估算高度被实测替换时不跳动；布局缓存按估算字节 LRU 淘汰（默认 8 MB），`ReleaseLayouts` 供后台标签释放。
- 输入：`TextInputClient` 以原文 UTF-16 坐标对接输入法；组字为虚拟插入，提交后与被替换内容合为一步撤销；连续键入、连续删除合并；撤销栈不裁剪。
- 打开 → 不编辑 → 原文字节不变（`sample.md`、`README.md`）。

### 9.2 实测（无头 CPU 渲染，未经真机验证）

环境：Apple Silicon、macOS、Go 1.27.1、MyGo v0.6.2，`ui.Tester` 1200×900，按键 → 编辑 → 重建并绘制一帧。无头渲染在 CPU 上光栅化，比 GPU 窗口更保守，但不含合成与上屏延迟。

| 场景（10.3 万字符、788 块） | p50 | p95 |
|---|---:|---:|
| 键入 ASCII | 1.2 ms | 1.7 ms |
| 键入中文 | 1.6 ms | 2.2 ms |
| 拼音组字/提交 | 1.8 ms | 2.4 ms |
| 退格 | 1.9 ms | 2.4 ms |
| 方向键 | 0.5 ms | 2.0 ms |
| 滚动 120 DIP | 1.2 ms | 3.3 ms |
| 3000 行代码块内键入 | 1.3 ms | 1.6 ms |
| 2 万字无换行单段落内键入 | 10.5 ms | 10.8 ms |

打开 10 万字文档到首帧约 50 ms；测试进程 Go 堆 15 MB（含无头渲染宿主），布局缓存约 5 MB。真机窗口首帧 870 ms，其中 808 ms 为 MyGo 首次编译 GPU 管线（`present`），属框架冷启动成本，需计入冷启动基线。

### 9.3 与计划的偏差

- **未用 `ui.TextBuffer` 作事实源**：`parser.Document` 必须持有完整原文，再维护一份 rope 会让正文常驻两份；解析器使用字节偏移，而 `TextBuffer` 没有字节与 UTF-16 的换算。原型以解析器原文为唯一副本，块级前缀和提供 UTF-16 换算，撤销记录差量文本而非快照。
- **行高**：`ui.TextLayout` 不能设置行高，正文按字体自然行高排列，与现版本约 1.75 倍行高不一致；需在 KP 排版移植（第 3 项）中基于 `Shape` + `Painter.Glyphs` 自研行布局解决。

### 9.4 门禁尚缺的项目

1. 现版本基线（第 1 项）：须按 `AGENTS.md` 在 Release 构建中实测，尚未进行。
2. 输入法实机验证：macOS 拼音与 Windows 微软拼音的组字、候选窗位置、撤销，须在真机上完成；无头测试只覆盖了组字协议。
3. 原生窗口的稳定内存与按键上屏延迟：用 `MYGO_FRAME_STATS=1` 运行原型测量，并与基线对照。
4. KP 排版移植一段（第 3 项）、公式方案实测（第 4 项）。

### 9.5 已知限制（P2/P3 处理）

列表、表格、引用、callout、front matter 原样显示；间隙中回车不生成空段落；超长单段落整段重排（上表最后一行）；代码块不支持横向滚动；Tab 不缩进；拖选不自动滚动；光标不闪烁；无障碍只暴露角色与名称。

### 9.6 块组件（2026-10-22，对齐原版 WebView 显示）

真实文档（正文写在列表里）实测后，提前实现了 P2 中的块级组件，代码在 `desktop/ui/mdedit/blocks.go`、`draw.go`、`structure.go`。

- 顶层块展开为“行”：叶子段落/标题/表格单元格按行内语法投影，代码块等按原文行；容器结构（列表、引用、提示块、表格、代码块、front matter、分割线）记为跨行装饰。行布局按需生成，编辑后按行哈希复用未变行。
- 显示对齐原版默认主题：列表符号随层级变化、有序序号、任务复选框、引用条、五类提示块（类型色条 + 标题）、表头底色与网格、代码卡片与语言标签（围栏行仅在光标进入代码块时显示）、front matter 卡片、分割线；行内 `<strong>`/`<u>`/`<mark>`/`<a>` 等标签、实体（如 `&#32;`）与注释按渲染结果显示，光标触及时展开原文。
- 结构编辑：列表与引用内回车续写、空项回车退出、行首退格提出容器、Tab/Shift+Tab 缩进列表项、表格内 Tab 切换单元格、点击任务框勾选；全部为原文区间替换，一步撤销。
- 解析适配：front matter 合并为一个合成块；块区间延伸到所在行行尾（解析器在空列表项、ATX 闭合 `#`、表格末行处止于行中）；`\[…\]` 按原版视为转义括号。

实测（无头 CPU 渲染，同 9.2 口径）：10 万字文档键入中文 p95 1.9 ms、退格 2.1 ms、滚动 3.6 ms；3000 行代码块内键入 p95 2.0 ms。真实窗口（GPU）中大跨度滚动（一次 35–40 格滚轮）后首帧需整屏排版，绘制 13–24 ms；常规滚动与编辑帧低于 8 ms。

尚未实现：图片、公式、Mermaid、脚注引用渲染（P3）；表格按内容自动列宽与横向滚动；代码高亮；空段落；大跨度滚动的空闲预排版（需可取消、有界）。

### 9.7 原生外壳（2026-10-22，P2 外壳部分）

入口：`nomo --native` 或 `NOMO_NATIVE=1`（`desktop/native.go`），默认仍启动 WebView 外壳。代码在 `desktop/ui/shell`，直接调用 `config`、`docfs`、`i18n`，不经过 IPC；与 WebView 版共用数据目录、`config.json` 的偏好键（`themeMode`、`editorMode`、`sidebarHidden`、`outlineVisible`、`toolbarHidden`）与最近打开记录。设置写入的分区路由从 `main` 包下沉为 `config.PutSetting`，两个外壳共用。

- 布局对齐原版：自绘标题栏（文件/编辑/格式/查看菜单、文件名与未保存标记、侧栏/大纲/主题按钮）、资源管理器（懒加载目录树，沿用 docfs 忽略规则与 .gitignore）、文档标签页、格式工具栏、查找替换栏、编辑区、文档大纲、状态栏（字数/字符/行、光标行列、路径、语义/源码切换）、空白工作区（新建、打开、最近文件）、未保存确认。macOS 另装系统菜单栏，与标题栏菜单共用一张命令表；快捷键沿用原版。
- 语义模式为 mdedit；源码模式为 ui/editor（Markdown 高亮、自带查找），其缓冲区是 Document 的镜像，按最小差异双向同步，切换模式不丢失内容或位置。
- 文件：新建、打开（多选）、打开文件夹、保存、另存为（后台写入，不阻塞界面）、关闭标签与关闭窗口时确认未保存、窗口获得焦点时检测外部修改（未修改的文档自动重载，有未保存修改时只提示）、第二实例与 macOS 打开文件事件。
- 文案：`scripts/generate-native-messages.mjs` 从 `src/app/i18n.ts` 与 inlang 消息生成 Go 文案表（zh-CN/zh-TW/en-US，各约 865 条，查找顺序与原版 t() 一致）；图标：`scripts/generate-native-icons.mjs` 从 `@lucide/svelte` 提取。

实机验证（macOS，隔离数据目录，Computer Use）：打开两个文档、标签切换、大纲显示与跳转、⌘⌥2/⌘⌥1 切换模式且源码编辑同步回语义视图、⌘S 只写入改动行、⌘F 查找（23 处匹配高亮与跳转）、⌘W 未保存确认、深色主题、⌘⇧O 打开文件夹并在资源管理器中显示、关闭窗口确认后退出；偏好与最近记录按原格式写入。常规帧 8–11 ms（超出部分为 GPU present），切换主题时一帧 67 ms（全部布局失效重排）。

尚未合并（后续阶段）：设置窗口（P1）、导出 HTML/PDF/Word（P5）、多窗口与标签移交、组合标签与双栏对照、文档小窗、TXT/JSON 分段编辑（P4）、自动保存与快照恢复、托盘、软件更新入口、资源管理器的新建/重命名/删除、快捷插入、表情选择器、链接快速编辑、图片插入与粘贴落盘、标签拖动排序；以及 9.6 所列的公式、Mermaid、图片渲染。

### 9.8 平台原生外观、设置窗口与性能（2026-10-22）

**平台原生外观**（`desktop/ui/shell/platform.go`、`view.go`）：按平台构建不同的布局与配色，不再一套界面通用。

- macOS：菜单只在系统菜单栏（应用菜单按惯例含关于、设置…⌘,、服务、隐藏、退出）；侧栏全高、使用 vibrancy 材质，交通灯位于侧栏顶部；内容区顶部为统一标题栏工具区（文件名、语义/源码分段控件、查找、大纲、设置）；多个标签时为 Safari 式等宽标签栏；13 pt 系统字体、系统强调色。明暗设置通过 `mygo.Theme.SetSource` 作用于整个应用，材质与系统控件随之切换。
- Windows 11：标题栏内为文件/编辑/格式/查看菜单，窗口为 Mica 材质，内容区为左上圆角卡片，Fluent 风格标签，14 pt Segoe UI Variable、4 px 圆角、系统强调色。Linux 为 Adwaita 风格配色，菜单在标题栏内。
- 标签右键菜单（关闭、关闭其他、关闭右侧、复制路径、在文件夹中显示），资源管理器右键菜单（打开、新建文档、重命名、复制路径、在文件夹中显示、刷新）。未提供删除：原版为永久删除，MyGo 暂无移到废纸篓的接口。

**设置窗口**（`settings.go`、`prefs.go`）：分类、文案与原版一致（通用、编辑器、外观、文件与窗口、统计与大纲、关于），macOS 为 vibrancy 源列表 + 右对齐表单，Windows 为 Mica 导航。偏好模型与原版 `AppPreferences` 同键、同默认值、同取值范围（规范化规则逐项对照 `normalizeAppPreferences`），与 WebView 版共用 `config.json`。只列出原生外壳已生效的设置：默认编辑模式、自动保存及延迟、界面语言（即时生效）、字号、内容宽度、明暗模式、界面缩放、启动时隐藏资源管理器、外部文件变更处理、大纲与统计显示、默认统计项。滑块拖动时只改内存，松开后写盘。

**继续合并**：自动保存（按原版设置在最后一次修改后延时保存，`c.After` 预约，不常驻定时器）、外部文件变更按 `externalFileChangeBehavior` 处理、界面缩放（⌘= / ⌘−，`zoomPercent`）、写作统计按 `writingStatsMetric` 排序、编辑器字号与内容宽度随设置即时生效（`mdedit.Editor.SetStyle`）。

**性能**：
- 空闲预排版（`mdedit` `draw.go`）：输入或滚动 120 ms 后，在空闲帧中为视口上下各一屏的块预先排版，每帧上限 4 ms，只处理锚点块之前与可见区之后的整块（视口不移动），窗口不可见时随绘制暂停。10 万字文档整屏翻页那一帧：p95 21.7 ms → 5.1 ms，最大 26.6 ms → 5.1 ms；键入、滚动的帧不受影响（预排版不在处理输入的帧中运行）。
- 保存在后台写入；设置滑块不逐帧写盘。

**实机验证**（macOS，隔离数据目录，Computer Use）：深色主题下侧栏材质为深色、交通灯位于侧栏；⌘, 打开设置，切换明暗时两个窗口与材质同时切换；拖动字号滑块正文即时放大，松开后写入 `fontSize`（同时路由到 editor 分区，与 WebView 版一致）；设置窗口中 ⌘W 只关闭设置窗口。修复了实测发现的两处问题：大纲内容撑高编辑行导致正文压到状态栏下；设置窗口中 ⌘W 误关闭文档标签。Windows 与 Linux 布局只做了无头渲染检查，未在真机运行。

**仍未合并**：设置中的图片上传、快捷插入、Markdown 检查、渲染模式、自定义快捷键、主题导入导出与自定义字体（随对应功能合并）；正文行高（需自研行布局）；以及 9.7 所列的导出、多窗口、双栏、小窗、分段编辑、快照恢复、托盘、更新入口。

### 9.9 图标、设置补齐、Quick Look 与性能对比（2026-10-22）

**图标**：应用图标嵌入二进制（`desktop/appicon.go`），Windows / Linux 设为窗口图标，macOS 在 `.app` 外运行时设为 Dock 图标，“关于”页按明暗显示；文档图标沿用 `mygo.config.ts` 的文件关联（与 WebView 版同一 bundle）。界面图标集中到 `desktop/ui/icons`（从 `@lucide/svelte` 生成）：文件类型图标（资源管理器、标签、最近打开）、展开箭头、提示块图标、状态栏检查状态。

**设置补齐**（`settings.go`）：新增“图片”“高级”分类，以及颜色主题（选择、导入、导出模板、删除用户主题并确认）、正文 / 界面 / 代码字体（校验与原版一致）、提示块样式、快捷插入（开关与触发符校验）、Markdown 检查（开关与规则集）、图片插入策略与 PicGo（含连接测试）、默认图片宽度与对齐、渲染模式（重启生效）、自定义快捷键（修改后重建菜单）、保存前快照、检查更新。对应功能均已实现：

- 快捷插入（`quickinsert.go`）：语义模式下行首或空格后输入触发符，在光标处弹出菜单，可筛选，↑↓ / 回车 / Esc，插入时删除触发符。
- 图片（`images.go`）：粘贴剪贴板位图或图片文件、拖放图片到编辑区、菜单插入；按策略复制到资源目录（同内容复用）或经 PicGo / PicGo-Core 上传，在后台完成；插入的 Markdown / HTML 与原版 `imageInsertion.ts` 一致。
- Markdown 检查（`desktop/ui/mdlint`）：Go 实现 markdownlint 的 23 条常用规则，含行内配置注释与 front matter 标题；规则集与原版的 relaxed / default 一致。对照真实 markdownlint 0.41（sample.md、README、CLASSMAP 等 7 份文档，两种规则集），实现的规则逐行一致（CLASSMAP default 691/691）；未实现的规则（如 MD005、MD007、MD029、MD030、MD037–MD039、MD049–MD060）不报告。后台检查、修改后 500 ms 防抖、过期结果丢弃，状态栏显示问题数，弹层列出问题并可跳转。
- 导出 HTML / PDF（`export.go`）：沿用原版 `export-document.css` 与主题 CSS 变量，本地图片内联；PDF 复用 WebView 版的打印管线（按需创建的隐藏 WebView，导出后销毁，含标题书签）。差异：代码不高亮，公式与 Mermaid 以源码显示，提示块按引用显示。
- 保存前快照：快照逻辑移到 `desktop/internal/config/snapshot.go`，两个外壳共用。
- TXT / JSON：按扩展名只用源码编辑器（JSON 显示行号），不提供语义模式、检查、快捷插入与导出。
- 检查更新：复用 `checkSoftwareUpdate`；有新版本时打开发布页，原生外壳暂不内置下载安装。

**Quick Look**（`desktop/packaging/macos/NomoQuickLookPreview`）：去掉 WKWebView，改为 Swift 原生渲染：`AttributedString(markdown:)` 解析，`PresentationIntent` 映射为 TextKit 富文本（标题、列表与任务项、引用与提示块用 `NSTextBlock`、代码块、表格用 `NSTextTable`、分隔线、行内样式、链接、本地图片、front matter）；HTML 注释行隐藏，README 常见的 HTML 行（`<p align>`、`<img>`、`<h1>`、`<a>`）转为图片、标题、链接。配色读取同一份 `config.json` 与用户主题，内置主题由 `generate-native-themes.mjs` 生成。扩展不再链接 WebKit，去掉了 `network.client` 权限。sample.md 渲染约 55–65 ms、整篇排版约 0.5 s（视图启用非连续排版，只排可见部分）。差异：公式与 Mermaid 以源码显示；沙盒不允许读取时本地图片显示替代文字。验证范围：本机宿主程序直接调用 `PreviewViewController.preparePreviewOfFile` 截图（浅色 / 深色、sample.md、README）；系统 Quick Look 面板（`qlmanage`）中的实际加载未验证，因为注册测试扩展会与已安装的 Nomo 争用 Markdown 预览。旧 WebView 渲染器 `src/quicklook/` 仍被 WebView 版的 Markdown 小窗使用，`preview-entry.ts`、`vite.quicklook.config.ts` 与 `build:quicklook-renderer` 已不再参与扩展构建。

**性能对比**（macOS arm64，release 构建，同一 sample.md，隔离数据目录，每项 3 次；内存为进程 phys_footprint，WebView 版合计 Nomo + WebContent + GPU + Networking 进程）：

| 指标 | WebView 版 | 原生版 |
|---|---|---|
| 冷启动到空闲 | 2.58 / 2.61 / 2.71 s | 1.40 / 0.52 / 0.55 s（首次含字体缓存） |
| 启动 CPU 时间 | ≈ 3.0 s | ≈ 0.38 s |
| 启动后内存 | ≈ 447 MB（80 + 342 + 22 + 5.5） | ≈ 64 MB |
| 滚动 120 格往返 + 输入 24 字的 CPU | +3.32 s | +0.29 s |
| 交互后内存 | 445.8 → 507.7 MB | 64 → 72 MB |

注意：测量时原生版尚未渲染 KaTeX、Mermaid、代码高亮与图片，同等功能下差距会缩小；单次流程，不代表长期稳定占用。

**仍未合并**：正文行高（MyGo v0.6.2 的 `TextLayout` 未公开行高参数，`ui.Font` 无此字段，需上游支持）、双栏、Word 导出、多窗口、小窗、托盘、分段大文件编辑、代码高亮、公式与 Mermaid 渲染。


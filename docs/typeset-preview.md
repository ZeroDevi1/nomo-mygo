# LaTeX / Typst 编译预览

打开可信的主文件后，工具栏可切换**语义编辑、源码模式、双栏模式**。预览与双栏共用实时编译结果，切回源码模式会释放预览会话。内容宽度滑块按预览区宽度缩放纸面，页面较高时可上下滚动。各页上下连续排列，滚动即可跨页。拖动分隔条调整双栏比例。打开文档大纲可按层级浏览 PDF 书签并跳到对应页；在源码模式中，点击章节标题会将光标移到对应行。编辑器中的主文件不必先保存，同目录的章节、图片、文献库及模板从磁盘读取。文件菜单中的**导出 PDF**重新编译当前缓冲区，成功后才替换目标文件。

| 输入 | 完整编译和导出 | 实时段落试排 |
|---|---|---|
| LaTeX | XeLaTeX；有文献库时需对应的 BibTeX 或 Biber | 可选 Typst CLI |
| Typst | Typst CLI | 合并输入后由 Typst 编译整页 |

分页预览需要 Poppler 的 `pdfinfo` 和 `pdftoppm`；仅导出 PDF 不需要 Poppler。工具从 PATH 查找，macOS 也检查 Homebrew 和 MacTeX 的常见安装目录。模板指定的字体、宏包和资源需要在本机可用。

LaTeX 实时模式使用 Poppler 的 `pdftotext -bbox-layout` 读取上一份 PDF 的文本块。若变更只涉及一个段落，且能找到唯一匹配，就在右侧切到对应页，在原矩形内更新文字。纯正文直接使用原页的字号、行高和缩进试排；支持的粗体、强调和行内公式用 Typst 试排。输入期间不挤动周边内容，超出原框的正文可能暂时裁切。停顿后 XeLaTeX 重新排整页，若能重新定位该段落则跟随新页码。重复段落、跨页段落、缺少文本层或缺少 `pdftotext` 时，不猜位置，使用试排卡片或等待整页。每次成功编译替换上一份索引，最多 8192 个块，超过 12 MiB 的位置文件不建索引。

点击 PDF 正文可定位到当前主文件的源码行；在纯预览中点击会展开双栏。定位需要 Poppler 的 `pdftotext`。LaTeX 优先使用 SyncTeX，Typst 按点击的词和相邻文本匹配源码。空白区域、无法区分的重复行或过期页面不跳转。

源码编辑器提供本地高亮、括号配对和补全。在 LaTeX 中输入 `\`、在 Typst 中输入 `#` 即可查看命令、签名和所选命令的简要说明。继续输入 `\subsec`、`\begin{equa`、`#fig` 或 `#set tex` 可看到候选名称与签名；按 Enter 接受，也可在命令或环境名位置按 Ctrl+Space 手动触发。LaTeX 导言区的常用宏声明、Typst 的 `#let` 名称也会参与提示（当前文件前 32 KiB，最多 128 个声明）。没有启动语言服务器或扫描工作区。

XeLaTeX 禁用 shell escape，使用独立辅助目录；检测到文献库时运行相应后端，再按日志补跑，总计最多三遍 XeLaTeX。依赖外部脚本的项目仍应使用项目原有构建流程。

## 从 Go 调用

`nomo/desktop/typeset` 不导入 UI 框架，接受内存中的源码，返回标准库的 `image.Image`。以下代码在仓库的 Go 模块中运行，`path` 是主文件路径，`source` 是当前缓冲内容：

```go
session, err := typeset.Open(path)
if err != nil { return err }
defer session.Close()

pages, err := session.Compile(ctx, strings.NewReader(source))
if err != nil { return err }
page, err := session.Page(ctx, pages) // 页码从 1 开始；这里读取末页
if err != nil { return err }
// page 可交给 png.Encode 或交由原生界面绘制。
_ = page

return typeset.ExportPDF(ctx, path, strings.NewReader(source), destination)
```

每个预览窗口由单个工作协程串行使用会话，后台标签没有常驻编译器。UI 只投递最新的只读源码快照，新编辑会取消过期整页任务，关闭预览会结束子进程并清理临时文件。预览保留最近一次成功的 PDF（上限 128 MiB），按需将可见页栅格化为长边不超过 1400 像素的图片。通常缓存五页（未压缩 RGBA 像素最多约 38 MiB），窄栏同屏页数更多时降低页图分辨率。重编译成功后替换缓存，关闭预览后释放。段落缓存只保留一张、最多 1200 × 640 像素。独立导出不复用试排图片。

## 连续输入回归

`TestThesisContinuousPreview` 从 `NOMO_THESIS_PREVIEW` 指定的目录读取 `math-talk.tex` 和 `figures/`，编辑临时副本。使用 1200 × 800 的 MyGo 无头窗口，每 60 ms 向编辑器输入一个中文字符；每轮 24 字，共三轮，每轮都等 XeLaTeX 校正后再继续。测试测量输入事件到试排帧的时间，检查页码、页面与锚框的坐标，并将本轮末次试排截图与本轮初始页面逐像素对比（排除锚框与两像素边缘）。正式重排允许后续内容移动。

本机 Apple M4、macOS、TeX Live 2026 的 36 页讲稿测试：72 次输入全部在输入期间出现对应更新；首帧约 35 ms，预热后 71 帧 P50 16.7 ms、P95 19.9 ms。三轮末次按键到试排帧为 16.5、15.5、17.7 ms。同轮内无跳页、锚框无位移，抽查的框外像素无变化。这些数字是首页普通中文段落的编辑器帧延迟，不包括显示器扫描、输入法组词或整篇 XeLaTeX 编译。

```sh
NOMO_THESIS_PREVIEW=/path/to/Thesis go test ./desktop/ui/shell -run '^TestThesisContinuousPreview$' -count=1 -v
```

## Finder 快速查看

安装带 Quick Look 扩展的 macOS 构建后，选中 `.tex` 或 `.typ` 并按空格，可连续滚动、选择和缩放 PDF。首次使用时，在 Nomo 中预览或导出**已保存**的主文件即可生成快照。Finder 按主文件的路径和内容摘要读取最近一次成功排版，并标注构建日期。只改动章节、图片或字体时，请在 Nomo 中重新编译以更新快照。

扩展也会尝试读取有权限访问且不早于主文件的同名 PDF。两者均不可用时显示源码开头（最多 256 KiB）及生成 PDF 的提示。Quick Look 沙盒内不启动外部编译器，翻页复用同一份 PDF。快照存放在用户缓存目录的 `com.nomo.desktop/QuickLookPDF`，对不超过 16 MiB 的主文件生成，最多 12 份、合计 128 MiB，按最早编译时间淘汰。Markdown 沿用原生富文本预览。

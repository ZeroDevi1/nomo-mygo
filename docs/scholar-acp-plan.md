# Nomo Scholar ACP 开发计划

## 协议基线

使用 `github.com/caelis-labs/acp-go-sdk/experimental/v2`（模块 v1.6.0）负责类型、双向 JSON-RPC 和 stdio 进程。它固定在 ACP `schema-v2.0.0-alpha.13`，与 Codex 适配器 2.6.0 使用的 TypeScript SDK 2.4.0 对齐。由用户安装 `@agentclientprotocol/codex-acp@2.6.0`，Nomo 按需启动本地二进制，复用适配器登录。初始化要求协议版本 2。

[ACP v2 提案](https://agentclientprotocol.com/rfds/v2/overview)持续更新。逐项核对当前提案与固定 schema 的差异，记录可用能力与实测边界。传输以首个本地 agent 使用的 stdio 为准。模型、推理档位、模式与命令以 agent 返回的选项为准。

## 实施顺序

1. 接入初始化、认证、会话创建／列举／恢复／关闭、配置、提示提交、停止、消息与状态更新。以 idle 更新结束前台轮次，处理提示响应与更新的不同到达顺序。接收工具、计划、终端和权限事件。按协商启用表单提问，允许用户提交、拒绝或取消。
2. 复用 Scholar 右侧检查器。上方提供会话列表和新建，中部显示对话和当前轮状态，底部显示引用、输入框、斜杠命令和 agent 配置。同一项目内按源码路径、Zotero 文献标识或规范化网址选择最近会话。显式新建与追加都先准备草稿，由用户确认发送。
3. 源码选区提供贴近选区的操作菜单。PDF 使用页面文字层与已有页图处理区域选取，提供复制文字和截图引用。网页复用可见文献的隔离 WebView，由用户操作触发提取选区或截图。引用记录来源与定位，在发送前可预览或移除。文本和图片按 ACP 内容能力编码，并限制单次负载。
4. 稿件修改在临时目录形成提议，按本轮开始时冻结的显式稿件基准转换为现有审阅片段。复用版本、内容哈希、逐处接受／拒绝和试排；接受前再次校验缓冲。改动涉及附件外文件、不可表示的文件操作或过期基准时显示原因，并保留其余可审阅的文本改动。
5. 对照本计划复核代码差异和受影响的诊断。重点验收真实 Codex 登录、流式响应与停止、跨文献会话、PDF／网页选取、逐处接受／拒绝以及不同窗口布局。记录实际执行的检查和未验证项。

## 性能与生命周期

首个窗口启动时在后台建立一次临时 ACP 连接，读取认证状态和默认会话选项，供各窗口复用。登录由用户操作触发。探测和实际会话各自释放进程及临时目录。会话索引、流式显示、单轮输入和图像有独立上限。网页只保留当前可见标签的 WebView，PDF 仅在复制或引用文字时提取当前页文本框。保持编辑器原有选择和滚动路径。

参考：[Go SDK 版本基线](https://github.com/caelis-labs/acp-go-sdk/blob/v1.6.0/upstream/lock.json) · [Codex 适配器的 v2 能力](https://github.com/agentclientprotocol/codex-acp/blob/v2.6.0/readme-dev.md)

## 已实现的范围

| ACP v2 草案能力 | Nomo 客户端行为 |
| --- | --- |
| 初始化、版本、stdio、开放变体 | 要求 wire version 2。拒绝 JSON-RPC 批处理。通过 Go SDK 解析已知变体，未知变体可被跳过。 |
| 会话、认证和配置 | 创建、列举、恢复及关闭。使用 `replayFrom: start` 读取可用历史。接入 agent 处理的登录与退出，显示 agent 给出的模型、推理及命令。 |
| 提示、消息、状态和取消 | 按 `messageId` 合并整条消息与分块。提示响应只确认插入。正常结束以 idle 的 `stopReason` 为准。支持前台取消与按能力停止终端或工具调用。 |
| 计划、工具、终端和后台工作 | 显示计划条目、工具调用、diff 操作、终端输出与当前用量。保留有界的活动记录，后台工作仍在运行时继续显示本轮状态。 |
| 结构化提问 | 声明 form 能力，侧栏显示默认值、必填项和可选项。用户可提交、拒绝或取消，agent 撤回请求后移除表单。 |
| 权限和元数据 | ACP 权限请求走拒绝或取消结果。消息、工具调用和终端应用顶层元数据补丁，明确区分省略、清空和删除键。 |
| 文件变更与审阅 | 展示 ACP diff 事件。稿件审阅基于临时工作区实际变更，并校验显式附带的缓冲基准。现有编辑器审阅支持稿件内容修改。跳过无法表示或基准已过期的改动时会显示提示，同轮可应用的片段继续进入审阅。 |

固定基线为 `alpha.13`。后续 `alpha.14` 稳定了 client system prompt，`alpha.15` 增加 `session/options`、初始配置选项与会话类型工具输出，这些变更需在 SDK 与适配器升级后补齐。首版采用 stdio。可选的终端式认证、URL 模式提问、MCP 服务器配置界面、其他 agent 以及 Codex 私有扩展仍待接入。

macOS 开发版用已登录的 Codex 和 GPT-6-Luna 验证了源码引用的新会话与追问、PDF 区域复制及文字／截图引用、网页选区引用和逐处接受／拒绝。WebView 截图通过原生菜单取得，补齐不透明背景后用同一张截图经 ACP 复测，Luna 返回了图中的两个标记。Windows 界面、触屏长按及首次登录流程仍待实机验收。

[ACP alpha.14](https://github.com/agentclientprotocol/agent-client-protocol/releases/tag/schema-v2.0.0-alpha.14) · [ACP alpha.15](https://github.com/agentclientprotocol/agent-client-protocol/releases/tag/schema-v2.0.0-alpha.15)

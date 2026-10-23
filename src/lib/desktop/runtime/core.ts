/**
 * 桌面外壳（MyGo）的调用入口：页面通过 Go 侧唯一的 Bridge.Invoke 按命令名调用原生能力。
 *
 * 命令名与参数沿用既有约定（snake_case 命令名、camelCase 参数对象），返回值与错误形状与
 * 原实现一致：失败时 reject 原样的错误值（字符串，或分段文档的结构化错误对象）。
 */
import { call, isMyGo, runtime, type Platform } from 'mygo-runtime';

interface InvokeEnvelope<T> {
  ok: boolean;
  value?: T;
  error?: unknown;
}

/** 是否运行在桌面窗口内（而非普通浏览器标签页中的开发服务器页面）。 */
export function isDesktopRuntime(): boolean {
  return isMyGo();
}

/** 当前桌面平台；浏览器中返回 undefined。 */
export function desktopPlatform(): Platform | undefined {
  return isMyGo() ? runtime().platform : undefined;
}

/**
 * 调用桌面命令。成功时解析为命令返回值（Go 侧无返回值时为 null），失败时以错误值 reject。
 *
 * @param command 命令名，如 `read_markdown_file`。
 * @param args 参数对象；字段名与命令约定一致。
 */
export async function invoke<T = unknown>(command: string, args: Record<string, unknown> = {}): Promise<T> {
  const envelope = await call<InvokeEnvelope<T>>('Bridge.Invoke', command, args);
  if (!envelope.ok) {
    throw envelope.error;
  }
  return envelope.value as T;
}

/**
 * 把本地文件路径转换为页面可加载的 URL（由 Go 侧注册的 asset 协议提供）。
 * Windows 的 WebView2 以 `http://<scheme>.localhost/` 提供自定义协议，其他平台为 `<scheme>://localhost/`。
 */
export function convertFileSrc(filePath: string): string {
  const encoded = encodeURIComponent(filePath);
  return desktopPlatform() === 'win32' ? `http://asset.localhost/${encoded}` : `asset://localhost/${encoded}`;
}

/**
 * 读取 Go 侧暂存的大文档正文（`nomodoc` 协议，令牌一次性）。大正文不经调用结果返回：Windows 的 WebView2
 * 会把结果整段编译为脚本，几十 MB 时很慢且占用数倍内存。
 *
 * @param token `read_markdown_file` 结果中的 `content_token`。
 * @throws 令牌失效（已读取或超过一分钟）或读取失败时抛出错误。
 */
export async function fetchDocumentContent(token: string): Promise<string> {
  const url = desktopPlatform() === 'win32' ? `http://nomodoc.localhost/${token}` : `nomodoc://localhost/${token}`;
  const response = await fetch(url, { cache: 'no-store' });
  if (!response.ok) {
    throw new Error(`读取文档正文失败：HTTP ${response.status}`);
  }
  return response.text();
}

/** 把二进制编码为 base64，用于经 JSON 调用传输文件内容；分块拼接避免参数个数超限。 */
export function bytesToBase64(bytes: Uint8Array): string {
  let binary = '';
  const chunkSize = 0x8000;
  for (let offset = 0; offset < bytes.length; offset += chunkSize) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + chunkSize));
  }
  return btoa(binary);
}

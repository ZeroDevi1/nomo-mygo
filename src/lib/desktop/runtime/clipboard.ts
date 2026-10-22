/**
 * 系统剪贴板，由 Go 侧读写；用于 WebView 拒绝 navigator.clipboard 时的桌面降级路径。
 */
import { bytesToBase64, invoke } from './core';

/** 写入纯文本，html 非空时同时提供 HTML 表示。 */
export async function writeClipboard(text: string, html?: string): Promise<void> {
  await invoke('clipboard_write', { text, html: html ?? '' });
}

/** 写入图片字节（PNG/JPEG/GIF/WebP），由 Go 侧统一转为 PNG。 */
export async function writeClipboardImage(bytes: Uint8Array): Promise<void> {
  await invoke('clipboard_write_image', { bytesBase64: bytesToBase64(bytes) });
}

/** 读取纯文本；没有文本时为空字符串。 */
export function readClipboardText(): Promise<string> {
  return invoke<string>('clipboard_read_text');
}

/** 读取 PNG 图片；没有图片时为 null。 */
export async function readClipboardImage(): Promise<File | null> {
  const encoded = await invoke<string>('clipboard_read_image');
  if (!encoded) return null;
  const binary = atob(encoded);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index);
  return new File([bytes], 'clipboard.png', { type: 'image/png' });
}

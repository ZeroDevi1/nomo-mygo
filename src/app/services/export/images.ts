import { readFileAsBase64 } from '../../../lib/desktop/desktopStorage';
import { resolveImagePath } from '../exportImagePath';
import { awaitExport, throwIfExportAborted, type ExportTaskOptions } from './types';

export const EXPORT_IMAGE_LIMIT = 32 * 1024 * 1024;
export const EXPORT_MEDIA_LIMIT = 128 * 1024 * 1024;
const TIMEOUT = 8_000;

export class ExportBudgetError extends Error {
  constructor() { super('导出图片超过资源预算（单资源 32 MiB，合计 128 MiB）'); }
}

export class ExportResourceBudget {
  private used = 0;
  checkSingle(bytes: number) { if (bytes > EXPORT_IMAGE_LIMIT) throw new ExportBudgetError(); }
  add(bytes: number) {
    this.checkSingle(bytes);
    if (this.used + bytes > EXPORT_MEDIA_LIMIT) throw new ExportBudgetError();
    this.used += bytes;
  }
}

function dataUrlBytes(url: string): number {
  const comma = url.indexOf(',');
  if (comma < 0) throw new Error('无效的图片 data URL');
  const body = url.slice(comma + 1);
  if (/;base64$/i.test(url.slice(0, comma))) {
    const length = body.replace(/\s/g, '').length;
    return Math.floor(length * 3 / 4) - (body.endsWith('==') ? 2 : body.endsWith('=') ? 1 : 0);
  }
  return new TextEncoder().encode(decodeURIComponent(body)).byteLength;
}

/** 缓存、并发和字节预算仅属于本次任务；失败不会永久缓存图片。 */
export async function inlineLocalImages(html: string, documentPath: string | null, options: ExportTaskOptions = {}) {
  const doc = new DOMParser().parseFromString(html, 'text/html');
  const images = Array.from(doc.querySelectorAll<HTMLImageElement>('img[src]'));
  const bySource = new Map<string, HTMLImageElement[]>();
  for (const image of images) {
    const src = image.getAttribute('src')!.trim();
    const list = bySource.get(src) ?? [];
    list.push(image);
    bySource.set(src, list);
    // 内嵌后 srcset 不能再覆盖 src、重新访问应用私有地址或远程资源。
    image.removeAttribute('srcset');
    image.removeAttribute('loading');
  }
  const entries = [...bySource];
  const budget = new ExportResourceBudget();
  const warnings: string[] = [];
  const controller = new AbortController();
  const abort = () => controller.abort();
  options.signal?.addEventListener('abort', abort, { once: true });
  let next = 0;
  try {
    throwIfExportAborted(options.signal);
    const results = await Promise.allSettled(Array.from({ length: Math.min(2, entries.length) }, async () => {
      while (next < entries.length) {
        throwIfExportAborted(controller.signal);
        const [src, targets] = entries[next++];
        try {
          const data = await readImage(src, documentPath, controller.signal, budget);
          throwIfExportAborted(controller.signal);
          budget.add(dataUrlBytes(data));
          for (const target of targets) target.setAttribute('src', data);
        } catch (error) {
          if (error instanceof ExportBudgetError) { controller.abort(); throw error; }
          throwIfExportAborted(controller.signal);
          warnings.push(`图片未能内嵌（${error instanceof Error ? error.message : String(error)}）：${src.slice(0, 180)}`);
        }
      }
    }));
    const failure = results.find(result => result.status === 'rejected' && result.reason instanceof ExportBudgetError)
      ?? results.find(result => result.status === 'rejected');
    if (failure?.status === 'rejected') throw failure.reason;
    throwIfExportAborted(options.signal);
    return { html: doc.body.innerHTML, warnings };
  } finally {
    controller.abort();
    options.signal?.removeEventListener('abort', abort);
    bySource.clear();
  }
}

async function readImage(src: string, documentPath: string | null, signal: AbortSignal, budget: ExportResourceBudget): Promise<string> {
  if (/^data:/i.test(src)) { budget.checkSingle(dataUrlBytes(src)); return src; }
  if (/^(?:https?:|asset:|blob:)/i.test(src)) return fetchImage(src, signal, budget);
  let local = src;
  if (/^file:/i.test(src)) {
    const url = new URL(src);
    local = decodeURIComponent(url.pathname);
    if (/^\/[a-z]:\//i.test(local)) local = local.slice(1);
    if (url.hostname) local = `//${url.hostname}${local}`;
  }
  const resolved = await awaitExport(resolveImagePath(local, documentPath), signal, TIMEOUT);
  if (!resolved.absolutePath) throw new Error(resolved.warning || '无法解析图片路径');
  try {
    const result = await awaitExport(readFileAsBase64(resolved.absolutePath, EXPORT_IMAGE_LIMIT), signal, TIMEOUT);
    budget.checkSingle(dataUrlBytes(result.data_url));
    return result.data_url;
  } catch (error) {
    if (String(error).includes('[WORD_MEDIA_BUDGET]')) throw new ExportBudgetError();
    throw error;
  }
}

async function fetchImage(url: string, signal: AbortSignal, budget: ExportResourceBudget): Promise<string> {
  const controller = new AbortController();
  const abort = () => controller.abort();
  signal.addEventListener('abort', abort, { once: true });
  const timer = setTimeout(abort, TIMEOUT);
  let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
  try {
    throwIfExportAborted(signal);
    const response = await fetch(url, { signal: controller.signal });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    budget.checkSingle(Number(response.headers?.get('content-length')) || 0);
    let blob: Blob;
    if (response.body) {
      reader = response.body.getReader();
      const chunks: Uint8Array<ArrayBuffer>[] = [];
      let size = 0;
      for (;;) {
        const result = await reader.read();
        if (result.done) break;
        throwIfExportAborted(controller.signal);
        size += result.value.byteLength;
        budget.checkSingle(size);
        chunks.push(result.value as Uint8Array<ArrayBuffer>);
      }
      blob = new Blob(chunks, { type: response.headers.get('content-type')?.split(';')[0] || 'application/octet-stream' });
    } else blob = await response.blob();
    budget.checkSingle(blob.size);
    return await awaitExport(blobToDataUrl(blob, controller.signal), controller.signal, TIMEOUT);
  } catch (error) {
    if (controller.signal.aborted && !signal.aborted) throw new Error(`fetch timeout after ${TIMEOUT}ms`);
    throw error;
  } finally {
    controller.abort();
    clearTimeout(timer);
    signal.removeEventListener('abort', abort);
    await reader?.cancel().catch(() => undefined);
  }
}

function blobToDataUrl(blob: Blob, signal: AbortSignal): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    const cleanup = () => { signal.removeEventListener('abort', abort); reader.onload = reader.onerror = reader.onabort = null; };
    const abort = () => reader.abort();
    reader.onload = () => { const result = reader.result; cleanup(); typeof result === 'string' ? resolve(result) : reject(new Error('图片读取失败')); };
    reader.onerror = () => { const error = reader.error; cleanup(); reject(error || new Error('图片读取失败')); };
    reader.onabort = () => { cleanup(); reject(new DOMException('Cancelled', 'AbortError')); };
    signal.addEventListener('abort', abort, { once: true });
    reader.readAsDataURL(blob);
    if (signal.aborted) abort();
  });
}

import { t } from '../../i18n';
import { assertWordCanvasSize, WORD_CONTENT_WIDTH_PX, WORD_MEDIA_BUDGET, WordBudgetError, type WordMedia, type WordNode } from './protocol';

const TIMEOUT = 8_000;

/** 网络、解码和渲染等待必须有界；取消后不提交过期结果。 */
export function withWordAbort<T>(promise: Promise<T>, signal: AbortSignal, timeout = TIMEOUT): Promise<T> {
  return new Promise((resolve, reject) => {
    let settled = false;
    const abort = () => finish(() => reject(new DOMException('Cancelled', 'AbortError')));
    const timer = setTimeout(() => finish(() => reject(new Error('timeout'))), timeout);
    const finish = (callback: () => void) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      signal.removeEventListener('abort', abort);
      callback();
    };
    signal.addEventListener('abort', abort, { once: true });
    if (signal.aborted) abort();
    promise.then((value) => finish(() => resolve(value)), (error) => finish(() => reject(error)));
  });
}

/**
 * 读取 Word 导出用的图片。原生界面导出前已把本地图片替换为 data URL（离屏渲染页没有读取本地文件的接口），
 * 这里只处理 data:、blob: 与 http(s) 地址；其他地址按缺失图片处理（documentPath 保留以兼容调用方签名）。
 */
export async function readWordImage(src: string, _documentPath: string | null, signal: AbortSignal, maxBytes = WORD_MEDIA_BUDGET): Promise<Blob> {
  signal.throwIfAborted();
  const url = src.trim();
  if (!/^(?:https?:|data:|blob:)/i.test(url)) throw new Error('missing-image');
  if (url.startsWith('data:') && url.length > maxBytes * 1.4) throw new WordBudgetError('media');
  const controller = new AbortController();
  const abort = () => controller.abort();
  signal.addEventListener('abort', abort, { once: true });
  const timer = setTimeout(abort, TIMEOUT);
  let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
  try {
    signal.throwIfAborted();
    const response = await fetch(url, { signal: controller.signal });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    if (Number(response.headers.get('content-length')) > maxBytes) throw new WordBudgetError('media');
    reader = response.body?.getReader();
    if (!reader) throw new Error('empty-image');
    const chunks: Uint8Array<ArrayBuffer>[] = [];
    let size = 0;
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      signal.throwIfAborted();
      size += value.byteLength;
      if (size > maxBytes) throw new WordBudgetError('media');
      chunks.push(value as Uint8Array<ArrayBuffer>);
    }
    return new Blob(chunks, { type: response.headers.get('content-type')?.split(';')[0] || 'application/octet-stream' });
  } finally {
    await reader?.cancel().catch(() => undefined);
    clearTimeout(timer);
    signal.removeEventListener('abort', abort);
    controller.abort();
  }
}

async function decodeImage(blob: Blob, signal: AbortSignal) {
  const url = URL.createObjectURL(blob);
  const image = new Image();
  try {
    image.src = url;
    await withWordAbort(image.decode(), signal);
    assertWordCanvasSize(image.naturalWidth, image.naturalHeight);
    return { image, dispose: () => { image.src = ''; URL.revokeObjectURL(url); } };
  } catch (error) {
    image.src = '';
    URL.revokeObjectURL(url);
    throw error;
  }
}

async function rasterImage(blob: Blob, signal: AbortSignal): Promise<Omit<WordMedia, 'id'>> {
  const decoded = await decodeImage(blob, signal);
  try {
    const { naturalWidth: width, naturalHeight: height } = decoded.image;
    const signature = new Uint8Array(await blob.slice(0, 12).arrayBuffer());
    const png = signature[0] === 0x89 && signature[1] === 0x50 && signature[2] === 0x4e && signature[3] === 0x47;
    const jpg = signature[0] === 0xff && signature[1] === 0xd8;
    if (png || jpg) return { bytes: new Uint8Array(await blob.arrayBuffer()), type: png ? 'png' : 'jpg', width, height };
    const canvas = document.createElement('canvas');
    canvas.width = width;
    canvas.height = height;
    try {
      const context = canvas.getContext('2d');
      if (!context) throw new Error('canvas');
      const animatedFormat = (signature[0] === 71 && signature[1] === 73 && signature[2] === 70)
        || (signature[8] === 87 && signature[9] === 69 && signature[10] === 66 && signature[11] === 80);
      if (animatedFormat) {
        // 对动画格式固定使用首帧；SVG 则使用已解码 Image，兼容不支持 SVG Bitmap 的浏览器。
        let abandoned = false;
        let bitmap: ImageBitmap | undefined;
        try {
          const decodedBitmap = await withWordAbort<ImageBitmap>(createImageBitmap(blob).then((value) => {
            if (abandoned) { value.close(); throw new Error('abandoned-image'); }
            return value;
          }), signal);
          bitmap = decodedBitmap;
          context.drawImage(decodedBitmap, 0, 0);
        } finally { abandoned = true; bitmap?.close(); }
      } else context.drawImage(decoded.image, 0, 0);
      const output = await withWordAbort(new Promise<Blob>((resolve, reject) => canvas.toBlob((value) => value ? resolve(value) : reject(new Error('canvas')), 'image/png')), signal);
      return { bytes: new Uint8Array(await output.arrayBuffer()), type: 'png', width, height };
    } finally { canvas.width = canvas.height = 0; }
  } finally { decoded.dispose(); }
}

async function renderSpecial(node: WordNode, signal: AbortSignal): Promise<Omit<WordMedia, 'id'>> {
  const holder = document.createElement('div');
  holder.dataset.nomoWordResource = '';
  holder.style.cssText = 'position:fixed;left:-100000px;top:0;pointer-events:none;';
  const root = holder.attachShadow({ mode: 'open' });
  const element = document.createElement('div');
  element.style.cssText = 'display:inline-block;background:white;color:#202124;font:14.6667px Calibri,"宋体",serif;padding:4px;';
  let fontEmbedCSS = '';
  let style: HTMLStyleElement | undefined;
  root.append(element);
  document.body.append(holder);
  try {
    if (node.type === 'mermaid_block') {
      const { createMermaidDiagramRenderer } = await import('../../../lib/services/mermaidDiagramRenderer');
      const result = await withWordAbort(createMermaidDiagramRenderer().renderMermaid(String(node.attrs?.code ?? ''), {
        theme: { theme: 'default', themeVariables: { darkMode: 'false', background: '#ffffff', primaryTextColor: '#202124' } }, signal,
      }), signal, 30_000);
      if (result.error || !result.svg) throw new Error('diagram');
      element.innerHTML = result.svg;
      const svg = element.querySelector('svg');
      if (!svg) throw new Error('diagram');
      const box = svg.viewBox.baseVal;
      const width = box.width || Number(svg.getAttribute('width')) || WORD_CONTENT_WIDTH_PX;
      const height = box.height || Number(svg.getAttribute('height')) || width;
      assertWordCanvasSize(width + 8, height + 8, 2);
      svg.style.cssText = `width:${width}px;height:${height}px;max-width:none;`;
      svg.setAttribute('width', String(width));
      svg.setAttribute('height', String(height));
    } else {
      const [{ default: katex }, { default: css }] = await Promise.all([import('katex'), import('virtual:nomo-katex-css')]);
      fontEmbedCSS = css;
      style = document.createElement('style');
      style.textContent = css;
      root.prepend(style);
      element.innerHTML = katex.renderToString(String(node.attrs?.tex ?? ''), {
        displayMode: node.type === 'math_block', throwOnError: true, trust: false, strict: 'ignore',
      });
      if (document.fonts?.ready) await withWordAbort(document.fonts.ready, signal);
    }
    signal.throwIfAborted();
    const rect = element.getBoundingClientRect();
    const width = Math.ceil(Math.max(element.scrollWidth, rect.width));
    const height = Math.ceil(Math.max(element.scrollHeight, rect.height));
    assertWordCanvasSize(width, height, 2);
    const { toBlob } = await import('html-to-image');
    const blob = await withWordAbort(toBlob(element, { width, height, pixelRatio: 2, backgroundColor: '#ffffff', fontEmbedCSS }), signal, 30_000);
    if (!blob) throw new Error('raster');
    signal.throwIfAborted();
    return { bytes: new Uint8Array(await blob.arrayBuffer()), type: 'png', width, height };
  } finally {
    element.replaceChildren();
    style?.remove();
    holder.remove();
  }
}

/** 所有缓存仅属于一次任务，总字节成本计入预算；失败也记录，避免重复慢请求。 */
export function createWordResources(documentPath: string | null, signal: AbortSignal, warnings: Set<string>) {
  const cache = new Map<string, string | null>();
  const media: WordMedia[] = [];
  let bytes = 0;
  return {
    media,
    async resolve(node: WordNode) {
      const value = String(node.attrs?.src ?? node.attrs?.tex ?? node.attrs?.code ?? '');
      const key = `${node.type}:${value}`;
      if (cache.has(key)) return cache.get(key);
      try {
        signal.throwIfAborted();
        let result: Omit<WordMedia, 'id'>;
        if (node.type === 'image') {
          const blob = await readWordImage(value, documentPath, signal, WORD_MEDIA_BUDGET - bytes);
          if (bytes + blob.size > WORD_MEDIA_BUDGET) throw new WordBudgetError('media');
          const magic = new Uint8Array(await blob.slice(0, 3).arrayBuffer());
          if (magic[0] === 71 && magic[1] === 73 && magic[2] === 70) warnings.add(t.wordExportGifWarning());
          result = await rasterImage(blob, signal);
        } else result = await renderSpecial(node, signal);
        signal.throwIfAborted();
        bytes += result.bytes.byteLength;
        if (bytes > WORD_MEDIA_BUDGET) throw new WordBudgetError('media');
        const id = `media_${media.length}`;
        media.push({ ...result, id });
        cache.set(key, id);
        return id;
      } catch (error) {
        if (signal.aborted || error instanceof WordBudgetError) throw error;
        const kind = node.type === 'image' ? t.wordExportImage() : node.type === 'mermaid_block' ? 'Mermaid' : t.wordExportFormula();
        warnings.add(t.wordExportResourceWarning({ kind, source: value.slice(0, 80) }));
        cache.set(key, null);
        return null;
      }
    },
    dispose() { cache.clear(); media.length = 0; },
  };
}

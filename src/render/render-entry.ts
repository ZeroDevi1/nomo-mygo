// 原生外壳的离屏渲染页（render.html）：在按需创建的隐藏窗口中把一批公式与 Mermaid 图表纵向排布到 #stage，
// 返回各自的位置与 CSS 像素尺寸，由 Go 侧一次截图后分别裁剪为位图（desktop/ui/shell/render.go）。渲染调用与
// 原版编辑器相同（katexMathRenderer / mermaidDiagramRenderer），视觉一致。
import katex from 'katex';
import 'katex/dist/katex.min.css';
import { createMermaidDiagramRenderer } from '../lib/services/mermaidDiagramRenderer';

interface RenderRequest {
  kind: 'math-inline' | 'math-display' | 'mermaid' | 'svg';
  src: string;
  fg: string;
  bg: string;
  fontSize: number;
  mermaidTheme: string;
  /** 仅 svg：显示尺寸（CSS 像素）。 */
  w?: number;
  h?: number;
}

interface RenderItem {
  x: number;
  y: number;
  w: number;
  h: number;
  /** 行内公式基线以下的深度（CSS 像素），供原生侧与正文基线对齐。 */
  depth: number;
  error?: string;
  /** 本批放不下（超出离屏视口高度），由原生侧重新排队。 */
  skipped?: boolean;
}

interface RenderBatchResult {
  dpr: number;
  items: RenderItem[];
}

const mermaid = createMermaidDiagramRenderer();

// 隐藏窗口里 WebKit 会暂停 requestAnimationFrame，等待一律用定时器；读取尺寸本身会同步完成布局。
const tick = (ms = 16) => new Promise((resolve) => setTimeout(resolve, ms));

/** 把一个请求渲染进 box，返回错误信息（成功时为空）。 */
async function renderInto(box: HTMLElement, request: RenderRequest): Promise<string> {
  box.style.color = request.fg;
  box.style.fontSize = `${request.fontSize}px`;
  if (request.kind === 'svg') {
    // 以 <img> 加载而非内联：SVG 中的脚本与外部资源不会执行或加载。
    const img = new Image(request.w, request.h);
    img.style.display = 'block';
    img.src = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(request.src)}`;
    box.style.padding = '0';
    box.appendChild(img);
    try {
      await img.decode();
    } catch {
      return 'svg decode failed';
    }
    return '';
  }
  if (request.kind === 'mermaid') {
    const { svg, error } = await mermaid.renderMermaid(request.src, {
      theme: { theme: request.mermaidTheme as never, themeVariables: {} },
    });
    if (error || !svg) return error || 'empty';
    box.innerHTML = svg;
    // Mermaid 以 max-width 与 width=100% 自适应容器；这里固定为图表的自然宽度，容器按内容收缩。
    const element = box.querySelector('svg');
    const maxWidth = element ? parseFloat(element.style.maxWidth) : NaN;
    if (element && Number.isFinite(maxWidth)) element.style.width = `${maxWidth}px`;
    return '';
  }
  // 与原版一致：throwOnError 为 false，错误公式以红色原文显示，不影响后续内容。
  box.innerHTML = katex.renderToString(request.src, {
    displayMode: request.kind === 'math-display',
    throwOnError: false,
    strict: 'ignore',
  });
  if (request.kind === 'math-inline') {
    // 基线标记：零尺寸的行内块，顶边即所在行的基线。
    const marker = document.createElement('span');
    marker.className = 'nomo-baseline';
    marker.style.cssText = 'display:inline-block;width:0;height:0;vertical-align:baseline';
    box.appendChild(marker);
  }
  return '';
}

async function nomoRenderBatch(requests: RenderRequest[], viewport: number): Promise<RenderBatchResult> {
  const stage = document.getElementById('stage')!;
  stage.replaceChildren();
  document.body.style.background = requests[0]?.bg ?? 'transparent';
  const boxes: Array<{ box: HTMLElement; error: string }> = [];
  for (const request of requests) {
    const box = document.createElement('div');
    box.className = 'nomo-item';
    stage.appendChild(box);
    boxes.push({ box, error: await renderInto(box, request) });
  }
  await Promise.race([document.fonts.ready, tick(3000)]);
  await tick();
  const origin = stage.getBoundingClientRect();
  const items: RenderItem[] = [];
  let full = false;
  for (const [index, { box, error }] of boxes.entries()) {
    if (full) {
      items.push({ x: 0, y: 0, w: 0, h: 0, depth: 0, skipped: true });
      continue;
    }
    let rect = box.getBoundingClientRect();
    // 单个超出视口的大图表按比例缩小到视口内（只在它是本批第一个时；否则留到下一批单独渲染）。
    if ((rect.width > viewport || rect.height > viewport) && index === 0) {
      box.style.zoom = String(Math.min(viewport / rect.width, viewport / rect.height));
      await tick();
      rect = box.getBoundingClientRect();
    }
    if (rect.bottom - origin.top > viewport) {
      full = true;
      items.push({ x: 0, y: 0, w: 0, h: 0, depth: 0, skipped: true });
      continue;
    }
    const marker = box.querySelector('.nomo-baseline');
    const depth = marker ? Math.max(0, rect.bottom - marker.getBoundingClientRect().top) : 0;
    items.push({
      x: Math.floor(rect.left - origin.left),
      y: Math.floor(rect.top - origin.top),
      w: Math.ceil(rect.width),
      h: Math.ceil(rect.height),
      depth,
      error: error || undefined,
    });
  }
  return { dpr: devicePixelRatio, items };
}

interface MarkupRequest {
  kind: 'math-inline' | 'math-display' | 'mermaid';
  src: string;
  mermaidTheme: string;
}

/** 导出用：返回 KaTeX HTML 或 Mermaid SVG 标记（原版导出同样内联这两种结果），以及按需附带的 KaTeX 样式。 */
async function nomoRenderMarkup(
  requests: MarkupRequest[],
): Promise<{ css: string; items: Array<{ html: string; error?: string }> }> {
  const items: Array<{ html: string; error?: string }> = [];
  let usesMath = false;
  for (const request of requests) {
    if (request.kind === 'mermaid') {
      const { svg, error } = await mermaid.renderMermaid(request.src, {
        theme: { theme: request.mermaidTheme as never, themeVariables: {} },
      });
      items.push(error || !svg ? { html: '', error: error || 'empty' } : { html: svg });
      continue;
    }
    usesMath = true;
    const html = katex.renderToString(request.src, {
      displayMode: request.kind === 'math-display',
      throwOnError: false,
      strict: 'ignore',
    });
    items.push(html.includes('katex-error') ? { html: '', error: '公式语法错误' } : { html });
  }
  // KaTeX 样式内联了全部字体（约 1 MB），只在导出且用到公式时加载，平时创建离屏窗口不加载。
  const css = usesMath ? (await import('virtual:nomo-katex-css')).default : '';
  return { css, items };
}
(window as unknown as { nomoRenderMarkup: typeof nomoRenderMarkup }).nomoRenderMarkup = nomoRenderMarkup;

/**
 * 导出 Word：复用原版的文档模型与打包（word/model.ts、word/document.ts），返回 base64 编码的 .docx 与降级说明。
 * 本地图片已由原生侧替换为 data URL（离屏页面没有读取本地文件的接口）。模块按需加载，平时不进入渲染页。
 */
async function nomoExportWord(markdown: string, title: string): Promise<{ data: string; warnings: string[] }> {
  const [{ createWordModel }, { createWordResources }, { buildWordDocument }] = await Promise.all([
    import('../app/services/word/model'),
    import('../app/services/word/resources'),
    import('../app/services/word/document'),
  ]);
  const signal = new AbortController().signal;
  const warnings = new Set<string>();
  const resources = createWordResources(null, signal, warnings);
  try {
    const model = await createWordModel(markdown, title, signal, resources.resolve, warnings);
    model.media = resources.media;
    const bytes = new Uint8Array(await buildWordDocument(model));
    let binary = '';
    for (let i = 0; i < bytes.length; i += 0x8000) binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
    return { data: btoa(binary), warnings: [...warnings] };
  } finally {
    resources.dispose();
  }
}
(window as unknown as { nomoExportWord: typeof nomoExportWord }).nomoExportWord = nomoExportWord;

// 一批渲染至多 20 秒（如 Mermaid 卡在超大图上），超时按失败返回，不让原生侧的串行队列一直等待。
(window as unknown as { nomoRenderBatch: typeof nomoRenderBatch }).nomoRenderBatch = (requests, viewport) =>
  Promise.race([
    nomoRenderBatch(requests, viewport),
    tick(20000).then(() => ({
      dpr: devicePixelRatio,
      items: requests.map(() => ({ x: 0, y: 0, w: 0, h: 0, depth: 0, error: 'render timeout' })),
    })),
  ]);

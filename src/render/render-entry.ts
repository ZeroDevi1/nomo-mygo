// 原生外壳的离屏渲染页（render.html）：在按需创建的隐藏窗口中把公式与 Mermaid 图表渲染到 #stage，
// 返回其 CSS 像素尺寸，由 Go 侧截图裁剪为位图（desktop/ui/shell/render.go）。渲染调用与原版编辑器相同
// （katexMathRenderer / mermaidDiagramRenderer），视觉一致。
import katex from 'katex';
import 'katex/dist/katex.min.css';
import { createMermaidDiagramRenderer } from '../lib/services/mermaidDiagramRenderer';

interface RenderRequest {
  kind: 'math-inline' | 'math-display' | 'mermaid';
  src: string;
  fg: string;
  bg: string;
  fontSize: number;
  mermaidTheme: string;
  viewport: number;
}

interface RenderResult {
  w: number;
  h: number;
  dpr: number;
  /** 行内公式基线以下的深度（CSS 像素），供原生侧与正文基线对齐。 */
  depth?: number;
  error?: string;
}

const mermaid = createMermaidDiagramRenderer();

// 隐藏窗口里 WebKit 会暂停 requestAnimationFrame，等待一律用定时器；读取尺寸本身会同步完成布局。
const tick = (ms = 16) => new Promise((resolve) => setTimeout(resolve, ms));

async function nomoRender(request: RenderRequest): Promise<RenderResult> {
  const stage = document.getElementById('stage')!;
  document.body.style.background = request.bg;
  stage.style.color = request.fg;
  stage.style.fontSize = `${request.fontSize}px`;
  stage.replaceChildren();
  stage.style.zoom = '';
  if (request.kind === 'mermaid') {
    const { svg, error } = await mermaid.renderMermaid(request.src, {
      theme: { theme: request.mermaidTheme as never, themeVariables: {} },
    });
    if (error || !svg) return { w: 0, h: 0, dpr: devicePixelRatio, error: error || 'empty' };
    stage.innerHTML = svg;
    // Mermaid 以 max-width 与 width=100% 自适应容器；舞台按内容收缩，这里固定为图表的自然宽度。
    const element = stage.querySelector('svg');
    const maxWidth = element ? parseFloat(element.style.maxWidth) : NaN;
    if (element && Number.isFinite(maxWidth)) element.style.width = `${maxWidth}px`;
  } else {
    // 与原版一致：throwOnError 为 false，错误公式以红色原文显示，不影响后续内容。
    stage.innerHTML = katex.renderToString(request.src, {
      displayMode: request.kind === 'math-display',
      throwOnError: false,
      strict: 'ignore',
    });
  }
  // 基线标记：零尺寸的行内块，顶边即所在行的基线。
  const marker = document.createElement('span');
  marker.style.cssText = 'display:inline-block;width:0;height:0;vertical-align:baseline';
  if (request.kind === 'math-inline') stage.appendChild(marker);
  await Promise.race([document.fonts.ready, tick(3000)]);
  await tick();
  let rect = stage.getBoundingClientRect();
  // 超出离屏窗口的大图表按比例缩小到窗口内再截图。
  if (rect.width > request.viewport || rect.height > request.viewport) {
    stage.style.zoom = String(Math.min(request.viewport / rect.width, request.viewport / rect.height));
    await tick();
    rect = stage.getBoundingClientRect();
  }
  const depth = request.kind === 'math-inline' ? Math.max(0, rect.bottom - marker.getBoundingClientRect().top) : 0;
  return { w: Math.ceil(rect.width), h: Math.ceil(rect.height), dpr: devicePixelRatio, depth };
}

// 单次渲染至多 15 秒（如 Mermaid 卡在超大图上），超时按失败返回，不让原生侧的串行队列一直等待。
(window as unknown as { nomoRender: typeof nomoRender }).nomoRender = (request) =>
  Promise.race([
    nomoRender(request),
    tick(15000).then(() => ({ w: 0, h: 0, dpr: devicePixelRatio, error: 'render timeout' })),
  ]);

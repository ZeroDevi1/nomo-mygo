// 原生外壳的离屏渲染页（render.html）：在按需创建的隐藏窗口中把一批公式与 Mermaid 图表纵向排布到 #stage，返回各自的
// 位置与 CSS 像素尺寸，由 Go 侧一次截图后分别裁剪为位图（desktop/ui/shell/render.go）；导出时返回 KaTeX HTML 与
// Mermaid SVG 标记。渲染参数与原版编辑器一致（KaTeX throwOnError=false、Mermaid securityLevel=strict）。
// KaTeX 随页面加载；Mermaid 约 3 MB，第一次遇到图表时才加载。
'use strict';

// 隐藏窗口里 WebKit 会暂停 requestAnimationFrame，等待一律用定时器；读取尺寸本身会同步完成布局。
const tick = (ms = 16) => new Promise((resolve) => setTimeout(resolve, ms));

let mermaidLoading = null;
let mermaidThemeKey = null;
let renderSequence = 0;

/** 加载 Mermaid（只加载一次），主题变化时重新初始化。 */
async function loadMermaid(theme) {
  mermaidLoading ??= new Promise((resolve, reject) => {
    const script = document.createElement('script');
    script.src = 'mermaid/mermaid.min.js';
    script.onload = () => resolve(window.mermaid);
    script.onerror = () => reject(new Error('Mermaid 加载失败'));
    document.head.appendChild(script);
  });
  const mermaid = await mermaidLoading;
  if (mermaidThemeKey !== theme) {
    mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme: theme || 'default' });
    mermaidThemeKey = theme;
  }
  return mermaid;
}

/** 渲染一张 Mermaid 图表，返回 { svg, error }。Mermaid 渲染失败时会在 body 留下临时节点，这里一并清理。 */
async function renderMermaid(code, theme) {
  renderSequence += 1;
  const id = `nomo-mermaid-${renderSequence}`;
  try {
    const mermaid = await loadMermaid(theme);
    const result = await mermaid.render(id, code);
    return { svg: result.svg, error: '' };
  } catch (error) {
    return { svg: '', error: error instanceof Error ? error.message : 'Mermaid 渲染失败' };
  } finally {
    document.getElementById(`d${id}`)?.remove();
    document.getElementById(`i${id}`)?.remove();
  }
}

function renderMath(src, display) {
  // 与原版一致：throwOnError 为 false，错误公式以红色原文显示，不影响后续内容。
  return window.katex.renderToString(src, { displayMode: display, throwOnError: false, strict: 'ignore' });
}

/** 把一个请求渲染进 box，返回错误信息（成功时为空）。 */
async function renderInto(box, request) {
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
    const { svg, error } = await renderMermaid(request.src, request.mermaidTheme);
    if (error || !svg) return error || 'empty';
    box.innerHTML = svg;
    // Mermaid 以 max-width 与 width=100% 自适应容器；这里固定为图表的自然宽度，容器按内容收缩。
    const element = box.querySelector('svg');
    const maxWidth = element ? parseFloat(element.style.maxWidth) : NaN;
    if (element && Number.isFinite(maxWidth)) element.style.width = `${maxWidth}px`;
    return '';
  }
  box.innerHTML = renderMath(request.src, request.kind === 'math-display');
  if (request.kind === 'math-inline') {
    // 基线标记：零尺寸的行内块，顶边即所在行的基线。
    const marker = document.createElement('span');
    marker.className = 'nomo-baseline';
    marker.style.cssText = 'display:inline-block;width:0;height:0;vertical-align:baseline';
    box.appendChild(marker);
  }
  return '';
}

/**
 * 渲染一批请求并测量：返回 { dpr, items }，items[k] 为 { x, y, w, h, depth, error?, skipped? }（CSS 像素，相对 #stage）。
 * 超出视口高度的项标记 skipped，由原生侧重新排队；本批第一个超大的图表按比例缩小到视口内。
 */
window.nomoRenderBatch = async function nomoRenderBatch(requests, viewport) {
  const stage = document.getElementById('stage');
  stage.replaceChildren();
  document.body.style.background = requests[0]?.bg ?? 'transparent';
  const boxes = [];
  for (const request of requests) {
    const box = document.createElement('div');
    box.className = 'nomo-item';
    stage.appendChild(box);
    boxes.push({ box, error: await renderInto(box, request) });
  }
  await Promise.race([document.fonts.ready, tick(3000)]);
  await tick();
  const origin = stage.getBoundingClientRect();
  const items = [];
  let full = false;
  for (const [index, { box, error }] of boxes.entries()) {
    if (full) {
      items.push({ x: 0, y: 0, w: 0, h: 0, depth: 0, skipped: true });
      continue;
    }
    let rect = box.getBoundingClientRect();
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
};

/** 导出用：返回 { items: [{ html, error? }] }，公式为 KaTeX HTML、图表为 Mermaid SVG（样式由原生侧内联）。 */
window.nomoRenderMarkup = async function nomoRenderMarkup(requests) {
  const items = [];
  for (const request of requests) {
    if (request.kind === 'mermaid') {
      const { svg, error } = await renderMermaid(request.src, request.mermaidTheme);
      items.push(error || !svg ? { html: '', error: error || 'empty' } : { html: svg });
      continue;
    }
    const html = renderMath(request.src, request.kind === 'math-display');
    items.push(html.includes('katex-error') ? { html: '', error: '公式语法错误' } : { html });
  }
  return { items };
};

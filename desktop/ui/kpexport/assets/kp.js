// 导出页面的精细排版（Knuth–Plass）：测量段落各片段宽度，交给 WebAssembly 中的排版算法（desktop/ui/typography，
// 与编辑区相同）计算断行，再按结果插入换行、字距调整、行首压缩与断词连字符。打开 HTML 时按窗口宽度排版，窗口尺寸
// 变化后重排；导出 PDF 时主程序在打印前调用 __NOMO_PREPARE_PRINT__ 按版心宽度重排（desktop/export.go）。
// 只处理纯行内内容的段落、列表项与表格单元格；含图片、公式、SVG 或居中/右对齐的段落保持浏览器排版。
// 依赖：同页面的 Go wasm_exec.js（全局 Go）与 #nomo-kp-wasm（gzip + base64 的 WebAssembly）。
(() => {
  'use strict';

  const INLINE = new Set(['STRONG', 'B', 'EM', 'I', 'U', 'INS', 'S', 'DEL', 'STRIKE', 'MARK', 'CODE', 'KBD', 'SAMP',
    'VAR', 'CITE', 'DFN', 'ABBR', 'SMALL', 'SUB', 'SUP', 'SPAN', 'A', 'TIME', 'BDI', 'BR']);
  const ATOMIC = new Set(['CODE', 'KBD', 'SAMP']); // 整段为一个排版盒（与编辑区的行内代码一致）
  const originals = new Map(); // 已排版元素 → 原始 HTML（重排前恢复）
  const segmenter = typeof Intl !== 'undefined' && Intl.Segmenter ? new Intl.Segmenter(undefined, { granularity: 'grapheme' }) : null;
  const canvas = document.createElement('canvas').getContext('2d');
  let ready = null;

  /** 解压并启动 WebAssembly（只启动一次）。 */
  function start() {
    ready ??= (async () => {
      const b64 = document.getElementById('nomo-kp-wasm').textContent.trim();
      const bin = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
      const stream = new Blob([bin]).stream().pipeThrough(new DecompressionStream('gzip'));
      const bytes = await new Response(stream).arrayBuffer();
      const go = new Go();
      const { instance } = await WebAssembly.instantiate(bytes, go.importObject);
      go.run(instance);
      if (typeof globalThis.nomoKPLayout !== 'function') throw new Error('typography wasm unavailable');
    })();
    return ready;
  }

  function graphemes(text) {
    if (segmenter) return Array.from(segmenter.segment(text), (s) => s.segment);
    return Array.from(text);
  }

  function fontOf(el) {
    const cs = getComputedStyle(el);
    return `${cs.fontStyle} ${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily}`;
  }

  /** 段落是否只含可排版的行内内容。 */
  function eligible(el) {
    const align = getComputedStyle(el).textAlign;
    if (align === 'center' || align === 'right' || align === 'end') return false;
    const walk = (node) => {
      for (const child of node.childNodes) {
        if (child.nodeType === Node.TEXT_NODE) continue;
        if (child.nodeType !== Node.ELEMENT_NODE || !INLINE.has(child.tagName)) return false;
        if (child.classList.contains('katex') || child.classList.contains('math')) return false;
        if (!ATOMIC.has(child.tagName) && !walk(child)) return false;
      }
      return true;
    };
    return el.textContent.trim() !== '' && walk(el);
  }

  /** 候选元素：段落、只含行内内容的列表项与表格单元格。 */
  function targets(root) {
    const out = [];
    for (const el of root.querySelectorAll('p, li, td, th')) {
      if (el.closest('pre, .nomo-callout-title')) continue;
      if (el.tagName === 'LI' && el.querySelector('p, ul, ol, div, table')) continue;
      if (eligible(el)) out.push(el);
    }
    return out;
  }

  /** 把元素内的空白规整为单个空格（与浏览器折叠一致），去掉首尾空白。 */
  function normalize(el) {
    const nodes = [];
    const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
    while (walker.nextNode()) nodes.push(walker.currentNode);
    let prevSpace = true;
    for (const node of nodes) {
      let out = '';
      for (const ch of node.data.replace(/[\t\n\r ]+/g, ' ')) {
        if (ch === ' ' && prevSpace) continue;
        out += ch;
        prevSpace = ch === ' ';
      }
      node.data = out;
    }
    for (let k = nodes.length - 1; k >= 0; k--) {
      const t = nodes[k].data.replace(/ +$/, '');
      nodes[k].data = t;
      if (t) break;
    }
  }

  /** 测量片段：普通文字逐个字素（前缀宽度差），行内代码整段（含内边距），<br> 为强制断行。 */
  function measure(el) {
    const pieces = [];
    const visit = (node, link) => {
      for (const child of node.childNodes) {
        if (child.nodeType === Node.TEXT_NODE) {
          const parent = child.parentElement;
          canvas.font = fontOf(parent);
          const size = parseFloat(getComputedStyle(parent).fontSize);
          let prefix = '';
          let x = 0;
          for (const g of graphemes(child.data)) {
            prefix += g;
            const nx = canvas.measureText(prefix).width;
            pieces.push({ t: g, w: Math.max(0, nx - x), s: size, link: link || undefined });
            x = nx;
          }
        } else if (child.tagName === 'BR') {
          pieces.push({ t: '\n', w: 0, br: true });
        } else if (ATOMIC.has(child.tagName)) {
          const size = parseFloat(getComputedStyle(child).fontSize);
          pieces.push({ t: child.textContent, w: child.getBoundingClientRect().width, s: size, code: true });
        } else {
          visit(child, link || child.tagName === 'A');
        }
      }
    };
    visit(el, false);
    return pieces;
  }

  /** 返回元素内第 offset 个 UTF-16 单元所在的文本节点与节点内偏移；offset 位于两个节点之间时取后一个节点的开头，
   * 位于全部文字末尾时取最后一个节点的末尾。 */
  function locate(el, offset) {
    const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
    let at = 0;
    let last = null;
    while (walker.nextNode()) {
      const node = walker.currentNode;
      if (offset < at + node.length) return { node, offset: offset - at };
      at += node.length;
      last = node;
    }
    return last ? { node: last, offset: last.length } : null;
  }

  /** 把 [at, at+n) 包进带样式的 span。 */
  function wrap(el, at, n, style) {
    const a = locate(el, at);
    if (!a || a.offset + n > a.node.length) return;
    const range = document.createRange();
    range.setStart(a.node, a.offset);
    range.setEnd(a.node, a.offset + n);
    const span = document.createElement('span');
    span.style.cssText = style;
    range.surroundContents(span);
  }

  /** 在 offset 处插入节点（行末连字符、换行）。 */
  function insertAt(el, offset, node) {
    const a = locate(el, offset);
    if (!a) return;
    const tail = a.offset < a.node.length ? a.node.splitText(a.offset) : null;
    a.node.parentNode.insertBefore(node, tail ?? a.node.nextSibling);
  }

  function layoutElement(el) {
    normalize(el);
    const pieces = measure(el);
    const cs = getComputedStyle(el);
    const width = el.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
    canvas.font = fontOf(el);
    const req = {
      width,
      minLineHeight: parseFloat(cs.lineHeight) || parseFloat(cs.fontSize) * 1.5,
      fontSize: parseFloat(cs.fontSize),
      spaceWidth: canvas.measureText(' ').width,
      hyphenWidth: canvas.measureText('-').width,
      pieces,
    };
    const res = JSON.parse(globalThis.nomoKPLayout(JSON.stringify(req)));
    if (!res.ok || res.lines.length < 2) return false;
    // 从后往前修改，前面的偏移保持有效：先插入换行（含连字符），再加字距与行首压缩。
    const lines = res.lines;
    for (let k = lines.length - 1; k >= 0; k--) {
      const line = lines[k];
      for (const op of (line.ops || []).sort((x, y) => y.at - x.at)) {
        wrap(el, op.at, op.n, `letter-spacing:${op.px.toFixed(3)}px`);
      }
      if (line.trim > 0) wrap(el, line.u0, 1, `margin-left:${(-line.trim).toFixed(3)}px`);
      if (k > 0) {
        insertAt(el, line.u0, document.createElement('br'));
        if (lines[k - 1].hyphen) {
          const hy = document.createElement('span');
          hy.textContent = '-';
          insertAt(el, line.u0, hy);
        }
      }
    }
    el.style.whiteSpace = 'nowrap';
    el.style.textAlign = 'left';
    return true;
  }

  /** 恢复原始内容后重新排版全部段落。 */
  async function relayout() {
    await start();
    if (document.fonts && document.fonts.ready) await document.fonts.ready;
    for (const [el, html] of originals) {
      el.innerHTML = html;
      el.style.whiteSpace = '';
      el.style.textAlign = '';
    }
    originals.clear();
    const root = document.querySelector('.nomo-export') || document.body;
    for (const el of targets(root)) {
      const html = el.innerHTML;
      try {
        if (layoutElement(el)) originals.set(el, html);
        else el.innerHTML = html;
      } catch {
        el.innerHTML = html; // 单段失败保持浏览器排版
      }
    }
  }

  // PDF：按版心宽度（毫米）重排后打印。
  window.__NOMO_PREPARE_PRINT__ = async (widthMM) => {
    const root = document.querySelector('.nomo-export');
    if (root && widthMM > 0) {
      root.style.width = `${widthMM}mm`;
      root.style.maxWidth = 'none';
      root.style.padding = '0';
      root.style.margin = '0';
    }
    await relayout();
  };

  let timer = 0;
  window.addEventListener('resize', () => {
    clearTimeout(timer);
    timer = setTimeout(() => relayout().catch(() => {}), 200);
  });
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', () => relayout().catch(() => {}));
  } else {
    relayout().catch(() => {});
  }
})();

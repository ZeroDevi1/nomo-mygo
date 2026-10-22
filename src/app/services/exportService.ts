export { resolveImagePath } from './exportImagePath';
import { restoreMermaidExportSize } from '../../lib/services/mermaidDiagramView';
import {
  exportHtmlFile,
  exportPdfFromHtml,
} from '../../lib/desktop/desktopStorage';
import { logDebug, logInfo, logWarn } from '../../lib/services/logger';
import exportCssContent from '../styles/export-document.css?inline';
import typographyRuntime from 'virtual:nomo-typography-runtime';
import katexCss from 'virtual:nomo-katex-css';
import { captureExportAppearance } from './export/snapshot';
import { renderExportBody } from './export/render';
import { throwIfExportAborted, type ExportSnapshot, type ExportAppearance, type ExportTaskOptions } from './export/types';
export { inlineLocalImages } from './export/images';
import { inlineLocalImages } from './export/images';

const PDF_OUTLINE_MARKER_HOST = 'nomo-pdf-outline.invalid';

interface PdfOutlineEntry {
  marker_uri: string;
  title: string;
  level: number;
}

export type ExportDocumentInput = ExportSnapshot;

export interface ExportResult {
  success: boolean;
  filePath?: string;
  cancelled?: boolean;
  error?: string;
  warnings?: string[];
}

/**
 * 导出当前文档为单文件 HTML。
 */
export async function exportHtml(input: ExportDocumentInput, options: ExportTaskOptions = {}): Promise<ExportResult> {
  return perfAsync('exportService', 'exportHtml', async () => {
    throwIfExportAborted(options.signal);
    logInfo('exportService', '开始导出 HTML', {
      title: input.title,
      documentPath: input.documentPath,
    });

    const defaultName = input.suggestedFileName || 'Untitled';
    const filePath = await pickSavePath(`${defaultName}.html`, [
      { name: 'HTML', extensions: ['html', 'htm'] },
    ]);

    if (!filePath) {
      logInfo('exportService', '用户取消保存 HTML');
      return { success: false, cancelled: true };
    }

    const { html, warnings } = await buildExportDocument(input, options);
    throwIfExportAborted(options.signal);
    const result = await exportHtmlFile({ html_content: html, file_path: filePath });
    logInfo('exportService', 'HTML 导出完成', { filePath, bytes: result.bytes_written });
    return { success: true, filePath, warnings };
  }).catch((error) => {
    const message = error instanceof Error ? error.message : String(error);
    logWarn('exportService', 'HTML 导出失败', { error: message });
    return { success: false, error: message };
  });
}

/**
 * 导出当前文档为 PDF（复用同一份内嵌图片的 HTML）。
 */
export async function exportPdf(input: ExportDocumentInput, options: ExportTaskOptions = {}): Promise<ExportResult> {
  return perfAsync('exportService', 'exportPdf', async () => {
    throwIfExportAborted(options.signal);
    logInfo('exportService', '开始导出 PDF', {
      title: input.title,
      documentPath: input.documentPath,
    });

    const defaultName = input.suggestedFileName || 'Untitled';
    const filePath = await pickSavePath(`${defaultName}.pdf`, [
      { name: 'PDF', extensions: ['pdf'] },
    ]);

    if (!filePath) {
      logInfo('exportService', '用户取消保存 PDF');
      return { success: false, cancelled: true };
    }

    const { html, warnings } = await buildExportDocument(input, options);
    throwIfExportAborted(options.signal);
    const pdfDocument = addPdfOutlineMarkers(html);
    const result = await exportPdfFromHtml({
      html_content: pdfDocument.html,
      file_path: filePath,
      paper_size: 'A4',
      orientation: 'portrait',
      margins: { top: 20, right: 20, bottom: 20, left: 20 },
      print_background: true,
      outline: pdfDocument.outline,
    });

    const mergedWarnings = [...warnings, ...(result.warnings ?? [])];
    logInfo('exportService', 'PDF 导出完成', {
      filePath,
      bytes: result.bytes_written,
      outlineCount: pdfDocument.outline.length,
      warningCount: mergedWarnings.length,
    });
    return { success: true, filePath, warnings: mergedWarnings };
  }).catch((error) => {
    const message = error instanceof Error ? error.message : String(error);
    logWarn('exportService', 'PDF 导出失败', { error: message });
    return { success: false, error: message };
  });
}

/**
 * 为 PDF 打印副本加入标题定位标记。标记链接会在 Rust 后处理中转换为 PDF 书签并移除。
 */
export function addPdfOutlineMarkers(html: string): { html: string; outline: PdfOutlineEntry[] } {
  const parser = new DOMParser();
  const doc = parser.parseFromString(html, 'text/html');
  const headings = Array.from(
    doc.querySelectorAll(
      '.nomo-export h1, .nomo-export h2, .nomo-export h3, .nomo-export h4, .nomo-export h5, .nomo-export h6',
    ),
  );
  const outline: PdfOutlineEntry[] = [];
  const jobId = createPdfOutlineJobId();

  for (const heading of headings) {
    const title = (heading.getAttribute('data-nomo-heading-title') ?? heading.textContent ?? '').replace(/\s+/g, ' ').trim();
    if (!title) {
      continue;
    }

    const level = Number.parseInt(heading.tagName.slice(1), 10);
    const markerUri = `https://${PDF_OUTLINE_MARKER_HOST}/${jobId}/${outline.length}`;
    const marker = doc.createElement('a');
    marker.className = 'nomo-pdf-outline-marker';
    marker.href = markerUri;
    marker.setAttribute('aria-hidden', 'true');
    marker.setAttribute('tabindex', '-1');
    heading.classList.add('nomo-pdf-outline-heading');
    heading.prepend(marker);
    outline.push({ marker_uri: markerUri, title, level });
  }

  if (outline.length > 0) {
    const style = doc.createElement('style');
    style.textContent = `
.nomo-pdf-outline-heading { position: relative; }
.nomo-pdf-outline-marker {
  position: absolute;
  top: 0;
  left: 0;
  display: block;
  width: 1px;
  height: 1px;
  overflow: hidden;
  color: transparent;
  font-size: 0;
  line-height: 0;
  text-decoration: none;
}
`;
    doc.head.append(style);
  }

  return {
    html: `<!DOCTYPE html>\n${doc.documentElement.outerHTML}`,
    outline,
  };
}

function createPdfOutlineJobId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
}

/** HTML 与 PDF 共用同一份只读正文、资源及外观快照。 */
export async function buildExportDocument(input: ExportSnapshot, options: ExportTaskOptions = {}): Promise<{ html: string; warnings: string[] }> {
  const rendered = await renderExportBody(input, options);
  const resources = await inlineLocalImages(rendered.html, input.documentPath, options);
  throwIfExportAborted(options.signal);
  return {
    html: createExportHtmlDocument(resources.html, input.title, exportCssContent, input.appearance),
    warnings: [...rendered.warnings, ...resources.warnings],
  };
}

/**
 * 清理编辑器 UI 痕迹，只保留正文渲染结果。
 */
export function cleanEditorArtifacts(htmlFragment: string): string {
  const parser = new DOMParser();
  const doc = parser.parseFromString(htmlFragment, 'text/html');
  const root = doc.body;

  if (!root.children.length) {
    return htmlFragment;
  }

  restoreMermaidExportSize(root);
  root.querySelectorAll('colgroup[data-table-layout-owned]').forEach(node => node.remove());
  root.querySelectorAll<HTMLElement>('table[data-table-layout]').forEach(table => {
    table.style.removeProperty('width');
    table.style.removeProperty('table-layout');
    table.style.removeProperty('zoom');
    for (const attribute of [...table.attributes]) if (attribute.name.startsWith('data-table-layout')) table.removeAttribute(attribute.name);
  });
  // Remove visual layout while retaining the semantic soft/hard break distinction.
  root.querySelectorAll('[data-kp-owned]').forEach((node) => node.remove());
  root.querySelectorAll('[data-kp-reflow],[data-kp-geometry]').forEach((node) => {
    node.removeAttribute('data-kp-reflow');
    node.removeAttribute('data-kp-geometry');
  });
  root.querySelectorAll<HTMLElement>('[data-kp-original-style]').forEach((node) => {
    const style = node.getAttribute('data-kp-original-style');
    if (style) node.setAttribute('style', style);
    else node.removeAttribute('style');
    node.removeAttribute('data-kp-original-style');
    node.removeAttribute('data-kp-layout');
  });
  root.querySelectorAll<HTMLElement>('.kp-unit').forEach((node) => {
    if (node.hasAttribute('data-kp-unit-original-style')) {
      const style = node.getAttribute('data-kp-unit-original-style');
      if (style) node.setAttribute('style', style);
      else node.removeAttribute('style');
      node.classList.remove('kp-unit');
      node.removeAttribute('data-kp-unit-original-style');
    } else node.replaceWith(...node.childNodes);
  });
  root.querySelectorAll<HTMLElement>('span[data-nomo-break]').forEach((node) => {
    const br = doc.createElement('br');
    br.dataset.nomoBreak = node.dataset.nomoBreak;
    node.replaceWith(br);
  });

  // 移除 ProseMirror 选区高亮、挂件、光标等编辑器痕迹。
  const selectorsToRemove = [
    '.ProseMirror-widget',
    '.ProseMirror-cursor',
    '.image-node-fullscreen-button',
    '.image-size-editor-overlay',
    '.image-fullscreen-overlay',
    '.image-node-placeholder',
    '.code-copy-button',
    '.code-expand-button',
    '.code-card .lang-selector',
    '.toc-delete',
    '.code-edit-area',
    '.inline-comment-editor',
    '.inline-comment-edit-hint',
    '.footnote-backref',
    '.table-resize-handle',
    '.mermaid-block-fullscreen-button',
    '.callout-type-picker',
    '[contenteditable="false"]:not(.image-node):not(.mermaid-block):not(.table-widget):not(.horizontal-rule-node):not(.math-inline):not(.math-block):not(.footnote-ref):not(.code-card):not(.toc-block)',
  ];

  for (const selector of selectorsToRemove) {
    const elements = root.querySelectorAll(selector);
    elements.forEach((el) => el.remove());
  }

  // 选区高亮是正文节点上的 class，只移除编辑器类名，保留节点和正文样式。
  root.querySelectorAll('[class*="ProseMirror"]').forEach((el) => {
    const classes = Array.from(el.classList).filter((cls) => !cls.startsWith('ProseMirror'));
    el.className = classes.join(' ');
  });

  // 代码正文来自展示层；导出时去掉编辑态隐藏和行号滚动偏移，完整显示所有代码行。
  root.querySelectorAll<HTMLElement>('.code-card').forEach((node) => {
    node.classList.remove('is-editing', 'is-expanded');
    node.querySelector<HTMLElement>('.code-content')?.style.removeProperty('display');
    node.querySelector<HTMLElement>('.line-numbers-wrapper')?.style.removeProperty('transform');
    const languageLabel = node.querySelector<HTMLElement>('header > span');
    languageLabel?.style.removeProperty('cursor');
    languageLabel?.removeAttribute('title');
  });

  // 图片节点只保留 img 本身，同时将对齐样式从 wrapper 迁移到 img。
  root.querySelectorAll('.image-node').forEach((node) => {
    const img = node.querySelector('img');
    if (img) {
      const wrapper = node as HTMLElement;
      // ImageNodeView 将对齐（居中/右对齐/左对齐）通过内联样式设置在 .image-node wrapper 上，
      // 导出 HTML 中没有 .image-node 包装，需要将这些样式迁移到 <img> 内联样式。
      if (wrapper.style.display === 'block') {
        img.style.display = 'block';
      }
      if (wrapper.style.marginLeft) {
        img.style.marginLeft = wrapper.style.marginLeft;
      }
      if (wrapper.style.marginRight) {
        img.style.marginRight = wrapper.style.marginRight;
      }
      node.replaceWith(img);
    } else {
      node.remove();
    }
  });

  // 水平分割线节点只保留 hr 本身。
  root.querySelectorAll('.horizontal-rule-node').forEach((node) => {
    const hr = node.querySelector('hr');
    if (hr) {
      node.replaceWith(hr);
    } else {
      node.remove();
    }
  });

  // 移除所有 contenteditable 属性——导出 HTML 是只读文档，
  // 若保留 contenteditable="true"，浏览器点击时会显示 focus outline 黑边框。
  root.querySelectorAll('[contenteditable]').forEach((el) => {
    el.removeAttribute('contenteditable');
  });

  return root.innerHTML;
}

/**
 * 创建完整 HTML 文档外壳。
 */
export function createExportHtmlDocument(
  bodyHtml: string,
  title: string,
  cssContent: string,
  appearance: ExportAppearance = captureExportAppearance(),
): string {
  const escapedTitle = escapeHtml(title || 'Exported Document');
  const configuration = JSON.stringify(appearance.typography).replace(/</g, '\\u003c');
  const style = document.createElement('div').style;
  for (const [name, value] of Object.entries(appearance.cssVariables)) style.setProperty(name, value);
  style.setProperty('--nomo-export-width', `${appearance.maxWidthPx}px`);
  style.setProperty('color-scheme', appearance.attributes['data-theme']);
  const attributes = Object.entries(appearance.attributes).map(([key, value]) => `${key}="${escapeHtml(value)}"`).join(' ');
  const usesKatex = /class=["'][^"']*\bkatex(?:\s|["'])/.test(bodyHtml);
  const css = `${cssContent}\n${usesKatex ? katexCss : ''}\n:root{${style.cssText}}`;
  return `<!DOCTYPE html>
<html lang="${escapeHtml(appearance.locale)}" ${attributes}>
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>${escapedTitle}</title>
  <style>${css.replace(/<\/style/gi, '<\\/style')}</style>
</head>
<body><article class="nomo-export markdown-body">${bodyHtml}</article>
  <script id="nomo-typography-options" type="application/json">${configuration}</script>
  <script>${typographyRuntime.replace(/<\/script/gi, '<\\/script')}</script>
</body>
</html>
`;
}

/**
 * 弹出保存路径选择对话框。
 */
async function pickSavePath(
  defaultName: string,
  filters: Array<{ name: string; extensions: string[] }>,
): Promise<string | null> {
  const { save } = await import('../../lib/desktop/runtime/dialog');
  return save({
    defaultPath: defaultName,
    filters,
  });
}

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

// 简化 perfAsync 本地实现，避免循环依赖。
function perfAsync<T>(namespace: string, name: string, fn: () => Promise<T>): Promise<T> {
  const start = performance.now();
  return fn().finally(() => {
    logDebug(namespace, `${name} 耗时`, { ms: Math.round(performance.now() - start) });
  });
}

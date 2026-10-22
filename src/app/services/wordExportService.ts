import type { ExportResult } from './exportService';
import { exportWordFile } from '../../lib/desktop/desktopStorage';
import { t } from '../i18n';
import { createWordModel } from './word/model';
import { createWordResources } from './word/resources';
import { WordBudgetError, type WordDocument, type WordWorkerResult } from './word/protocol';

export interface WordExportInput {
  markdown: string;
  documentPath: string | null;
  suggestedFileName: string;
  title: string;
}

function packWord(model: WordDocument, signal: AbortSignal): Promise<ArrayBuffer> {
  return new Promise((resolve, reject) => {
    signal.throwIfAborted();
    const worker = new Worker(new URL('./word/wordExport.worker.ts', import.meta.url), { type: 'module' });
    const stop = () => { clearTimeout(timer); signal.removeEventListener('abort', abort); worker.terminate(); };
    const abort = () => { stop(); reject(new DOMException('Cancelled', 'AbortError')); };
    const timer = setTimeout(() => { stop(); reject(new Error(t.wordExportTimeout())); }, 120_000);
    signal.addEventListener('abort', abort, { once: true });
    worker.onmessage = (event: MessageEvent<WordWorkerResult>) => {
      stop();
      if ('error' in event.data) reject(new Error(event.data.error));
      else resolve(event.data.bytes);
    };
    worker.onerror = () => { stop(); reject(new Error(t.exportFailed())); };
    worker.onmessageerror = () => { stop(); reject(new Error(t.exportFailed())); };
    try { worker.postMessage(model, model.media.map((item) => item.bytes.buffer as ArrayBuffer)); }
    catch (error) { stop(); reject(error); }
  });
}

/** 保存对话框先行；完成整个包后才进入原子写入，失败不能产生半个 Word 文件。 */
export async function exportWord(input: WordExportInput, options: { signal?: AbortSignal; onGenerating?: () => void } = {}): Promise<ExportResult> {
  const signal = options.signal ?? new AbortController().signal;
  const warnings = new Set<string>();
  const resources = createWordResources(input.documentPath, signal, warnings);
  try {
    signal.throwIfAborted();
    const { save } = await import('../../lib/desktop/runtime/dialog');
    const filePath = await save({ defaultPath: `${input.suggestedFileName || 'Untitled'}.docx`, filters: [{ name: 'Word', extensions: ['docx'] }] });
    if (!filePath) return { success: false, cancelled: true };
    signal.throwIfAborted();
    // 不在对话框确认后改写目标路径，否则可能绕过对实际目标的覆盖确认。
    if (!/\.docx$/i.test(filePath)) return { success: false, error: t.wordExportExtensionError() };
    options.onGenerating?.();
    const model = await createWordModel(input.markdown, input.title, signal, resources.resolve, warnings);
    model.media = resources.media;
    const bytes = await packWord(model, signal);
    signal.throwIfAborted();
    const result = await exportWordFile(filePath, new Uint8Array(bytes));
    return { success: true, filePath: result.file_path, warnings: [...warnings] };
  } catch (error) {
    if (signal.aborted || (error instanceof DOMException && error.name === 'AbortError')) return { success: false, cancelled: true };
    return { success: false, error: error instanceof WordBudgetError ? t.wordExportBudgetError() : `${t.exportFailed()}: ${error instanceof Error ? error.message : String(error)}` };
  } finally { resources.dispose(); }
}

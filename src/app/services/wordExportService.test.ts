import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { exportWord } from './wordExportService';

const mocks = vi.hoisted(() => ({ save: vi.fn(), write: vi.fn(), model: vi.fn(), dispose: vi.fn(), resolve: vi.fn() }));
vi.mock('../../lib/desktop/runtime/dialog', () => ({ save: mocks.save }));
vi.mock('../../lib/desktop/desktopStorage', () => ({ exportWordFile: mocks.write }));
vi.mock('./word/model', () => ({ createWordModel: mocks.model }));
vi.mock('./word/resources', () => ({ createWordResources: () => ({ media: [], resolve: mocks.resolve, dispose: mocks.dispose }) }));

class TestWorker {
  static instances: TestWorker[] = [];
  onmessage?: (event: MessageEvent) => void;
  onerror?: () => void;
  onmessageerror?: () => void;
  terminate = vi.fn();
  postMessage = vi.fn(() => {});
  constructor() { TestWorker.instances.push(this); }
  finish() { this.onmessage?.({ data: { bytes: new Uint8Array([80, 75, 3, 4]).buffer } } as MessageEvent); }
}
const input = { markdown: '# original', documentPath: 'D:/a.md', suggestedFileName: 'a', title: 'a' };
beforeEach(() => {
  vi.clearAllMocks();
  TestWorker.instances = [];
  vi.stubGlobal('Worker', TestWorker);
  mocks.save.mockResolvedValue('D:/中文.docx');
  mocks.model.mockResolvedValue({ title: 'a', nodes: [], headings: [], labels: {}, media: [] });
  mocks.write.mockResolvedValue({ file_path: 'D:/中文.docx', bytes_written: 4 });
});
afterEach(() => vi.unstubAllGlobals());
async function workerReady() { await vi.waitFor(() => expect(TestWorker.instances).toHaveLength(1)); return TestWorker.instances[0]; }

describe('Word export service', () => {
  it('不在保存确认后改写目标路径，错误扩展名不会写入', async () => {
    mocks.save.mockResolvedValue('D:/中文.pdf');
    expect(await exportWord(input)).toMatchObject({ success: false, error: expect.any(String) });
    expect(mocks.model).not.toHaveBeenCalled();
    expect(mocks.write).not.toHaveBeenCalled();
  });
  it('保存取消不解析文档、不创建 Worker、不写文件', async () => {
    mocks.save.mockResolvedValue(null);
    expect(await exportWord(input)).toEqual({ success: false, cancelled: true });
    expect(mocks.model).not.toHaveBeenCalled();
    expect(TestWorker.instances).toHaveLength(0);
    expect(mocks.write).not.toHaveBeenCalled();
    expect(mocks.dispose).toHaveBeenCalledOnce();
  });

  it('完整二进制包生成后才保存并释放 Worker 和资源', async () => {
    const exporting = exportWord(input);
    const worker = await workerReady();
    expect(mocks.write).not.toHaveBeenCalled();
    worker.finish();
    expect(await exporting).toMatchObject({ success: true, filePath: 'D:/中文.docx' });
    expect(mocks.write).toHaveBeenCalledWith('D:/中文.docx', new Uint8Array([80, 75, 3, 4]));
    expect(worker.terminate).toHaveBeenCalledOnce();
    expect(mocks.dispose).toHaveBeenCalledOnce();
  });

  it('打包期间取消终止 Worker，迟到结果不落盘', async () => {
    const abort = new AbortController();
    const exporting = exportWord(input, { signal: abort.signal });
    const worker = await workerReady();
    abort.abort();
    worker.finish();
    expect(await exporting).toEqual({ success: false, cancelled: true });
    expect(mocks.write).not.toHaveBeenCalled();
    expect(worker.terminate).toHaveBeenCalled();
  });

  it('保存失败返回错误，不能报成功', async () => {
    mocks.write.mockRejectedValue(new Error('file locked'));
    const exporting = exportWord(input);
    (await workerReady()).finish();
    expect(await exporting).toMatchObject({ success: false, error: expect.stringContaining('file locked') });
  });

  it('Worker 失败不写入，允许后续再次导出', async () => {
    const exporting = exportWord(input);
    const worker = await workerReady();
    worker.onerror?.();
    expect(await exporting).toMatchObject({ success: false });
    expect(mocks.write).not.toHaveBeenCalled();
    expect(worker.terminate).toHaveBeenCalledOnce();
  });
});

import { afterEach, describe, expect, it, vi } from 'vitest';
import { createWordResources, readWordImage, withWordAbort } from './resources';
import { assertWordCanvasSize, WORD_MEDIA_BUDGET, WordBudgetError } from './protocol';

const read = vi.hoisted(() => vi.fn());
vi.mock('../../../lib/desktop/desktopStorage', () => ({ readFileAsBase64: read }));
vi.mock('../exportImagePath', () => ({ resolveImagePath: vi.fn(async (path) => ({ absolutePath: path })) }));
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers(); read.mockReset(); });

describe('Word resources', () => {
  it('缺失图片保留失败结果，重复引用只读取一次', async () => {
    read.mockRejectedValue(new Error('missing'));
    const warnings = new Set<string>();
    const resources = createWordResources('D:/docs/a.md', new AbortController().signal, warnings);
    const node = { type: 'image', attrs: { src: 'D:/docs/missing.png' } };
    expect(await resources.resolve(node)).toBeNull();
    expect(await resources.resolve(node)).toBeNull();
    expect(read).toHaveBeenCalledOnce();
    expect(warnings.size).toBe(1);
    resources.dispose();
    expect(resources.media).toHaveLength(0);
  });

  it('原生读取的预算错误不能降级为成功', async () => {
    read.mockRejectedValue('[WORD_MEDIA_BUDGET] too large');
    const resources = createWordResources(null, new AbortController().signal, new Set());
    await expect(resources.resolve({ type: 'image', attrs: { src: 'D:/large.png' } })).rejects.toBeInstanceOf(WordBudgetError);
  });

  it('远程请求 8 秒超时会中断底层 fetch', async () => {
    vi.useFakeTimers();
    let aborted = false;
    vi.stubGlobal('fetch', vi.fn((_url, options) => new Promise((_resolve, reject) => {
      options.signal.addEventListener('abort', () => { aborted = true; reject(new DOMException('Abort', 'AbortError')); });
    })));
    const result = readWordImage('https://example.com/slow.png', null, new AbortController().signal);
    const rejection = expect(result).rejects.toThrow();
    await vi.advanceTimersByTimeAsync(8001);
    await rejection;
    expect(aborted).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('窗口取消会中断请求并清理定时器', async () => {
    vi.useFakeTimers();
    vi.stubGlobal('fetch', vi.fn((_url, options) => new Promise((_resolve, reject) => options.signal.addEventListener('abort', () => reject(new Error('abort'))))));
    const abort = new AbortController();
    const result = readWordImage('https://example.com/image.png', null, abort.signal);
    const rejection = expect(result).rejects.toThrow();
    abort.abort();
    await rejection;
    expect(vi.getTimerCount()).toBe(0);
  });

  it('拒绝超过预算的响应，不读取响应体', async () => {
    const getReader = vi.fn();
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, headers: new Headers({ 'content-length': String(WORD_MEDIA_BUDGET + 1) }), body: { getReader } })));
    await expect(readWordImage('https://example.com/large.png', null, new AbortController().signal)).rejects.toBeInstanceOf(WordBudgetError);
    expect(getReader).not.toHaveBeenCalled();
  });

  it('拒绝非法尺寸和超大 Canvas，异步超时释放监听器', async () => {
    expect(() => assertWordCanvasSize(2000, 2000, 2)).not.toThrow();
    expect(() => assertWordCanvasSize(2001, 2000, 2)).toThrow(WordBudgetError);
    expect(() => assertWordCanvasSize(0, 1)).toThrow(WordBudgetError);
    vi.useFakeTimers();
    const abort = new AbortController();
    const remove = vi.spyOn(abort.signal, 'removeEventListener');
    const task = withWordAbort(new Promise(() => {}), abort.signal, 10);
    const rejection = expect(task).rejects.toThrow('timeout');
    await vi.advanceTimersByTimeAsync(11);
    await rejection;
    expect(remove).toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });
});

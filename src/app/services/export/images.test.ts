import { afterEach, describe, expect, it, vi } from 'vitest';
import { EXPORT_IMAGE_LIMIT, EXPORT_MEDIA_LIMIT, ExportResourceBudget, inlineLocalImages } from './images';

vi.mock('../../../lib/desktop/desktopStorage', () => ({ readFileAsBase64: vi.fn() }));
afterEach(() => vi.unstubAllGlobals());

describe('export resources', () => {
  it('同源图片只读取一次；成功内嵌后移除 srcset 私有地址', async () => {
    const fetcher = vi.fn(async () => new Response(new Uint8Array([1, 2, 3]), { headers: { 'content-type': 'image/png' } }));
    vi.stubGlobal('fetch', fetcher);
    const result = await inlineLocalImages('<img src="blob:one" srcset="asset://private 2x"><img src="blob:one" alt="重复">', null);
    expect(fetcher).toHaveBeenCalledOnce();
    expect(result.html).not.toMatch(/blob:|asset:|srcset/);
    expect(result.html.match(/data:image\/png;base64,AQID/g)).toHaveLength(2);
    expect(result.warnings).toEqual([]);
  });

  it('超限中止整个导出，不能降级成普通图片警告', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('', { headers: { 'content-length': String(EXPORT_IMAGE_LIMIT + 1) } })));
    await expect(inlineLocalImages('<img src="https://example.test/large.png">', null)).rejects.toThrow('资源预算');
    const budget = new ExportResourceBudget();
    for (let bytes = 0; bytes < EXPORT_MEDIA_LIMIT; bytes += EXPORT_IMAGE_LIMIT) budget.add(EXPORT_IMAGE_LIMIT);
    expect(() => budget.add(1)).toThrow('资源预算');
  });

  it('资源最多并行两个，取消后不继续处理排队图片', async () => {
    let active = 0;
    let maximum = 0;
    vi.stubGlobal('fetch', vi.fn((_url, init) => new Promise<Response>((_resolve, reject) => {
      maximum = Math.max(maximum, ++active);
      init?.signal?.addEventListener('abort', () => { active--; reject(new DOMException('Cancelled', 'AbortError')); });
    })));
    const abort = new AbortController();
    const pending = inlineLocalImages('<img src="blob:a"><img src="blob:b"><img src="blob:c">', null, { signal: abort.signal });
    abort.abort();
    await expect(pending).rejects.toMatchObject({ name: 'AbortError' });
    expect(maximum).toBe(2);
    expect(active).toBe(0);
    expect(fetch).toHaveBeenCalledTimes(2);
  });
});

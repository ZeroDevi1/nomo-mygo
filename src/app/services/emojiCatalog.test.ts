import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fixture } from '../../lib/emoji/catalog.fixture';
const invoke = vi.hoisted(() => vi.fn());
vi.mock('../../lib/desktop/desktopStorage', () => ({ isDesktopRuntime: () => true }));
vi.mock('../../lib/desktop/runtime/core', () => ({ invoke }));
beforeEach(() => {
  vi.resetModules();
  invoke.mockReset();
});

describe('shared emoji catalog loading', () => {
  it('deduplicates concurrent requests and reuses the immutable catalog across documents', async () => {
    let resolve!: (value: typeof fixture) => void;
    invoke.mockImplementation(() => new Promise((done) => (resolve = done)));
    const { loadEmojiCatalog } = await import('./emojiCatalog');
    const first = loadEmojiCatalog();
    const second = loadEmojiCatalog();
    await vi.waitFor(() => expect(invoke).toHaveBeenCalledOnce());
    resolve(fixture);
    const catalog = await first;
    expect(await second).toBe(catalog);
    expect(await loadEmojiCatalog()).toBe(catalog);
    expect(invoke).toHaveBeenCalledWith('load_emoji_catalog');
    expect(invoke).toHaveBeenCalledOnce();
  });
  it('cools down failures without consuming input and allows an explicit retry', async () => {
    invoke.mockRejectedValueOnce(new Error('offline')).mockResolvedValue(fixture);
    const { loadEmojiCatalog } = await import('./emojiCatalog');
    await expect(loadEmojiCatalog()).rejects.toThrow('offline');
    await expect(loadEmojiCatalog()).rejects.toThrow('unavailable');
    expect(invoke).toHaveBeenCalledOnce();
    expect(await loadEmojiCatalog(true)).toHaveLength(3);
    expect(invoke).toHaveBeenCalledTimes(2);
  });
});

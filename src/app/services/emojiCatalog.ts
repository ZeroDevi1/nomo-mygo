import { isDesktopRuntime } from '../../lib/desktop/desktopStorage';
import {
  buildEmojiCatalog,
  type EmojiCatalogPayload,
  type EmojiEntry,
} from '../../lib/emoji/catalog';

let catalog: readonly EmojiEntry[] | undefined;
let pending: Promise<readonly EmojiEntry[]> | undefined;
let failedUntil = 0;

/** 每个窗口共享一次按需加载；失败冷却避免每次按键重复请求。 */
export function loadEmojiCatalog(retry = false): Promise<readonly EmojiEntry[]> {
  if (catalog) return Promise.resolve(catalog);
  if (pending) return pending;
  if (!retry && Date.now() < failedUntil)
    return Promise.reject(new Error('emoji-catalog-unavailable'));
  pending = (async () => {
    if (!isDesktopRuntime()) throw new Error('emoji-catalog-desktop-required');
    const { invoke } = await import('../../lib/desktop/runtime/core');
    const payload = await invoke<EmojiCatalogPayload>('load_emoji_catalog');
    catalog = buildEmojiCatalog(payload);
    if (!catalog.length) throw new Error('emoji-catalog-empty');
    return catalog;
  })()
    .catch((error) => {
      catalog = undefined;
      failedUntil = Date.now() + 60_000;
      throw error;
    })
    .finally(() => {
      pending = undefined;
    });
  return pending;
}

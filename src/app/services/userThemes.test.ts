import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  deleteUserThemeFile,
  listUserThemeFiles,
  saveUserThemeFile,
} from '../../lib/desktop/desktopStorage';
import { isRegisteredThemeId, themeRegistry } from './themeRegistry';
import {
  deleteUserTheme,
  ensureUserThemesLoaded,
  importUserThemeSource,
  reloadUserThemes,
} from './userThemes';

vi.mock('../../lib/desktop/desktopStorage', () => ({
  listUserThemeFiles: vi.fn(),
  saveUserThemeFile: vi.fn().mockResolvedValue(undefined),
  deleteUserThemeFile: vi.fn().mockResolvedValue(undefined),
}));

const source = (id: string) =>
  JSON.stringify({ schemaVersion: 1, id, localizedNames: { 'en-US': id } });

beforeEach(() => {
  for (const theme of themeRegistry.listThemes()) {
    if (!themeRegistry.isBuiltinTheme(theme.id)) themeRegistry.unregisterTheme(theme.id);
  }
  vi.mocked(listUserThemeFiles).mockReset();
  vi.mocked(saveUserThemeFile).mockClear();
});

describe('userThemes', () => {
  it('registers valid files, reports invalid ones and drops themes removed from disk', async () => {
    vi.mocked(listUserThemeFiles).mockResolvedValueOnce([
      { id: 'user-a', content: source('user-a') },
      { id: 'user-b', content: '{' },
      { id: 'user-c', content: source('user-other') },
    ]);
    const result = await ensureUserThemesLoaded(true);
    expect(isRegisteredThemeId('user-a')).toBe(true);
    expect(result.failures.map((failure) => failure.id)).toEqual(['user-b', 'user-c']);
    // 同一窗口只读取一次目录。
    await ensureUserThemesLoaded(true);
    expect(listUserThemeFiles).toHaveBeenCalledTimes(1);

    vi.mocked(listUserThemeFiles).mockResolvedValueOnce([]);
    await reloadUserThemes(true);
    expect(isRegisteredThemeId('user-a')).toBe(false);
  });

  it('keeps registered themes when the directory cannot be read', async () => {
    themeRegistry.registerTheme(
      (await import('./userThemeDefinition')).parseUserTheme(JSON.parse(source('user-kept'))),
      { replace: true },
    );
    vi.mocked(listUserThemeFiles).mockRejectedValueOnce(new Error('denied'));
    const result = await reloadUserThemes(true);
    expect(result.failures).toEqual([{ id: '', message: 'denied' }]);
    expect(isRegisteredThemeId('user-kept')).toBe(true);
  });

  it('validates before saving and registers only after the file is written', async () => {
    await expect(importUserThemeSource('not json')).rejects.toThrow(/JSON/);
    await expect(importUserThemeSource(source('nomo-x'))).rejects.toThrow(/user-/);
    expect(saveUserThemeFile).not.toHaveBeenCalled();

    vi.mocked(saveUserThemeFile).mockRejectedValueOnce(new Error('disk full'));
    await expect(importUserThemeSource(source('user-new'))).rejects.toThrow('disk full');
    expect(isRegisteredThemeId('user-new')).toBe(false);

    const theme = await importUserThemeSource(source('user-new'));
    expect(saveUserThemeFile).toHaveBeenLastCalledWith('user-new', source('user-new'));
    expect(isRegisteredThemeId(theme.id)).toBe(true);

    await deleteUserTheme('user-new');
    expect(deleteUserThemeFile).toHaveBeenCalledWith('user-new');
    expect(isRegisteredThemeId('user-new')).toBe(false);
  });
});

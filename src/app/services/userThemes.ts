import type { ThemeDefinition } from '../../lib/theme/types';
import { logWarn } from '../../lib/services/logger';
import {
  deleteUserThemeFile,
  listUserThemeFiles,
  saveUserThemeFile,
} from '../../lib/desktop/desktopStorage';
import { themeRegistry } from './themeRegistry';
import { parseUserTheme } from './userThemeDefinition';

/**
 * 当前窗口的用户主题注册状态：从应用数据目录读取 `themes/*.json`，校验后注册到 themeRegistry。
 *
 * 每个 WebView 各自持有一份注册表，因此主窗和设置窗都要加载；设置窗增删主题后广播
 * USER_THEMES_CHANGED_EVENT，其他窗口收到后调用 `reloadUserThemes`。
 */

export const USER_THEMES_CHANGED_EVENT = 'nomo://user-themes-changed';

export interface UserThemeLoadFailure {
  /** 文件对应的主题 ID；目录整体不可读时为 `''`。 */
  id: string;
  message: string;
}

export interface UserThemeLoadResult {
  failures: UserThemeLoadFailure[];
}

/** 当前窗口最近一次加载的进行中或已完成结果；`null` 表示尚未加载。 */
let loadPromise: Promise<UserThemeLoadResult> | null = null;

/**
 * 确保用户主题已加载；同一窗口只读取一次目录，并发调用共享同一个 Promise。
 *
 * 必须在用 `isRegisteredThemeId` 归一化偏好之前完成，否则用户主题 ID 会被修复为默认主题。
 * 非桌面环境没有主题目录，直接返回空结果。
 */
export function ensureUserThemesLoaded(desktopEnabled: boolean): Promise<UserThemeLoadResult> {
  if (!desktopEnabled) return Promise.resolve({ failures: [] });
  loadPromise ??= loadUserThemes();
  return loadPromise;
}

/** 重新读取主题目录（其他窗口增删主题后），注销目录中已不存在或已失效的用户主题。 */
export function reloadUserThemes(desktopEnabled: boolean): Promise<UserThemeLoadResult> {
  if (!desktopEnabled) return Promise.resolve({ failures: [] });
  loadPromise = loadUserThemes();
  return loadPromise;
}

/**
 * 校验并保存一份用户主题源文，然后在当前窗口注册。同 ID 主题会被覆盖。
 *
 * @param source 用户选择的 JSON 原文。
 * @returns 已注册的主题定义。
 * @throws Error JSON 语法错误、主题校验失败或原生保存失败时抛出，消息可直接展示。
 */
export async function importUserThemeSource(source: string): Promise<ThemeDefinition> {
  let raw: unknown;
  try {
    raw = JSON.parse(source);
  } catch (error) {
    throw new Error(`JSON 格式错误：${error instanceof Error ? error.message : String(error)}`);
  }
  const theme = parseUserTheme(raw);
  // 先落盘再注册：保存失败时当前窗口不出现一个重启后就消失的主题。
  await saveUserThemeFile(theme.id, source);
  themeRegistry.registerTheme(theme, { replace: true });
  return theme;
}

/** 删除用户主题文件并在当前窗口注销；调用方负责把仍引用该 ID 的偏好改回默认主题。 */
export async function deleteUserTheme(id: string) {
  await deleteUserThemeFile(id);
  themeRegistry.unregisterTheme(id);
}

async function loadUserThemes(): Promise<UserThemeLoadResult> {
  let files;
  try {
    files = await listUserThemeFiles();
  } catch (error) {
    // 目录读取失败时保留已注册主题（含启动快照恢复的当前主题），避免误把偏好修复为默认。
    const message = error instanceof Error ? error.message : String(error);
    logWarn('userThemes', '读取用户主题目录失败', { error: message });
    return { failures: [{ id: '', message }] };
  }

  const failures: UserThemeLoadFailure[] = [];
  const loadedIds = new Set<string>();
  for (const file of files) {
    try {
      const theme = parseUserTheme(JSON.parse(file.content));
      if (theme.id !== file.id) {
        throw new Error(`主题 id（${theme.id}）与文件名不一致`);
      }
      themeRegistry.registerTheme(theme, { replace: true });
      loadedIds.add(theme.id);
    } catch (error) {
      failures.push({
        id: file.id,
        message: error instanceof Error ? error.message : String(error),
      });
    }
  }
  for (const theme of themeRegistry.listThemes()) {
    if (!themeRegistry.isBuiltinTheme(theme.id) && !loadedIds.has(theme.id)) {
      themeRegistry.unregisterTheme(theme.id);
    }
  }
  if (failures.length > 0) {
    logWarn('userThemes', '部分用户主题无效', { failures });
  }
  return { failures };
}

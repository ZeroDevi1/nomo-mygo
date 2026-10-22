import { getCurrentWindow } from '../../lib/desktop/runtime/window';
import { createPerfTimer, logError, logInfo } from '../../lib/services/logger';
import { getPlatformCapabilities } from './platform';
import { takeWorkspaceWriteStamp } from './tabTransfer';

export type OpenTarget =
  | { kind: 'documents'; paths: string[] }
  | { kind: 'folder'; path: string };

export interface OpenDirectoryWindow {
  windowLabel: string;
  folderPath: string;
  paths: string[];
}

export type OpenTargetRouteDecision =
  | { action: 'handled' }
  | { action: 'activate-current'; target: OpenTarget; remainingTarget?: OpenTarget }
  | { action: 'open-current'; target: OpenTarget; directoryWindows?: OpenDirectoryWindow[] }
  | { action: 'create-window'; windowLabel: string; target: OpenTarget };

export interface WindowOpenTargetsSnapshot {
  folderPath?: string | null;
  filePaths: string[];
  ownershipEpoch?: number;
  revision?: number;
}

export async function closeAppWindow(
  desktopEnabled: boolean,
  closeToTrayEnabled = false,
  sessions: Array<{ sessionId: string; discardChanges: boolean }> = [],
) {
  if (!desktopEnabled) {
    return;
  }

  try {
    logInfo('DesktopWindow', closeToTrayEnabled ? '隐藏窗口到托盘' : '关闭窗口');
    const { invoke } = await import('../../lib/desktop/runtime/core');
    await invoke(closeToTrayEnabled ? 'hide_window_to_tray' : 'close_window', { sessions });
  } catch (error) {
    logError('DesktopWindow', 'Failed to close window', { error: formatError(error) });
    throw error;
  }
}

export async function exitApp(desktopEnabled: boolean) {
  if (!desktopEnabled) {
    return;
  }

  try {
    logInfo('DesktopWindow', '退出应用');
    const { invoke } = await import('../../lib/desktop/runtime/core');
    await invoke('exit_app');
  } catch (error) {
    logError('DesktopWindow', 'Failed to exit app', { error: formatError(error) });
  }
}

export async function createAppWindow(
  desktopEnabled: boolean,
  preparedWindowLabel?: string,
): Promise<string | undefined> {
  if (!desktopEnabled) {
    return undefined;
  }

  const { invoke } = await import('../../lib/desktop/runtime/core');
  const timer = createPerfTimer('DesktopWindow', '创建新窗口');
  try {
    logInfo('DesktopWindow', '开始创建新窗口', { preparedWindowLabel });
    const windowId = preparedWindowLabel ?? (await invoke<string>('create_new_window'));
    // 尺寸、标题栏样式与首帧显示时机由 Go 侧按平台统一配置。
    await invoke('create_document_window', { windowLabel: windowId });
    timer.end({ windowId });
    logInfo('DesktopWindow', '新窗口创建完成', { windowId });
    return windowId;
  } catch (error) {
    if (preparedWindowLabel) {
      await invoke('release_open_target_reservation', {
        windowLabel: preparedWindowLabel,
      }).catch(() => undefined);
    }
    timer.end({ failed: true });
    logError('DesktopWindow', 'Failed to create new window', { error: formatError(error) });
    return undefined;
  }
}

export async function activateDocumentWindow(desktopEnabled: boolean): Promise<void> {
  if (!desktopEnabled) return;
  const { invoke } = await import('../../lib/desktop/runtime/core');
  await invoke('activate_document_window');
}

export async function resolveDroppedOpenTarget(
  desktopEnabled: boolean,
  paths: string[],
): Promise<OpenTarget | null> {
  if (!desktopEnabled || paths.length === 0) return null;
  const { invoke } = await import('../../lib/desktop/runtime/core');
  return invoke<OpenTarget | null>('resolve_dropped_open_target', { paths });
}

export async function prepareOpenTargetWindow(
  desktopEnabled: boolean,
  target: OpenTarget,
  createIfMissing: boolean,
  options: { reuseDirectoryWindow?: boolean } = {},
): Promise<OpenTargetRouteDecision> {
  if (!desktopEnabled) {
    return { action: 'open-current', target };
  }
  const { invoke } = await import('../../lib/desktop/runtime/core');
  return invoke<OpenTargetRouteDecision>('prepare_open_target_window', {
    target,
    createIfMissing,
    reuseDirectoryWindow: options.reuseDirectoryWindow ?? false,
  });
}

export async function openInDirectoryWindow(
  desktopEnabled: boolean,
  candidate: OpenDirectoryWindow,
): Promise<void> {
  if (!desktopEnabled) return;
  const { invoke } = await import('../../lib/desktop/runtime/core');
  await invoke('open_documents_in_directory_window', {
    windowLabel: candidate.windowLabel,
    paths: candidate.paths,
  });
}

export async function syncWindowOpenTargets(
  desktopEnabled: boolean,
  snapshot: WindowOpenTargetsSnapshot,
): Promise<void> {
  if (!desktopEnabled) {
    return;
  }
  const stamp = snapshot.ownershipEpoch === undefined ? await takeWorkspaceWriteStamp() : {};
  const { invoke } = await import('../../lib/desktop/runtime/core');
  await invoke('sync_window_open_targets', { input: { ...snapshot, ...stamp } });
}

export async function openSettingsWindow(desktopEnabled: boolean) {
  if (!desktopEnabled) {
    return;
  }

  try {
    const timer = createPerfTimer('DesktopWindow', '打开设置窗口');
    logInfo('DesktopWindow', '打开设置窗口');
    const { invoke } = await import('../../lib/desktop/runtime/core');
    await invoke('open_settings_window');
    timer.end();
  } catch (error) {
    logError('DesktopWindow', 'Failed to open settings window', { error: formatError(error) });
  }
}

export async function enterMarkdownMiniMode(desktopEnabled: boolean, pinned: boolean) {
  if (!desktopEnabled) {
    return;
  }

  const timer = createPerfTimer('DesktopWindow', '主窗口切换为 Markdown 小窗');
  try {
    const { invoke } = await import('../../lib/desktop/runtime/core');
    await invoke('enter_markdown_mini_mode', { pinned });
    timer.end();
  } catch (error) {
    timer.end({ failed: true });
    throw error;
  }
}

export async function exitMarkdownMiniMode(desktopEnabled: boolean) {
  if (!desktopEnabled) {
    return;
  }

  const timer = createPerfTimer('DesktopWindow', 'Markdown 小窗恢复为主窗口');
  try {
    const { invoke } = await import('../../lib/desktop/runtime/core');
    await invoke('exit_markdown_mini_mode');
    timer.end();
  } catch (error) {
    timer.end({ failed: true });
    throw error;
  }
}

export async function setMarkdownMiniModePinned(desktopEnabled: boolean, pinned: boolean) {
  if (!desktopEnabled) {
    return;
  }

  const { invoke } = await import('../../lib/desktop/runtime/core');
  await invoke('set_markdown_mini_mode_pinned', { pinned });
}

export async function setDesktopIconTheme(
  desktopEnabled: boolean,
  theme: 'light' | 'dark',
  captionBackground: string,
) {
  if (!desktopEnabled) {
    return;
  }

  try {
    logInfo('DesktopWindow', '同步桌面图标主题', { theme });
    const { invoke } = await import('../../lib/desktop/runtime/core');
    await invoke('set_desktop_icon_theme', { theme, captionBackground });
  } catch (error) {
    logError('DesktopWindow', 'Failed to sync desktop icon theme', { error: formatError(error) });
  }
}

export async function refreshInterfaceLanguageChrome(desktopEnabled: boolean) {
  if (!desktopEnabled) {
    return;
  }

  try {
    logInfo('DesktopWindow', '刷新界面语言 chrome');
    const { invoke } = await import('../../lib/desktop/runtime/core');
    await invoke('refresh_interface_language_chrome');
  } catch (error) {
    logError('DesktopWindow', 'Failed to refresh interface language chrome', {
      error: formatError(error),
    });
  }
}

export async function getDesktopSystemTheme(
  desktopEnabled: boolean,
): Promise<'light' | 'dark' | undefined> {
  if (!desktopEnabled) {
    return undefined;
  }

  try {
    const timer = createPerfTimer('DesktopWindow', '读取系统主题');
    const { invoke } = await import('../../lib/desktop/runtime/core');
    const theme = await invoke<'light' | 'dark'>('get_desktop_system_theme');
    timer.end({ theme });
    return theme === 'dark' ? 'dark' : 'light';
  } catch (error) {
    logError('DesktopWindow', '读取系统主题失败', { error: formatError(error) });
    return undefined;
  }
}

export async function updateAppWindowTitle(
  desktopEnabled: boolean,
  fileName: string,
  dirty: boolean,
) {
  if (!desktopEnabled) {
    return;
  }

  const title = `${fileName}${dirty ? ' *' : ''} - Nomo`;
  // macOS 窗口使用隐藏标题栏，标题不可见；写入 NSWindow 标题会触发标题栏重新布局并
  // 让红绿灯短暂回到默认位置，因此只上报给托盘菜单（report_window_title）。
  if (!getPlatformCapabilities().isMac) {
    const win = getCurrentWindow();
    await win.setTitle(title).catch(() => undefined);
  }
  const { invoke } = await import('../../lib/desktop/runtime/core');
  await invoke('report_window_title', { title }).catch(() => undefined);
}

function formatError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

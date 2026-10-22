/**
 * 当前页面所在窗口的操作，对应原 `getCurrentWindow()` 中项目实际用到的方法。
 * 窗口标签由 Go 侧在页面脚本之前注入（window.__NOMO_WINDOW__）。
 */
import { invoke } from './core';
import { listen, type DesktopEvent, type UnlistenFn } from './event';

declare global {
  interface Window {
    __NOMO_WINDOW__?: { label: string };
  }
}

export interface WindowGeometry {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface DesktopWindow {
  readonly label: string;
  isFullscreen(): Promise<boolean>;
  isMaximized(): Promise<boolean>;
  minimize(): Promise<void>;
  toggleMaximize(): Promise<void>;
  setTitle(title: string): Promise<void>;
  /** 外框位置与内容区尺寸（设备无关像素）。 */
  geometry(): Promise<WindowGeometry>;
  /** 回调参数为事件对象，调用方可据此区分事件触发与主动调用。 */
  onResized(handler: (event: DesktopEvent<unknown>) => void): Promise<UnlistenFn>;
  onThemeChanged(handler: (event: { payload: 'light' | 'dark' }) => void): Promise<UnlistenFn>;
}

const RESIZED_EVENT = 'nomo-window://resized';
const SYSTEM_THEME_CHANGED_EVENT = 'nomo://system-theme-changed';

const currentWindow: DesktopWindow = {
  get label() {
    return typeof window !== 'undefined' ? (window.__NOMO_WINDOW__?.label ?? 'main') : 'main';
  },
  isFullscreen: () => invoke<boolean>('window_is_fullscreen'),
  isMaximized: () => invoke<boolean>('window_is_maximized'),
  minimize: async () => {
    await invoke('window_minimize');
  },
  toggleMaximize: async () => {
    await invoke('window_toggle_maximize');
  },
  setTitle: async (title: string) => {
    await invoke('window_set_title', { title });
  },
  geometry: () => invoke<WindowGeometry>('window_geometry'),
  // 尺寸变化事件只发给本窗口，无需按标签过滤。
  onResized: (handler) => listen(RESIZED_EVENT, handler),
  onThemeChanged: (handler) =>
    listen<string>(SYSTEM_THEME_CHANGED_EVENT, (event) => {
      handler({ payload: event.payload === 'dark' ? 'dark' : 'light' });
    }),
};

export function getCurrentWindow(): DesktopWindow {
  return currentWindow;
}

/**
 * 桌面事件：Go 侧把所有事件经单一通道 `nomo:event` 推给页面，这里按事件名分发。
 *
 * 投递范围与原实现一致：全局 listen 收到同名的全部事件（含发往其他窗口的定向事件），
 * 事件负载中携带窗口标签的由调用方自行过滤。页面之间的 emit / emitTo 经 Go 转发。
 */
import { call, isMyGo, on } from 'mygo-runtime';

export type UnlistenFn = () => void;

/** 与原事件对象形状一致，监听回调从 `event.payload` 读取负载。 */
export interface DesktopEvent<T> {
  event: string;
  payload: T;
}

interface DesktopEventMessage {
  event: string;
  payload: unknown;
  target?: string;
  source?: string;
}

type Handler = (event: DesktopEvent<unknown>) => void;

const handlers = new Map<string, Set<Handler>>();
let channelUnlisten: (() => void) | null = null;

/** 首次监听时订阅 Go 事件通道；页面生命周期内只订阅一次。 */
function ensureChannel() {
  if (channelUnlisten || !isMyGo()) return;
  channelUnlisten = on<DesktopEventMessage>('nomo:event', (message) => {
    const listeners = handlers.get(message.event);
    if (!listeners) return;
    for (const handler of [...listeners]) {
      handler({ event: message.event, payload: message.payload });
    }
  });
}

/**
 * 监听事件，返回取消函数。浏览器中不会收到任何事件。
 */
export async function listen<T>(event: string, handler: (event: DesktopEvent<T>) => void): Promise<UnlistenFn> {
  ensureChannel();
  let listeners = handlers.get(event);
  if (!listeners) {
    listeners = new Set();
    handlers.set(event, listeners);
  }
  const wrapped = handler as Handler;
  listeners.add(wrapped);
  return () => {
    listeners.delete(wrapped);
    if (listeners.size === 0) handlers.delete(event);
  };
}

/** 只接收一次的 listen。 */
export async function once<T>(event: string, handler: (event: DesktopEvent<T>) => void): Promise<UnlistenFn> {
  let unlisten: UnlistenFn | null = null;
  unlisten = await listen<T>(event, (payload) => {
    unlisten?.();
    handler(payload);
  });
  return unlisten;
}

/** 向所有窗口广播事件。 */
export async function emit(event: string, payload?: unknown): Promise<void> {
  await call('Bridge.Emit', event, payload ?? null, '');
}

/** 向指定窗口发送事件（全局监听者同样收到，接收方按标签过滤）。 */
export async function emitTo(target: string, event: string, payload?: unknown): Promise<void> {
  await call('Bridge.Emit', event, payload ?? null, target);
}

/** 原生文件拖放事件名：负载为 `{ paths: string[] }`。 */
export const FILE_DROP_EVENT = 'nomo-window://file-drop';

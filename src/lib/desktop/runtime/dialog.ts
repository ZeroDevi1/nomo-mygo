/**
 * 系统文件与消息对话框，由 Go 侧以调用窗口为父窗口弹出（macOS 上为 sheet）。
 */
import { invoke } from './core';

export interface DialogFilter {
  name: string;
  extensions: string[];
}

export interface OpenDialogOptions {
  title?: string;
  defaultPath?: string;
  filters?: DialogFilter[];
  multiple?: boolean;
  directory?: boolean;
}

export interface SaveDialogOptions {
  title?: string;
  defaultPath?: string;
  filters?: DialogFilter[];
}

export interface MessageDialogOptions {
  title?: string;
  kind?: 'info' | 'warning' | 'error';
  okLabel?: string;
  cancelLabel?: string;
}

/** 选择文件或目录；取消时为 null，multiple 时为数组。 */
export async function open(options: OpenDialogOptions = {}): Promise<string | string[] | null> {
  const paths = await invoke<string[] | null>('dialog_open', { options });
  if (!paths || paths.length === 0) return null;
  return options.multiple ? paths : paths[0];
}

/** 选择保存位置；取消时为 null。 */
export async function save(options: SaveDialogOptions = {}): Promise<string | null> {
  const path = await invoke<string>('dialog_save', { options });
  return path ? path : null;
}

/** 询问确认；用户选择确认按钮时为 true。 */
export async function ask(message: string, options: MessageDialogOptions | string = {}): Promise<boolean> {
  const normalized = typeof options === 'string' ? { title: options } : options;
  return invoke<boolean>('dialog_ask', { message, options: normalized });
}

/**
 * 按当前平台的路径规则拼接与取目录（由 Go 的 filepath 实现，保证与磁盘访问一致）。
 */
import { invoke } from './core';

export function join(...paths: string[]): Promise<string> {
  return invoke<string>('path_join', { paths });
}

export function dirname(path: string): Promise<string> {
  return invoke<string>('path_dirname', { path });
}

export function isAbsolute(path: string): Promise<boolean> {
  return invoke<boolean>('path_is_absolute', { path });
}

/** 与 Node `path.resolve` 一致，返回规范化的绝对路径。 */
export function resolve(...paths: string[]): Promise<string> {
  return invoke<string>('path_resolve', { paths });
}

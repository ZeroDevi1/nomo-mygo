import { beforeEach, describe, expect, it, vi } from 'vitest';

const { invoke } = vi.hoisted(() => ({ invoke: vi.fn() }));
vi.mock('../../lib/desktop/runtime/core', () => ({ invoke }));
import {
  activateDocumentWindow,
  prepareOpenTargetWindow,
  resolveDroppedOpenTarget,
} from './desktopWindow';

beforeEach(() => {
  vi.resetAllMocks();
});

it.each([undefined, false, true])('目录复用默认关闭，显式选项 %s 原样传递', async (reuseDirectoryWindow) => {
  const target = { kind: 'documents' as const, paths: ['D:/notes/b.md'] };
  await prepareOpenTargetWindow(true, target, false,
    reuseDirectoryWindow === undefined ? undefined : { reuseDirectoryWindow });
  expect(invoke).toHaveBeenLastCalledWith('prepare_open_target_window', {
    target,
    createIfMissing: false,
    reuseDirectoryWindow: reuseDirectoryWindow ?? false,
  });
});

describe('resolveDroppedOpenTarget', () => {
  it('将原始拖放顺序交给原生分类，并返回目录目标', async () => {
    const paths = ['D:/notes/image.png', 'D:/中文 工作目录/example.md', 'D:/notes/file.txt'];
    const target = { kind: 'folder', path: paths[1] };
    invoke.mockResolvedValue(target);
    await expect(resolveDroppedOpenTarget(true, paths)).resolves.toEqual(target);
    expect(invoke).toHaveBeenCalledWith('resolve_dropped_open_target', { paths });
  });

  it('空批次或浏览器环境不调用 IPC', async () => {
    await expect(resolveDroppedOpenTarget(true, [])).resolves.toBeNull();
    await expect(resolveDroppedOpenTarget(false, ['D:/notes'])).resolves.toBeNull();
    expect(invoke).not.toHaveBeenCalled();
  });

  it('保留无目标结果，并将读取错误交给拖放入口显示', async () => {
    invoke.mockResolvedValueOnce(null).mockRejectedValueOnce(new Error('permission denied'));
    await expect(resolveDroppedOpenTarget(true, ['D:/image.png'])).resolves.toBeNull();
    await expect(resolveDroppedOpenTarget(true, ['D:/private'])).rejects.toThrow('permission denied');
  });
});

describe('activateDocumentWindow', () => {
  it('只激活调用者，不向 IPC 传入任意窗口标签', async () => {
    await activateDocumentWindow(true);
    expect(invoke).toHaveBeenCalledTimes(1);
    expect(invoke).toHaveBeenCalledWith('activate_document_window');
  });

  it('浏览器环境不调用原生窗口操作', async () => {
    await activateDocumentWindow(false);
    expect(invoke).not.toHaveBeenCalled();
  });

  it('激活失败向调用方传递错误', async () => {
    invoke.mockRejectedValue(new Error('cannot activate'));
    await expect(activateDocumentWindow(true)).rejects.toThrow('cannot activate');
  });
});

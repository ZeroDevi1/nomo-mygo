export async function resolveImagePath(
  src: string,
  documentPath: string | null,
): Promise<{ absolutePath?: string; warning?: string }> {
  const trimmed = src.trim();
  if (!trimmed) {
    return { warning: '图片 src 为空' };
  }

  // 已经是本地绝对路径。
  if (isAbsoluteLocalPath(trimmed)) {
    return { absolutePath: trimmed };
  }

  // 相对路径需要基于文档目录解析。
  if (!documentPath) {
    return { warning: `当前文档未保存，无法解析相对图片路径：${trimmed}` };
  }

  try {
    const { invoke } = await import('../../lib/desktop/runtime/core');
    const result = await invoke<{
      src: string;
      display_src: string;
      exists: boolean;
      absolute_path?: string | null;
      error?: string | null;
    }>('resolve_image_asset', {
      input: {
        document_path: documentPath,
        src: trimmed,
      },
    });

    if (!result.exists) {
      return {
        warning: result.error ?? `图片文件不存在：${trimmed}`,
      };
    }

    const absolutePath = result.absolute_path ?? result.display_src;
    return { absolutePath };
  } catch (error) {
    return {
      warning: `解析图片路径失败（${error instanceof Error ? error.message : String(error)}）：${trimmed}`,
    };
  }
}

function isAbsoluteLocalPath(src: string): boolean {
  // Windows: C:\path 或 C:/path
  if (/^[a-zA-Z]:[\\/]/.test(src) || src.startsWith('\\\\')) {
    return true;
  }
  // Unix: /path
  if (src.startsWith('/')) {
    return true;
  }
  return false;
}

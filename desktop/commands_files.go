package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/docfs"
)

// 文件读写、文件夹树、外部链接与系统文件管理器相关命令。

// sampleDocumentResource 是随安装包分发的示例文档在资源目录中的相对路径。
const sampleDocumentResource = "samples/sample.md"

// localAttachmentExtensions 是允许交给系统默认应用打开的本地附件类型。
var localAttachmentExtensions = []string{
	"pdf", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "csv", "png", "jpg", "jpeg", "gif", "webp", "bmp", "svg", "avif",
}

func init() {
	register("create_folder", func(_ *commandCall, args struct {
		Path string `json:"path"`
	}) (any, error) {
		applog.Info("FileSystem", "创建文件夹："+args.Path)
		if err := os.MkdirAll(args.Path, 0o755); err != nil {
			return nil, fmt.Errorf("创建文件夹失败：%w", err)
		}
		return nil, nil
	})
	register("rename_file", func(_ *commandCall, args struct {
		OldPath string `json:"oldPath"`
		NewPath string `json:"newPath"`
	}) (any, error) {
		applog.Info("FileSystem", fmt.Sprintf("重命名：%s -> %s", args.OldPath, args.NewPath))
		if err := os.Rename(args.OldPath, args.NewPath); err != nil {
			return nil, fmt.Errorf("重命名失败：%w", err)
		}
		return nil, nil
	})
	register("delete_file", func(_ *commandCall, args struct {
		Path string `json:"path"`
	}) (any, error) {
		applog.Info("FileSystem", "删除文件或目录："+args.Path)
		info, err := os.Stat(args.Path)
		if err != nil {
			return nil, fmt.Errorf("文件不存在：%s", args.Path)
		}
		if info.IsDir() {
			if err := os.RemoveAll(args.Path); err != nil {
				return nil, fmt.Errorf("删除文件夹失败：%w", err)
			}
			return nil, nil
		}
		if err := os.Remove(args.Path); err != nil {
			return nil, fmt.Errorf("删除文件失败：%w", err)
		}
		return nil, nil
	})
	register("read_markdown_file", func(_ *commandCall, args struct {
		Path string `json:"path"`
	}) (docfs.DocumentPayload, error) {
		started := time.Now()
		applog.Info("FileSystem", "开始打开文档："+args.Path)
		payload, err := docfs.ReadDocument(args.Path)
		applog.Perf("FileSystem", "文档打开", time.Since(started))
		return payload, err
	})
	register("write_markdown_file", func(_ *commandCall, args struct {
		Path     string `json:"path"`
		Markdown string `json:"markdown"`
	}) (docfs.DocumentPayload, error) {
		encoding, _, err := docfs.DetectFileEncoding(args.Path)
		if err != nil {
			return docfs.DocumentPayload{}, err
		}
		return writeMarkdownFile(args.Path, args.Markdown, encoding)
	})
	register("write_markdown_file_with_encoding", func(_ *commandCall, args struct {
		Path     string          `json:"path"`
		Markdown string          `json:"markdown"`
		Encoding *docfs.Encoding `json:"encoding"`
	}) (docfs.DocumentPayload, error) {
		var encoding docfs.Encoding
		if args.Encoding != nil {
			encoding = *args.Encoding
		}
		return writeMarkdownFile(args.Path, args.Markdown, encoding)
	})
	register("install_sample_document", func(_ *commandCall, _ noArgs) (docfs.DocumentPayload, error) {
		started := time.Now()
		applog.Info("FileSystem", "打开实例文档")
		// 直接读取安装目录中的示例文档，不复制到用户数据目录；安装目录不可写时由保存流程报错。
		path, err := resolveSampleDocument()
		if err != nil {
			return docfs.DocumentPayload{}, err
		}
		payload, err := docfs.ReadDocument(path)
		if err != nil {
			return docfs.DocumentPayload{}, fmt.Errorf("读取实例文档失败：%w", err)
		}
		applog.Perf("FileSystem", "打开实例文档", time.Since(started))
		return payload, nil
	})
	register("stat_markdown_file", func(_ *commandCall, args struct {
		Path string `json:"path"`
	}) (docfs.FileStatus, error) {
		return docfs.Stat(args.Path), nil
	})
	register("list_folder_markdown_files", func(_ *commandCall, args struct {
		Path string `json:"path"`
	}) ([]docfs.FolderFileInfo, error) {
		return docfs.ListFolderDocuments(args.Path)
	})
	register("get_folder_tree", func(_ *commandCall, args struct {
		Path string `json:"path"`
	}) ([]docfs.FileTreeEntry, error) {
		applog.Info("FileSystem", "读取文件夹树："+args.Path)
		return docfs.ListFolderChildren(args.Path, args.Path)
	})
	register("list_folder_children", func(_ *commandCall, args struct {
		Path     string  `json:"path"`
		RootPath *string `json:"rootPath"`
	}) ([]docfs.FileTreeEntry, error) {
		root := ""
		if args.RootPath != nil {
			root = *args.RootPath
		}
		return docfs.ListFolderChildren(args.Path, root)
	})
	register("check_paths_exist", func(_ *commandCall, args struct {
		Paths []string `json:"paths"`
	}) ([]bool, error) {
		return docfs.CheckPathsExist(args.Paths), nil
	})
	register("open_external_link", func(_ *commandCall, args struct {
		Href string `json:"href"`
	}) (any, error) {
		return nil, openExternalLink(args.Href)
	})
	register("open_local_attachment", func(_ *commandCall, args struct {
		Path string `json:"path"`
	}) (any, error) {
		return nil, openLocalAttachment(args.Path)
	})
	register("reveal_in_explorer", func(_ *commandCall, args struct {
		Path string `json:"path"`
	}) (any, error) {
		path := strings.TrimSpace(args.Path)
		if path == "" {
			return nil, errors.New("文件路径为空")
		}
		mygo.Shell.ShowItemInFolder(path)
		return nil, nil
	})
	register("copy_file_to_clipboard", func(_ *commandCall, args struct {
		Path string `json:"path"`
	}) (any, error) {
		info, err := os.Stat(args.Path)
		if !filepath.IsAbs(args.Path) || err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("File does not exist or is not an absolute file path")
		}
		return nil, mygo.Clipboard.WriteFiles(args.Path)
	})
	register("log_message", func(_ *commandCall, args struct {
		Level   string `json:"level"`
		Tag     string `json:"tag"`
		Message string `json:"message"`
	}) (any, error) {
		applog.Write(args.Level, args.Tag, args.Message)
		return nil, nil
	})
	register("set_logger_enabled", func(_ *commandCall, args struct {
		Enabled bool `json:"enabled"`
	}) (any, error) {
		applog.SetEnabled(args.Enabled)
		return nil, nil
	})
	register("get_logger_enabled", func(_ *commandCall, _ noArgs) (bool, error) {
		return applog.Enabled(), nil
	})
}

func writeMarkdownFile(path, markdown string, encoding docfs.Encoding) (docfs.DocumentPayload, error) {
	started := time.Now()
	applog.Info("FileSystem", fmt.Sprintf("开始保存文档：%s bytes=%d", path, len(markdown)))
	payload, err := docfs.WriteDocument(path, markdown, encoding)
	applog.Perf("FileSystem", "文档保存", time.Since(started))
	return payload, err
}

// resolveSampleDocument 依次在资源目录与可执行文件旁查找非空的示例文档；
// 开发运行（mygo dev 不打包资源）时回退到仓库根目录的 sample.md。
func resolveSampleDocument() (string, error) {
	var candidates []string
	if resources, err := mygo.App.Path(mygo.PathResources); err == nil {
		candidates = append(candidates, filepath.Join(resources, sampleDocumentResource))
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(dir, sampleDocumentResource), filepath.Join(dir, "resources", sampleDocumentResource))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, filepath.Base(sampleDocumentResource)))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return candidate, nil
		}
	}
	return "", errors.New("定位实例文档资源失败：未找到有效的 samples/sample.md")
}

// openExternalLink 用系统默认应用打开 http/https/mailto 链接；拒绝脚本类协议。
// 页面中的 window.open 可能被 WebView 静默拦截，因此统一收口到 Go 侧。
func openExternalLink(href string) error {
	href = strings.TrimSpace(href)
	if href == "" {
		return errors.New("链接地址为空")
	}
	lower := strings.ToLower(href)
	if strings.HasPrefix(lower, "javascript:") || strings.HasPrefix(lower, "vbscript:") || strings.HasPrefix(lower, "data:") {
		return errors.New("链接协议不安全，已阻止打开")
	}
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") && !strings.HasPrefix(lower, "mailto:") {
		return errors.New("仅支持打开 http、https 或 mailto 外部链接")
	}
	if err := mygo.Shell.OpenExternal(href); err != nil {
		return fmt.Errorf("打开链接失败：%w", err)
	}
	return nil
}

// openLocalAttachment 用系统默认应用打开白名单类型的本地附件。路径由前端按文档目录解析，
// 但类型与最终磁盘目标在这里再次校验，避免把任意本地路径的打开能力直接暴露给页面。
func openLocalAttachment(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("附件路径为空")
	}
	if isUNCPath(path) {
		return errors.New("不支持打开 UNC 或网络共享路径")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("附件不存在或无法访问：%w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return fmt.Errorf("附件不存在或无法访问：%w", err)
	}
	if isUNCPath(canonical) {
		return errors.New("不支持打开 UNC 或网络共享路径")
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.Mode().IsRegular() {
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("附件不存在或无法访问：%w", err)
		}
		return errors.New("附件路径不是文件")
	}
	extension := strings.ToLower(strings.TrimPrefix(filepath.Ext(canonical), "."))
	if extension == "" {
		return errors.New("附件类型不受支持")
	}
	if !slices.Contains(localAttachmentExtensions, extension) {
		return fmt.Errorf("附件类型不受支持：.%s", extension)
	}
	if err := mygo.Shell.OpenPath(canonical); err != nil {
		return fmt.Errorf("打开本地附件失败：%w", err)
	}
	return nil
}

// isUNCPath 识别 \\server\share 与 \\?\UNC\ 形式的网络路径（扩展长度的盘符路径除外）。
func isUNCPath(path string) bool {
	normalized := strings.ReplaceAll(path, "/", `\`)
	if extended, ok := strings.CutPrefix(normalized, `\\?\`); ok {
		return strings.HasPrefix(strings.ToLower(extended), `unc\`) || !looksLikeDrivePath(extended)
	}
	return strings.HasPrefix(normalized, `\\`)
}

func looksLikeDrivePath(path string) bool {
	return len(path) >= 3 && (path[0] >= 'a' && path[0] <= 'z' || path[0] >= 'A' && path[0] <= 'Z') && path[1] == ':' && path[2] == '\\'
}

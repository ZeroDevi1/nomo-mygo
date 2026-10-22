package docfs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FolderFileInfo 是目录中一个可打开的文档。
type FolderFileInfo struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// FileTreeEntry 是文件夹树的一项；目录的 Children 按需懒加载，HasChildren 决定是否显示展开箭头。
type FileTreeEntry struct {
	Name           string          `json:"name"`
	Path           string          `json:"path"`
	IsDir          bool            `json:"is_dir"`
	HasChildren    bool            `json:"has_children"`
	ChildrenLoaded bool            `json:"children_loaded"`
	Children       []FileTreeEntry `json:"children"`
}

// ListFolderDocuments 列出目录第一层的可打开文档，按名称忽略大小写排序。
func ListFolderDocuments(path string) ([]FolderFileInfo, error) {
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("不是一个有效的目录：%s", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("读取目录失败：%w", err)
	}
	files := []FolderFileInfo{}
	for _, entry := range entries {
		full := filepath.Join(path, entry.Name())
		if info, err := os.Stat(full); err == nil && info.Mode().IsRegular() && IsSupportedDocument(full) {
			files = append(files, FolderFileInfo{Name: entry.Name(), Path: full})
		}
	}
	sort.SliceStable(files, func(i, j int) bool {
		return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
	})
	return files, nil
}

// ListFolderChildren 浅读 dir 的直接子项：目录在前，同类按名称忽略大小写排序；
// 应用内置忽略规则与 root、dir 两级 .gitignore。rootPath 为空时以 dir 为根。
func ListFolderChildren(dir, rootPath string) ([]FileTreeEntry, error) {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("不是一个有效的目录：%s", dir)
	}
	root := rootPath
	if root == "" {
		root = dir
	}
	rules := loadIgnoreRules(root, dir)
	return readDirChildren(dir, root, rules)
}

type entrySnapshot struct {
	path string
	name string
}

// snapshotDirectory 读取目录项名称后立即释放目录句柄；后续元数据探测不延长其生命周期，
// Windows 上因此可以在列出后立刻重命名该目录。
func snapshotDirectory(dir string) ([]entrySnapshot, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读取目录失败：%w", err)
	}
	snapshots := make([]entrySnapshot, 0, len(entries))
	for _, entry := range entries {
		snapshots = append(snapshots, entrySnapshot{path: filepath.Join(dir, entry.Name()), name: entry.Name()})
	}
	return snapshots, nil
}

func readDirChildren(dir, root string, rules ignoreRules) ([]FileTreeEntry, error) {
	snapshots, err := snapshotDirectory(dir)
	if err != nil {
		return nil, err
	}
	entries := []FileTreeEntry{}
	for _, snapshot := range snapshots {
		// 跟随符号链接判断类型，与 Rust Path::is_dir / is_file 语义一致。
		info, err := os.Stat(snapshot.path)
		isDir := err == nil && info.IsDir()
		if rules.isIgnored(root, snapshot.path, snapshot.name, isDir) {
			continue
		}
		switch {
		case isDir:
			entries = append(entries, FileTreeEntry{
				Name:        snapshot.name,
				Path:        snapshot.path,
				IsDir:       true,
				HasChildren: hasVisibleChildren(snapshot.path, root, rules),
				Children:    []FileTreeEntry{},
			})
		case err == nil && info.Mode().IsRegular() && IsSupportedDocument(snapshot.path):
			entries = append(entries, FileTreeEntry{
				Name:           snapshot.name,
				Path:           snapshot.path,
				ChildrenLoaded: true,
				Children:       []FileTreeEntry{},
			})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries, nil
}

func hasVisibleChildren(dir, root string, rules ignoreRules) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		full := filepath.Join(dir, entry.Name())
		info, err := os.Stat(full)
		isDir := err == nil && info.IsDir()
		if rules.isIgnored(root, full, entry.Name(), isDir) {
			continue
		}
		if isDir || err == nil && info.Mode().IsRegular() && IsSupportedDocument(full) {
			return true
		}
	}
	return false
}

// CheckPathsExist 批量返回各路径是否存在。
func CheckPathsExist(paths []string) []bool {
	result := make([]bool, len(paths))
	for i, path := range paths {
		_, err := os.Stat(path)
		result[i] = err == nil
	}
	return result
}

type ignorePattern struct {
	value         string
	negated       bool
	directoryOnly bool
}

// ignoreRules 是 .gitignore 的轻量子集：支持 * ? 通配、! 取反、/ 结尾仅匹配目录、
// 含 / 的模式按相对根路径匹配；后出现的规则覆盖先前结果。
type ignoreRules []ignorePattern

var builtInIgnorePatterns = []string{
	".git/", ".hg/", ".svn/", "node_modules/", "target/", "dist/", "build/", "out/",
	"coverage/", ".next/", ".svelte-kit/", ".turbo/", ".cache/",
}

func loadIgnoreRules(root, extraDir string) ignoreRules {
	rules := ignoreRules{}
	for _, line := range builtInIgnorePatterns {
		if pattern, ok := parseIgnorePattern(line); ok {
			rules = append(rules, pattern)
		}
	}
	rules = appendGitignore(root, rules)
	if extraDir != "" && extraDir != root {
		rules = appendGitignore(extraDir, rules)
	}
	return rules
}

func appendGitignore(dir string, rules ignoreRules) ignoreRules {
	content, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	for _, line := range strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n") {
		if pattern, ok := parseIgnorePattern(line); ok {
			rules = append(rules, pattern)
		}
	}
	return rules
}

func parseIgnorePattern(line string) (ignorePattern, bool) {
	value := strings.TrimSpace(line)
	if value == "" || strings.HasPrefix(value, "#") {
		return ignorePattern{}, false
	}
	negated := strings.HasPrefix(value, "!")
	if negated {
		value = strings.TrimSpace(strings.TrimLeft(value, "!"))
	}
	if value == "" {
		return ignorePattern{}, false
	}
	directoryOnly := strings.HasSuffix(value, "/")
	value = strings.ReplaceAll(strings.TrimRight(strings.TrimLeft(value, "/"), "/"), `\`, "/")
	if value == "" {
		return ignorePattern{}, false
	}
	return ignorePattern{value: value, negated: negated, directoryOnly: directoryOnly}, true
}

func (rules ignoreRules) isIgnored(root, path, name string, isDir bool) bool {
	ignored := false
	for _, pattern := range rules {
		if pattern.directoryOnly && !isDir {
			continue
		}
		if patternMatches(root, path, name, pattern) {
			ignored = !pattern.negated
		}
	}
	return ignored
}

func patternMatches(root, path, name string, pattern ignorePattern) bool {
	relative := strings.ReplaceAll(name, `\`, "/")
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		relative = strings.ReplaceAll(filepath.ToSlash(rel), `\`, "/")
	}
	if strings.Contains(pattern.value, "/") {
		return wildcardMatch(pattern.value, relative) || strings.HasPrefix(relative, pattern.value+"/")
	}
	return wildcardMatch(pattern.value, name)
}

// wildcardMatch 是按字节的 * / ? 通配匹配（贪心回溯，O(len(text)·len(pattern)) 最坏）。
func wildcardMatch(pattern, text string) bool {
	p, t := 0, 0
	star, matchAfterStar := -1, 0
	for t < len(text) {
		switch {
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == text[t]):
			p++
			t++
		case p < len(pattern) && pattern[p] == '*':
			star = p
			matchAfterStar = t
			p++
		case star >= 0:
			p = star + 1
			matchAfterStar++
			t = matchAfterStar
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

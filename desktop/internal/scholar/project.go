// Package scholar contains the project-scoped data and integrations of Nomo Scholar.
package scholar

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const IndexBudget = 8 << 20

type Project struct {
	Root string
	Main string
}

// Path resolves project inputs without following a symlink outside the project.
func (p Project) Path(name string) (string, error) {
	if !filepath.IsLocal(name) {
		return "", fmt.Errorf("路径不在项目内：%s", name)
	}
	path := filepath.Join(p.Root, name)
	root, err := filepath.EvalSymlinks(p.Root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if _, err := Relative(root, resolved); err != nil {
		return "", err
	}
	return path, nil
}

func Relative(root, path string) (string, error) {
	name, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsLocal(name) || name == "." {
		return "", fmt.Errorf("路径不在项目内：%s", path)
	}
	return name, nil
}

func Read(path string, maxBytes int) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("需要普通文件：%s", path)
	}
	if info.Size() > int64(maxBytes) {
		return nil, fmt.Errorf("文件超过字节上限：%s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !stat.Mode().IsRegular() {
		return nil, fmt.Errorf("需要普通文件：%s", path)
	}
	return readBounded(f, maxBytes)
}

func readBounded(r io.Reader, maxBytes int) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, int64(maxBytes)+1))
	if err == nil && len(b) > maxBytes {
		return nil, errors.New("内容超过字节上限")
	}
	return b, err
}

var (
	texResources = regexp.MustCompile(`\\(addbibresource|bibliography)(?:\s*\[[^\]\n]*\])?\s*\{([^{}]+)\}`)
	typResources = regexp.MustCompile(`(?m)^\s*#bibliography\(\s*(?:"([^"\n]+)"|\(([^()]+)\))`)
	typPaths     = regexp.MustCompile(`"([^"\n]+)"`)
)

func (p Project) Bibliographies(source string) ([]string, error) {
	var names []string
	if strings.EqualFold(filepath.Ext(p.Main), ".typ") {
		for _, m := range typResources.FindAllStringSubmatch(source, -1) {
			if m[1] != "" {
				names = append(names, m[1])
			}
			for _, path := range typPaths.FindAllStringSubmatch(m[2], -1) {
				names = append(names, path[1])
			}
		}
	} else {
		var plain strings.Builder
		for line := range strings.SplitSeq(source, "\n") {
			for i := 0; i < len(line); i++ {
				if line[i] == '\\' {
					i++
				} else if line[i] == '%' {
					line = line[:i]
					break
				}
			}
			plain.WriteString(line)
			plain.WriteByte('\n')
		}
		for _, m := range texResources.FindAllStringSubmatch(plain.String(), -1) {
			for _, name := range strings.Split(m[2], ",") {
				name = strings.TrimSpace(name)
				if m[1] == "bibliography" && filepath.Ext(name) == "" {
					name += ".bib"
				}
				names = append(names, name)
			}
		}
	}
	paths := make([]string, 0, len(names))
	seen := make(map[string]bool)
	for _, name := range names {
		if name == "" || filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
			return nil, fmt.Errorf("无法解析文献文件声明：%s", name)
		}
		path, err := Relative(p.Root, filepath.Join(filepath.Dir(p.Main), filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		if !seen[path] {
			paths = append(paths, path)
			seen[path] = true
		}
	}
	return paths, nil
}

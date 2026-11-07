// Package providers 是应用的内置 Provider 注册入口。文件识别独立于用户是否启用增强能力。
package providers

import (
	"iter"
	"path/filepath"
	"slices"
	"strings"

	"nomo/desktop/provider"
	"nomo/desktop/providers/typesetting"
)

var builtins = [...]*provider.Descriptor{&typesetting.LaTeX, &typesetting.Typst}

func All() iter.Seq[*provider.Descriptor] {
	return func(yield func(*provider.Descriptor) bool) {
		for _, p := range builtins {
			if !yield(p) {
				return
			}
		}
	}
}
func ForPath(path string) *provider.Descriptor {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	for _, p := range builtins {
		if slices.Contains(p.Extensions, ext) {
			return p
		}
	}
	return nil
}

package mdedit

import (
	"sort"
	"strings"

	"github.com/egoist/mygo/ui/markdown/parser"
)

// 上下标语法 ^上标^ 与 ~下标~（Pandoc 规则）：内容非空、不含空白与同种定界符，定界符不与同种字符相邻。
// 与 GFM 删除线的分工：~~x~~ 以及内容含空白的 ~a b~ 仍是删除线；内容不含空白的单个 ~x~ 是下标。
// MyGo 解析器把 ~x~ 解析为删除线、把词内的 H~2~O 与 ^x^ 留作文本，投影与导出都按本文件的规则改判。
// 编辑区以 75% 字号显示（同 <sup>、<sub>，MyGo 的文字片段没有基线偏移），导出为 <sup>、<sub>。

// scriptSpan 报告 src[k] 处的 ^ 或 ~ 是否开始一段上标或下标（闭合定界符须在 limit 之前），
// 返回闭合定界符的偏移；不是时返回 -1。脚注引用 [^x] 的 ^ 不作为开头。
func scriptSpan(src string, k, limit int) int {
	c := src[k]
	if c != '^' && c != '~' {
		return -1
	}
	if k > 0 && (src[k-1] == c || c == '^' && src[k-1] == '[') || k+1 < len(src) && src[k+1] == c {
		return -1
	}
	for q := k + 1; q < limit; q++ {
		switch src[q] {
		case c:
			if q == k+1 || q+1 < len(src) && src[q+1] == c {
				return -1
			}
			return q
		case ' ', '\t', '\n', '\r':
			return -1
		case '\\':
			q++ // 转义的字符属于内容
		}
	}
	return -1
}

// subscriptStrike 报告删除线节点原文 s 是否实为下标：单个 ~ 定界、内容不含空白。
func subscriptStrike(s string) bool {
	if len(s) < 3 || s[0] != '~' || s[1] == '~' || s[len(s)-1] != '~' || s[len(s)-2] == '~' {
		return false
	}
	return !strings.ContainsAny(s[1:len(s)-1], " \t\r\n")
}

// ExpandScripts 返回把 md 原文中的 ^上标^、~下标~ 改写为 <sup>、<sub> 标签后的原文（供导出 HTML 前使用）；
// 没有上下标时原样返回。md 须已解析完整原文。
func ExpandScripts(md *parser.Document) string {
	src := md.Source()
	type edit struct {
		at, n int
		text  string
	}
	var edits []edit
	wrap := func(open, close int, tag string) {
		edits = append(edits, edit{open, 1, "<" + tag + ">"}, edit{close, 1, "</" + tag + ">"})
	}
	for i := 0; i < md.Len(); i++ {
		for _, in := range md.Inlines(i) {
			switch in.Kind {
			case parser.Text:
				for k := in.Start; k < in.End; k++ {
					if q := scriptSpan(src, k, in.End); q > 0 {
						wrap(k, q, map[byte]string{'^': "sup", '~': "sub"}[src[k]])
						k = q
					} else if src[k] == '\\' {
						k++
					}
				}
			case parser.Strikethrough:
				if subscriptStrike(src[in.Start:in.End]) {
					wrap(in.Start, in.End-1, "sub")
				}
			}
		}
	}
	if len(edits) == 0 {
		return src
	}
	sort.Slice(edits, func(a, b int) bool { return edits[a].at < edits[b].at })
	var b strings.Builder
	pos := 0
	for _, e := range edits {
		b.WriteString(src[pos:e.at])
		b.WriteString(e.text)
		pos = e.at + e.n
	}
	b.WriteString(src[pos:])
	return b.String()
}

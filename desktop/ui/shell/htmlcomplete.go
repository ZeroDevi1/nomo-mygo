package shell

import (
	"regexp"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui/editor"
)

// 源码模式的 HTML / CSS 补全（Todo：HTML5／CSS3 代码提示与补全）：光标前为“<字母”时补全标签名，位于开始标签内
// 输入属性名时补全属性，位于 style="…" 中输入属性名时补全 CSS 属性；代码块与公式中不补全。其余情况交给表情补全。
// 列表只收 Markdown 文档常用的 HTML5 标签、属性与 CSS 属性。

var htmlTags = []string{
	"a", "abbr", "article", "aside", "audio", "b", "blockquote", "br", "caption", "center", "cite", "code", "col", "colgroup",
	"dd", "del", "details", "dfn", "div", "dl", "dt", "em", "figcaption", "figure", "font", "footer", "h1", "h2", "h3", "h4", "h5", "h6",
	"header", "hr", "i", "iframe", "img", "ins", "kbd", "li", "mark", "nav", "ol", "p", "picture", "pre", "q", "s", "samp",
	"section", "small", "source", "span", "strong", "sub", "summary", "sup", "table", "tbody", "td", "tfoot", "th", "thead",
	"time", "tr", "u", "ul", "var", "video",
}

var htmlGlobalAttrs = []string{"class", "id", "style", "title", "lang", "dir", "hidden", "align"}

var htmlTagAttrs = map[string][]string{
	"a":       {"href", "target", "rel", "download", "name"},
	"img":     {"src", "alt", "width", "height", "loading"},
	"details": {"open"},
	"td":      {"colspan", "rowspan"}, "th": {"colspan", "rowspan", "scope"},
	"video":  {"src", "controls", "autoplay", "loop", "muted", "poster", "width", "height"},
	"audio":  {"src", "controls", "autoplay", "loop", "muted"},
	"source": {"src", "type", "srcset", "media"},
	"iframe": {"src", "width", "height", "allowfullscreen", "loading"},
	"ol":     {"start", "type", "reversed"},
	"time":   {"datetime"},
	"font":   {"color", "size", "face"},
	"col":    {"span", "width"},
}

var cssProperties = []string{
	"color", "background", "background-color", "font-size", "font-weight", "font-style", "font-family", "text-align",
	"text-decoration", "text-indent", "line-height", "letter-spacing", "margin", "margin-top", "margin-bottom", "padding",
	"border", "border-radius", "width", "height", "max-width", "min-width", "display", "float", "vertical-align", "opacity",
	"white-space", "overflow",
}

var (
	tagNameTail = regexp.MustCompile(`<([a-zA-Z][a-zA-Z0-9]*)?$`)
	openTagTail = regexp.MustCompile(`<([a-zA-Z][a-zA-Z0-9]*)\b[^<>]*$`)
	attrTail    = regexp.MustCompile(`\s([a-zA-Z-]*)$`)
	styleTail   = regexp.MustCompile(`(?i)style\s*=\s*"([^"]*)$`)
	cssPropTail = regexp.MustCompile(`(?:^|;)\s*([a-zA-Z-]*)$`)
)

// sourceCompletion 是源码编辑器的补全入口：HTML / CSS 补全优先，不适用时为表情补全。
func (s *Shell) sourceCompletion(t *Tab, req editor.CompletionRequest) {
	if s.htmlCompletion(t, req) {
		return
	}
	s.sourceEmojiCompletion(t, req)
}

// htmlCompletion 按光标前的同一行原文给出 HTML / CSS 补全，返回是否处理了本次请求。
func (s *Shell) htmlCompletion(t *Tab, req editor.CompletionRequest) bool {
	st := t.source
	if st == nil || t.Kind() != kindMarkdown {
		return false
	}
	text := st.Buffer.String()
	end := clampInt(req.Offset, 0, len(text))
	from := max(0, end-256)
	if nl := strings.LastIndexByte(text[from:end], '\n'); nl >= 0 {
		from += nl + 1
	}
	line := text[from:end]
	items, start, ok := htmlCompletionItems(line)
	if !ok || t.Doc.CodeAt(end) {
		return false
	}
	st.ShowCompletions(from+start, items)
	return true
}

// htmlCompletionItems 返回 line（光标前的同一行原文）末尾适用的补全与被替换部分在 line 中的起点。
func htmlCompletionItems(line string) ([]editor.Completion, int, bool) {
	pick := func(words []string, prefix string, kind editor.CompletionKind, suffix string) []editor.Completion {
		var out []editor.Completion
		lp := strings.ToLower(prefix)
		for _, w := range words {
			if strings.HasPrefix(w, lp) && w != lp {
				out = append(out, editor.Completion{Label: w, Insert: w + suffix, Kind: kind, FilterText: w})
			}
		}
		return out
	}
	if m := tagNameTail.FindStringSubmatchIndex(line); m != nil {
		prefix := ""
		if m[2] >= 0 {
			prefix = line[m[2]:m[3]]
		}
		if items := pick(htmlTags, prefix, editor.CompletionKeyword, ""); len(items) > 0 {
			return items, m[0] + 1, true
		}
		return nil, 0, false
	}
	m := openTagTail.FindStringSubmatchIndex(line)
	if m == nil {
		return nil, 0, false
	}
	tag := strings.ToLower(line[m[2]:m[3]])
	if sm := styleTail.FindStringSubmatchIndex(line); sm != nil {
		decls := line[sm[2]:sm[3]]
		if pm := cssPropTail.FindStringSubmatchIndex(decls); pm != nil {
			prefix := decls[pm[2]:pm[3]]
			if items := pick(cssProperties, prefix, editor.CompletionField, ": "); len(items) > 0 {
				return items, sm[2] + pm[2], true
			}
		}
		return nil, 0, false
	}
	if strings.Count(line[m[0]:], `"`)%2 == 1 {
		return nil, 0, false // 在属性值中
	}
	am := attrTail.FindStringSubmatchIndex(line)
	if am == nil {
		return nil, 0, false
	}
	attrs := append(append([]string{}, htmlTagAttrs[tag]...), htmlGlobalAttrs...)
	sort.Strings(attrs)
	if items := pick(attrs, line[am[2]:am[3]], editor.CompletionField, `=""`); len(items) > 0 {
		return items, am[2], true
	}
	return nil, 0, false
}

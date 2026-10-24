package mdedit

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

// 文字颜色（Todo：文字颜色设置与快捷键）：原文写作 <span style="color: #rrggbb">文字</span>，保存后在其他 Markdown
// 渲染器中同样显示颜色；也识别 <font color="…">。投影按颜色着色（projection.go 的 tagColor），编辑命令
// SetTextColor 包裹、改色或去掉颜色，每次为一个撤销步。

var (
	cssColorRe = regexp.MustCompile(`(?i)(?:^|;)\s*color\s*:\s*([^;"']+)`)
	hexColorRe = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)
	rgbColorRe = regexp.MustCompile(`(?i)^rgb\(\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})\s*\)$`)
	// colorSpanRe 匹配本命令写出的颜色标签（只含 color 样式的 span）。
	colorSpanRe = regexp.MustCompile(`(?i)^<span\s+style\s*=\s*"\s*color\s*:\s*[^;"]+;?\s*"\s*>$`)
)

// namedColors 是常见的 CSS 颜色名（只收文字颜色常用的几种）。
var namedColors = map[string]string{
	"red": "#ff0000", "green": "#008000", "blue": "#0000ff", "orange": "#ffa500", "purple": "#800080",
	"gray": "#808080", "grey": "#808080", "black": "#000000", "white": "#ffffff", "brown": "#a52a2a",
	"pink": "#ffc0cb", "teal": "#008080", "navy": "#000080", "olive": "#808000", "maroon": "#800000",
}

// parseCSSColor 解析 #rgb、#rrggbb、rgb(r, g, b) 与常见颜色名；其他写法返回 false。
func parseCSSColor(v string) (ui.Color, bool) {
	v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "!important"))
	if hex, ok := namedColors[strings.ToLower(v)]; ok {
		v = hex
	}
	if hexColorRe.MatchString(v) {
		return ui.Hex(v), true
	}
	if m := rgbColorRe.FindStringSubmatch(v); m != nil {
		var c [3]uint8
		for i := range c {
			n, _ := strconv.Atoi(m[i+1])
			if n > 255 {
				return ui.Color{}, false
			}
			c[i] = uint8(n)
		}
		return ui.RGB(c[0], c[1], c[2]), true
	}
	return ui.Color{}, false
}

// tagColor 返回开始标签指定的文字颜色：span 的 style 中的 color，font 的 color 属性。
func tagColor(name, tag string) (ui.Color, bool) {
	switch name {
	case "span":
		if m := cssColorRe.FindStringSubmatch(htmlAttr(tag, "style")); m != nil {
			return parseCSSColor(m[1])
		}
	case "font":
		return parseCSSColor(htmlAttr(tag, "color"))
	}
	return ui.Color{}, false
}

// ValidTextColor 报告 hex 是否为 SetTextColor 接受的颜色（#rgb 或 #rrggbb）。
func ValidTextColor(hex string) bool { return hexColorRe.MatchString(hex) }

// SetTextColor 把选区文字设为 hex 颜色；hex 为空时去掉颜色。选区恰好是一个颜色标签的内容（或包含整个标签）时
// 改写该标签，否则用新标签包裹选区。选区为空或跨段落时不做任何事，返回 false。
func (e *Editor) SetTextColor(hex string) bool {
	if hex != "" && !ValidTextColor(hex) {
		return false
	}
	d := e.Doc
	src := d.Source()
	a, z := d.Sel.Range()
	if a == z || strings.Contains(src[a:z], "\n\n") {
		return false
	}
	open := `<span style="color: ` + strings.ToLower(hex) + `">`
	const closeTag = "</span>"
	// 选区包含整个颜色标签：<span …>文字</span>
	if oa, oz, ok := colorSpanAt(src, a); ok && strings.HasSuffix(src[a:z], closeTag) && oa == a {
		inner := src[oz : z-len(closeTag)]
		if hex == "" {
			e.Replace(a, z, inner)
			e.Doc.Sel = Selection{Anchor: a, Head: a + len(inner)}
		} else {
			e.Replace(a, oz, open)
			e.Doc.Sel = Selection{Anchor: a, Head: a + len(open) + len(inner) + len(closeTag)}
		}
		return true
	}
	// 选区恰好是颜色标签的内容：标签紧贴在选区两侧。
	if oa, oz, ok := colorSpanBefore(src, a); ok && strings.HasPrefix(src[z:], closeTag) {
		inner := src[a:z]
		if hex == "" {
			e.Replace(oa, z+len(closeTag), inner)
			e.Doc.Sel = Selection{Anchor: oa, Head: oa + len(inner)}
		} else {
			e.Replace(oa, oz, open)
			na := oa + len(open)
			e.Doc.Sel = Selection{Anchor: na, Head: na + len(inner)}
		}
		return true
	}
	if hex == "" {
		return false
	}
	e.Replace(a, z, open+src[a:z]+closeTag)
	na := a + len(open)
	e.Doc.Sel = Selection{Anchor: na, Head: na + (z - a)}
	return true
}

// colorSpanAt 报告 src[at:] 是否以颜色标签开头，返回标签范围。
func colorSpanAt(src string, at int) (int, int, bool) {
	end := strings.IndexByte(src[at:], '>')
	if end < 0 || !colorSpanRe.MatchString(src[at:at+end+1]) {
		return 0, 0, false
	}
	return at, at + end + 1, true
}

// colorSpanBefore 报告紧贴在 at 之前的是否为颜色标签，返回标签范围。
func colorSpanBefore(src string, at int) (int, int, bool) {
	if at == 0 || src[at-1] != '>' {
		return 0, 0, false
	}
	start := strings.LastIndex(src[:at], "<span")
	if start < 0 || !colorSpanRe.MatchString(src[start:at]) {
		return 0, 0, false
	}
	return start, at, true
}

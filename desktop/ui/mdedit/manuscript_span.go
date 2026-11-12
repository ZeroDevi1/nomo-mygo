package mdedit

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"nomo/desktop/typeset"
)

const (
	manuscriptSpanShift = 24
	manuscriptSpanMask  = style(255) << manuscriptSpanShift
)

type manuscriptSpanStyle struct {
	font        string
	size        float32
	fill, color ui.Color
}

type manuscriptSpanWrapper struct {
	body, end, next int
	flags           style
	format          manuscriptSpanStyle
}

var manuscriptLaTeXColor = regexp.MustCompile(`^\\(textcolor|colorbox)\[HTML\]\{([0-9a-fA-F]{6})\}\{`)
var manuscriptLaTeXFont = regexp.MustCompile(`^\{(?:\\fontspec\{([^{}\n]+)\})?(?:\\fontsize\{([\d.]+)\}\{[^{}\n]+\}\\selectfont )?`)
var manuscriptTypstSize = regexp.MustCompile(`(?:^|,\s*)size:\s*([\d.]+)pt\b`)
var manuscriptTypstColor = regexp.MustCompile(`(?:^|,\s*)fill:\s*rgb\("#?([0-9a-fA-F]{6})"\)`)
var manuscriptTypstFont = regexp.MustCompile(`(?:^|,\s*)font:\s*"([^"\n]+)"`)

func manuscriptStyledSpan(src string, latex bool) (out manuscriptSpanWrapper, ok bool) {
	open, close := byte('['), byte(']')
	if latex {
		open, close = '{', '}'
		switch {
		case strings.HasPrefix(src, `\textcolor[HTML]`) || strings.HasPrefix(src, `\colorbox[HTML]`):
			m := manuscriptLaTeXColor.FindStringSubmatch(src)
			if len(m) != 0 {
				out.body = len(m[0])
				if m[1] == "textcolor" {
					out.format.color = ui.Hex("#" + m[2])
				} else {
					out.format.fill = ui.Hex("#" + m[2])
				}
			}
		case strings.HasPrefix(src, `{\fontspec{`) || strings.HasPrefix(src, `{\fontsize{`):
			if m := manuscriptLaTeXFont.FindStringSubmatch(src); len(m) != 0 && len(m[0]) > 1 {
				out.body, out.format.font = len(m[0]), m[1]
				size, _ := strconv.ParseFloat(m[2], 32)
				out.format.size = float32(size)
			}
		default:
			out.body, out.flags = manuscriptLaTeXStyle(src)
		}
	} else {
		switch {
		case strings.HasPrefix(src, "#text("), strings.HasPrefix(src, "#highlight("):
			argStart := strings.IndexByte(src, '(') + 1
			argEnd := manuscriptSpanEnd(src, argStart, '(', ')', true)
			if argEnd >= 0 && argEnd+1 < len(src) && src[argEnd+1] == '[' {
				args := src[argStart:argEnd]
				out.body = argEnd + 2
				if m := manuscriptTypstSize.FindStringSubmatch(args); len(m) != 0 {
					size, _ := strconv.ParseFloat(m[1], 32)
					out.format.size = float32(size)
				}
				if m := manuscriptTypstFont.FindStringSubmatch(args); len(m) != 0 {
					out.format.font = m[1]
				}
				if m := manuscriptTypstColor.FindStringSubmatch(args); len(m) != 0 {
					if strings.HasPrefix(src, "#highlight(") {
						out.format.fill = ui.Hex("#" + m[1])
					} else {
						out.format.color = ui.Hex("#" + m[1])
					}
				}
				// Keep unfamiliar argument lists visible as source.
				rest := strings.TrimSpace(strings.Trim(manuscriptTypstFont.ReplaceAllString(manuscriptTypstColor.ReplaceAllString(manuscriptTypstSize.ReplaceAllString(args, ""), ""), ""), ", "))
				if rest != "" || out.format == (manuscriptSpanStyle{}) {
					out.body = 0
				}
			}
		default:
			for _, item := range []struct {
				prefix string
				flags  style
			}{{"#strong[", stBold}, {"#emph[", stItalic}, {"#underline[", stUnderline}, {"#strike[", stStrike}, {"#super[", stSmall | stRaise}, {"#sub[", stSmall | stLower}, {"#highlight[", stMark}} {
				if strings.HasPrefix(src, item.prefix) {
					out.body, out.flags = len(item.prefix), item.flags
					break
				}
			}
		}
	}
	if out.body == 0 {
		return out, false
	}
	out.end = manuscriptSpanEnd(src, out.body, open, close, false)
	if out.end < 0 {
		return out, false
	}
	out.next = out.end + 1
	return out, true
}

func manuscriptSpanEnd(src string, start int, open, close byte, quoted bool) int {
	depth, inString := 1, false
	for i := start; i < len(src); i++ {
		ch := src[i]
		if ch == '\\' {
			i++
			continue
		}
		if quoted && ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if ch == open {
			depth++
		} else if ch == close {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func manuscriptSpanFlags(p *projection, outer style, span manuscriptSpanWrapper) style {
	flags := outer | span.flags
	m := span.format
	if m == (manuscriptSpanStyle{}) || len(p.manuscriptStyles) >= 255 {
		return flags
	}
	if i := int(outer >> manuscriptSpanShift); i > 0 {
		parent := p.manuscriptStyles[i-1]
		if m.font == "" {
			m.font = parent.font
		}
		if m.size == 0 {
			m.size = parent.size
		}
		if m.color == (ui.Color{}) {
			m.color = parent.color
		}
		if m.fill == (ui.Color{}) {
			m.fill = parent.fill
		}
	}
	m.font = strings.Clone(m.font)
	p.manuscriptStyles = append(p.manuscriptStyles, m)
	return flags&^manuscriptSpanMask | style(len(p.manuscriptStyles))<<manuscriptSpanShift
}

// ManuscriptTextStyle recovers the visual properties of supported inline wrappers.
func (e *Editor) ManuscriptTextStyle() (start, end int, body string, values typeset.AuthorValues, ok bool) {
	d := e.Doc
	a, z := d.Sel.Range()
	i, _ := d.BlockAt(a)
	if i < 0 || z > d.blocks[i].End || d.blocks[i].End-d.blocks[i].Start > maxLeafBytes {
		return
	}
	b := d.blocks[i]
	src := d.slice(b.Start, b.End)
	for at := 0; at <= a-b.Start && at < len(src); at++ {
		if !strings.ContainsRune("\\{#", rune(src[at])) {
			continue
		}
		span, found := manuscriptStyledSpan(src[at:], d.manuscript.latex)
		if !found || span.format == (manuscriptSpanStyle{}) || b.Start+at+span.next < z {
			continue
		}
		end := b.Start + at + span.next
		content := src[at+span.body : at+span.end]
		fields := make(typeset.AuthorValues)
		for {
			format := span.format
			if format.font != "" {
				fields["font"] = format.font
			}
			if format.size != 0 {
				fields["size"] = strconv.FormatFloat(float64(format.size), 'f', -1, 32)
			}
			for _, color := range []struct {
				name string
				c    ui.Color
			}{{"color", format.color}, {"background", format.fill}} {
				if color.c.A != 0 {
					fields[color.name] = fmt.Sprintf("#%02X%02X%02X", color.c.R, color.c.G, color.c.B)
				}
			}
			inner, found := manuscriptStyledSpan(content, d.manuscript.latex)
			if !found || inner.next != len(content) || inner.format == (manuscriptSpanStyle{}) {
				break
			}
			content, span = content[inner.body:inner.end], inner
		}
		return b.Start + at, end, content, fields, true
	}
	return
}

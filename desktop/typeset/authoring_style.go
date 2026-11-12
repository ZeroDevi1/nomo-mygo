package typeset

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// AuthorValues holds the fields of a source-backed style form. Empty fields inherit the surrounding style.
type AuthorValues map[string]string

const (
	AuthorPage               = "page"
	AuthorParagraph          = "paragraph"
	AuthorLaTeXParagraphOpen = `\begingroup% nomo-style:paragraph`
)

var authorHex = regexp.MustCompile(`^[0-9A-Fa-f]{6}$`)
var authorFont = regexp.MustCompile(`^[\p{L}\p{N} .,+_()-]{1,80}$`)

func (v AuthorValues) number(key string, min, max float64) (string, error) {
	s := strings.TrimSpace(v[key])
	if s == "" {
		return "", nil
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(n) || n < min || n > max {
		return "", fmt.Errorf("%s", key)
	}
	return strconv.FormatFloat(n, 'f', -1, 64), nil
}

func (v AuthorValues) color(key string) (string, error) {
	s := strings.TrimPrefix(strings.TrimSpace(v[key]), "#")
	if s != "" && !authorHex.MatchString(s) {
		return "", fmt.Errorf("%s", key)
	}
	return strings.ToUpper(s), nil
}

func (v AuthorValues) point(key string) float32 {
	n, _ := strconv.ParseFloat(v[key], 32)
	return float32(n)
}

// AuthorCanvasStyle describes document settings the continuous writing canvas can display directly.
type AuthorCanvasStyle struct {
	Font, TextColor, Background, Alignment  string
	Size, LineSpacing, ParagraphGap, Indent float32
}

func (v AuthorValues) CanvasStyle() AuthorCanvasStyle {
	fg, _ := v.color("color")
	bg, _ := v.color("background")
	style := AuthorCanvasStyle{
		Font: v["font"], Size: v.point("size"), LineSpacing: v.point("line"),
		ParagraphGap: v.point("spacing"), Indent: v.point("indent"), Alignment: v["alignment"],
	}
	if fg != "" {
		style.TextColor = "#" + fg
	}
	if bg != "" {
		style.Background = "#" + bg
	}
	return style
}

func authorComment(latex bool) string {
	if latex {
		return "% "
	}
	return "// "
}

func AuthorStylePrefix(latex bool, scope string) string {
	return authorComment(latex) + "nomo-style:" + scope + " "
}

func AuthorStyleEnd(latex bool, scope string) string {
	return authorComment(latex) + "nomo-style:end-" + scope
}

// AuthorStyleSource writes the settings alongside ordinary source. The marker allows the form
// to reopen its own settings without guessing the meaning of a template's arbitrary code.
func AuthorStyleSource(latex bool, scope string, fields AuthorValues, body string) (string, error) {
	page := scope == AuthorPage
	if !page && scope != AuthorParagraph {
		return "", errors.New("scope")
	}
	v := make(AuthorValues)
	for key, value := range fields {
		if value = strings.TrimSpace(value); value != "" {
			v[key] = value
		}
	}
	if len(v) == 0 {
		return body, nil
	}
	code, err := authorStyleCode(latex, page, v)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	header, end := AuthorStylePrefix(latex, scope)+string(data)+"\n", AuthorStyleEnd(latex, scope)
	if page {
		return header + code + end + "\n", nil
	}
	close := authorParagraphClose(latex, v)
	if latex {
		return AuthorLaTeXParagraphOpen + "\n" + header + code + body + close + "\n\\par\n" + end + "\n\\endgroup", nil
	}
	return "#block[\n" + header + code + body + close + "\n" + end + "\n]", nil
}

// ReadAuthorStyle only accepts source that the form can round-trip. Manual changes remain editable as source.
func ReadAuthorStyle(latex bool, scope, source string) (AuthorValues, string, bool) {
	header := AuthorStylePrefix(latex, scope)
	var start string
	var end string
	switch {
	case scope == AuthorPage:
		end = AuthorStyleEnd(latex, scope) + "\n"
	case latex:
		start = AuthorLaTeXParagraphOpen + "\n"
		end = "\n\\par\n" + AuthorStyleEnd(latex, scope) + "\n\\endgroup"
	default:
		start = "#block[\n"
		end = "\n" + AuthorStyleEnd(latex, scope) + "\n]"
	}
	src, ok := strings.CutPrefix(source, start+header)
	if !ok || !strings.HasSuffix(src, end) {
		return nil, "", false
	}
	raw, rest, ok := strings.Cut(src, "\n")
	var v AuthorValues
	if !ok || len(raw) > 4096 || json.Unmarshal([]byte(raw), &v) != nil {
		return nil, "", false
	}
	code, err := authorStyleCode(latex, scope == AuthorPage, v)
	if err != nil {
		return nil, "", false
	}
	body, ok := strings.CutPrefix(strings.TrimSuffix(rest, end), code)
	if ok && scope != AuthorPage {
		body, ok = strings.CutSuffix(body, authorParagraphClose(latex, v))
	}
	return v, body, ok && (scope != AuthorPage || body == "")
}

func authorParagraphClose(latex bool, v AuthorValues) string {
	if !latex {
		if fill, _ := v.color("background"); fill != "" {
			return "\n#parbreak()\n]"
		}
		return "\n#parbreak()"
	}
	if fill, _ := v.color("background"); fill != "" {
		return "\n\\par\\end{minipage}}"
	}
	return ""
}

func authorStyleCode(latex, page bool, v AuthorValues) (string, error) {
	numbers := make(AuthorValues)
	for _, spec := range []struct {
		key      string
		min, max float64
	}{
		{"size", 5, 96}, {"line", 1, 3}, {"spacing", 0, 72}, {"indent", 0, 72},
		{"margin", 5, 70}, {"columns", 1, 2},
	} {
		value, err := v.number(spec.key, spec.min, spec.max)
		if err != nil {
			return "", err
		}
		numbers[spec.key] = value
	}
	if numbers["columns"] != "" && numbers["columns"] != "1" && numbers["columns"] != "2" {
		return "", errors.New("columns")
	}
	colors := make(AuthorValues)
	for _, key := range []string{"color", "background"} {
		color, err := v.color(key)
		if err != nil {
			return "", err
		}
		colors[key] = color
	}
	font := v["font"]
	if font != "" && !authorFont.MatchString(font) {
		return "", errors.New("font")
	}
	align, paper, orientation, numbering := v["alignment"], v["paper"], v["orientation"], v["numbering"]
	if !authorChoice(align, "left", "center", "right", "justify") {
		return "", errors.New("alignment")
	}
	if !authorChoice(paper, "a4", "a5", "us-letter") {
		return "", errors.New("paper")
	}
	if !authorChoice(orientation, "portrait", "landscape") {
		return "", errors.New("orientation")
	}
	if !authorChoice(numbering, "none", "1", "i", "I") {
		return "", errors.New("numbering")
	}
	for _, key := range []string{"header", "footer"} {
		if len(v[key]) > 256 || strings.ContainsAny(v[key], "\n\r") {
			return "", fmt.Errorf("%s", key)
		}
	}
	if latex {
		return latexStyleCode(page, v, numbers, colors), nil
	}
	return typstStyleCode(page, v, numbers, colors), nil
}

func authorChoice(value string, choices ...string) bool {
	if value == "" {
		return true
	}
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

func latexStyleCode(page bool, v, n, colors AuthorValues) string {
	var setup, body strings.Builder
	write := func(text string) {
		body.WriteString(text)
		body.WriteByte('\n')
	}
	if page {
		var geometry []string
		if paper := v["paper"]; paper != "" {
			if paper == "us-letter" {
				paper = "letter"
			}
			geometry = append(geometry, paper+"paper")
		}
		if orientation := v["orientation"]; orientation != "" {
			geometry = append(geometry, orientation)
		}
		if margin := n["margin"]; margin != "" {
			geometry = append(geometry, "margin="+margin+"mm")
		}
		if len(geometry) > 0 {
			fmt.Fprintf(&setup, "\\RequirePackage{geometry}\n\\geometry{%s}\n", strings.Join(geometry, ","))
		}
		if v["font"] != "" {
			fmt.Fprintf(&setup, "\\RequirePackage{fontspec}\n\\setmainfont{%s}\n", v["font"])
		}
		if colors["background"] != "" {
			write(`\pagecolor[HTML]{` + colors["background"] + "}")
		}
		if n["columns"] == "2" {
			write(`\twocolumn`)
		} else if n["columns"] == "1" {
			write(`\onecolumn`)
		}
		if numbering := v["numbering"]; numbering != "" && numbering != "none" {
			write(`\pagenumbering{` + map[string]string{"1": "arabic", "i": "roman", "I": "Roman"}[numbering] + "}")
		}
		if v["header"] != "" || v["footer"] != "" || v["numbering"] == "none" {
			setup.WriteString("\\RequirePackage{fancyhdr}\n")
			setup.WriteString("\\fancypagestyle{nomoauthoring}{\n\\fancyhf{}\n")
			if v["header"] != "" {
				fmt.Fprintf(&setup, "\\fancyhead[C]{%s}\n", AuthorLiteral(true, v["header"]))
			}
			footer := AuthorLiteral(true, v["footer"])
			if v["numbering"] != "none" {
				footer += `\enspace\thepage`
			}
			fmt.Fprintf(&setup, "\\fancyfoot[C]{%s}\n}\n", footer)
			write(`\pagestyle{nomoauthoring}`)
		}
	} else if v["font"] != "" {
		write(`\fontspec{` + v["font"] + "}")
	}
	if colors["color"] != "" || colors["background"] != "" {
		if page {
			setup.WriteString("\\RequirePackage{xcolor}\n")
		}
		if fg := colors["color"]; fg != "" {
			write(`\color[HTML]{` + fg + "}")
		}
	}
	if size := n["size"]; size != "" {
		pt, _ := strconv.ParseFloat(size, 64)
		write(fmt.Sprintf("\\fontsize{%s}{%.3g}\\selectfont", size, pt*1.2))
	}
	if line := n["line"]; line != "" {
		write(`\linespread{` + line + `}\selectfont`)
	}
	for _, item := range [][2]string{{"spacing", "parskip"}, {"indent", "parindent"}} {
		if value := n[item[0]]; value != "" {
			write(`\setlength{\` + item[1] + "}{" + value + "pt}")
		}
	}
	if align := v["alignment"]; align != "" {
		switch align {
		case "left":
			write(`\raggedright`)
		case "center":
			write(`\centering`)
		case "right":
			write(`\raggedleft`)
		case "justify":
			write(`\leftskip=0pt\rightskip=0pt\parfillskip=0pt plus 1fil\relax`)
		}
	}
	if !page {
		if fill := colors["background"]; fill != "" {
			style := body.String()
			return "\\noindent\\colorbox[HTML]{" + fill + "}{\\begin{minipage}{\\dimexpr\\linewidth-2\\fboxsep\\relax}\n" + style
		}
		return body.String()
	}
	if body.Len() != 0 {
		setup.WriteString("\\AtBeginDocument{%\n")
		setup.WriteString(body.String())
		setup.WriteString("}\n")
	}
	return setup.String()
}

func typstStyleCode(page bool, v, n, colors AuthorValues) string {
	var text, par, paper []string
	if v["font"] != "" {
		text = append(text, "font: "+strconv.Quote(v["font"]))
	}
	if n["size"] != "" {
		text = append(text, "size: "+n["size"]+"pt")
	}
	if colors["color"] != "" {
		text = append(text, "fill: rgb("+strconv.Quote(colors["color"])+")")
	}
	if n["line"] != "" {
		line, _ := strconv.ParseFloat(n["line"], 64)
		par = append(par, fmt.Sprintf("leading: %.2fem", line-.55))
	}
	if n["spacing"] != "" {
		par = append(par, "spacing: "+n["spacing"]+"pt")
	}
	if n["indent"] != "" {
		par = append(par, "first-line-indent: (amount: "+n["indent"]+"pt, all: true)")
	}
	align := v["alignment"]
	if align != "" {
		par = append(par, "justify: "+strconv.FormatBool(align == "justify"))
	}
	if page {
		if v["paper"] != "" {
			paper = append(paper, "paper: "+strconv.Quote(v["paper"]))
		}
		if v["orientation"] != "" {
			paper = append(paper, "flipped: "+strconv.FormatBool(v["orientation"] == "landscape"))
		}
		if n["margin"] != "" {
			paper = append(paper, "margin: "+n["margin"]+"mm")
		}
		if n["columns"] != "" {
			paper = append(paper, "columns: "+n["columns"])
		}
		if colors["background"] != "" {
			paper = append(paper, "fill: rgb("+strconv.Quote(colors["background"])+")")
		}
		if style := v["numbering"]; style != "" {
			if style != "none" {
				style = strconv.Quote(style)
			}
			paper = append(paper, "numbering: "+style)
		}
		if v["header"] != "" {
			paper = append(paper, "header: align(center, text("+strconv.Quote(v["header"])+"))")
		}
		if v["footer"] != "" {
			content := "text(" + strconv.Quote(v["footer"]) + ")"
			if style := v["numbering"]; style != "" && style != "none" {
				content += " + h(1em) + context counter(page).display(" + strconv.Quote(style) + ")"
			}
			paper = append(paper, "footer: align(center, "+content+")")
		}
	}
	var code strings.Builder
	for _, style := range []struct {
		name string
		args []string
	}{{"page", paper}, {"text", text}, {"par", par}} {
		if len(style.args) > 0 {
			fmt.Fprintf(&code, "#set %s(%s)\n", style.name, strings.Join(style.args, ", "))
		}
	}
	if align != "" {
		if align == "justify" {
			align = "left"
		}
		fmt.Fprintf(&code, "#set align(%s)\n", align)
	}
	if !page && colors["background"] != "" {
		fmt.Fprintf(&code, "#block(fill: rgb(%q), inset: 4pt, width: 100%%)[\n", colors["background"])
	}
	return code.String()
}

var latexLiteral = strings.NewReplacer("\\", `\textbackslash{}`, "{", `\{`, "}", `\}`, "$", `\$`, "%", `\%`, "#", `\#`, "&", `\&`, "_", `\_`, "^", `\textasciicircum{}`, "~", `\textasciitilde{}`)
var typstLiteral = strings.NewReplacer("\\", "\\\\", "#", "\\#", "[", "\\[", "]", "\\]", "$", "\\$", "*", "\\*", "_", "\\_", "@", "\\@", "<", "\\<", ">", "\\>", "`", "\\`", "/", "\\/", "=", "\\=")

func AuthorLiteral(latex bool, s string) string {
	if latex {
		return latexLiteral.Replace(s)
	}
	return typstLiteral.Replace(s)
}

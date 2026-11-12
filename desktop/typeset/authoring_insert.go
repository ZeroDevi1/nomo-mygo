package typeset

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// AuthorInsert holds native markup and the range to leave selected after insertion.
type AuthorInsert struct {
	Before, Body, After string
	Packages            []string
	Block               bool
}

func (e AuthorInsert) Source() string { return e.Before + e.Body + e.After }

func AuthorObjectSource(latex bool, action string, v AuthorValues) (AuthorInsert, error) {
	out, err := AuthorCommand(latex, action, v, "")
	if err != nil {
		return AuthorInsert{}, err
	}
	fields, err := json.Marshal(v)
	if err != nil || len(fields) > 8192 {
		return AuthorInsert{}, errors.New("data")
	}
	scope := "object-" + action
	out.Before = AuthorStylePrefix(latex, scope) + string(fields) + "\n" + out.Before
	out.After += "\n" + AuthorStyleEnd(latex, scope)
	if len(out.Source()) > 16<<10 {
		return AuthorInsert{}, errors.New("data")
	}
	return out, nil
}

func ReadAuthorObject(latex bool, action, source string) (AuthorValues, bool) {
	header, _, ok := strings.Cut(source, "\n")
	raw, match := strings.CutPrefix(header, AuthorStylePrefix(latex, "object-"+action))
	if !ok || !match || len(raw) > 8192 {
		return nil, false
	}
	var fields AuthorValues
	if json.Unmarshal([]byte(raw), &fields) != nil {
		return nil, false
	}
	out, err := AuthorObjectSource(latex, action, fields)
	return fields, err == nil && out.Source() == source
}

func AuthorCommand(latex bool, action string, v AuthorValues, selected string) (AuthorInsert, error) {
	wrap := func(tex, typ string, packages ...string) (AuthorInsert, error) {
		if latex {
			return AuthorInsert{Before: tex + "{", Body: selected, After: "}", Packages: packages}, nil
		}
		return AuthorInsert{Before: typ + "[", Body: selected, After: "]"}, nil
	}
	switch action {
	case "bold":
		return wrap(`\textbf`, "#strong")
	case "italic":
		return wrap(`\emph`, "#emph")
	case "underline":
		return wrap(`\underline`, "#underline")
	case "strike":
		return wrap(`\sout`, "#strike", "ulem")
	case "monospace":
		return wrap(`\texttt`, `#text(font: "DejaVu Sans Mono")`)
	case "super":
		return wrap(`\textsuperscript`, "#super")
	case "sub":
		return wrap(`\textsubscript`, "#sub")
	case "footnote":
		return wrap(`\footnote`, "#footnote")
	case "heading":
		level, _ := strconv.Atoi(v["level"])
		if level < 0 || level > 5 {
			return AuthorInsert{}, errors.New("level")
		}
		if level == 0 {
			return AuthorInsert{Body: selected, Block: true}, nil
		}
		if latex {
			cmd := [...]string{`\section`, `\subsection`, `\subsubsection`, `\paragraph`, `\subparagraph`}[level-1]
			return AuthorInsert{Before: cmd + "{", Body: selected, After: "}", Block: true}, nil
		}
		return AuthorInsert{Before: strings.Repeat("=", level) + " ", Body: selected, Block: true}, nil
	case "text":
		return authorTextStyle(latex, v, selected)
	case "equation":
		return authorEquation(latex, v, selected)
	case "bullet", "ordered":
		if selected == "" {
			selected = "Item"
		}
		lines := strings.Split(selected, "\n")
		for i, line := range lines {
			prefix := "- "
			if action == "ordered" {
				prefix = "+ "
			}
			if latex {
				prefix = `\item `
			}
			lines[i] = prefix + line
		}
		out := AuthorInsert{Body: strings.Join(lines, "\n"), Block: true}
		if latex {
			env := "itemize"
			if action == "ordered" {
				env = "enumerate"
			}
			out.Before, out.After = "\\begin{"+env+"}\n", "\n\\end{"+env+"}"
		}
		return out, nil
	case "quote":
		if latex {
			return AuthorInsert{Before: "\\begin{quote}\n", Body: selected, After: "\n\\end{quote}", Block: true}, nil
		}
		return AuthorInsert{Before: "#quote(block: true)[\n", Body: selected, After: "\n]", Block: true}, nil
	case "table":
		return authorTable(latex, v)
	case "figure", "link":
		path := strings.TrimSpace(v["path"])
		if path == "" || strings.ContainsAny(path, "\n\r") || len(path) > 512 || latex && strings.ContainsAny(path, "{}%\\#") {
			return AuthorInsert{}, errors.New("path")
		}
		if action == "link" {
			if latex {
				return wrap(`\href{`+path+"}", "#link", "hyperref")
			}
			return wrap("", "#link("+strconv.Quote(path)+")")
		}
		w, err := v.number("width", 5, 100)
		if err != nil {
			return AuthorInsert{}, err
		}
		if w == "" {
			w = "75"
		}
		caption := AuthorLiteral(latex, v["caption"])
		if latex {
			pct, _ := strconv.ParseFloat(w, 64)
			src := fmt.Sprintf("\\begin{figure}[htbp]\n\\centering\n\\includegraphics[width=%.3g\\linewidth]{%s}", pct/100, path)
			if caption != "" {
				src += "\n\\caption{" + caption + "}"
			}
			return AuthorInsert{Body: src + "\n\\end{figure}", Block: true, Packages: []string{"graphicx"}}, nil
		}
		src := "#image(" + strconv.Quote(path) + ", width: " + w + "%)"
		if caption != "" {
			src = "#figure(" + src[1:] + ", caption: [" + caption + "])"
		}
		return AuthorInsert{Body: src, Block: true}, nil
	case "reference", "citation", "label":
		key := strings.TrimSpace(v["name"])
		if !authorLabel.MatchString(key) {
			return AuthorInsert{}, errors.New("name")
		}
		if latex {
			cmd := map[string]string{"reference": `\ref`, "citation": `\cite`, "label": `\label`}[action]
			return AuthorInsert{Body: cmd + "{" + key + "}"}, nil
		}
		if action == "label" {
			return AuthorInsert{Body: "<" + key + ">"}, nil
		}
		return AuthorInsert{Body: "@" + key}, nil
	case "pagebreak", "outline":
		src := map[string][2]string{"pagebreak": {`\newpage`, "#pagebreak()"}, "outline": {`\tableofcontents`, "#outline()"}}[action]
		if latex {
			return AuthorInsert{Body: src[0], Block: true}, nil
		}
		return AuthorInsert{Body: src[1], Block: true}, nil
	case "custom":
		if v["kind"] == "source" {
			return AuthorInsert{Body: selected}, nil
		}
		return authorCustom(latex, v, selected)
	}
	return AuthorInsert{}, errors.New("action")
}

func authorTextStyle(latex bool, v AuthorValues, selected string) (AuthorInsert, error) {
	result := AuthorInsert{Body: selected}
	fg, err := v.color("color")
	if err != nil {
		return result, err
	}
	bg, err := v.color("background")
	if err != nil {
		return result, err
	}
	size, err := v.number("size", 5, 96)
	if err != nil {
		return result, err
	}
	font := strings.TrimSpace(v["font"])
	if font != "" && !authorFont.MatchString(font) {
		return result, errors.New("font")
	}
	wrap := func(a, z string) {
		result.Before = a + result.Before
		result.After += z
	}
	if latex {
		if size != "" || font != "" {
			var prefix strings.Builder
			prefix.WriteByte('{')
			if font != "" {
				result.Packages = append(result.Packages, "fontspec")
				fmt.Fprintf(&prefix, "\\fontspec{%s}", font)
			}
			if size != "" {
				n, _ := strconv.ParseFloat(size, 64)
				fmt.Fprintf(&prefix, "\\fontsize{%s}{%.3g}\\selectfont ", size, n*1.2)
			}
			wrap(prefix.String(), "}")
		}
		if fg != "" {
			wrap(`\textcolor[HTML]{`+fg+"}{", "}")
		}
		if bg != "" {
			wrap(`\colorbox[HTML]{`+bg+"}{", "}")
		}
		if bg != "" || fg != "" {
			result.Packages = append(result.Packages, "xcolor")
		}
		return result, nil
	}
	var args []string
	if size != "" {
		args = append(args, "size: "+size+"pt")
	}
	if font != "" {
		args = append(args, "font: "+strconv.Quote(font))
	}
	if fg != "" {
		args = append(args, "fill: rgb("+strconv.Quote(fg)+")")
	}
	if len(args) != 0 {
		wrap("#text("+strings.Join(args, ", ")+")[", "]")
	}
	if bg != "" {
		wrap("#highlight(fill: rgb("+strconv.Quote(bg)+"))[", "]")
	}
	return result, nil
}

func authorEquation(latex bool, v AuthorValues, selected string) (AuthorInsert, error) {
	a, b, c := v["a"], v["b"], v["c"]
	placeholder := func(s, fallback string) string {
		if strings.TrimSpace(s) == "" {
			return fallback
		}
		return s
	}
	var src string
	packages := []string(nil)
	if latex && (v["template"] == "matrix" || v["template"] == "cases" || v["template"] == "text") {
		packages = []string{"amsmath"}
	}
	two := func(tex, typ string) string {
		if latex {
			return fmt.Sprintf(tex, a, b)
		}
		return fmt.Sprintf(typ, a, b)
	}
	switch v["template"] {
	case "fraction":
		a, b = placeholder(a, "a"), placeholder(b, "b")
		src = two(`\frac{%s}{%s}`, "frac(%s, %s)")
	case "root":
		a = placeholder(a, "x")
		if latex {
			src = `\sqrt{` + a + "}"
		} else {
			src = "sqrt(" + a + ")"
		}
	case "power", "subscript":
		a, b = placeholder(a, "x"), placeholder(b, "2")
		op := "^"
		if v["template"] == "subscript" {
			op = "_"
		}
		src = two("%s"+op+"{%s}", "%s"+op+"(%s)")
	case "integral", "sum", "limit":
		a, b, c = placeholder(a, "0"), placeholder(b, "1"), placeholder(c, "x")
		if v["template"] == "limit" {
			a, b, c = placeholder(v["a"], "x"), placeholder(v["b"], "0"), placeholder(v["c"], "f(x)")
			if latex {
				src = fmt.Sprintf(`\lim_{%s\to %s} %s`, a, b, c)
			} else {
				src = fmt.Sprintf("lim_(%s -> %s) %s", a, b, c)
			}
		} else {
			op := v["template"]
			if op == "integral" {
				op = "int"
			}
			if latex {
				src = fmt.Sprintf(`\%s_{%s}^{%s} %s`, op, a, b, c)
			} else {
				src = fmt.Sprintf("%s_(%s)^(%s) %s", op, a, b, c)
			}
		}
	case "matrix":
		input := placeholder(v["data"], "a | b\nc | d")
		rows := strings.Split(input, "\n")
		if len(rows) > 12 {
			return AuthorInsert{}, errors.New("data")
		}
		columns := -1
		for i, row := range rows {
			cells := strings.Split(strings.TrimSpace(row), "|")
			if columns == -1 {
				columns = len(cells)
			}
			if len(cells) != columns || columns > 12 {
				return AuthorInsert{}, errors.New("data")
			}
			for j := range cells {
				cells[j] = strings.TrimSpace(cells[j])
			}
			separator := ", "
			if latex {
				separator = " & "
			}
			rows[i] = strings.Join(cells, separator)
		}
		if latex {
			src = "\\begin{pmatrix}" + strings.Join(rows, ` \\ `) + "\\end{pmatrix}"
		} else {
			src = "mat(" + strings.Join(rows, "; ") + ")"
		}
	case "cases":
		a, b = placeholder(a, "x, x >= 0"), placeholder(b, "-x, x < 0")
		if latex {
			src = "\\begin{cases}" + strings.ReplaceAll(a, ",", " & ") + ` \\ ` + strings.ReplaceAll(b, ",", " & ") + "\\end{cases}"
		} else {
			src = "cases(" + strings.ReplaceAll(a, ",", " & ") + ", " + strings.ReplaceAll(b, ",", " & ") + ")"
		}
	default:
		src = placeholder(a, placeholder(selected, "x = y"))
	}
	src = strings.TrimSpace(src)
	if len(src) > 8192 {
		return AuthorInsert{}, errors.New("a")
	}
	if v["display"] == "numbered" {
		label := strings.TrimSpace(v["name"])
		if label != "" && !authorLabel.MatchString(label) {
			return AuthorInsert{}, errors.New("name")
		}
		if latex {
			before := "\\begin{equation}\n"
			if label != "" {
				before += "\\label{" + label + "}\n"
			}
			return AuthorInsert{Before: before, Body: src, After: "\n\\end{equation}", Packages: packages, Block: true}, nil
		}
		after := "\n$)"
		if label != "" {
			after += " <" + label + ">"
		}
		return AuthorInsert{Before: "#math.equation(block: true, numbering: \"(1)\", $\n", Body: src, After: after, Block: true}, nil
	}
	if v["display"] == "block" {
		if latex {
			return AuthorInsert{Before: "\\[\n", Body: src, After: "\n\\]", Packages: packages, Block: true}, nil
		}
		return AuthorInsert{Before: "$\n", Body: src, After: "\n$", Block: true}, nil
	}
	if strings.ContainsAny(src, "\r\n") {
		return AuthorInsert{}, errors.New("a")
	}
	return AuthorInsert{Before: "$", Body: src, After: "$", Packages: packages}, nil
}

func authorTable(latex bool, v AuthorValues) (AuthorInsert, error) {
	var grid [][]string
	if data := v["csv"]; data != "" {
		if len(data) > 8192 || json.Unmarshal([]byte(data), &grid) != nil || len(grid) == 0 || len(grid) > 30 {
			return AuthorInsert{}, errors.New("data")
		}
	} else {
		lines := strings.Split(strings.TrimSpace(v["data"]), "\n")
		if len(lines) > 30 || len(v["data"]) > 8192 {
			return AuthorInsert{}, errors.New("data")
		}
		if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
			lines = []string{"A | B", "1 | 2"}
		}
		for _, line := range lines {
			separator := "|"
			if strings.Contains(line, "\t") {
				separator = "\t"
			}
			grid = append(grid, strings.Split(strings.TrimSuffix(line, "\r"), separator))
		}
	}
	cols := 0
	newlines := strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ")
	for _, row := range grid {
		cols = max(cols, len(row))
		for i := range row {
			cell := newlines.Replace(strings.TrimSpace(row[i]))
			row[i] = AuthorLiteral(latex, cell)
		}
	}
	if cols == 0 || cols > 12 {
		return AuthorInsert{}, errors.New("data")
	}
	var out strings.Builder
	if latex {
		if v["caption"] != "" {
			fmt.Fprintf(&out, "\\begin{table}[htbp]\n\\centering\n\\caption{%s}\n", AuthorLiteral(true, v["caption"]))
		}
		fmt.Fprintf(&out, "\\begin{tabular}{%s}\n\\toprule\n", strings.Repeat("l", cols))
	} else {
		if v["caption"] != "" {
			out.WriteString("#figure(\n  table(")
		} else {
			out.WriteString("#table(")
		}
		fmt.Fprintf(&out, "columns: %d,\n", cols)
	}
	for i, row := range grid {
		row = append(row, make([]string, cols-len(row))...)
		if latex {
			out.WriteString(strings.Join(row, " & "))
			out.WriteString(" \\\\\n")
			if i == 0 {
				out.WriteString("\\midrule\n")
			}
		} else {
			cells := "[" + strings.Join(row, "], [") + "]"
			if i == 0 {
				cells = "table.header(" + cells + ")"
			}
			fmt.Fprintf(&out, "  %s,\n", cells)
		}
	}
	if latex {
		out.WriteString("\\bottomrule\n\\end{tabular}")
		if v["caption"] != "" {
			out.WriteString("\n\\end{table}")
		}
		return AuthorInsert{Body: out.String(), Packages: []string{"booktabs"}, Block: true}, nil
	}
	out.WriteByte(')')
	if v["caption"] != "" {
		fmt.Fprintf(&out, ", caption: [%s])", AuthorLiteral(false, v["caption"]))
	}
	return AuthorInsert{Body: out.String(), Block: true}, nil
}

var authorLabel = regexp.MustCompile(`^[A-Za-z0-9_:.\-]{1,96}$`)
var authorLaTeXCommand = regexp.MustCompile(`^[a-zA-Z@]+\*?$`)
var authorTypstCommand = regexp.MustCompile(`^[\p{L}_][\p{L}\p{N}_.-]*$`)

func authorCustom(latex bool, v AuthorValues, selected string) (AuthorInsert, error) {
	name, kind := strings.TrimSpace(v["name"]), v["kind"]
	if len(name) > 120 || len(v["args"]) > 8192 || len(v["options"]) > 2048 {
		return AuthorInsert{}, errors.New("name")
	}
	args := strings.Split(strings.TrimSpace(v["args"]), "\n")
	if len(args) == 1 && args[0] == "" {
		args = nil
	}
	if latex {
		name = strings.TrimPrefix(name, "\\")
		options := strings.TrimSpace(v["options"])
		if options != "" {
			options = "[" + options + "]"
		}
		for i := range args {
			args[i] = "{" + strings.TrimSpace(args[i]) + "}"
		}
		arguments := options + strings.Join(args, "")
		if kind == "environment" {
			if !authorLabel.MatchString(name) && !authorLaTeXCommand.MatchString(name) {
				return AuthorInsert{}, errors.New("name")
			}
			return AuthorInsert{Before: "\\begin{" + name + "}" + arguments + "\n", Body: selected, After: "\n\\end{" + name + "}", Block: true}, nil
		}
		if !authorLaTeXCommand.MatchString(name) {
			return AuthorInsert{}, errors.New("name")
		}
		out := AuthorInsert{Before: "\\" + name + arguments}
		if selected != "" {
			out.Before += "{"
			out.Body, out.After = selected, "}"
		}
		return out, nil
	}
	name = strings.TrimPrefix(name, "#")
	if !authorTypstCommand.MatchString(name) {
		return AuthorInsert{}, errors.New("name")
	}
	for i := range args {
		args[i] = strings.TrimSpace(args[i])
	}
	call := name + "(" + strings.Join(args, ", ") + ")"
	switch kind {
	case "set":
		return AuthorInsert{Body: "#set " + call, Block: true}, nil
	case "show":
		if len(args) == 0 {
			return AuthorInsert{}, errors.New("args")
		}
		return AuthorInsert{Body: "#show " + name + ": " + strings.Join(args, " "), Block: true}, nil
	default:
		out := AuthorInsert{Before: "#" + call}
		if selected != "" {
			out.Before += "["
			out.Body, out.After = selected, "]"
		}
		return out, nil
	}
}

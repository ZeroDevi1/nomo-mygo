package typeset

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// SourceRange is a half-open UTF-8 byte range within a manuscript fragment.
type SourceRange struct{ Start, End int }

type SourceChange struct {
	SourceRange
	Text string
}

// AuthorObject describes supported fields of existing native markup. Missing
// fields are not writable; edits are limited to the ranges of recognized values.
type AuthorObject struct {
	Kind, Label, DataPath string
	Fields                AuthorValues
	Ranges                map[string]SourceRange
	widthUnit             string
}

type sourcePart struct {
	SourceRange
	text string
}

func trimPart(src string, start, end int) sourcePart {
	for start < end && strings.ContainsRune(" \r\n\t", rune(src[start])) {
		start++
	}
	for end > start && strings.ContainsRune(" \r\n\t", rune(src[end-1])) {
		end--
	}
	return sourcePart{SourceRange{start, end}, src[start:end]}
}

// markupGroup recognizes a single delimited group, including nested brackets,
// strings and comments in Typst, and escaped commands and comments in TeX.
func markupGroup(src string, at int, latex bool) (int, bool) {
	if at < 0 || at >= len(src) || !strings.ContainsRune("([{", rune(src[at])) {
		return 0, false
	}
	stack := []byte{src[at]}
	for i := at + 1; i < len(src); i++ {
		ch := src[i]
		switch {
		case ch == '\\':
			i++
		case latex && ch == '%' || !latex && strings.HasPrefix(src[i:], "//"):
			line := strings.IndexByte(src[i:], '\n')
			if line < 0 {
				return 0, false
			}
			i += line
		case !latex && strings.HasPrefix(src[i:], "/*"):
			depth := 1
			for i += 2; i < len(src) && depth > 0; i++ {
				if strings.HasPrefix(src[i:], "/*") {
					depth++
					i++
				} else if strings.HasPrefix(src[i:], "*/") {
					depth--
					i++
				}
			}
			i--
		case !latex && ch == '"' && stack[len(stack)-1] != '[':
			for i++; i < len(src) && src[i] != '"'; i++ {
				if src[i] == '\\' {
					i++
				}
			}
		case !latex && ch == '`':
			n := len(src[i:]) - len(strings.TrimLeft(src[i:], "`"))
			i += n
			end := strings.Index(src[i:], strings.Repeat("`", n))
			if end < 0 {
				return 0, false
			}
			i += end + n - 1
		case latex && stack[0] == '{' && ch == '{',
			latex && stack[0] == '[' && (ch == '{' || ch == '[' && stack[len(stack)-1] != '{'),
			!latex && strings.ContainsRune("([{", rune(ch)):
			if len(stack) >= 128 {
				return 0, false
			}
			stack = append(stack, ch)
		case ch == '}' && stack[len(stack)-1] == '{',
			ch == ']' && stack[len(stack)-1] == '[',
			ch == ')' && stack[len(stack)-1] == '(':
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

func markupSpace(src string, at int) int {
	for at < len(src) && strings.ContainsRune(" \r\n\t", rune(src[at])) {
		at++
	}
	return at
}

func typstParts(src string, start, end int) []sourcePart {
	var out []sourcePart
	for i := start; i < end; i++ {
		switch src[i] {
		case '\\':
			i++
		case '"':
			for i++; i < end && src[i] != '"'; i++ {
				if src[i] == '\\' {
					i++
				}
			}
		case '(', '[', '{':
			if z, ok := markupGroup(src[:end], i, false); ok {
				i = z - 1
			} else {
				return nil
			}
		case '/':
			if strings.HasPrefix(src[i:end], "//") {
				n := strings.IndexByte(src[i:end], '\n')
				if n < 0 {
					i = end
				} else {
					i += n
				}
			}
		case ',':
			out = append(out, trimPart(src, start, i))
			start = i + 1
		}
	}
	if p := trimPart(src, start, end); p.text != "" {
		out = append(out, p)
	}
	return out
}

func typstCall(src string, part sourcePart, name string) ([]sourcePart, bool) {
	text := strings.TrimPrefix(part.text, "#")
	if !strings.HasPrefix(text, name+"(") {
		return nil, false
	}
	at := part.End - len(text) + len(name)
	end, ok := markupGroup(src, at, false)
	if !ok || end > part.End {
		return nil, false
	}
	return typstParts(src, at+1, end-1), true
}

func namedPart(src string, parts []sourcePart, name string) sourcePart {
	for _, part := range parts {
		if raw, ok := strings.CutPrefix(part.text, name+":"); ok {
			return trimPart(src, part.End-len(raw), part.End)
		}
	}
	return sourcePart{}
}

var (
	nativeTypstLabel = regexp.MustCompile(`<([A-Za-z0-9_:.-]+)>\s*$`)
	typstCSVBinding  = regexp.MustCompile(`(?m)^\s*#let\s+([\w-]+)\s*=\s*csv\(\s*("(?:[^"\\]|\\.)*")\s*\)`)
	typstCSVSpread   = regexp.MustCompile(`^\.\.([\w-]+)\.flatten\(\)$`)
	latexWidth       = regexp.MustCompile(`^([\d.]+)\s*(\\(?:line|text|column)width)$`)
)

func ManuscriptLabel(src string, latex bool) string {
	if latex {
		if _, label, ok := texArguments(src, "label"); ok && authorLabel.MatchString(label.text) {
			return label.text
		}
		return ""
	}
	if m := nativeTypstLabel.FindStringSubmatch(src); m != nil {
		return m[1]
	}
	return ""
}

func (o *AuthorObject) add(key string, part sourcePart, value string) {
	o.Fields[key], o.Ranges[key] = value, part.SourceRange
}

func newAuthorObject(kind string, src string, latex bool) AuthorObject {
	return AuthorObject{Kind: kind, Label: ManuscriptLabel(src, latex), Fields: make(AuthorValues), Ranges: make(map[string]SourceRange)}
}

// ReadNativeAuthorObject reads common figure/table syntax without evaluating
// user code. A recognized object with no writable fields can still be inspected.
func ReadNativeAuthorObject(src, declarations string, latex bool) (AuthorObject, bool) {
	if len(src) > 64<<10 {
		return AuthorObject{}, false
	}
	if latex {
		return readTeXAuthorObject(src)
	}
	root := trimPart(src, 0, len(src))
	parts, figure := typstCall(src, root, "figure")
	body := root
	if figure {
		if len(parts) == 0 {
			return newAuthorObject("figure", src, false), true
		}
		body = parts[0]
	}
	if image, ok := typstCall(src, body, "image"); ok {
		o := newAuthorObject("figure", src, false)
		if len(image) > 0 {
			if path, err := strconv.Unquote(image[0].text); err == nil {
				o.add("path", image[0], path)
			}
		}
		if width := namedPart(src, image, "width"); strings.HasSuffix(width.text, "%") {
			pct := strings.TrimSpace(strings.TrimSuffix(width.text, "%"))
			if _, err := strconv.ParseFloat(pct, 64); err == nil {
				o.add("width", width, pct)
			}
		}
		addTypstCaption(&o, src, parts)
		return o, true
	}
	if table, ok := typstCall(src, body, "table"); ok {
		o := newAuthorObject("table", src, false)
		addTypstCaption(&o, src, parts)
		o.Fields["data"] = typstTablePreview(src, table)
		for _, arg := range table {
			if args, ok := typstCall(src, arg, "csv"); ok && len(args) > 0 {
				o.DataPath, _ = strconv.Unquote(args[0].text)
			}
			if m := typstCSVSpread.FindStringSubmatch(arg.text); m != nil {
				for _, bind := range typstCSVBinding.FindAllStringSubmatch(declarations, -1) {
					if bind[1] == m[1] {
						o.DataPath, _ = strconv.Unquote(bind[2])
					}
				}
			}
		}
		return o, true
	}
	if figure {
		o := newAuthorObject("figure", src, false)
		addTypstCaption(&o, src, parts)
		return o, true
	}
	return AuthorObject{}, false
}

func addTypstCaption(o *AuthorObject, src string, parts []sourcePart) {
	caption := namedPart(src, parts, "caption")
	if strings.HasPrefix(caption.text, "[") {
		if end, ok := markupGroup(src, caption.Start, false); ok && end == caption.End {
			o.add("caption", sourcePart{SourceRange{caption.Start + 1, end - 1}, src[caption.Start+1 : end-1]}, src[caption.Start+1:end-1])
		}
	}
}

func typstTablePreview(src string, parts []sourcePart) string {
	var cells []string
	add := func(part sourcePart) {
		if strings.HasPrefix(part.text, "[") {
			if end, ok := markupGroup(src, part.Start, false); ok && end == part.End {
				cells = append(cells, strings.TrimSpace(part.text[1:len(part.text)-1]))
			}
		}
	}
	for _, part := range parts {
		if head, ok := typstCall(src, part, "table.header"); ok {
			for _, cell := range head {
				add(cell)
			}
		} else {
			add(part)
		}
	}
	columns, _ := strconv.Atoi(namedPart(src, parts, "columns").text)
	if columns <= 0 || columns > len(cells) {
		columns = max(1, len(cells))
	}
	var rows []string
	for i := 0; i < len(cells); i += columns {
		rows = append(rows, strings.Join(cells[i:min(i+columns, len(cells))], " | "))
	}
	return strings.Join(rows, "\n")
}

// texCommand finds a unique, uncommented control word and returns its argument
// start. Ambiguous repeated commands are not offered as editable fields.
func texCommand(src, name string) (int, bool) {
	found := -1
	for i := 0; i < len(src); i++ {
		if src[i] == '%' {
			n := strings.IndexByte(src[i:], '\n')
			if n < 0 {
				break
			}
			i += n
		} else if src[i] == '\\' && i+1 < len(src) {
			j := i + 1
			for j < len(src) && (src[j] >= 'a' && src[j] <= 'z' || src[j] >= 'A' && src[j] <= 'Z') {
				j++
			}
			if src[i+1:j] == name {
				if found >= 0 {
					return 0, false
				}
				found = j
			}
			i = max(i+1, j-1)
		}
	}
	return found, found >= 0
}

func texArguments(src, name string) (option, body sourcePart, ok bool) {
	at, found := texCommand(src, name)
	if !found {
		return
	}
	at = markupSpace(src, at)
	if at < len(src) && src[at] == '[' {
		z, valid := markupGroup(src, at, true)
		if !valid {
			return
		}
		option = sourcePart{SourceRange{at + 1, z - 1}, src[at+1 : z-1]}
		at = markupSpace(src, z)
	}
	if at < len(src) && src[at] == '{' {
		if end, valid := markupGroup(src, at, true); valid {
			return option, sourcePart{SourceRange{at + 1, end - 1}, src[at+1 : end-1]}, true
		}
	}
	return
}

// ManuscriptListingPath recognizes standalone listings that read a literal file.
func ManuscriptListingPath(src string, latex bool) string {
	src = strings.TrimSpace(src)
	if len(src) > 4096 {
		return ""
	}
	if latex {
		if strings.HasPrefix(src, `\lstinputlisting`) {
			if _, body, ok := texArguments(src, "lstinputlisting"); ok &&
				strings.TrimSpace(src[body.End+1:]) == "" && !strings.ContainsAny(body.text, "\\{}%\r\n") {
				return strings.TrimSpace(body.text)
			}
		}
		return ""
	}
	if !strings.HasPrefix(src, "#raw(") {
		return ""
	}
	end, ok := markupGroup(src, 4, false)
	if !ok || end != len(src) {
		return ""
	}
	args := typstParts(src, 5, end-1)
	if len(args) == 0 || namedPart(src, args, "block").text != "true" {
		return ""
	}
	if read, ok := typstCall(src, args[0], "read"); ok && len(read) > 0 {
		name, _ := strconv.Unquote(read[0].text)
		return name
	}
	return ""
}

func readTeXAuthorObject(src string) (AuthorObject, bool) {
	trim := strings.TrimSpace(src)
	kind := "figure"
	switch {
	case strings.HasPrefix(trim, `\begin{figure}`), strings.HasPrefix(trim, `\begin{figure*}`), strings.HasPrefix(trim, `\includegraphics`):
	case strings.HasPrefix(trim, `\begin{table}`), strings.HasPrefix(trim, `\begin{table*}`), strings.HasPrefix(trim, `\begin{tabular}`):
		kind = "table"
	default:
		return AuthorObject{}, false
	}
	o := newAuthorObject(kind, src, true)
	if _, body, ok := texArguments(src, "caption"); ok {
		o.add("caption", body, body.text)
	}
	if kind == "figure" {
		if option, body, ok := texArguments(src, "includegraphics"); ok {
			if !strings.ContainsAny(body.text, "\\{}%") {
				o.add("path", body, body.text)
			}
			for _, opt := range typstParts(src, option.Start, option.End) {
				if key, val, ok := strings.Cut(opt.text, "="); ok && strings.TrimSpace(key) == "width" {
					part := trimPart(src, opt.End-len(val), opt.End)
					if m := latexWidth.FindStringSubmatch(part.text); m != nil {
						n, _ := strconv.ParseFloat(m[1], 64)
						o.add("width", part, strconv.FormatFloat(n*100, 'f', -1, 64))
						o.widthUnit = m[2]
					}
				}
			}
		}
	} else if _, body, ok := texArguments(src, "input"); ok && !strings.ContainsAny(body.text, "\\{}%") {
		o.DataPath = body.text
	} else {
		o.Fields["data"] = TeXTablePreview(src)
	}
	return o, true
}

func TeXTablePreview(src string) string {
	start := strings.Index(src, `\begin{tabular}`)
	end := strings.LastIndex(src, `\end{tabular}`)
	if start < 0 || end <= start {
		return ""
	}
	table := src[start+len(`\begin{tabular}`) : end]
	if i := strings.IndexByte(table, '\n'); i >= 0 {
		table = table[i+1:]
	}
	var rows []string
	for line := range strings.Lines(table) {
		if strings.Contains(line, "&") {
			line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), `\\`))
			rows = append(rows, strings.ReplaceAll(line, "&", "|"))
		}
	}
	return strings.Join(rows, "\n")
}

// Changes emits only the modified, parsed fields. Captions are native markup,
// with matching delimiters checked before writing back into the original source.
func (o AuthorObject) Changes(values AuthorValues, latex bool) ([]SourceChange, error) {
	var changes []SourceChange
	for key, span := range o.Ranges {
		text := strings.TrimSpace(values[key])
		if text == strings.TrimSpace(o.Fields[key]) {
			continue
		}
		switch key {
		case "caption":
			open, close := "[", "]"
			if latex {
				open, close = "{", "}"
			}
			group := open + text + close
			if end, ok := markupGroup(group, 0, latex); !ok || end != len(group) {
				return nil, errors.New(key)
			}
		case "path":
			if text == "" || len(text) > 1024 || strings.ContainsAny(text, "\r\n\x00") {
				return nil, errors.New(key)
			}
			if latex {
				if strings.ContainsAny(text, "\\{}%") {
					return nil, errors.New(key)
				}
			} else {
				text = strconv.Quote(text)
			}
		case "width":
			n, err := strconv.ParseFloat(text, 64)
			if err != nil || !(n > 0 && n <= 1000) {
				return nil, errors.New(key)
			}
			if latex {
				text = fmt.Sprintf("%g%s", n/100, o.widthUnit)
			} else {
				text = fmt.Sprintf("%g%%", n)
			}
		}
		changes = append(changes, SourceChange{span, text})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Start < changes[j].Start })
	return changes, nil
}

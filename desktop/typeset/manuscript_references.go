package typeset

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type ManuscriptReference struct {
	Label, Kind, Title, Number string
}

type ManuscriptReferences struct {
	ByLabel map[string]ManuscriptReference
}

const maxManuscriptReferences = 2048

var shortReferenceNumber = regexp.MustCompile(`^[\p{L}\p{N} .:()\[\]/+−–-]{1,80}$`)

var (
	texReferenceUse    = regexp.MustCompile(`^\\(ref|eqref)\{([A-Za-z0-9_:.-]+)\}`)
	typstReferenceUse  = regexp.MustCompile(`^@([A-Za-z0-9_:.-]+)`)
	typstReferenceCall = regexp.MustCompile(`^#([A-Za-z_][A-Za-z0-9_-]{0,63})\([\t ]*<([A-Za-z0-9_:.-]{1,96})>[\t ]*(,[\t ]*supplement:[\t ]*none[\t ]*)?\)`)
)

// TypstReferenceCall accepts only a literal label and the simple no-supplement option.
func TypstReferenceCall(text string) (name, label string, end int, noSupplement bool) {
	if m := typstReferenceCall.FindStringSubmatch(text); m != nil {
		return m[1], m[2], len(m[0]), m[3] != ""
	}
	return "", "", 0, false
}

func ManuscriptReferenceUse(text string, latex bool) (label string, end int, parentheses bool) {
	if latex {
		if m := texReferenceUse.FindStringSubmatch(text); m != nil {
			return m[2], len(m[0]), m[1] == "eqref"
		}
		return "", 0, false
	}
	if m := typstReferenceUse.FindStringSubmatch(text); m != nil {
		key := strings.TrimRight(m[1], ".")
		if len(text) > len(key)+1 && text[len(key)+1] == '[' {
			return "", 0, false
		}
		return key, len(key) + 1, false
	}
	return "", 0, false
}

func ManuscriptCounterKind(src string, latex bool) string {
	if strings.HasPrefix(src, "// nomo-style:object-") || strings.HasPrefix(src, "% nomo-style:object-") {
		_, body, _ := strings.Cut(src, "\n")
		src = strings.TrimSpace(body)
	}
	if latex {
		for _, kind := range []string{"equation", "figure", "table"} {
			if strings.HasPrefix(src, "\\begin{"+kind+"}") || kind != "equation" && strings.HasPrefix(src, "\\begin{"+kind+"*}") {
				return kind
			}
		}
		return ""
	}
	switch {
	case strings.HasPrefix(src, "$"), strings.HasPrefix(src, "#math.equation("):
		return "equation"
	case strings.HasPrefix(src, "#figure("):
		return "figure"
	}
	return ""
}

func typstPreviewNumber(src, kind, number string) (string, string) {
	name := kind
	if kind == "equation" {
		name = "math.equation"
	}
	value := "none"
	if number != "" && shortReferenceNumber.MatchString(number) {
		value = "(..args) => " + strconv.Quote(number)
	}
	if parts, ok := typstCall(src, trimPart(src, 0, len(src)), name); ok {
		if explicit := namedPart(src, parts, "numbering"); explicit.text != "" {
			src = src[:explicit.Start] + value + src[explicit.End:]
		}
	}
	return "#show " + name + ": set " + name + "(numbering: " + value + ")\n", src
}

func ReferenceKind(label string) string {
	switch prefix, _, _ := strings.Cut(label, ":"); prefix {
	case "fig", "figure":
		return "figure"
	case "tab", "table":
		return "table"
	case "eq", "equation":
		return "equation"
	case "sec", "ch", "chap", "app":
		return "section"
	}
	for _, pair := range [][2]string{{"eq-", "equation"}, {"fig-", "figure"}, {"tab-", "table"}, {"sec-", "section"}} {
		if strings.HasPrefix(label, pair[0]) {
			return pair[1]
		}
	}
	return ""
}

func LaTeXManuscriptReferences(aux string) *ManuscriptReferences {
	index := &ManuscriptReferences{ByLabel: make(map[string]ManuscriptReference)}
	for line := range strings.Lines(aux) {
		const cmd = `\newlabel`
		if !strings.HasPrefix(line, cmd+"{") || len(index.ByLabel) >= maxManuscriptReferences {
			continue
		}
		end, ok := markupGroup(line, len(cmd), true)
		if !ok {
			continue
		}
		key := line[len(cmd)+1 : end-1]
		if !authorLabel.MatchString(key) {
			continue
		}
		fields, ok := markupGroup(line, end, true)
		if !ok || end+1 >= fields {
			continue
		}
		n, ok := markupGroup(line[:fields], end+1, true)
		if !ok {
			continue
		}
		item := ManuscriptReference{Label: key, Kind: ReferenceKind(key), Number: line[end+2 : n-1]}
		if !shortReferenceNumber.MatchString(item.Number) {
			item.Number = ""
		}
		// hyperref adds the name and anchor after the number and page.
		if page, ok := markupGroup(line[:fields], n, true); ok {
			if title, ok := markupGroup(line[:fields], page, true); ok {
				value := line[page+1 : title-1]
				if len(value) <= 256 && !strings.ContainsAny(value, `\{}`) {
					item.Title = value
				}
			}
		}
		index.ByLabel[key] = item
	}
	return index
}

// TypstPreviewNumbers serializes the project numbers once per compiler revision.
func (refs *ManuscriptReferences) TypstPreviewNumbers() string {
	if refs == nil {
		return ""
	}
	var keys []string
	for key, item := range refs.ByLabel {
		if authorLabel.MatchString(key) && shortReferenceNumber.MatchString(item.Number) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	slices.Sort(keys)
	var out strings.Builder
	for _, key := range keys {
		item := strconv.Quote(key) + ": " + strconv.Quote(refs.ByLabel[key].Number) + ","
		if out.Len()+len(item) > 64<<10 {
			break
		}
		out.WriteString(item)
	}
	return out.String()
}

const typstReferenceQuery = `{
  let labelled = query(selector(figure).or(math.equation).or(heading)).filter(it => it.has("label"));
  labelled.slice(0, calc.min(2048, labelled.len())).map(it => {
    let kind = if it.func() == figure {
      if it.kind == table { "table" } else if it.kind == image { "figure" } else { "" }
    } else if it.func() == heading { "section" } else { "equation" }
    let key = if it.func() == figure { figure.where(kind: it.kind) } else { it.func() }
    let n = if it.numbering == none { "" } else { counter(key).display(it.numbering, at: it.location()) }
    (label: str(it.label), kind: kind, number: if type(n) == str { n } else { "" })
  })
}`

type referenceOutput struct{ bytes.Buffer }

func (o *referenceOutput) Write(p []byte) (int, error) {
	if len(p) > (1<<20)-o.Len() {
		return 0, errors.New("reference metadata exceeds 1 MiB")
	}
	return o.Buffer.Write(p)
}

// QueryTypstReferences lays out the revision for introspection without writing
// or rasterizing a PDF. Project sessions evaluate the mirrored main file.
func (s *Session) QueryTypstReferences(ctx context.Context, source io.Reader) (*ManuscriptReferences, error) {
	dir, entry := filepath.Dir(s.path), "-"
	if s.mirror != nil {
		entry = s.path
	}
	var out referenceOutput
	if _, err := runReaderOutput(ctx, dir, source, nil, &out, "typst", "eval", "--in", entry, "--root", s.projectRoot(dir), typstReferenceQuery); err != nil {
		return nil, err
	}
	var items []ManuscriptReference
	if err := json.Unmarshal(out.Bytes(), &items); err != nil {
		return nil, err
	}
	index := &ManuscriptReferences{ByLabel: make(map[string]ManuscriptReference, len(items))}
	for _, item := range items {
		if !authorLabel.MatchString(item.Label) {
			continue
		}
		if !shortReferenceNumber.MatchString(item.Number) {
			item.Number = ""
		}
		index.ByLabel[item.Label] = item
	}
	return index, nil
}

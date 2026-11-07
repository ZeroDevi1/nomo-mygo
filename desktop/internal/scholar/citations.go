package scholar

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Citation struct {
	Key        string
	Start, End int
}

var (
	texCitation = regexp.MustCompile(`\\(?:cite|parencite|autocite)\*?(?:\s*\[[^\]\n]*\]){0,2}\s*\{([^{}%\n]*)\}`)
	texCiteTail = regexp.MustCompile(`\\(?:cite|parencite|autocite)\*?(?:\s*\[[^\]\n]*\]){0,2}\s*\{[^{}%\n]*$`)
)

func citeRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_:.+/-", r)
}

func citeEnd(source string, start int) int {
	for start < len(source) {
		r, width := utf8.DecodeRuneInString(source[start:])
		if !citeRune(r) {
			break
		}
		start += width
	}
	return start
}

func texLine(source string) string {
	for i := 0; i < len(source); i++ {
		switch source[i] {
		case '\\':
			i++
		case '%':
			return source[:i]
		}
	}
	return source
}

func escaped(source string, at int) bool {
	start := at
	for at > 0 && source[at-1] == '\\' {
		at--
	}
	return (start-at)%2 != 0
}

// typstMarkup scans only text outside math, comments, and raw spans.
func typstMarkup(source string, visit func(int)) bool {
	math, lineComment, blockDepth, raw := false, false, 0, 0
	for at := 0; at < len(source); {
		c := source[at]
		if lineComment {
			lineComment = c != '\n'
			at++
			continue
		}
		if raw == 0 && strings.HasPrefix(source[at:], "/*") {
			blockDepth++
			at += 2
			continue
		}
		if blockDepth > 0 {
			if strings.HasPrefix(source[at:], "*/") {
				blockDepth--
				at += 2
			} else {
				at++
			}
			continue
		}
		if raw == 0 && strings.HasPrefix(source[at:], "//") {
			lineComment = true
			at += 2
			continue
		}
		if raw == 0 && c == '\\' {
			at += 2
			continue
		}
		if c == '`' {
			end := at + 1
			for end < len(source) && source[end] == '`' {
				end++
			}
			if raw == 0 {
				raw = end - at
			} else if raw == end-at {
				raw = 0
			}
			at = end
			continue
		}
		if raw == 0 {
			if c == '$' {
				math = !math
			}
			if c == '<' && !math {
				visit(at)
			}
			if c == '@' && !math {
				prev, _ := utf8.DecodeLastRuneInString(source[:at])
				if !unicode.IsLetter(prev) && !unicode.IsDigit(prev) && prev != '_' {
					visit(at)
				}
			}
		}
		at++
	}
	return !math && raw == 0 && blockDepth == 0 && !lineComment
}

func Citations(source string, typst bool, visit func(Citation)) {
	if typst {
		labels := make(map[string]bool)
		typstMarkup(source, func(at int) {
			if source[at] != '<' || len(labels) >= 8192 {
				return
			}
			end := citeEnd(source, at+1)
			if end > at+1 && end < len(source) && end-at <= 256 && source[end] == '>' {
				labels[source[at+1:end]] = true
			}
		})
		typstMarkup(source, func(at int) {
			if source[at] != '@' {
				return
			}
			start := at + 1
			end := citeEnd(source, start)
			end = start + len(strings.TrimRight(source[start:end], ".:/"))
			if start < end && !labels[source[start:end]] {
				visit(Citation{source[start:end], start, end})
			}
		})
		return
	}
	off := 0
	for line := range strings.SplitSeq(source, "\n") {
		plain := texLine(line)
		for _, m := range texCitation.FindAllStringSubmatchIndex(plain, -1) {
			if escaped(plain, m[0]) {
				continue
			}
			start := m[2]
			for key := range strings.SplitSeq(plain[start:m[3]], ",") {
				trimmed := strings.TrimSpace(key)
				if keyPattern.MatchString(trimmed) {
					a := off + start + strings.Index(key, trimmed)
					visit(Citation{trimmed, a, a + len(trimmed)})
				}
				start += len(key) + 1
			}
		}
		off += len(line) + 1
	}
}

// CitationTail returns the replaceable prefix at the end of a source prefix.
// The caller supplies the full Typst prefix to avoid guessing multiline math state.
func CitationTail(source string, typst bool) (int, bool) {
	if typst {
		marker := strings.LastIndexByte(source, '@')
		if marker < 0 || citeEnd(source, marker+1) != len(source) {
			return 0, false
		}
		active := false
		ok := typstMarkup(source, func(at int) {
			if at == marker {
				active = true
			}
		})
		return marker + 1, active && ok
	}
	base := strings.LastIndexByte(source, '\n') + 1
	line := source[base:]
	if len(texLine(line)) != len(line) {
		return 0, false
	}
	m := texCiteTail.FindStringIndex(line)
	if m == nil || escaped(line, m[0]) {
		return 0, false
	}
	at := strings.LastIndexAny(line, "{,") + 1
	for at < len(line) && (line[at] == ' ' || line[at] == '\t') {
		at++
	}
	return base + at, citeEnd(line, at) == len(line)
}

// CitationKeyEnd returns the end of the identifier beginning at start.
func CitationKeyEnd(source string, start int) int { return citeEnd(source, start) }

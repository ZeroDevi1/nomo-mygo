package scholar

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var keyPattern = regexp.MustCompile(`^[\pL\pN_:.+/-]+$`)

type bibParser struct {
	source string
	at     int
	macros map[string]string
}

func (p *bibParser) space() {
	for p.at < len(p.source) {
		c := p.source[p.at]
		switch c {
		case ' ', '\t', '\r', '\n', '\f':
			p.at++
		case '%':
			n := strings.IndexByte(p.source[p.at:], '\n')
			if n < 0 {
				p.at = len(p.source)
			} else {
				p.at += n + 1
			}
		default:
			return
		}
	}
}

func (p *bibParser) take(c byte) bool {
	p.space()
	if p.at < len(p.source) && p.source[p.at] == c {
		p.at++
		return true
	}
	return false
}

func (p *bibParser) word() string {
	p.space()
	start := p.at
	for p.at < len(p.source) && !strings.ContainsRune(" \t\r\n\f%,=(){}#\"@", rune(p.source[p.at])) {
		p.at++
	}
	return p.source[start:p.at]
}

// group handles nested TeX braces, including inside quoted strings.
func (p *bibParser) group(close byte) (string, error) {
	start := p.at
	depth := 0
	for p.at < len(p.source) {
		c := p.source[p.at]
		p.at++
		if c == '\\' && p.at < len(p.source) {
			p.at++
			continue
		}
		if depth == 0 && c == close {
			return p.source[start : p.at-1], nil
		}
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth < 0 {
				return "", fmt.Errorf("BibTeX 括号不匹配（字节 %d）", p.at)
			}
		}
	}
	return "", fmt.Errorf("BibTeX 值未闭合（字节 %d）", start)
}

func (p *bibParser) value() (string, error) {
	var out strings.Builder
	for {
		var part string
		var err error
		switch {
		case p.take('{'):
			part, err = p.group('}')
		case p.take('"'):
			part, err = p.group('"')
		default:
			part = p.word()
			if part == "" {
				return "", fmt.Errorf("缺少 BibTeX 值（字节 %d）", p.at)
			}
			if value, ok := p.macros[strings.ToLower(part)]; ok {
				part = value
			}
		}
		if err != nil {
			return "", err
		}
		if len(part)+out.Len() > 128<<10 {
			return "", fmt.Errorf("BibTeX 字段超过 128 KiB")
		}
		out.WriteString(part)
		if !p.take('#') {
			return out.String(), nil
		}
	}
}

func parseBib(source string, add func(Entry) error) error {
	return scanBib(source, add, false)
}

func exportedBibEntry(raw []byte) (Entry, error) {
	var item Entry
	seen := false
	err := scanBib(strings.TrimPrefix(string(raw), "\ufeff"), func(e Entry) error {
		if seen || !keyPattern.MatchString(e.Key) || e.Title == "" {
			return fmt.Errorf("需要一条具有有效引用键和标题的 BibTeX 记录")
		}
		item, seen = e, true
		return nil
	}, true)
	if err == nil && !seen {
		err = fmt.Errorf("导出结果没有 BibTeX 记录")
	}
	return item, err
}

func scanBib(source string, add func(Entry) error, standalone bool) error {
	return scanBibWithMacros(source, add, standalone, nil)
}

func scanBibWithMacros(source string, add func(Entry) error, standalone bool, macros map[string]string) error {
	if macros == nil {
		macros = make(map[string]string)
	}
	p := bibParser{source: source, macros: macros}
	for i, month := range strings.Fields("jan feb mar apr may jun jul aug sep oct nov dec") {
		if _, exists := p.macros[month]; !exists {
			p.macros[month] = fmt.Sprintf("%02d", i+1)
		}
	}
	macroBytes := 0
	for p.at < len(source) {
		p.space()
		if p.at >= len(source) {
			break
		}
		if source[p.at] != '@' {
			// BibTeX permits arbitrary text outside entries.
			p.at++
			continue
		}
		recordStart := p.at
		p.at++
		kind := strings.ToLower(p.word())
		close := byte('}')
		if !p.take('{') {
			if !p.take('(') {
				continue
			}
			close = ')'
		}
		switch kind {
		case "comment":
			if _, err := p.group(close); err != nil {
				return err
			}
			continue
		case "preamble":
			if standalone {
				return fmt.Errorf("导出条目包含独立 BibTeX 导言")
			}
			if _, err := p.value(); err != nil {
				return err
			}
			if !p.take(close) {
				return fmt.Errorf("BibTeX 导言未闭合（字节 %d）", p.at)
			}
			continue
		}
		if standalone && kind == "string" {
			return fmt.Errorf("导出条目包含独立 BibTeX 字符串定义")
		}
		var e Entry
		if kind != "string" {
			e.Key = p.word()
			if e.Key == "" || (!p.take(',') && !p.take(close)) {
				return fmt.Errorf("无效 BibTeX 键（字节 %d）", p.at)
			}
			if source[p.at-1] == close {
				if err := add(e); err != nil {
					return err
				}
				continue
			}
		}
		for !p.take(close) {
			field := strings.ToLower(p.word())
			if field == "" || !p.take('=') {
				return fmt.Errorf("无效 BibTeX 字段（字节 %d）", p.at)
			}
			value, err := p.value()
			if err != nil {
				return err
			}
			if kind == "string" {
				macroBytes += len(field) + len(value)
				if macroBytes > 1<<20 {
					return fmt.Errorf("BibTeX 宏超过 1 MiB")
				}
				p.macros[field] = value
			} else {
				switch field {
				case "title":
					e.Title = value
				case "author", "editor":
					if field == "author" || e.Author == "" {
						e.Author = value
					}
				case "year":
					e.Year = value
				case "date":
					if e.Year == "" {
						e.Year = value
					}
				case "journal", "journaltitle", "booktitle":
					e.Venue = value
				case "doi":
					e.DOI = value
				case "abstract":
					e.Abstract = value
				case "url":
					e.URL = value
				case "file":
					e.File = value
				}
			}
			if p.take(close) {
				break
			}
			if !p.take(',') {
				return fmt.Errorf("BibTeX 缺少逗号（字节 %d）", p.at)
			}
		}
		if kind != "string" {
			e.Raw = strings.Clone(source[recordStart:p.at])
			if err := add(e); err != nil {
				return err
			}
		}
	}
	return nil
}

func cleanField(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) > limit {
		for limit > 0 && !utf8.RuneStart(s[limit]) {
			limit--
		}
		s = s[:limit]
	}
	return strings.Clone(s)
}

func NormalizeDOI(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, prefix := range []string{"https://doi.org/", "http://doi.org/", "doi:"} {
		s = strings.TrimPrefix(s, prefix)
	}
	return strings.TrimSpace(s)
}

func generatedKey(e Entry) string {
	var out strings.Builder
	for _, r := range strings.ToLower(e.Author + " " + e.Title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out.WriteRune(r)
			if out.Len() >= 24 {
				break
			}
		}
	}
	if out.Len() == 0 {
		out.WriteString("paper")
	}
	if keyPattern.MatchString(e.Year) && len(e.Year) <= 10 {
		out.WriteString(e.Year)
	}
	return out.String()
}

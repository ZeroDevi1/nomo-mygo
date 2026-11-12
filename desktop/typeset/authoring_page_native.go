package typeset

import (
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type nativePageField struct {
	SourceRange
	unit string
}

type NativePageSettings struct {
	Values AuthorValues
	fields map[string]nativePageField
}

var (
	typstPageRule = regexp.MustCompile(`(?m)^#set\s+(page|text|par)\s*\(`)
	texGeometry   = regexp.MustCompile(`(?m)^\s*\\usepackage\s*\[([^\]\n]+)\]\s*\{geometry\}`)
	nativeLength  = regexp.MustCompile(`^([0-9]*\.?[0-9]+)\s*(mm|cm|pt|in|em)$`)
)

var nativePaper = map[string]string{"a4paper": "a4", "a5paper": "a5", "letterpaper": "us-letter"}

func (p *NativePageSettings) add(key string, part sourcePart, unit, value string) {
	p.Values[key] = value
	p.fields[key] = nativePageField{part.SourceRange, unit}
}

func nativeLengthValue(part sourcePart, unit string) (string, bool) {
	m := nativeLength.FindStringSubmatch(part.text)
	if m == nil {
		return "", false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return "", false
	}
	if unit == "mm" {
		ratio, ok := map[string]float64{"mm": 1, "cm": 10, "pt": 25.4 / 72, "in": 25.4}[m[2]]
		if !ok {
			return "", false
		}
		n *= ratio
	} else if m[2] != unit {
		return "", false
	}
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(n, 'f', 4, 64), "0"), "."), true
}

func (p *NativePageSettings) addLength(key string, part sourcePart, unit string) {
	if value, ok := nativeLengthValue(part, unit); ok {
		p.add(key, part, unit, value)
	}
}

func (p *NativePageSettings) addMargins(src string, part sourcePart) {
	if strings.HasPrefix(part.text, "(") {
		end, ok := markupGroup(src, part.Start, false)
		if !ok || end != part.End {
			return
		}
		parts := typstParts(src, part.Start+1, end-1)
		for _, side := range []string{"top", "bottom", "left", "right"} {
			p.addLength("margin-"+side, namedPart(src, parts, side), "mm")
		}
		return
	}
	p.addLength("margin", part, "mm")
}

func ReadNativePageSettings(src string, latex bool) *NativePageSettings {
	p := &NativePageSettings{Values: make(AuthorValues), fields: make(map[string]nativePageField)}
	if latex {
		if options, _, ok := texArguments(src, "documentclass"); ok {
			for _, opt := range typstParts(src, options.Start, options.End) {
				if value := nativePaper[opt.text]; value != "" {
					p.add("paper", opt, "paper-tex", value)
				}
			}
		}
		matches := texGeometry.FindAllStringSubmatchIndex(src, 2)
		if len(matches) == 1 {
			m := matches[0]
			for _, opt := range typstParts(src, m[2], m[3]) {
				key, val, ok := strings.Cut(opt.text, "=")
				if !ok {
					continue
				}
				key = strings.TrimSpace(key)
				part := trimPart(src, opt.End-len(val), opt.End)
				switch key {
				case "top", "bottom", "left", "right":
					p.addLength("margin-"+key, part, "mm")
				case "margin":
					p.addLength(key, part, "mm")
				}
			}
		}
		if _, body, ok := texArguments(src, "setstretch"); ok {
			if _, err := strconv.ParseFloat(strings.TrimSpace(body.text), 64); err == nil {
				p.add("line", body, "", strings.TrimSpace(body.text))
			}
		}
		if _, body, ok := texArguments(src, "setmainfont"); ok && authorFont.MatchString(strings.TrimSpace(body.text)) {
			p.add("font", body, "font-tex", strings.TrimSpace(body.text))
		}
	} else {
		for _, m := range typstPageRule.FindAllStringSubmatchIndex(src, -1) {
			start := m[1] - 1
			end, ok := markupGroup(src, start, false)
			if !ok {
				continue
			}
			args := typstParts(src, start+1, end-1)
			switch src[m[2]:m[3]] {
			case "page":
				paper := namedPart(src, args, "paper")
				if name, err := strconv.Unquote(paper.text); err == nil && authorChoice(name, "a4", "a5", "us-letter") {
					p.add("paper", paper, "paper", name)
				}
				p.addMargins(src, namedPart(src, args, "margin"))
			case "text":
				p.addLength("size", namedPart(src, args, "size"), "pt")
				font := namedPart(src, args, "font")
				if z, ok := markupGroup(src, font.Start, false); strings.HasPrefix(font.text, "(") && ok && z == font.End {
					if fonts := typstParts(src, font.Start+1, z-1); len(fonts) > 0 {
						font = fonts[0]
					}
				}
				if name, err := strconv.Unquote(font.text); err == nil && authorFont.MatchString(name) {
					p.add("font", font, "font-typst", name)
				}
			case "par":
				if j := namedPart(src, args, "justify"); j.text == "true" || j.text == "false" {
					p.add("justify", j, "bool", j.text)
				}
				lead, gap := namedPart(src, args, "leading"), namedPart(src, args, "spacing")
				p.addLength("leading-em", lead, "em")
				p.addLength("leading-pt", lead, "pt")
				p.addLength("spacing", gap, "pt")
				p.addLength("spacing-em", gap, "em")
				indent := namedPart(src, args, "first-line-indent")
				if strings.HasPrefix(indent.text, "(") {
					if z, ok := markupGroup(src, indent.Start, false); ok && z == indent.End {
						indent = namedPart(src, typstParts(src, indent.Start+1, z-1), "amount")
					}
				}
				p.addLength("indent", indent, "pt")
				p.addLength("indent-em", indent, "em")
			}
		}
	}
	if len(p.fields) == 0 {
		return nil
	}
	return p
}

func (p *NativePageSettings) Changes(values AuthorValues) ([]SourceChange, error) {
	var changes []SourceChange
	for key, field := range p.fields {
		value := strings.TrimSpace(values[key])
		if value == p.Values[key] {
			continue
		}
		switch field.unit {
		case "font-tex", "font-typst":
			if !authorFont.MatchString(value) {
				return nil, errors.New(key)
			}
			if field.unit == "font-typst" {
				value = strconv.Quote(value)
			}
		case "paper":
			if value == "" || !authorChoice(value, "a4", "a5", "us-letter") {
				return nil, errors.New(key)
			}
			value = strconv.Quote(value)
		case "paper-tex":
			switch value {
			case "a4", "a5":
				value += "paper"
			case "us-letter":
				value = "letterpaper"
			default:
				return nil, errors.New(key)
			}
		case "bool":
			if value != "true" && value != "false" {
				return nil, errors.New(key)
			}
		default:
			min, max := 0.0, 144.0
			if key == "size" {
				min, max = 5, 96
			}
			if key == "line" {
				min, max = 0.6, 4
			}
			n, err := values.number(key, min, max)
			if err != nil || n == "" {
				return nil, errors.New(key)
			}
			value = n + field.unit
		}
		changes = append(changes, SourceChange{field.SourceRange, value})
	}
	slices.SortFunc(changes, func(a, b SourceChange) int { return a.Start - b.Start })
	return changes, nil
}

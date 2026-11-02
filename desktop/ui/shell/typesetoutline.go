package shell

import (
	"regexp"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui/editor"
	"nomo/desktop/ui/mdedit"
)

var sectionLine = regexp.MustCompile(`^\\(part|chapter|section|subsection|subsubsection|paragraph|subparagraph)\*?\s*(?:\[[^]]*\]\s*)?\{`)
var sectionLevel = map[string]int{"part": 1, "chapter": 1, "section": 2, "subsection": 3, "subsubsection": 4, "paragraph": 5, "subparagraph": 6}

func sourceHeading(line, kind string) (level int, title string) {
	line = strings.TrimSpace(line)
	if kind == "typst" {
		title = strings.TrimLeft(line, "=")
		level = len(line) - len(title)
		if level == 0 || !strings.HasPrefix(title, " ") {
			return 0, ""
		}
		title = strings.TrimSpace(title)
		if i := strings.LastIndex(title, " <"); i >= 0 && strings.HasSuffix(title, ">") {
			title = title[:i]
		}
		return level, title
	}
	m := sectionLine.FindStringSubmatch(line)
	if m == nil {
		return 0, ""
	}
	rest := line[len(m[0]):]
	depth := 1
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case '\\':
			i++
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return sectionLevel[m[1]], rest[:i]
			}
		}
	}
	return 0, ""
}

// 一般输入只重新识别变更行。保留一个共享 rope 快照来核对未变的后缀。
func (t *Tab) typesetHeadings(s editor.Snapshot) []mdedit.Heading {
	if t.outline != nil && t.outVer == s.Version() {
		return t.outline
	}
	start, end := 0, s.Len()
	old := t.outSnap
	oldEnd := old.Len()
	if t.outline != nil {
		off := changedOffset(old, s)
		start = s.LineStart(s.LineOf(off))
		delta := s.Len() - old.Len()
		next := s.LineOf(max(off, off+delta)) + 1
		if next < s.LineCount() {
			newAt := s.LineStart(next)
			oldAt := newAt - delta
			if oldAt >= start && oldAt <= old.Len() && old.LineStart(old.LineOf(oldAt)) == oldAt && sameSuffix(old, oldAt, s, newAt) {
				end, oldEnd = newAt, oldAt
			}
		}
	}
	heads := t.outline
	before := sort.Search(len(heads), func(i int) bool { return heads[i].Pos >= start })
	after := sort.Search(len(heads), func(i int) bool { return heads[i].Pos >= oldEnd })
	var changed []mdedit.Heading
	kind := t.Kind()
	for line := s.LineOf(start); line < s.LineCount() && s.LineStart(line) < end; line++ {
		if level, title := sourceHeading(s.Line(line), kind); title != "" {
			changed = append(changed, mdedit.Heading{Level: level, Text: title, Pos: s.LineStart(line)})
		}
	}
	for i := after; i < len(heads); i++ {
		heads[i].Pos += end - oldEnd
	}
	if before == after && len(changed) == 0 && heads != nil {
		t.outline = heads
	} else {
		merged := make([]mdedit.Heading, 0, before+len(changed)+len(heads)-after)
		merged = append(merged, heads[:before]...)
		merged = append(merged, changed...)
		t.outline = append(merged, heads[after:]...)
	}
	t.outSnap, t.outVer = s, s.Version()
	return t.outline
}

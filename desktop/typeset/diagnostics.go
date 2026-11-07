package typeset

import (
	"regexp"
	"strconv"
	"strings"
)

type Diagnostic struct {
	Text string
	File string
	Line int // 1-based in File. Zero means no reliable source location.
}

var sourceLine = regexp.MustCompile(`^l\.([1-9][0-9]*)\b|(?:^|\s)(?:-|<stdin>|preview\.tex):([1-9][0-9]*):`)

func DiagnosticLine(log string) int {
	for line := range strings.SplitSeq(log, "\n") {
		if m := sourceLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			n, _ := strconv.Atoi(m[1] + m[2])
			if n > 0 {
				return n
			}
		}
	}
	return 0
}

func (s *Session) collectWarnings(log string, tex bool) {
	lines := strings.Split(log, "\n")
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		isBox := strings.HasPrefix(line, "Overfull ") || strings.HasPrefix(line, "Underfull ")
		if !isBox && !strings.HasPrefix(line, "LaTeX Warning:") &&
			!strings.HasPrefix(line, "Package ") && !strings.HasPrefix(line, "warning:") &&
			!strings.Contains(line, " - WARN - ") {
			continue
		}
		if strings.HasPrefix(line, "Package ") && !strings.Contains(line, " Warning:") {
			continue
		}
		if len(s.warnings) >= 16 {
			return
		}
		d := Diagnostic{Text: line}
		if !tex && i+1 < len(lines) {
			location := DiagnosticLocation(strings.Join(lines[i+1:min(len(lines), i+5)], "\n"))
			d.File, d.Line = location.File, location.Line
		}
		if len(d.Text) > 480 {
			d.Text = d.Text[:480]
		}
		if len(s.warnings) > 0 && s.warnings[len(s.warnings)-1] == d {
			continue
		}
		s.warnings = append(s.warnings, d)
	}
}

func (s *Session) Warnings() []Diagnostic { return s.warnings }

var diagnosticFileLine = regexp.MustCompile(`^((?:[A-Za-z]:)?[^:\n]+):([1-9][0-9]*):`)

// DiagnosticLocation requires an explicit source filename. Bare l.N TeX log
// lines are deliberately left without a jump target when includes are present.
func DiagnosticLocation(log string) Diagnostic {
	for raw := range strings.SplitSeq(log, "\n") {
		line := strings.TrimLeft(raw, " \t│┌─╭")
		if m := diagnosticFileLine.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[2])
			return Diagnostic{File: strings.TrimSpace(m[1]), Line: n}
		}
	}
	return Diagnostic{}
}

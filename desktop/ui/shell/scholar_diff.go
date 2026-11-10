package shell

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

type scholarToken struct{ start, end int }

const scholarDiffLineHeight = 1.5 // MyGo measures line height as a font-size multiplier.

type scholarDiff struct {
	*editor.DiffState
	long                 bool
	oldView, newView     *editor.State
	oldRanges, newRanges []scholarToken
}

func scholarDiffView(old, next string) *scholarDiff {
	state := &editor.DiffState{Old: editor.NewBuffer(old), New: editor.NewBuffer(next), Layout: editor.DiffUnified, Inline: editor.DiffInlineWord, Wrap: true, LineHeight: scholarDiffLineHeight}
	d := &scholarDiff{DiffState: state}
	for _, text := range []string{old, next} {
		for line := range strings.SplitSeq(text, "\n") {
			if len(line) > 1000 {
				d.long = true
				break
			}
		}
	}
	if !d.long {
		return d
	}
	d.oldView = &editor.State{Buffer: state.Old, ReadOnly: true, SoftWrap: true, LineHeight: scholarDiffLineHeight}
	d.newView = &editor.State{Buffer: state.New, ReadOnly: true, SoftWrap: true, LineHeight: scholarDiffLineHeight}
	a, b := scholarTokens(old), scholarTokens(next)
	tokenLines := func(text string, tokens []scholarToken) string {
		var out strings.Builder
		for i, t := range tokens {
			if i > 0 {
				out.WriteByte('\n')
			}
			out.WriteString(strconv.Quote(text[t.start:t.end]))
		}
		return out.String()
	}
	tokens := &editor.DiffState{Old: editor.NewBuffer(tokenLines(old, a)), New: editor.NewBuffer(tokenLines(next, b))}
	span := func(hStart, hEnd int, ts []scholarToken, n int) scholarToken {
		begin, end := n, n
		if hStart < len(ts) {
			begin = ts[hStart].start
		}
		if hEnd < len(ts) {
			end = ts[hEnd].start
		}
		return scholarToken{begin, end}
	}
	for _, h := range tokens.Hunks() {
		if h.OldStart < h.OldEnd {
			d.oldRanges = append(d.oldRanges, span(h.OldStart, h.OldEnd, a, len(old)))
		}
		if h.NewStart < h.NewEnd {
			d.newRanges = append(d.newRanges, span(h.NewStart, h.NewEnd, b, len(next)))
		}
	}
	return d
}

// TeX control sequences, words, whitespace and individual math/punctuation
// symbols retain their original byte spans. A capped tail stays one token,
// bounding the auxiliary token model without altering displayed source text.
func scholarTokens(text string) []scholarToken {
	var tokens []scholarToken
	for at := 0; at < len(text); {
		start := at
		if len(tokens) >= 8191 {
			tokens = append(tokens, scholarToken{at, len(text)})
			break
		}
		r, n := utf8.DecodeRuneInString(text[at:])
		at += n
		switch {
		case r == '\\' && at < len(text):
			r, n = utf8.DecodeRuneInString(text[at:])
			at += n
			if unicode.IsLetter(r) {
				for at < len(text) {
					r, n = utf8.DecodeRuneInString(text[at:])
					if !unicode.IsLetter(r) {
						break
					}
					at += n
				}
			}
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			for at < len(text) {
				r, n = utf8.DecodeRuneInString(text[at:])
				if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
					break
				}
				at += n
			}
		case r != '\n' && unicode.IsSpace(r):
			for at < len(text) {
				r, n = utf8.DecodeRuneInString(text[at:])
				if r == '\n' || !unicode.IsSpace(r) {
					break
				}
				at += n
			}
		}
		tokens = append(tokens, scholarToken{start, at})
	}
	return tokens
}

func (d *scholarDiff) GoToHunk(index int) {
	d.DiffState.GoToHunk(index)
	if !d.long {
		return
	}
	hunks := d.Hunks()
	if index < 0 || index >= len(hunks) {
		return
	}
	d.oldView.ScrollTo(hunks[index].OldStart)
	d.newView.ScrollTo(hunks[index].NewStart)
}

func scholarDiffElement(c *ui.Context, d *scholarDiff) ui.Element {
	if !d.long {
		return editor.DiffView(c, d.DiffState)
	}
	// MyGo's built-in inline model skips lines over 1000 bytes. Use its public
	// text-background API for long academic paragraphs, preserving actual lines.
	container := ui.Column(c).MinHeight(0).Gap(3)
	container.Children(func() {
		for i, part := range []struct {
			view            *editor.State
			ranges          []scholarToken
			symbol, channel string
		}{{d.oldView, d.oldRanges, "−", "deletion"}, {d.newView, d.newRanges, "+", "addition"}} {
			ck := c.Key(i)
			ink := scholarInk(c, part.channel)
			part.view.TextBackgrounds = part.view.TextBackgrounds[:0]
			part.view.LineBackgrounds = part.view.LineBackgrounds[:0]
			if d.Inline != editor.DiffInlineNone {
				for _, r := range part.ranges {
					part.view.TextBackgrounds = append(part.view.TextBackgrounds, editor.TextBackground{Start: r.start, End: r.end, Color: ui.RGBA(ink.R, ink.G, ink.B, .20)})
				}
			} else {
				for _, h := range d.Hunks() {
					start, end := h.OldStart, h.OldEnd
					if i == 1 {
						start, end = h.NewStart, h.NewEnd
					}
					if start < end {
						part.view.LineBackgrounds = append(part.view.LineBackgrounds, editor.LineBackground{First: start, Last: end - 1, Color: ui.RGBA(ink.R, ink.G, ink.B, .16)})
					}
				}
			}
			ui.Row(ck).Grow(1).FillWidth().MinHeight(0).Gap(4).Children(func() {
				ui.Text(ck, part.symbol).TextColor(ink).Width(16)
				editor.View(ck, part.view).Grow(1).FillHeight().MinWidth(0)
			})
		}
	})
	return container
}

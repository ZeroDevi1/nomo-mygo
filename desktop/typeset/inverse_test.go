package typeset

import (
	"context"
	"testing"
)

func TestTextSourceLine(t *testing.T) {
	words := []pdfWord{{Text: "first"}, {Text: "正文"}, {Text: "last"}}
	for _, tc := range []struct {
		source string
		want   int
	}{
		{"正文 only\nfirst *正文* last", 1},
		{"正文 only\nfirst 正文 last", 1},
		{"first 正文 last\nfirst 正文 last", -1},
		{"生成的文本", -1},
	} {
		if got := matchSourceLine(context.Background(), tc.source, words, 1); got != tc.want {
			t.Errorf("%q: line %d, want %d", tc.source, got, tc.want)
		}
	}
}

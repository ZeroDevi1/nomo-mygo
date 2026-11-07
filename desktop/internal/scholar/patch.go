package scholar

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// PatchEdit identifies an exact, unique span in an explicitly supplied file.
// Empty Before is allowed only for an explicitly empty selection.
type PatchEdit struct {
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type PatchProposal struct {
	Summary string      `json:"summary"`
	Edits   []PatchEdit `json:"edits"`
}

const PatchSchema = `{"type":"object","properties":{"summary":{"type":"string"},"edits":{"type":"array","items":{"type":"object","properties":{"path":{"type":"string"},"before":{"type":"string"},"after":{"type":"string"}},"required":["path","before","after"],"additionalProperties":false}}},"required":["summary","edits"],"additionalProperties":false}`

func DecodeProposal(raw string) (PatchProposal, error) {
	var result PatchProposal
	if len(raw) > CodexOutputLimit || !utf8.ValidString(raw) {
		return result, fmt.Errorf("proposal exceeds output budget")
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return result, err
	}
	if len(result.Edits) == 0 || len(result.Edits) > 64 {
		return result, fmt.Errorf("proposal requires 1–64 hunks")
	}
	for _, e := range result.Edits {
		if !filepath.IsLocal(e.Path) || e.Path == "." || strings.ContainsAny(e.Before+e.After, "\x00") || e.Before == e.After {
			return result, fmt.Errorf("invalid patch target %q", e.Path)
		}
	}
	return result, nil
}

type LocatedEdit struct {
	Start, End int
	After      string
}

// LocateEdits resolves all hunks against one immutable baseline before mutation.
func LocateEdits(source string, edits []PatchEdit) ([]LocatedEdit, error) {
	out := make([]LocatedEdit, 0, len(edits))
	for _, e := range edits {
		if e.Before == "" {
			if source != "" {
				return nil, fmt.Errorf("empty patch baseline in a nonempty file")
			}
			out = append(out, LocatedEdit{0, 0, e.After})
			continue
		}
		at := strings.Index(source, e.Before)
		if at < 0 || strings.Index(source[at+1:], e.Before) >= 0 {
			return nil, fmt.Errorf("patch baseline is missing or ambiguous")
		}
		out = append(out, LocatedEdit{at, at + len(e.Before), e.After})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	for i := 1; i < len(out); i++ {
		if out[i].Start < out[i-1].End || out[i].Start == out[i-1].Start {
			return nil, fmt.Errorf("patch hunks overlap")
		}
	}
	return out, nil
}

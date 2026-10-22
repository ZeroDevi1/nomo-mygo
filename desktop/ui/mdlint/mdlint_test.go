package mdlint

import (
	"fmt"
	"os"
	"slices"
	"testing"
)

func rules(issues []Issue) []string {
	var out []string
	for _, is := range issues {
		out = append(out, fmt.Sprintf("%s:%d", is.Rule, is.Line))
	}
	return out
}

func TestRules(t *testing.T) {
	cases := []struct {
		name, md, set string
		want          []string
	}{
		{"clean", "# 标题\n\n正文。\n\n- a\n- b\n\n```go\nx := 1\n```\n", "default", nil},
		{"heading increment and h1", "# A\n\n### B\n\n# C\n", "default", []string{"MD001:3", "MD025:5"}},
		{"spacing", "#A\n\n##  B\ntext  \nend \n\n\n", "default", []string{"MD018:1", "MD041:1", "MD019:3", "MD022:3", "MD009:5", "MD012:7"}},
		{"relaxed drops whitespace", "#A\n\n##  B\ntext  \nend \n\n\n", "relaxed", []string{"MD018:1", "MD019:3", "MD022:3"}},
		{"fences and lists", "# T\ntext\n```\ncode\n```\nafter\n- a\n* b\n", "default", []string{"MD022:1", "MD031:3", "MD040:3", "MD031:5", "MD032:7", "MD004:8"}},
		{"front matter and setext", "---\ntitle: x\n---\nTitle\n=====\n\nSee https://a.com and [x]() ![](a.png)", "default",
			[]string{"MD025:4", "MD034:7", "MD042:7", "MD045:7", "MD047:7"}},
		{"duplicate and punctuation", "# A\n\n## B。\n\n## B。\n", "default", []string{"MD026:3", "MD024:5", "MD026:5"}},
	}
	for _, c := range cases {
		got, _ := Lint(c.md, c.set)
		if r := rules(got); !slices.Equal(r, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, r, c.want)
		}
	}
}

func TestLimit(t *testing.T) {
	md := "# A\n\n"
	for range 300 {
		md += "x \n"
	}
	got, total := Lint(md, "default")
	if len(got) != Limit || total != 300 {
		t.Fatalf("len=%d total=%d", len(got), total)
	}
}

// TestDump 供与 markdownlint 对照：MDLINT_DUMP=<文件> MDLINT_SET=relaxed|default。
func TestDump(t *testing.T) {
	path := os.Getenv("MDLINT_DUMP")
	if path == "" {
		t.Skip()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := lint(string(data), os.Getenv("MDLINT_SET"))
	for _, is := range got {
		fmt.Println(is.Rule, is.Line)
	}
}

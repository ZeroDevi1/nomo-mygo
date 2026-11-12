package typeset

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func applySourceChanges(src string, changes []SourceChange) string {
	for i := len(changes) - 1; i >= 0; i-- {
		c := changes[i]
		src = src[:c.Start] + c.Text + src[c.End:]
	}
	return src
}

func TestNativeManuscriptSurgicalChanges(t *testing.T) {
	for _, tc := range []struct {
		name, src, old, new, path string
		latex                     bool
	}{
		{"typst", `#figure(image("figures/result.svg", width: 62%, alt: "Test (a)"), caption: [Nested #emph[title]]) <fig-a>`, "62%", "48%", "figures/result.svg", false},
		{"latex", `\begin{figure}[!ht]
\includegraphics[width=.62\linewidth,trim={0 0 1cm 0},clip]{figures/result.pdf}
\caption[Short]{Nested \textbf{title}}
% \label{ignored}
\label{fig-a}
\end{figure}`, `.62\linewidth`, `0.48\linewidth`, "figures/result.pdf", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj, ok := ReadNativeAuthorObject(tc.src, "", tc.latex)
			if !ok || obj.Label != "fig-a" || obj.Fields["path"] != tc.path || obj.Fields["caption"] == "" || obj.Fields["width"] != "62" {
				t.Fatalf("unexpected figure: %+v", obj)
			}
			values := AuthorValues{"path": obj.Fields["path"], "caption": obj.Fields["caption"], "width": "48"}
			changes, err := obj.Changes(values, tc.latex)
			if want := strings.Replace(tc.src, tc.old, tc.new, 1); err != nil || applySourceChanges(tc.src, changes) != want {
				t.Fatalf("unexpected figure changes: %+v (%v)", changes, err)
			}
		})
	}

	src := `#figure(table(columns: 2, table.header([Rank], [Error]), ..err-rows.flatten()), caption: [Results]) <tab-error>`
	obj, ok := ReadNativeAuthorObject(src, `#let err-rows = csv("figures/data.csv")`, false)
	if !ok || obj.Kind != "table" || obj.DataPath != "figures/data.csv" || obj.Fields["data"] != "Rank | Error" {
		t.Fatalf("unexpected CSV table: %+v", obj)
	}
}

func TestNativeManuscriptPageChanges(t *testing.T) {
	src := `#set page(paper: "a4", margin: (top: 2.5cm, left: 3cm), numbering: "1")
#set text(font: ("Times New Roman", "Songti SC"), size: 12pt)
#set par(justify: true, leading: 0.78em, first-line-indent: (amount: 2em, all: true))
#show heading: set text(size: 24pt)
`
	page := ReadNativePageSettings(src, false)
	if page == nil || page.Values["margin-left"] != "30" || page.Values["leading-em"] != "0.78" || page.Values["indent-em"] != "2" || page.Values["size"] != "12" {
		t.Fatalf("unexpected page fields: %+v", page)
	}
	values := make(AuthorValues)
	for key, value := range page.Values {
		values[key] = value
	}
	values["margin-left"], values["font"] = "31", "Arial"
	changes, err := page.Changes(values)
	want := strings.NewReplacer("left: 3cm", "left: 31mm", `"Times New Roman"`, `"Arial"`).Replace(src)
	if err != nil || applySourceChanges(src, changes) != want {
		t.Fatalf("unexpected template changes: %+v (%v)", changes, err)
	}
}

func TestManuscriptReferencesUseDocumentCounters(t *testing.T) {
	aux := LaTeXManuscriptReferences(`\newlabel{fig:result}{{4-2}{8}{A {nested} title}{figure.caption.6}{}}`)
	if got := aux.ByLabel["fig:result"].Number; got != "4-2" {
		t.Fatalf("LaTeX figure number %q", got)
	}
	src := `#math.equation(numbering: "(1)", block: true, $x = 1$) <eq-example>`
	_, waiting := typstPreviewNumber(src, "equation", "")
	_, numbered := typstPreviewNumber(src, "equation", "(2-3)")
	if strings.Contains(waiting, `"(1)"`) || !strings.Contains(numbered, `numbering: (..args) => "(2-3)"`) {
		t.Fatalf("incorrect fragment numbering: %s; %s", waiting, numbered)
	}

	exe, err := tool("typst")
	if err != nil || exec.Command(exe, "eval", "--help").Run() != nil {
		t.Skip("Typst eval unavailable")
	}
	s, err := Open(filepath.Join(t.TempDir(), "report.typ"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	index, err := s.QueryTypstReferences(t.Context(), strings.NewReader(`
#set heading(numbering: "1")
#set figure(numbering: (..n) => numbering("1-1", counter(heading).get().first(), ..n))
= First
= Second
#figure(table(columns: 1, [a]), caption: [Example]) <tab-snapshot>
`))
	if err != nil {
		t.Fatal(err)
	}
	if item := index.ByLabel["tab-snapshot"]; item.Number != "2-1" || item.Kind != "table" {
		t.Fatalf("unexpected snapshot index: %+v", item)
	}
	if _, err := os.Stat(s.pdf); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reference query should not write a PDF: %v", err)
	}
}

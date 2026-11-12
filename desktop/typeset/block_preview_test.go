package typeset

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManuscriptTitleBibliographyAndFragmentReferences(t *testing.T) {
	t.Run("LaTeX title and bibliography", func(t *testing.T) {
		for _, name := range []string{"xelatex", "pdftoppm"} {
			if _, err := tool(name); err != nil {
				t.Skip(name)
			}
		}
		const head = "\\documentclass{article}\n\\title{Research title}\\author{Author}\n"
		path := filepath.Join(t.TempDir(), "article.tex")
		for i, result := range RenderBlockPreviews(t.Context(), []BlockPreview{
			{Path: path, Preamble: head, Source: `\maketitle`},
			{Path: path, Preamble: head, Source: "\\begin{thebibliography}{1}\n\\bibitem{paper} Example paper.\n\\end{thebibliography}"},
		}) {
			if result.Err != nil || result.Image == nil {
				t.Fatalf("fragment %d: %v", i, result.Err)
			}
		}
	})

	t.Run("Typst bibliography and project label", func(t *testing.T) {
		if _, err := tool("typst"); err != nil {
			t.Skip("typst")
		}
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "refs.bib"),
			[]byte("@book{paper, title={A Paper}, author={Example, Anne}, year={2025}}"), 0600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "part.typ")
		const bibliography = `#bibliography("refs.bib", title: none)`
		for i, result := range RenderBlockPreviews(t.Context(), []BlockPreview{
			{Path: path, Root: root, Source: bibliography},
		}) {
			if result.Err != nil || result.Image == nil {
				t.Fatalf("fragment %d: %v", i, result.Err)
			}
		}
		refs := ManuscriptReferences{ByLabel: map[string]ManuscriptReference{"eq:other": {Number: "(4-2)"}}}
		result := RenderBlockPreviews(t.Context(), []BlockPreview{{
			Path: path, Root: root, Bibliography: bibliography, References: refs.TypstPreviewNumbers(),
			Source: `#block[Result #ref(<eq:other>, supplement: none) in @paper.]`,
		}})[0]
		if result.Err != nil || result.Image == nil {
			t.Fatalf("reference fragment: %v", result.Err)
		}
	})
}

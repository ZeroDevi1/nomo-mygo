package typeset

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotPDF(t *testing.T) {
	for _, tc := range []struct{ ext, compiler, good, bad string }{
		{"tex", "xelatex", `\documentclass{article}\begin{document}Unsaved snapshot\newpage Second page\end{document}`, `\invalidcommand`},
		{"typ", "typst", "Unsaved snapshot\n#pagebreak()\nSecond page", "#invalid("},
	} {
		t.Run(tc.ext, func(t *testing.T) {
			for _, exe := range []string{tc.compiler, "pdfinfo", "pdftoppm", "pdftotext"} {
				if _, e := tool(exe); e != nil {
					t.Skip(exe)
				}
			}
			ctx := context.Background()
			dir := t.TempDir()
			src := filepath.Join(dir, "my paper."+tc.ext)
			target := filepath.Join(dir, "output.pdf")
			if e := os.WriteFile(src, []byte("Old disk content"), 0600); e != nil {
				t.Fatal(e)
			}
			s, e := Open(src)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			if n, e := s.Compile(ctx, strings.NewReader(tc.good)); e != nil || n != 2 {
				t.Fatalf("pages=%d err=%v", n, e)
			}
			if _, e := s.Compile(ctx, strings.NewReader(tc.bad)); e == nil {
				t.Fatal("expected invalid source error")
			}
			if img, e := s.Page(ctx, 2); e != nil || img.Bounds().Empty() {
				t.Fatalf("lost last successful page: %v", e)
			}
			if e := ExportPDF(ctx, src, strings.NewReader(tc.good), target); e != nil {
				t.Fatal(e)
			}
			exe, _ := tool("pdftotext")
			txt, e := exec.Command(exe, target, "-").Output()
			if e != nil || !strings.Contains(string(txt), "Unsaved snapshot") || !strings.Contains(string(txt), "Second page") {
				t.Fatalf("wrong export: %q %v", txt, e)
			}
			before, e := os.ReadFile(target)
			if e != nil {
				t.Fatal(e)
			}
			if e := ExportPDF(ctx, src, strings.NewReader(tc.bad), target); e == nil {
				t.Fatal("invalid export succeeded")
			}
			after, e := os.ReadFile(target)
			if e != nil || !bytes.Equal(before, after) {
				t.Fatal("failed export replaced existing PDF")
			}
			b, e := os.ReadFile(src)
			if e != nil || string(b) != "Old disk content" {
				t.Fatal("source file changed")
			}
		})
	}
}

func TestBibliographyBackends(t *testing.T) {
	for _, tc := range []struct{ tool, source string }{
		{"bibtex", `\documentclass{article}\begin{document}Citation \cite{sample}.\bibliographystyle{plain}\bibliography{refs}\end{document}`},
		{"biber", `\documentclass{article}\usepackage[backend=biber]{biblatex}\addbibresource{refs.bib}\begin{document}Citation \cite{sample}.\printbibliography\end{document}`},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			for _, exe := range []string{tc.tool, "xelatex", "pdftotext", "kpsewhich"} {
				if _, e := tool(exe); e != nil {
					t.Skip(exe)
				}
			}
			if tc.tool == "biber" {
				kp, _ := tool("kpsewhich")
				if e := exec.Command(kp, "biblatex.sty").Run(); e != nil {
					t.Skip("biblatex unavailable")
				}
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "sample.tex")
			out := filepath.Join(dir, "sample.pdf")
			if e := os.WriteFile(filepath.Join(dir, "refs.bib"), []byte("@book{sample, author={Author, Jane},title={Verification Handbook},publisher={Press},year={2020}}"), 0600); e != nil {
				t.Fatal(e)
			}
			if e := ExportPDF(context.Background(), path, strings.NewReader(tc.source), out); e != nil {
				t.Fatal(e)
			}
			exe, _ := tool("pdftotext")
			txt, e := exec.Command(exe, out, "-").Output()
			if e != nil || !strings.Contains(string(txt), "Verification Handbook") {
				t.Fatalf("missing bibliography: %q %v", txt, e)
			}
		})
	}
}

func TestParagraphMathAndFallback(t *testing.T) {
	if _, e := tool("typst"); e != nil {
		t.Skip(e)
	}
	s, e := Open(filepath.Join(t.TempDir(), "main.tex"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	for _, src := range []string{`矩阵正文：\textbf{粗体}，且 $x_{i}^2+\frac{\alpha}{2}=y$。`, `下一次修改：$\sqrt{x+y}$。`} {
		img := s.Paragraph(context.Background(), "", src)
		if img == nil {
			t.Fatalf("failed to render %q", src)
		}
	}
	for _, src := range []string{`$\custom{x}$`, `\cite{unresolved}`, `\input{chapter}`, `$#read("file")$`} {
		if img := s.Paragraph(context.Background(), "", src); img != nil {
			t.Fatalf("unexpected speculative output for %q", src)
		}
	}
}

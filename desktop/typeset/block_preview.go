package typeset

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// BlockPreview describes a block and its in-scope declarations.
type BlockPreview struct {
	Path, Root, Main               string
	Source, Preamble, Bibliography string
	References                     string
	Counter, Number                string
	Inline, Dark, Transparent      bool
}

type BlockPreviewResult struct {
	Image image.Image
	Err   error
}

const MaxPreviewBatch = 6

// RenderBlockPreview renders one source block at approximately 2 pixels per point.
func RenderBlockPreview(ctx context.Context, block BlockPreview) (image.Image, error) {
	result := RenderBlockPreviews(ctx, []BlockPreview{block})[0]
	return result.Image, result.Err
}

// RenderBlockPreviews shares one compiler invocation among compatible fragments.
// It splits a rejected batch to isolate invalid source and bounds the total work.
func RenderBlockPreviews(parent context.Context, blocks []BlockPreview) []BlockPreviewResult {
	out := make([]BlockPreviewResult, len(blocks))
	if len(blocks) == 0 {
		return out
	}
	if IsLaTeXPath(blocks[0].Path) {
		if err := PrepareLaTeX(parent); err != nil {
			for i := range out {
				out[i].Err = err
			}
			return out
		}
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	var render func(int, int)
	render = func(a, z int) {
		result, err := compileBlocks(ctx, blocks[a:z])
		if err == nil {
			copy(out[a:z], result)
			return
		}
		if z-a > 1 && ctx.Err() == nil && !errors.Is(err, context.DeadlineExceeded) {
			mid := a + (z-a)/2
			render(a, mid)
			render(mid, z)
			return
		}
		for i := a; i < z; i++ {
			out[i].Err = err
		}
	}
	for a := 0; a < len(blocks); a += MaxPreviewBatch {
		render(a, min(a+MaxPreviewBatch, len(blocks)))
	}
	return out
}

func compileBlocks(parent context.Context, blocks []BlockPreview) ([]BlockPreviewResult, error) {
	if err := parent.Err(); err != nil {
		return nil, err
	}
	base := blocks[0]
	for _, block := range blocks {
		if len(block.Source) > 16<<10 || len(block.Preamble) > 64<<10 || len(block.Bibliography) > 4096 || len(block.References) > 64<<10 {
			return nil, errors.New("block preview exceeds size limit")
		}
		if block.Path != base.Path || block.Root != base.Root || block.Main != base.Main ||
			block.Preamble != base.Preamble || block.Bibliography != base.Bibliography || block.References != base.References {
			return nil, errors.New("block preview declarations differ")
		}
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "nomo-block-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if IsLaTeXPath(base.Path) {
		return renderLaTeXBlocks(ctx, dir, blocks)
	}
	return renderTypstBlocks(ctx, dir, blocks)
}

func renderLaTeXBlocks(ctx context.Context, dir string, blocks []BlockPreview) ([]BlockPreviewResult, error) {
	block := blocks[0]
	head := block.Preamble
	if head == "" && block.Main != "" && block.Main != block.Path {
		f, err := os.Open(block.Main)
		if err == nil {
			data, err := io.ReadAll(io.LimitReader(f, 64<<10))
			f.Close()
			if err == nil {
				head = latexPreamble(string(data))
			}
		}
	}
	if head == "" {
		head = "\\documentclass{article}\n\\usepackage{amsmath,amssymb,amsthm,graphicx,booktabs,array,multirow}\n"
	}
	var doc strings.Builder
	doc.WriteString(head)
	doc.WriteString(`
\begin{document}
`)
	if strings.Contains(head, AuthorStylePrefix(true, AuthorPage)) && strings.Contains(head, `\pagecolor[HTML]`) {
		// The canvas paints the page color. A solid fill would prevent cropping every local fragment.
		doc.WriteString("\\ifdefined\\nopagecolor\\nopagecolor\\fi\n\\ifdefined\\normalcolor\\normalcolor\\fi\n")
	}
	doc.WriteString(`
\pagestyle{empty}
\makeatletter\let\ps@plain\ps@empty\makeatother
\newcount\nomoPreviewPages
\newwrite\nomoPreviewIndex
\immediate\openout\nomoPreviewIndex=block-pages.txt
\AddToHook{shipout/after}{\global\advance\nomoPreviewPages by 1}
`)
	if block.References != "" {
		// LaTeX reads labels from the aux file during \begin{document}.
		if err := os.WriteFile(filepath.Join(dir, "block.aux"), []byte(block.References), 0600); err != nil {
			return nil, err
		}
	}
	for _, item := range blocks {
		doc.WriteString("\\begingroup\n\\thispagestyle{empty}\n")
		if item.Counter == "figure" || item.Counter == "table" || item.Counter == "equation" {
			number := item.Number
			if !shortReferenceNumber.MatchString(number) {
				number = `\ensuremath{\ldots}`
			}
			fmt.Fprintf(&doc, "\\expandafter\\def\\csname the%s\\endcsname{%s}\n", item.Counter, number)
		}
		if item.Inline {
			doc.WriteString("\\noindent ")
		}
		doc.WriteString(item.Source)
		doc.WriteString("\n\\par\\clearpage\\endgroup\n\\immediate\\write\\nomoPreviewIndex{\\the\\nomoPreviewPages}\n")
	}
	doc.WriteString("\\immediate\\closeout\\nomoPreviewIndex\n\\end{document}\n")
	if err := os.WriteFile(filepath.Join(dir, "block.tex"), []byte(doc.String()), 0600); err != nil {
		return nil, err
	}
	paths := []string{".", filepath.Dir(block.Path)}
	if block.Main != "" {
		paths = append(paths, filepath.Dir(block.Main))
	}
	if block.Root != "" {
		paths = append(paths, block.Root)
	}
	search := strings.Join(paths, string(os.PathListSeparator)) + string(os.PathListSeparator)
	env := []string{"TEXINPUTS=" + search + os.Getenv("TEXINPUTS"), "openout_any=p"}
	// Font probes should reach the template's fallback without spawning mktextfm for missing fonts.
	if _, err := run(ctx, dir, "", env, "xelatex", "-file-line-error", "-no-shell-escape", "-no-parse-first-line", "-no-mktex=tfm",
		"-interaction=nonstopmode", "-halt-on-error", "./block.tex"); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "block-pages.txt"))
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(data))
	if len(fields) != len(blocks) {
		return nil, errors.New("invalid block page index")
	}
	ends := make([]int, len(fields))
	for i, field := range fields {
		n, err := strconv.Atoi(field)
		if err != nil || n < 1 || i > 0 && n < ends[i-1] || n > 3*MaxPreviewBatch {
			return nil, errors.New("invalid block page range")
		}
		ends[i] = n
	}
	pdf := filepath.Join(dir, "block.pdf")
	out := make([]BlockPreviewResult, len(blocks))
	first := 1
	for i, last := range ends {
		out[i].Image, out[i].Err = readBlockPages(ctx, pdf, first, last, blocks[i])
		first = last + 1
	}
	return out, nil
}

var missingTypstLabel = regexp.MustCompile("label " + "`" + "<([^<>\n]+)>" + "`" + " does not exist")

func renderTypstBlocks(ctx context.Context, dir string, blocks []BlockPreview) ([]BlockPreviewResult, error) {
	base := blocks[0]
	cwd := dir
	if base.Path != "" {
		cwd = filepath.Dir(base.Path)
	}
	const paper = "height: auto, numbering: none, header: none, footer: none, background: none, "
	var body strings.Builder
	for i, block := range blocks {
		width, fill, margin := "450pt", "white", "8pt"
		if block.Inline || block.Path == "" {
			width, margin = "auto", "4pt"
		}
		if block.Transparent || block.Inline || block.Path == "" {
			fill = "none"
		}
		// A marker in the margin identifies each block even when a template emits other pages.
		fmt.Fprintf(&body, "\n#page(width: %s, %sfill: %s, margin: %s, foreground: place(top + left, rect(width: 3pt, height: 3pt, stroke: none, fill: rgb(1, 42, %d))))[\n",
			width, paper, fill, margin, i+180)
		if (block.Transparent || block.Inline) && block.Dark {
			body.WriteString("#set text(fill: rgb(\"#ebebeb\"))\n")
		}
		if strings.HasPrefix(block.Source, "#bibliography(") {
			body.WriteString("#set bibliography(full: true)\n")
		}
		if block.Counter == "figure" || block.Counter == "equation" {
			setup, src := typstPreviewNumber(block.Source, block.Counter, block.Number)
			body.WriteString(setup)
			body.WriteString(src)
		} else {
			body.WriteString(block.Source)
		}
		body.WriteString("\n]\n")
	}
	if base.Bibliography != "" {
		body.WriteString("\n#page(width: 450pt, height: auto)[\n")
		body.WriteString(base.Bibliography)
		body.WriteString("\n]\n")
	}
	compile := func(unknown string) error {
		rule := ""
		if base.References != "" || unknown != "" {
			rule = "#let nomo-preview-numbers = (:)\n"
			if base.References != "" {
				rule = "#let nomo-preview-numbers = (" + base.References + ")\n"
			}
			rule += "#show ref: it => {\n" +
				"  let key = str(it.target)\n" +
				"  if key in (" + unknown + ") { text(fill: gray)[@#key] }\n" +
				"  else if key in nomo-preview-numbers and it.form == \"normal\" {\n" +
				"    if it.supplement != auto and it.supplement != none and type(it.supplement) == content { [#it.supplement ] }\n" +
				"    nomo-preview-numbers.at(key)\n" +
				"  } else { it }\n" +
				"}\n"
		}
		_, err := run(ctx, cwd, base.Preamble+"\n"+rule+body.String(), nil, "typst", "compile", "--root", cwd,
			"--format", "png", "--ppi", "144", "--pages", fmt.Sprintf("1-%d", len(blocks)+2), "-", filepath.Join(dir, "typst-{p}.png"))
		return err
	}
	if err := compile(""); err != nil {
		matches := missingTypstLabel.FindAllStringSubmatch(err.Error(), 32)
		if len(matches) == 0 || ctx.Err() != nil {
			return nil, err
		}
		var keys strings.Builder
		for _, match := range matches {
			keys.WriteString(strconv.Quote(match[1]))
			keys.WriteByte(',')
		}
		// Keep successful references unchanged. An unresolved key stays visible in the local preview.
		if err = compile(keys.String()); err != nil {
			return nil, err
		}
	}
	pages, err := previewPageFiles(dir, "typst-")
	if err != nil {
		return nil, err
	}
	out := make([]BlockPreviewResult, len(blocks))
	for i := range out {
		out[i].Err = errors.New("block page not found")
	}
	for n := 1; n <= len(blocks)+2; n++ {
		if pages[n] == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		img, err := readPreviewPNG(pages[n])
		if err != nil {
			continue
		}
		r, g, b, _ := img.At(2, 2).RGBA()
		i := int(b>>8) - 180
		if r>>8 != 1 || g>>8 != 42 || i < 0 || i >= len(out) {
			continue
		}
		// Ignore the marker, which is confined to the upper-left six pixels.
		pad := 8
		if blocks[i].Inline || blocks[i].Path == "" {
			pad = 2
		}
		block := blocks[i]
		out[i].Image, out[i].Err = cropBlockPreview(ctx, img, image.Rect(0, 0, 6, 6), pad,
			block.Transparent || block.Inline || block.Path == "")
	}
	return out, nil
}

func previewPageFiles(dir, prefix string) (map[int]string, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	pages := make(map[int]string)
	for _, file := range files {
		if n, ok := strings.CutPrefix(file.Name(), prefix); ok && strings.HasSuffix(n, ".png") {
			if page, err := strconv.Atoi(strings.TrimSuffix(n, ".png")); err == nil {
				pages[page] = filepath.Join(dir, file.Name())
			}
		}
	}
	return pages, nil
}

func readBlockPages(ctx context.Context, pdf string, first, last int, block BlockPreview) (image.Image, error) {
	if first > last || last-first >= 3 {
		return nil, errors.New("block exceeds preview page limit")
	}
	var imgs []image.Image
	var w, h int
	for i := first; i <= last; i++ {
		raw, err := ReadPDFPage(ctx, pdf, i, 1684)
		if err != nil {
			return nil, err
		}
		pad := 8
		if block.Inline {
			pad = 2
		}
		img, err := cropBlockPreview(ctx, raw, image.Rectangle{}, pad, false)
		if err != nil {
			return nil, err
		}
		if block.Inline || block.Transparent {
			transparentInk(img, block.Dark)
		}
		imgs = append(imgs, img)
		w = max(w, img.Bounds().Dx())
		h += img.Bounds().Dy()
	}
	if len(imgs) == 1 {
		return imgs[0], nil
	}
	if h > 3600 {
		return nil, errors.New("block preview exceeds pixel limit")
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	y := 0
	for _, img := range imgs {
		b := img.Bounds()
		draw.Draw(out, b.Add(image.Pt(0, y)), img, b.Min, draw.Src)
		y += b.Dy()
	}
	return out, nil
}

func readPreviewPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		return nil, err
	}
	if cfg.Width > 1800 || cfg.Height > 2400 {
		return nil, errors.New("block preview exceeds pixel limit")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return png.Decode(f)
}

func cropBlockPreview(ctx context.Context, img image.Image, ignore image.Rectangle, pad int, transparent bool) (*image.RGBA, error) {
	b := img.Bounds()
	x0, y0, x1, y1 := b.Max.X, b.Max.Y, b.Min.X, b.Min.Y
	ink := func(x, y int) bool {
		r, g, b, a := img.At(x, y).RGBA()
		const white = 0xfcfc
		return a != 0 && (transparent || r < white || g < white || b < white)
	}
	// Poppler's opaque PNGs decode as RGBA. Avoid color interface calls across the page.
	if src, ok := img.(*image.RGBA); ok {
		ink = func(x, y int) bool {
			i := src.PixOffset(x, y)
			p := src.Pix[i : i+4]
			return p[3] != 0 && (transparent || p[0] < 252 || p[1] < 252 || p[2] < 252)
		}
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		if y%64 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		for x := b.Min.X; x < b.Max.X; x++ {
			if image.Pt(x, y).In(ignore) || !ink(x, y) {
				continue
			}
			x0, y0 = min(x0, x), min(y0, y)
			x1, y1 = max(x1, x+1), max(y1, y+1)
		}
	}
	if x0 >= x1 || y0 >= y1 {
		return nil, errors.New("empty block preview")
	}
	clip := image.Rect(x0-pad, y0-pad, x1+pad, y1+pad).Intersect(b)
	out := image.NewRGBA(image.Rect(0, 0, clip.Dx(), clip.Dy()))
	draw.Draw(out, out.Bounds(), img, clip.Min, draw.Src)
	if marker := ignore.Intersect(clip).Sub(clip.Min); !marker.Empty() {
		var paper image.Image = image.White
		if transparent {
			paper = image.Transparent
		}
		draw.Draw(out, marker, paper, image.Point{}, draw.Src)
	}
	return out, nil
}

// Recover coverage from the white PDF background for compositing math.
func transparentInk(img *image.RGBA, dark bool) {
	for i := 0; i < len(img.Pix); i += 4 {
		p := img.Pix[i : i+4]
		white := min(p[0], p[1], p[2])
		alpha := 255 - white
		if dark {
			for j := range 3 {
				p[j] = alpha - (p[j]-white)/2
			}
		} else {
			for j := range 3 {
				p[j] -= white
			}
		}
		p[3] = alpha
	}
}

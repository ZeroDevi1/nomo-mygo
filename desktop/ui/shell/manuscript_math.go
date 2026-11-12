package shell

import (
	"context"
	"time"

	"nomo/desktop/typeset"
	"nomo/desktop/ui/mdedit"
)

func (r *renderService) renderObjects(reqs []mdedit.RenderRequest) []mdedit.RenderResult {
	type groupKey struct {
		ctx  context.Context
		base typeset.BlockPreview
	}
	type group struct {
		key        groupKey
		blocks     []typeset.BlockPreview
		indices    []int
		references string
	}
	var web []mdedit.RenderRequest
	var indices []int
	var groups []group
	out := make([]mdedit.RenderResult, len(reqs))
	for i, req := range reqs {
		ctx := req.Context
		if ctx == nil {
			ctx = context.Background()
		}
		if err := ctx.Err(); err != nil {
			out[i].Err = err
			continue
		}
		if req.Kind == mdedit.RenderPDFThumbnail {
			thumbCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			img, err := typeset.ReadPDFPage(thumbCtx, req.Src, 1, 520)
			cancel()
			if err != nil {
				out[i].Err = err
				continue
			}
			size := img.Bounds().Size()
			out[i] = mdedit.RenderResult{Img: img, W: float32(size.X), H: float32(size.Y)}
			continue
		}
		var block typeset.BlockPreview
		if style := req.Manuscript; style != nil {
			p := style.Project
			inline := req.Kind == mdedit.RenderMathInline || req.Kind == mdedit.RenderTypstMathInline
			block = typeset.BlockPreview{
				Path: p.Path, Root: p.Root, Main: p.Main, Source: req.Src,
				Preamble: style.Preamble, Bibliography: style.Bibliography, References: style.References,
				Counter: style.Counter, Number: style.Number,
				Inline: inline, Dark: req.Dark,
				Transparent: inline || req.Kind == mdedit.RenderMathDisplay || req.Kind == mdedit.RenderTypstMathDisplay,
			}
		} else if req.Kind == mdedit.RenderTypstMathDisplay || req.Kind == mdedit.RenderTypstMathInline {
			var err error
			block, err = typeset.TypstFormulaBlock(req.Src, req.FontSize, req.Kind == mdedit.RenderTypstMathDisplay, req.Dark)
			if err != nil {
				out[i].Err = err
				continue
			}
		} else {
			web = append(web, req)
			indices = append(indices, i)
			continue
		}
		base := block
		base.Source, base.Inline, base.Dark, base.Transparent = "", false, false, false
		base.Counter, base.Number = "", ""
		base.References = ""
		key := groupKey{ctx: ctx, base: base}
		g := 0
		for ; g < len(groups) && groups[g].key != key; g++ {
		}
		if g == len(groups) {
			groups = append(groups, group{key: key})
		}
		groups[g].blocks = append(groups[g].blocks, block)
		groups[g].indices = append(groups[g].indices, i)
		if block.References != "" {
			groups[g].references = block.References
		}
	}
	for _, g := range groups {
		for i := range g.blocks {
			g.blocks[i].References = g.references
		}
		for j, result := range typeset.RenderBlockPreviews(g.key.ctx, g.blocks) {
			i, req, block := g.indices[j], reqs[g.indices[j]], g.blocks[j]
			if result.Err != nil {
				out[i].Err = result.Err
				continue
			}
			scale := float32(.5)
			if req.Manuscript != nil {
				scale = req.FontSize / 32
				if block.Inline {
					scale = req.FontSize / 22
				}
			}
			bounds := result.Image.Bounds()
			w, h := float32(bounds.Dx())*scale, float32(bounds.Dy())*scale
			depth := float32(0)
			if block.Inline {
				depth = min(h*.25, req.FontSize*.28)
			}
			out[i] = mdedit.RenderResult{Img: result.Image, W: w, H: h, Depth: depth}
		}
	}
	for i, item := range r.render(web) {
		out[indices[i]] = item
	}
	return out
}

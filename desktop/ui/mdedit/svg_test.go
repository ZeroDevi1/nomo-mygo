package mdedit

import (
	"hash/maphash"
	"strings"
	"testing"
)

func TestInlineSVG(t *testing.T) {
	const svg = `<svg width="20" height="10" viewBox="0 0 20 10"><rect width="20" height="10" fill="red"/></svg>`
	decoded := func(o imgObj) (float32, float32) {
		data, err := decodeDataURI(o.src)
		if err != nil {
			t.Fatal(err)
		}
		res := decodeImage(data, false)
		if res.svg == nil {
			t.Fatalf("SVG 未解码：%v", res.err)
		}
		return res.w, res.h
	}

	// 只含 SVG 的 HTML 块：整块一行显示为图片。
	d := NewDocument("<div align=\"center\">\n" + svg + "\n</div>\n\n文字\n")
	rows, _ := buildRows(d, 0, d.Blocks()[0], maphash.MakeSeed(), false)
	if len(rows) != 1 || !rows[0].htmlImg {
		t.Fatalf("HTML 块行 %+v", rows)
	}
	p := projectHTMLImages(d.Source()[d.Blocks()[0].Start:d.Blocks()[0].End])
	if len(p.objs) != 1 || !strings.HasPrefix(p.objs[0].src, "data:image/svg+xml;base64,") {
		t.Fatalf("HTML 块投影 %+v", p.objs)
	}
	if w, h := decoded(p.objs[0]); w != 20 || h != 10 {
		t.Fatalf("尺寸 %vx%v", w, h)
	}

	// 段落内的 SVG：整体为一个行内对象；光标在其中时显示原文。
	src := "a " + svg + " b\n"
	d = NewDocument(src)
	b := d.Blocks()[0]
	p = projectLeaf(d, b.Index, b.Start, b.End, expansion{}, leafOpts{})
	if p.text != "a "+objChar+" b" || len(p.objs) != 1 {
		t.Fatalf("行内投影 %q %d", p.text, len(p.objs))
	}
	decoded(p.objs[0])
	p = projectLeaf(d, b.Index, b.Start, b.End, expansion{inBlock: true, caret: 10}, leafOpts{})
	if !strings.Contains(p.text, "<rect") || len(p.objs) != 0 {
		t.Fatalf("展开 %q", p.text)
	}

	// 未闭合或含空行被拆开的 SVG 按原文显示。
	d = NewDocument("<svg width=\"4\">\n<rect/>\n")
	if rows, _ := buildRows(d, 0, d.Blocks()[0], maphash.MakeSeed(), false); rows[0].htmlImg {
		t.Fatal("未闭合的 SVG 不应显示为图片")
	}
}

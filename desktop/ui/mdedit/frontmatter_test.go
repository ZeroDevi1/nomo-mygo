package mdedit

import (
	"hash/maphash"
	"slices"
	"testing"
)

func TestFrontMatterCard(t *testing.T) {
	src := "---\ntitle: \"Nomo 示例\"\nauthor: 清羽\ndescription: 说明\ncreated: 2026-06-08\nupdated: 2026-08-12\ntags:\n  - nomo\n  - markdown\nstatus: draft\n---\n\n# 正文\n"
	f := parseFrontMatter(src[:len("---\ntitle: \"Nomo 示例\"\nauthor: 清羽\ndescription: 说明\ncreated: 2026-06-08\nupdated: 2026-08-12\ntags:\n  - nomo\n  - markdown\nstatus: draft\n---")])
	if f.title != "Nomo 示例" || f.created != "2026-06-08" || f.updated != "2026-08-12" || f.status != "draft" ||
		!slices.Equal(f.tags, []string{"nomo", "markdown"}) || f.extra != 2 {
		t.Fatalf("字段 %+v", f)
	}
	if g := parseFrontMatter("---\ntags: [a, 'b']\n---"); !slices.Equal(g.tags, []string{"a", "b"}) {
		t.Fatalf("行内标签 %+v", g.tags)
	}

	d := NewDocument(src)
	top := d.Blocks()[0]
	if top.Kind != KindFrontMatter {
		t.Fatalf("首块 %v", top.Kind)
	}
	rows, frames := buildRows(d, 0, top, maphash.MakeSeed(), false)
	if len(rows) != 1 || rows[0].htmlKind != previewFrontMatter || len(frames) != 1 || frames[0].kind != frameFront {
		t.Fatalf("行 %d、框 %+v", len(rows), frames)
	}
	// 收起时为卡片对象；排版后的卡片含标题、日期与标签。
	p := projectHTMLPreview(src[top.Start:top.Start+int(rows[0].b)], previewFrontMatter)
	if len(p.objs) != 1 || p.objs[0].html == nil || !p.objs[0].html.front {
		t.Fatalf("投影 %+v", p.objs)
	}
	ed, _ := newTester(t, src, 600, 400)
	lay := ed.htmlLayout(p.objs[0].html, 500)
	texts := 0
	for _, op := range lay.ops {
		if op.kind == hpText {
			texts++
		}
	}
	// 标题、状态、日期行、两个标签。
	if texts != 5 || lay.h <= 0 {
		t.Fatalf("卡片 %d 段文字，高 %.0f", texts, lay.h)
	}
}

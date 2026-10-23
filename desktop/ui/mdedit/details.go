package mdedit

import (
	"regexp"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui/markdown/parser"
)

// 折叠块（原版 details 节点，nomo-md/nomo 1aa9342）：原文写作
//
//	<details open>
//	<summary>标题</summary>
//
//	正文（任意 Markdown）
//
//	</details>
//
// 解析器把开始与结束标记各解析为一个 HTML 块，正文是其间的普通顶层块。这里在顶层 HTML 块中识别标记并配对，
// 形成折叠框架（detailsFrame）；视图据此缩进正文、绘制卡片、折叠隐藏正文（detailsview.go）。未配对的标记
// （如缺少 </details>）不构成框架，按原文显示，不影响其后内容。列表、引用中的折叠块按原文显示。

// detailsOpenTok 是 HTML 块中的一个开始标记：off 为“<details”相对块起点的偏移，end 为“</summary>”之后的偏移。
type detailsOpenTok struct {
	off, end    int
	defaultOpen bool
	summary     string // <summary> 与 </summary> 之间的原文（可含行内 HTML）
	summaryAt   int    // summary 原文相对块起点的偏移
}

var (
	detailsOpenRe    = regexp.MustCompile(`^<details(\s+open(\s*=\s*("open"|'open'|""|''|open))?)?\s*>`)
	detailsSummaryRe = regexp.MustCompile(`^<summary\s*>((?:[^<\n]|<(?:/?(?:strong|b|em|i|code|s|del|u|mark|sub|sup|a)\b[^>\n]*)>)*?)</summary\s*>`)
	detailsCloseRe   = regexp.MustCompile(`^</details\s*>`)
)

// parseDetailsMarker 把 HTML 块原文解析为若干结束标记后跟若干开始标记（每个开始标记紧跟 summary）；
// 含其他内容时 ok 为 false。标记名不区分大小写。
func parseDetailsMarker(src string) (closes []int, opens []detailsOpenTok, ok bool) {
	lower := strings.ToLower(src)
	i := 0
	skip := func() {
		for i < len(src) && (src[i] == ' ' || src[i] == '\t' || src[i] == '\n' || src[i] == '\r') {
			i++
		}
	}
	for skip(); i < len(src); skip() {
		if m := detailsCloseRe.FindString(lower[i:]); m != "" {
			if len(opens) > 0 {
				return nil, nil, false // 同一块内先开后关（空正文）不作为框架
			}
			closes = append(closes, i)
			i += len(m)
			continue
		}
		m := detailsOpenRe.FindStringSubmatch(lower[i:])
		if m == nil {
			return nil, nil, false
		}
		tok := detailsOpenTok{off: i, defaultOpen: m[1] != ""}
		i += len(m[0])
		skip()
		sm := detailsSummaryRe.FindStringSubmatchIndex(lower[i:])
		if sm == nil {
			return nil, nil, false
		}
		tok.summary, tok.summaryAt = src[i+sm[2]:i+sm[3]], i+sm[2]
		i += sm[1]
		tok.end = i
		opens = append(opens, tok)
	}
	return closes, opens, len(closes)+len(opens) > 0
}

// detailsFrame 是一对配对的折叠标记。open、close 为标记所在的顶层块序号，openTok、closeTok 为标记在块内的
// 序号；depth 为外层框架数；key 为“<details”的绝对字节偏移，作为折叠状态的身份（编辑后由视图平移）。
type detailsFrame struct {
	open, close       int
	openTok, closeTok int
	depth             int
	parent            int // 外层框架在 frames 中的序号，-1 为顶层
	key               int
	defaultOpen       bool
	summary           string
	summaryAt         int // summary 原文的绝对起点
}

// detailsMarkerBlock 是一个参与配对的标记块：block 为顶层块序号，closes、opens 为各标记依次对应的框架序号
// （先结束标记，后开始标记）。
type detailsMarkerBlock struct {
	block         int
	closes, opens []int
}

// detailsIndex 是某一原文版本的折叠框架：frames 按开始标记的文档顺序（先序）排列，markers 按块序号升序。
// 不涉及标记块的编辑由 shiftDetails 就地平移，ver 随之更新。
type detailsIndex struct {
	ver     uint64
	frames  []detailsFrame
	markers []detailsMarkerBlock
}

// marker 返回顶层块 i 的标记块，不是标记块时返回 nil。
func (x *detailsIndex) marker(i int) *detailsMarkerBlock {
	k := sort.Search(len(x.markers), func(k int) bool { return x.markers[k].block >= i })
	if k < len(x.markers) && x.markers[k].block == i {
		return &x.markers[k]
	}
	return nil
}

// shiftDetails 在一次编辑后尝试就地更新折叠框架：编辑前后受影响的块都不是（也不像）标记块时，框架结构不变，
// 只需平移块序号与字节偏移，O(框架数) 且不分配；否则丢弃索引，下次访问时全量重建。prev 为编辑前的版本。
func (d *Document) shiftDetails(ch Change, prev uint64) {
	x := d.detailsIdx
	if x == nil || x.ver != prev {
		return
	}
	src := d.Source()
	for i := ch.First; i < ch.First+ch.Inserted; i++ {
		if b := d.blocks[i]; b.Kind == parser.HTMLBlock && isDetailsMarkerHead(src[b.Start:b.End]) {
			d.detailsIdx = nil
			return
		}
	}
	k := sort.Search(len(x.markers), func(k int) bool { return x.markers[k].block >= ch.First })
	if k < len(x.markers) && x.markers[k].block < ch.First+ch.Removed {
		d.detailsIdx = nil
		return
	}
	db, dk, edge := ch.Inserted-ch.Removed, ch.NewLen-ch.OldLen, ch.At+ch.OldLen
	for ; k < len(x.markers); k++ {
		x.markers[k].block += db
	}
	for f := range x.frames {
		fr := &x.frames[f]
		if fr.open >= ch.First+ch.Removed {
			fr.open += db
		}
		if fr.close >= ch.First+ch.Removed {
			fr.close += db
		}
		if fr.key >= edge {
			fr.key += dk
			fr.summaryAt += dk
		}
	}
	x.ver = d.version
}

// details 返回当前原文的折叠框架（按版本缓存）。只扫描顶层 HTML 块，不含折叠块的文档成本为 O(顶层块数) 的整数比较。
func (d *Document) details() *detailsIndex {
	if d.detailsIdx != nil && d.detailsIdx.ver == d.version {
		return d.detailsIdx
	}
	src := d.Source()
	type tok struct {
		block, k int
		close    bool
		open     detailsOpenTok
		frame    int
	}
	var toks []tok
	for i, b := range d.blocks {
		if b.Kind != parser.HTMLBlock || b.End-b.Start < 10 || src[b.Start] != '<' {
			continue
		}
		if !isDetailsMarkerHead(src[b.Start:b.End]) {
			continue
		}
		m := d.detailsParsed(src[b.Start:b.End])
		if !m.ok {
			continue
		}
		closes, opens := m.closes, m.opens
		for k := range closes {
			toks = append(toks, tok{block: i, k: k, close: true, frame: -1})
		}
		for k, o := range opens {
			o.off += b.Start
			o.end += b.Start
			o.summaryAt += b.Start
			toks = append(toks, tok{block: i, k: k, open: o, frame: -1})
		}
	}
	idx := &detailsIndex{ver: d.version}
	if len(toks) == 0 {
		d.detailsIdx = idx
		return idx
	}
	// 配对：开始标记入栈，结束标记与栈顶配对。框架按开始标记先序编号，深度为配对时栈中剩余数。
	var stack []int // toks 中开始标记的下标
	type pair struct{ open, close int }
	var pairs []pair
	for t := range toks {
		if !toks[t].close {
			stack = append(stack, t)
			continue
		}
		if len(stack) == 0 {
			continue
		}
		o := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		pairs = append(pairs, pair{o, t})
	}
	// 含未配对标记的块整体按原文显示，与之相关的框架也作废；反复直到稳定（通常一轮）。
	bad := map[int]bool{}
	paired := make([]int, len(toks)) // 与之配对的标记下标，-1 未配对
	for t := range paired {
		paired[t] = -1
	}
	for _, p := range pairs {
		paired[p.open], paired[p.close] = p.close, p.open
	}
	for t, p := range paired {
		if p < 0 {
			bad[toks[t].block] = true
		}
	}
	for changed := len(bad) > 0; changed; {
		changed = false
		for t, p := range paired {
			if p >= 0 && (bad[toks[t].block] || bad[toks[p].block]) && !(bad[toks[t].block] && bad[toks[p].block]) {
				bad[toks[t].block], bad[toks[p].block] = true, true
				changed = true
			}
		}
	}
	var open []int // 当前打开的框架序号
	for t := range toks {
		if bad[toks[t].block] {
			continue
		}
		if n := len(idx.markers); n == 0 || idx.markers[n-1].block != toks[t].block {
			idx.markers = append(idx.markers, detailsMarkerBlock{block: toks[t].block})
		}
		mb := &idx.markers[len(idx.markers)-1]
		if toks[t].close {
			f := open[len(open)-1]
			open = open[:len(open)-1]
			idx.frames[f].close, idx.frames[f].closeTok = toks[t].block, toks[t].k
			mb.closes = append(mb.closes, f)
			continue
		}
		o := toks[t].open
		parent := -1
		if len(open) > 0 {
			parent = open[len(open)-1]
		}
		idx.frames = append(idx.frames, detailsFrame{open: toks[t].block, openTok: toks[t].k, depth: len(open), parent: parent,
			key: o.off, defaultOpen: o.defaultOpen, summary: o.summary, summaryAt: o.summaryAt})
		f := len(idx.frames) - 1
		open = append(open, f)
		mb.opens = append(mb.opens, f)
	}
	d.detailsIdx = idx
	return idx
}

// isDetailsMarkerHead 报告块原文是否以折叠标记开头（不分大小写，不分配内存）。
func isDetailsMarkerHead(s string) bool {
	return len(s) >= 9 && (strings.EqualFold(s[:8], "<details") || strings.EqualFold(s[:9], "</details"))
}

// detailsMarkerParse 是 parseDetailsMarker 的结果（偏移相对块起点）。
type detailsMarkerParse struct {
	closes []int
	opens  []detailsOpenTok
	ok     bool
}

// detailsParsed 返回标记块原文的解析结果，按原文缓存：编辑只改变个别块，其余块在下一版本直接命中。缓存键复制
// 为独立字符串（不引用整篇原文）；条目超过上限时整体清空。
func (d *Document) detailsParsed(text string) detailsMarkerParse {
	if m, ok := d.detailsCache[text]; ok {
		return m
	}
	if d.detailsCache == nil || len(d.detailsCache) > detailsCacheMax {
		d.detailsCache = map[string]detailsMarkerParse{}
	}
	var m detailsMarkerParse
	m.closes, m.opens, m.ok = parseDetailsMarker(text)
	d.detailsCache[strings.Clone(text)] = m
	return m
}

// detailsCacheMax 是标记解析缓存的条目上限。经验值：每条约为标记原文长度加 100 字节，65536 条约 10 MB 以内，
// 足以覆盖测试集中最多的 4224 个标记块。
const detailsCacheMax = 1 << 16

// frameAt 返回开始标记位于 key 的框架序号，没有时返回 -1。
func (x *detailsIndex) frameAt(key int) int {
	for f := range x.frames {
		if x.frames[f].key == key {
			return f
		}
	}
	return -1
}

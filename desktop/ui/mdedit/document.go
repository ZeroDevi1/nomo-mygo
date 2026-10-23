// Package mdedit 是以 Markdown 原文为唯一事实源的语义编辑器（P0 原型）。
//
// Document 持有原文、顶层块索引、选区与撤销历史，不持有任何视图对象；
// Editor 是绑定到窗口的视图，持有布局缓存，可随时释放并由 Document 重建。
package mdedit

import (
	"slices"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui/markdown/parser"
)

// Block 描述一个顶层块在原文中的位置。Start/End 为字节偏移，End 不含块末换行；
// U16Start/U16Len 是同一区间的 UTF-16 码元坐标，供输入法接口使用。
type Block struct {
	Index      int // parser 中的块序号，仅在下次编辑前有效
	Kind       parser.Kind
	Start, End int
	U16Start   int
	U16Len     int
}

// Change 描述一次编辑后顶层块列表的替换范围：旧的 [First, First+Removed)
// 被新的 [First, First+Inserted) 取代，其余块内容不变（偏移可能整体平移）。
type Change struct {
	First, Removed, Inserted int
	// At、OldLen、NewLen 为原文层面的替换：字节起点、被替换长度与新文本长度（视图据此平移按偏移记录的状态）。
	At, OldLen, NewLen int
}

// Selection 是字节偏移表示的选区；Anchor 固定端，Head 为光标所在端。
type Selection struct {
	Anchor, Head int
}

func (s Selection) Range() (int, int) { return min(s.Anchor, s.Head), max(s.Anchor, s.Head) }
func (s Selection) Empty() bool       { return s.Anchor == s.Head }

// Document 是一个 Markdown 文档的编辑状态。
//
// 生命周期：NewDocument 创建后即可编辑；不持有回调、视图或后台任务，丢弃即释放。
// 并发：只能在 UI 线程使用。
//
// 不变量：blocks 按 Start 升序，与 md 的顶层子块一一对应；u16Total 等于原文的
// UTF-16 长度；marked 非空时 marked.Anchor <= marked.Head 且位于原文范围内。
type Document struct {
	md     *tree
	blocks []Block
	spare  []Block // 编辑时复用的块数组，避免每次按键分配
	// u16Total 是原文的 UTF-16 长度，编辑时增量维护。
	u16Total int
	version  uint64
	// hasToc 表示文档含目录区（collect 时更新）；headings/headingsVer 缓存 Headings 的结果。
	hasToc bool
	// fm 是 front matter 结束偏移（collect 时更新，0 为没有），recollect 据此判断编辑是否可能改变它。
	fm          int
	headings    []Heading
	headingsVer uint64
	// detailsIdx 缓存折叠框架（见 details.go），按版本失效。
	detailsIdx *detailsIndex
	// detailsCache 缓存标记块原文的解析结果（detailsParsed），条目数有上限。
	detailsCache map[string]detailsMarkerParse
	// stats 为写作统计，statsOK 为 false 时尚未计算；计算后由 replace 增量维护（statsEdit）。
	stats   Stats
	statsOK bool
	// inputVer 是最近一次键盘输入（键入、删除、输入法）后的版本；撤销、粘贴与命令不更新它。
	inputVer uint64

	Sel Selection
	// marked 是输入法组字区间（字节），hasMarked 为 false 时无组字。
	marked    Selection
	hasMarked bool

	hist history
}

// NewDocument 解析 src 并建立块索引。
func NewDocument(src string) *Document {
	// 现版本支持 $...$ 行内公式，Default 不含单美元公式，需显式加上。
	d := &Document{md: newTree(parser.Default | parser.ExtDollarMath)}
	d.md.Set(src)
	d.blocks = d.collect(d.blocks[:0])
	d.fillU16(d.blocks, 0)
	d.u16Total = u16Len(src)
	return d
}

// Source 返回当前原文。返回值共享内部存储，调用方不应长期持有大文档的旧版本。
func (d *Document) Source() string { return d.md.Source() }

// Blocks 返回顶层块列表，下次编辑前有效。
func (d *Document) Blocks() []Block { return d.blocks }

// Version 每次原文变化时递增。
func (d *Document) Version() uint64 { return d.version }

// InputVersion 返回最近一次键盘输入（键入、删除、输入法组字）后的原文版本；等于 Version 时表示最近的
// 修改来自键盘输入，撤销、重做、粘贴与格式命令之后则不相等。表情建议等只在键入时弹出的功能据此判断。
func (d *Document) InputVersion() uint64 { return d.inputVer }

// U16Len 返回原文的 UTF-16 长度。
func (d *Document) U16Len() int { return d.u16Total }

// Marked 返回组字区间。
func (d *Document) Marked() (Selection, bool) { return d.marked, d.hasMarked }

// KindFrontMatter 是文档开头 YAML front matter 的合成块类型。解析器不认识
// front matter，会把它拆成分割线、段落或 Setext 标题；collect 把这些块合并成一块。
const KindFrontMatter parser.Kind = 200

// KindToc 是正文目录区（<!-- toc --> … <!-- /toc -->）的合成块类型，对应原版 toc_block 节点。
const KindToc parser.Kind = 201

const (
	tocStartMarker = "<!-- toc -->"
	tocEndMarker   = "<!-- /toc -->"
)

func (d *Document) collect(dst []Block) []Block {
	src := d.md.Source()
	fm := frontMatterEnd(src)
	d.fm = fm
	// 目录区：成对的 <!-- toc --> 与 <!-- /toc --> 注释块之间（含两端）合并为一个 KindToc 合成块；
	// 未配对的标记按普通注释块处理。先找出配对的起止子块序号，再在主循环中合并。
	tocEnd := map[int]int{}
	open := -1
	for c := range d.md.Children(0) {
		b := d.md.Block(c)
		if b.Kind != parser.HTMLBlock || b.Start < fm {
			continue
		}
		switch strings.TrimSpace(src[b.Start:b.End]) {
		case tocStartMarker:
			open = c
		case tocEndMarker:
			if open >= 0 {
				tocEnd[open] = c
				open = -1
			}
		}
	}
	d.hasToc = len(tocEnd) > 0
	inToc := -1 // 正在合并的目录区的结束子块序号
	for c := range d.md.Children(0) {
		b := d.md.Block(c)
		if inToc >= 0 {
			dst[len(dst)-1].End = max(dst[len(dst)-1].End, trailingEnd(src, b.End))
			if c == inToc {
				inToc = -1
			}
			continue
		}
		if end, ok := tocEnd[c]; ok {
			dst = append(dst, Block{Index: c, Kind: KindToc, Start: b.Start, End: trailingEnd(src, b.End)})
			inToc = end
			continue
		}
		if fm > 0 && b.Start < fm {
			// 与 front matter 相交的块并入合成块；跨过结束行的块整体并入，保持区间不重叠。
			if n := len(dst); n > 0 && dst[n-1].Kind == KindFrontMatter {
				dst[n-1].End = max(dst[n-1].End, b.End)
			} else {
				dst = append(dst, Block{Index: c, Kind: KindFrontMatter, Start: 0, End: max(fm, b.End)})
			}
			continue
		}
		dst = append(dst, Block{Index: c, Kind: b.Kind, Start: b.Start, End: trailingEnd(d.md.Source(), b.End)})
	}
	// 块区间不得重叠：原文含 CR 换行（外壳打开时会统一为 LF，这里只是防御）时，延伸到行尾可能越过下一块的起点。
	for i := 1; i < len(dst); i++ {
		dst[i-1].End = min(dst[i-1].End, dst[i].Start)
	}
	return dst
}

// trailingEnd 把块末尾延伸到所在行的行尾（不含换行）。解析器的块区间在几处止于
// 行中：空列表项“3. ”只含“3”，ATX 标题不含闭合的“#”，表格末行不含末尾的“|”。
// 块区间应覆盖其最后一行，否则光标会落在块间隙里、被隐藏的标记无法编辑。
func trailingEnd(src string, end int) int {
	if k := strings.IndexByte(src[end:], '\n'); k >= 0 {
		return end + k
	}
	return len(src)
}

// frontMatterEnd 返回 front matter 结束行（--- 或 ...）末尾、不含换行的字节偏移；
// 文档不以 front matter 开头时返回 0。
func frontMatterEnd(src string) int {
	if !strings.HasPrefix(src, "---\n") && !strings.HasPrefix(src, "---\r\n") {
		return 0
	}
	for pos := strings.IndexByte(src, '\n') + 1; pos < len(src); {
		end := strings.IndexByte(src[pos:], '\n')
		if end < 0 {
			end = len(src) - pos
		}
		line := strings.TrimRight(src[pos:pos+end], " \t\r")
		if line == "---" || line == "..." {
			return pos + len(line)
		}
		pos += end + 1
	}
	return 0
}

// fillU16 从 blocks[from] 起顺序计算 UTF-16 坐标，前一块的坐标须已有效。
func (d *Document) fillU16(blocks []Block, from int) {
	src := d.md.Source()
	pos, u := 0, 0
	if from > 0 {
		p := blocks[from-1]
		pos, u = p.End, p.U16Start+p.U16Len
	}
	for i := from; i < len(blocks); i++ {
		b := &blocks[i]
		u += u16Len(src[pos:b.Start])
		b.U16Start = u
		b.U16Len = u16Len(src[b.Start:b.End])
		u += b.U16Len
		pos = b.End
	}
}

// replace 把原文 [from, to) 替换为 text，增量更新解析与块索引，不改选区和历史。
func (d *Document) replace(from, to int, text string) Change {
	src := d.md.Source()
	from, to = clamp(from, 0, len(src)), clamp(to, 0, len(src))
	if from > to {
		from, to = to, from
	}
	if from == to && text == "" {
		return Change{}
	}
	delta := len(text) - (to - from)
	u16Delta := u16Len(text) - u16Len(src[from:to])
	old := d.blocks
	oldLen := d.md.Len()
	d.md.Edit(from, to, text)
	d.statsEdit(src, from, to, text)
	if ch, ok := d.recollect(src, from, to, delta, u16Delta, d.md.Len()-oldLen); ok {
		d.u16Total += u16Delta
		d.version++
		ch.At, ch.OldLen, ch.NewLen = from, to-from, len(text)
		d.shiftDetails(ch, d.version-1)
		return ch
	}
	nb := d.collect(d.spare[:0])

	// 前缀：完全位于编辑点之前且区间不变的块内容未变。
	p := 0
	for p < len(old) && p < len(nb) {
		o, n := old[p], nb[p]
		if o.End > from || o.Start != n.Start || o.End != n.End || o.Kind != n.Kind {
			break
		}
		nb[p].U16Start, nb[p].U16Len = o.U16Start, o.U16Len
		p++
	}
	// 后缀：完全位于被替换区间之后、平移 delta 后区间一致的块内容未变。
	s := 0
	for s < len(old)-p && s < len(nb)-p {
		o, n := old[len(old)-1-s], nb[len(nb)-1-s]
		if o.Start < to || o.Start+delta != n.Start || o.End+delta != n.End || o.Kind != n.Kind {
			break
		}
		s++
	}
	mid := len(nb) - s
	d.fillU16(nb[:mid], p)
	for k := 0; k < s; k++ {
		o := old[len(old)-s+k]
		nb[mid+k].U16Start = o.U16Start + u16Delta
		nb[mid+k].U16Len = o.U16Len
	}
	d.spare, d.blocks = old, nb
	d.u16Total += u16Delta
	d.version++
	ch := Change{First: p, Removed: len(old) - p - s, Inserted: mid - p, At: from, OldLen: to - from, NewLen: len(text)}
	d.shiftDetails(ch, d.version-1)
	return ch
}

// recollect 在编辑后只重建受影响的顶层块，避免每次按键遍历全部顶层子块。解析器只重解析编辑点附近，其后的块
// 原样平移（序号整体移动 shift = 新旧块数之差，偏移移动 delta）；这里保留编辑点之前的块、找到第一个在新解析中
// 对齐的旧块作为后缀起点，中间一段从解析器的顶层子块重新收集，后缀就地平移。成功时就地更新 d.blocks 并返回
// 替换范围；涉及 front matter 或目录区（它们由多个子块合成）或无法对齐时返回 false 且不改 d.blocks，由调用方
// 全量收集。oldSrc 为编辑前的原文。
func (d *Document) recollect(oldSrc string, from, to, delta, u16Delta, shift int) (Change, bool) {
	src := d.md.Source()
	old := d.blocks
	// front matter 的结束行可能被这次编辑创建或改变：文档以 “---” 开头而尚无 front matter，或编辑落在其中。
	if d.fm == 0 && strings.HasPrefix(src, "---") || from <= d.fm+1 {
		return Change{}, false
	}
	// 前缀：止于编辑点之前的块，再退一块——编辑可能并入前一块（懒惰续行、Setext 下划线、列表续行）。
	p := sort.Search(len(old), func(i int) bool { return old[i].End >= from })
	p = max(0, p-1)
	start := 1
	if p > 0 {
		k := old[p-1]
		b := d.md.Block(k.Index)
		if k.Kind == KindFrontMatter || k.Kind == KindToc || b.Parent != 0 || b.Start != k.Start || b.Kind != k.Kind {
			return Change{}, false
		}
		start = b.Next
	}
	// 后缀：第一个起点不早于被替换区间末尾、且在新解析中按 shift/delta 对齐的旧块。对齐的顶层块之后的解析
	// 只取决于其后的原文（容器在顶层块起点全部关闭），因此其后的块都不变。
	q := max(p, sort.Search(len(old), func(i int) bool { return old[i].Start >= to }))
	end := d.md.Len()
	for tries := 0; ; tries++ {
		if q == len(old) {
			break
		}
		if tries == 8 {
			return Change{}, false
		}
		o := old[q]
		if n := o.Index + shift; n >= start && n < end {
			if b := d.md.Block(n); b.Parent == 0 && b.Start == o.Start+delta && b.Kind == o.Kind && o.Kind != KindFrontMatter && o.Kind != KindToc {
				end = n
				break
			}
		}
		q++
	}
	for _, o := range old[p:q] {
		if o.Kind == KindFrontMatter || o.Kind == KindToc || o.Kind == parser.HTMLBlock && isTocMarker(oldSrc[o.Start:o.End]) {
			return Change{}, false
		}
	}
	// 中间段：新解析中 start 到 end 之间的顶层子块；新出现的目录标记可能与别处的标记配对，交给全量收集。
	mid := d.spare[:0]
	c := start
	for ; c < end; c = d.md.Block(c).Next {
		b := d.md.Block(c)
		if b.Kind == parser.HTMLBlock && isTocMarker(src[b.Start:b.End]) {
			return Change{}, false
		}
		mid = append(mid, Block{Index: c, Kind: b.Kind, Start: b.Start, End: trailingEnd(src, b.End)})
	}
	if c != end {
		return Change{}, false
	}
	for i := 1; i < len(mid); i++ {
		mid[i-1].End = min(mid[i-1].End, mid[i].Start)
	}
	if n := len(mid); n > 0 && q < len(old) {
		mid[n-1].End = min(mid[n-1].End, old[q].Start+delta)
	}
	// 收窄到真正变化的块：去掉与旧块相同的首尾（退回的那一块通常不变），让视图少重建。
	for len(mid) > 0 && p < q && mid[0].Start == old[p].Start && mid[0].End == old[p].End && mid[0].Kind == old[p].Kind && old[p].End < from {
		mid[0].U16Start, mid[0].U16Len = old[p].U16Start, old[p].U16Len
		mid = mid[1:]
		p++
	}
	for n := len(mid); n > 0 && q > p; n = len(mid) {
		o := old[q-1]
		if o.Start < to || mid[n-1].Start != o.Start+delta || mid[n-1].End != o.End+delta || mid[n-1].Kind != o.Kind {
			break
		}
		mid = mid[:n-1]
		q--
	}
	inserted, removed := len(mid), q-p
	nb := slices.Replace(old, p, q, mid...)
	d.fillU16(nb[:p+inserted], p)
	for k := p + inserted; k < len(nb); k++ {
		b := &nb[k]
		b.Index += shift
		b.Start += delta
		b.End += delta
		b.U16Start += u16Delta
	}
	d.blocks, d.spare = nb, mid[:0]
	return Change{First: p, Removed: removed, Inserted: inserted}, true
}

// isTocMarker 报告 HTML 块原文是否为目录区起止标记。
func isTocMarker(s string) bool {
	s = strings.TrimSpace(s)
	return s == tocStartMarker || s == tocEndMarker
}

// BlockAt 返回包含字节偏移 b 的块序号（Start <= b <= End），b 位于块间隙时
// 返回 -1，并在 after 中给出间隙前一块的序号（文档开头之前为 -1）。
func (d *Document) BlockAt(b int) (index int, after int) {
	i := sort.Search(len(d.blocks), func(i int) bool { return d.blocks[i].Start > b }) - 1
	if i >= 0 && b <= d.blocks[i].End {
		return i, i
	}
	return -1, i
}

// ByteToU16 把字节偏移换算为 UTF-16 偏移。
func (d *Document) ByteToU16(b int) int {
	src := d.md.Source()
	b = runeStartBefore(src, clamp(b, 0, len(src)))
	i := sort.Search(len(d.blocks), func(i int) bool { return d.blocks[i].Start > b }) - 1
	if i < 0 {
		return u16Len(src[:b])
	}
	return d.blocks[i].U16Start + u16Len(src[d.blocks[i].Start:b])
}

// U16ToByte 把 UTF-16 偏移换算为字节偏移，落在代理对中间时取到完整 rune 之后。
func (d *Document) U16ToByte(u int) int {
	src := d.md.Source()
	u = clamp(u, 0, d.u16Total)
	i := sort.Search(len(d.blocks), func(i int) bool { return d.blocks[i].U16Start > u }) - 1
	if i < 0 {
		return u16ToByte(src, u)
	}
	b := d.blocks[i]
	return b.Start + u16ToByte(src[b.Start:], u-b.U16Start)
}

// firstChildStart 返回解析器块 i 的第一个子块的起点（脚注定义的正文起点）。
func (d *Document) firstChildStart(i int) (int, bool) {
	for c := range d.md.Children(i) {
		return d.md.Block(c).Start, true
	}
	return 0, false
}

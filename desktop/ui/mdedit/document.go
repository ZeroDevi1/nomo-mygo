// Package mdedit 是以 Markdown 原文为唯一事实源的语义编辑器（P0 原型）。
//
// Document 持有原文、顶层块索引、选区与撤销历史，不持有任何视图对象；
// Editor 是绑定到窗口的视图，持有布局缓存，可随时释放并由 Document 重建。
package mdedit

import (
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
	md     *parser.Document
	blocks []Block
	spare  []Block // 编辑时复用的块数组，避免每次按键分配
	// u16Total 是原文的 UTF-16 长度，编辑时增量维护。
	u16Total int
	version  uint64

	Sel Selection
	// marked 是输入法组字区间（字节），hasMarked 为 false 时无组字。
	marked    Selection
	hasMarked bool

	hist history
}

// NewDocument 解析 src 并建立块索引。
func NewDocument(src string) *Document {
	// 现版本支持 $...$ 行内公式，Default 不含单美元公式，需显式加上。
	d := &Document{md: parser.New(parser.Default | parser.ExtDollarMath)}
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

// Parser 返回解析结果，供投影读取行内节点；下次编辑前有效。
func (d *Document) Parser() *parser.Document { return d.md }

// Version 每次原文变化时递增。
func (d *Document) Version() uint64 { return d.version }

// U16Len 返回原文的 UTF-16 长度。
func (d *Document) U16Len() int { return d.u16Total }

// Marked 返回组字区间。
func (d *Document) Marked() (Selection, bool) { return d.marked, d.hasMarked }

// KindFrontMatter 是文档开头 YAML front matter 的合成块类型。解析器不认识
// front matter，会把它拆成分割线、段落或 Setext 标题；collect 把这些块合并成一块。
const KindFrontMatter parser.Kind = 200

func (d *Document) collect(dst []Block) []Block {
	fm := frontMatterEnd(d.md.Source())
	for c := range d.md.Children(0) {
		b := d.md.Block(c)
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
	d.md.Edit(from, to, text)
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
	return Change{First: p, Removed: len(old) - p - s, Inserted: mid - p}
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

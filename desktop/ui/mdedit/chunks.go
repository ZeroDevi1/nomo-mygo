package mdedit

import (
	"iter"
	"slices"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui/markdown/parser"
)

// 分块阈值。经验值：50 MiB 文档整篇解析约 95 ms（约 2 ns/字节），256 KiB 的块重解析约 0.5 ms；小于 chunkMin
// 的文档按键成本本就很低，不分块、不多占内存。调大 chunkSize 会增加每键成本，调小会增加块数（块数只影响
// 编辑后的 O(块数) 平移与按序号定位的二分查找）。
const (
	chunkMin  = 2 << 20
	chunkSize = 256 << 10
)

// 解析结果保留预算。解析树约为原文的 9 倍（mygo v0.6.3 每个 parser.Block 272 字节；10 MiB 综合样本 35.5 万块约
// 92 MiB，其中表格单元格占六成），大文档不能每段都常驻。keep 是保留解析结果的各段原文总长上限：1 MiB 约对应
// 10 MiB 解析树；超出时丢弃最久未访问的段，下次访问时从原文重解析（256 KiB 约 0.5 ms）。至少保留 keepChunks 段，
// 覆盖可见区跨段与编辑时检查前后段切分条件的需要，避免来回重解析。调小 keep 省内存但滚动跨段时重解析更频繁。
const (
	chunkKeep  = 1 << 20
	keepChunks = 4
)

// tree 是分块的 Markdown 解析结果，提供 mdedit 用到的 parser.Document 接口（Block 按值返回）。
//
// 上游解析器每次编辑都扫描全部顶层块、复制编辑点之后的全部块并拼接整篇原文（parser.Document.edit），按键成本随
// 文档体量线性增长，且 v0.6.2 没有增量原文存储。这里在不改上游的前提下，把大文档（≥ chunkMin）在顶层块边界切成
// 约 chunkSize 的若干段，每段一个解析器，一次编辑只重解析所在段。切分点满足 cutSafe：前一段以空行结束且末尾的
// 顶层块不会延续（未闭合的围栏、HTML 块、公式块会延续），后一段首行从第 0 列开始且不是列表标记或缩进；此时
// 分段解析与整篇解析的块结构相同。编辑破坏这个条件时与相邻段合并重解析。链接引用定义与脚注定义跨段生效，
// 文档含有（或编辑中出现）它们时整篇作为一段，行为与上游一致。
//
// 序号：0 为虚拟根；第 k 段的局部序号 j（≥ 1）对应全局序号 base_k + j，各段的 base 连续，全局序号与整篇解析的
// 先序序号一致。偏移：第 k 段的局部偏移加 off_k。
//
// 内存：只有一段时 src 与解析器共享同一字符串。分段时只有最近访问的若干段保留解析结果（见 keep、trim），
// 这些段的解析器持有自己的段文本副本；其余段只记块数，访问时由 doc 重解析。重解析结果与丢弃前相同，块序号不变。
// 并发：只能在 UI 线程使用。
type tree struct {
	ext parser.Extensions
	// min、size 为分段阈值与目标段长（默认 chunkMin、chunkSize，测试中调小）；keep 为解析结果保留预算（字节，
	// 默认 chunkKeep）。
	min, size, keep int
	src             string
	chunks          []chunk
	n               int // 全部块数（含虚拟根），等于整篇解析的 Len()
	// split 表示按分段维护（Set 时足够大且无定义）；为 false 时只有一段且与 src 共享字符串。分段后即使合并成
	// 一段也保持为 true，以便闭合围栏等之后重新切分。
	split bool
	// tick 为访问计数，doc 每次访问递增并记到段的 used 上；reparses 统计重解析次数（测试观察用）。
	tick, reparses uint64
}

// topBlock 是顶层块的摘要：collect 只需要顶层块的序号、类型与区间。
type topBlock struct {
	i, start, end int32
	kind          parser.Kind
}

// chunk 是 tree 的一段。不变量：chunks[0].off == 0，off 严格递增；base_{k+1} == base_k + n_k - 1；md 不为 nil 时
// n == md.Len()。
type chunk struct {
	// md 为本段的解析结果；nil 表示已被 trim 丢弃，须经 tree.doc 访问。未分段时（tree.split 为 false）从不丢弃。
	md   *parser.Document
	off  int
	base int
	// n 是本段解析结果的块数（含段内虚拟根），丢弃 md 后仍保留，供 rebase 与定位使用。
	n int
	// used 是最近一次经 doc 访问时的 tree.tick，trim 据此淘汰最久未用的段。
	used uint64
	// tops 是丢弃 md 时留下的顶层块摘要（局部序号与偏移），供 Tops 在不重解析的情况下遍历；md 不为 nil 时为 nil。
	tops []topBlock
	// tried 是上次拆分失败时的段长（0 为未失败）；段长再增加 size 之前不再尝试，避免每次按键都整段重解析。
	tried int
}

func newTree(ext parser.Extensions) *tree {
	return &tree{ext: ext, min: chunkMin, size: chunkSize, keep: chunkKeep}
}

func (t *tree) Source() string { return t.src }
func (t *tree) Len() int       { return t.n }

// Set 解析整篇原文，文档足够大时分段。
func (t *tree) Set(src string) {
	t.src = src
	t.chunks = t.chunks[:0]
	t.split = len(src) >= t.min && !hasDefinitions(src)
	if !t.split {
		md := parser.New(t.ext)
		md.Set(src)
		t.chunks = append(t.chunks, chunk{md: md, n: md.Len()})
		t.n = md.Len()
		return
	}
	t.chunks = t.cutChunks(t.chunks, src, 0, len(src))
	t.rebase(0)
}

// cutChunks 把 src[a:z) 切成段并解析，追加到 dst；z 须是安全切分点或全文末尾。
func (t *tree) cutChunks(dst []chunk, src string, a, z int) []chunk {
	for a < z {
		b := min(z, nextCut(src, a+t.size))
		md := parser.New(t.ext)
		md.Set(strings.Clone(src[a:b]))
		// 末尾的块会延续到下一段（如段内开了未闭合的围栏）时，延长到下一个切分点重解析。
		for b < z && !endsClosed(md, src[a:b]) {
			b = min(z, nextCut(src, b+1))
			md.Set(strings.Clone(src[a:b]))
		}
		dst = append(dst, chunk{md: md, off: a, n: md.Len()})
		a = b
	}
	return dst
}

// maybeSplit 在第 k 段过长（合并后、如围栏闭合时）且末尾已闭合时重新切分它。整段重解析的代价与上游在同样
// 编辑下重解析其后全文相当。
func (t *tree) maybeSplit(k int) {
	c := t.chunks[k]
	end := len(t.src)
	if k+1 < len(t.chunks) {
		end = t.chunks[k+1].off
	}
	n := end - c.off
	if n <= 4*t.size || c.tried > 0 && n < c.tried+t.size {
		return
	}
	if !endsClosed(t.doc(k), t.src[c.off:end]) && end < len(t.src) {
		return
	}
	pieces := t.cutChunks(nil, t.src, c.off, end)
	if len(pieces) == 1 {
		t.chunks[k].md, t.chunks[k].n, t.chunks[k].tried = pieces[0].md, pieces[0].n, n
		return
	}
	t.chunks = append(t.chunks[:k], append(pieces, t.chunks[k+1:]...)...)
	t.rebase(k)
}

// rebase 从第 k 段起重算 base 与总块数。
func (t *tree) rebase(k int) {
	base := 0
	if k > 0 {
		p := t.chunks[k-1]
		base = p.base + p.n - 1
	}
	for ; k < len(t.chunks); k++ {
		t.chunks[k].base = base
		base += t.chunks[k].n - 1
	}
	t.n = base + 1
}

// end 返回第 k 段在原文中的结束偏移（不含）。
func (t *tree) end(k int) int {
	if k+1 < len(t.chunks) {
		return t.chunks[k+1].off
	}
	return len(t.src)
}

// doc 返回第 k 段的解析结果：已丢弃时从当前原文重解析，再按预算淘汰其他段。返回值在下一次 doc 调用前有效
// （之后可能被淘汰，但仍是可读的旧结果）；调用方不应跨编辑持有。
func (t *tree) doc(k int) *parser.Document {
	c := &t.chunks[k]
	t.tick++
	c.used = t.tick
	if c.md == nil {
		c.md = parser.New(t.ext)
		c.md.Set(strings.Clone(t.src[c.off:t.end(k)]))
		c.tops = nil
		t.reparses++
		t.trim()
	}
	return c.md
}

// trim 在保留解析结果的各段原文总长超过 keep 时，从最久未访问的段起丢弃解析结果，至少保留 keepChunks 段。
// 未分段时不丢弃（唯一的解析器与 src 共享字符串）。段数通常为几十到几百，逐次线性查找足够。
func (t *tree) trim() {
	if !t.split {
		return
	}
	total, live := 0, 0
	for k := range t.chunks {
		if t.chunks[k].md != nil {
			total += t.end(k) - t.chunks[k].off
			live++
		}
	}
	for total > t.keep && live > keepChunks {
		v := -1
		for k := range t.chunks {
			if t.chunks[k].md != nil && (v < 0 || t.chunks[k].used < t.chunks[v].used) {
				v = k
			}
		}
		total -= t.end(v) - t.chunks[v].off
		live--
		c := &t.chunks[v]
		c.tops = c.tops[:0]
		for j := range c.md.Children(0) {
			b := c.md.Block(j)
			c.tops = append(c.tops, topBlock{i: int32(j), start: int32(b.Start), end: int32(b.End), kind: b.Kind})
		}
		c.tops = slices.Clip(c.tops)
		c.md = nil
	}
}

// Tops 依次给出根的子块的全局序号与块（只填 Kind、Start、End，偏移为全局值）；已丢弃解析结果的段用摘要，
// 不触发重解析，供整篇收集顶层块使用。
func (t *tree) Tops() iter.Seq2[int, parser.Block] {
	return func(yield func(int, parser.Block) bool) {
		for k := range t.chunks {
			c := &t.chunks[k]
			if c.md == nil {
				for _, tb := range c.tops {
					if !yield(int(tb.i)+c.base, parser.Block{Kind: tb.kind, Start: int(tb.start) + c.off, End: int(tb.end) + c.off}) {
						return
					}
				}
				continue
			}
			for j := range c.md.Children(0) {
				b := c.md.Block(j)
				if !yield(j+c.base, parser.Block{Kind: b.Kind, Start: b.Start + c.off, End: b.End + c.off}) {
					return
				}
			}
		}
	}
}

// chunkOf 返回全局序号 g（≥ 1）所在的段。
func (t *tree) chunkOf(g int) int {
	if len(t.chunks) == 1 {
		return 0
	}
	return sort.Search(len(t.chunks), func(k int) bool { return t.chunks[k].base >= g }) - 1
}

// chunkAt 返回偏移 pos 所在的段（段区间左闭右开，pos 等于段起点时属于该段）。
func (t *tree) chunkAt(pos int) int {
	return sort.Search(len(t.chunks), func(k int) bool { return t.chunks[k].off > pos }) - 1
}

// Block 返回全局序号 g 的块，偏移与 Parent、Next 已换算为全局值；0 为覆盖全文的根。
func (t *tree) Block(g int) parser.Block {
	if g == 0 {
		return parser.Block{Kind: parser.Root, Parent: -1, Next: t.n, End: len(t.src)}
	}
	k := t.chunkOf(g)
	c := &t.chunks[k]
	b := *t.doc(k).Block(g - c.base)
	if k < len(t.chunks)-1 {
		b.Open = false // 整篇中只有位于全文末尾的块才可能未闭合
	}
	if c.base == 0 && c.off == 0 {
		return b
	}
	b.Start += c.off
	b.End += c.off
	b.Next += c.base
	if b.Parent > 0 {
		b.Parent += c.base
	}
	return b
}

// next 返回 Block(g).Next，不复制块。
func (t *tree) next(g int) int {
	if g == 0 {
		return t.n
	}
	k := t.chunkOf(g)
	return t.doc(k).Block(g-t.chunks[k].base).Next + t.chunks[k].base
}

// Children 返回块 i 的子块的全局序号；根的子块依次跨过各段。
func (t *tree) Children(i int) iter.Seq[int] {
	return func(yield func(int) bool) {
		end := t.next(i)
		for j := i + 1; j < end; j = t.next(j) {
			if !yield(j) {
				return
			}
		}
	}
}

// Inlines 返回块 g 的行内节点；不在首段时返回偏移已换算的副本（调用方不得修改）。
func (t *tree) Inlines(g int) []parser.Inline {
	k := t.chunkOf(g)
	c := &t.chunks[k]
	ins := t.doc(k).Inlines(g - c.base)
	if c.off == 0 || len(ins) == 0 {
		return ins
	}
	out := make([]parser.Inline, len(ins))
	for i, in := range ins {
		in.Start += c.off
		in.End += c.off
		out[i] = in
	}
	return out
}

// Edit 把原文 [from, to) 替换为 text：跨段的编辑先合并所涉各段；编辑后检查所在段与前后段的切分条件，
// 不再满足时与相邻段合并重解析；编辑引入链接引用或脚注定义时合并为一段。
func (t *tree) Edit(from, to int, text string) {
	if !t.split {
		md := t.chunks[0].md
		md.Edit(from, to, text)
		t.src = md.Source()
		t.n = md.Len()
		t.chunks[0].n = t.n
		return // 单段文档变大（如粘贴）后不在编辑中分段，重新打开时分段
	}
	k0, k1 := t.chunkAt(from), t.chunkAt(max(from, to-1))
	if k1 > k0 {
		t.merge(k0, k1)
	}
	k := k0
	md := t.doc(k)
	c := &t.chunks[k]
	old := c.n
	md.Edit(from-c.off, to-c.off, text)
	c.n = md.Len()
	delta := len(text) - (to - from)
	t.src = t.src[:from] + text + t.src[to:]
	for j := k + 1; j < len(t.chunks); j++ {
		t.chunks[j].off += delta
	}
	if d := c.n - old; d != 0 {
		for j := k + 1; j < len(t.chunks); j++ {
			t.chunks[j].base += d
		}
		t.n += d
	}
	if hasDefinitions(t.src[lineStart(t.src, from):lineEnd(t.src, from+len(text))]) {
		t.merge(0, len(t.chunks)-1)
		t.chunks[0].md.Set(t.src) // 与 src 共享字符串
		t.chunks[0].n = t.chunks[0].md.Len()
		t.split = false
		return
	}
	// 本段末尾与下一段的切分条件；本段首行（编辑可能改了它）与上一段的切分条件。
	for k+1 < len(t.chunks) && !t.cutOK(k+1) {
		t.merge(k, k+1)
	}
	for k > 0 && !t.cutOK(k) {
		t.merge(k-1, k)
		k--
	}
	t.maybeSplit(k)
}

// cutOK 报告第 k 段（k ≥ 1）的起点是否仍是安全的切分点。
func (t *tree) cutOK(k int) bool {
	off := t.chunks[k].off
	return cutSafe(t.src, off) && endsClosed(t.doc(k-1), t.src[t.chunks[k-1].off:off])
}

// merge 把第 k0..k1 段合并为一段并重解析。
func (t *tree) merge(k0, k1 int) {
	if k0 >= k1 {
		return
	}
	c := &t.chunks[k0]
	if c.md == nil {
		c.md, c.tops = parser.New(t.ext), nil
	}
	c.md.Set(strings.Clone(t.src[c.off:t.end(k1)]))
	c.n, c.tried = c.md.Len(), 0
	t.chunks = append(t.chunks[:k0+1], t.chunks[k1+1:]...)
	t.rebase(k0)
}

// nextCut 返回不早于 from 的第一个安全切分点；没有时返回 len(src)。
func nextCut(src string, from int) int {
	for from < len(src) {
		i := strings.Index(src[from:], "\n\n")
		if i < 0 {
			return len(src)
		}
		p := from + i + 2
		for p < len(src) && src[p] == '\n' {
			p++
		}
		if p < len(src) && cutSafe(src, p) {
			return p
		}
		from = p
	}
	return len(src)
}

// cutSafe 报告偏移 p 处能否开始新的一段：前面是空行（段落、表格、HTML 块 6/7 等都在空行处结束），本行从
// 第 0 列开始且不是列表标记、缩进或序号（这些可能续接前面的列表）。
func cutSafe(src string, p int) bool {
	if p <= 1 || p >= len(src) || src[p-1] != '\n' || src[p-2] != '\n' {
		return false
	}
	switch c := src[p]; {
	case c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '-' || c == '*' || c == '+' || c >= '0' && c <= '9':
		return false
	}
	return true
}

// endsClosed 报告一段的末尾不会延续到后文：以空行结束，且最后一个顶层块没有未闭合的围栏、HTML 块或公式块。
// 列表与缩进代码块在末尾总是 Open，但下一段首行满足 cutSafe 时它们必然结束，视为闭合。
func endsClosed(md *parser.Document, text string) bool {
	if !strings.HasSuffix(text, "\n\n") {
		return false
	}
	last := 0
	for c := range md.Children(0) {
		last = c
	}
	if last == 0 {
		return true
	}
	b := md.Block(last)
	return !b.Open || b.Kind == parser.List || b.Kind == parser.CodeBlock && !b.Fenced
}

// hasDefinitions 报告 s 中是否有形如“[label]:”的行（链接引用定义或脚注定义，最多 3 个空格缩进）。代码块中的
// 同样写法也算，只会让文档不分段。
func hasDefinitions(s string) bool {
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && j-i < 3 && s[j] == ' ' {
			j++
		}
		end := strings.IndexByte(s[j:], '\n')
		if end < 0 {
			end = len(s) - j
		}
		line := s[j : j+end]
		if strings.HasPrefix(line, "[") {
			if k := strings.Index(line, "]:"); k > 1 {
				return true
			}
		}
		i = j + end + 1
	}
	return false
}

func lineEnd(s string, p int) int {
	if k := strings.IndexByte(s[p:], '\n'); k >= 0 {
		return p + k
	}
	return len(s)
}

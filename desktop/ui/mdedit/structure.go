package mdedit

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui/markdown/parser"
)

// 结构编辑：列表、引用、表格的回车、退格、Tab 与任务勾选。全部实现为对原文的
// 区间替换（计划 D1：语义命令即原文事务），每个命令一步撤销。

// lineStart 返回 pos 所在行的行首偏移。
func lineStart(src string, pos int) int {
	return strings.LastIndexByte(src[:pos], '\n') + 1
}

// itemPrefix 是一行行首的容器前缀拆解：lead 为外层容器部分（引用的“> ”、缩进），
// marker 为本行的列表标记（“- ”“3. ”，含任务框“[ ] ”），无标记时为空。
type itemPrefix struct {
	lead, marker string
	ordered      bool
	number       int
	delim        byte
	task         bool
}

// parsePrefix 拆解行首前缀 p（行首到行内容起点之间的原文）。
func parsePrefix(p string) itemPrefix {
	rest := p
	task := false
	for _, t := range []string{"[ ] ", "[x] ", "[X] "} {
		if strings.HasSuffix(rest, t) {
			rest, task = strings.TrimSuffix(rest, t), true
			break
		}
	}
	body := strings.TrimRight(rest, " ")
	if body == "" {
		return itemPrefix{lead: p}
	}
	switch c := body[len(body)-1]; {
	case c == '-' || c == '*' || c == '+':
		lead := body[:len(body)-1]
		if lead == "" || strings.TrimLeft(lead, " >") == "" {
			return itemPrefix{lead: lead, marker: p[len(lead):], task: task}
		}
	case c == '.' || c == ')':
		k := len(body) - 1
		for k > 0 && body[k-1] >= '0' && body[k-1] <= '9' {
			k--
		}
		if n, err := strconv.Atoi(body[k : len(body)-1]); err == nil && k < len(body)-1 && strings.TrimLeft(body[:k], " >") == "" {
			return itemPrefix{lead: body[:k], marker: p[k:], ordered: true, number: n, delim: c, task: task}
		}
	}
	return itemPrefix{lead: p}
}

// next 返回同级下一项的标记：有序序号加一，任务框重置为未完成。
func (m itemPrefix) next() string {
	var b strings.Builder
	if m.ordered {
		b.WriteString(strconv.Itoa(m.number + 1))
		b.WriteByte(m.delim)
		b.WriteByte(' ')
	} else {
		b.WriteString(strings.TrimRight(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(m.marker, "[ ] "), "[x] "), "[X] "), " "))
		b.WriteByte(' ')
	}
	if m.task {
		b.WriteString("[ ] ")
	}
	return b.String()
}

// continuation 返回续行前缀：列表标记换成等宽空格，引用的“>”保留。
func (m itemPrefix) continuation() string {
	return m.lead + strings.Repeat(" ", len(m.marker))
}

// rowPrefix 返回行 s 所在原文行的行首前缀。
func (e *Editor) rowPrefix(s seg) string {
	src := e.Doc.Source()
	return src[lineStart(src, s.base):s.base]
}

// enterInRow 处理容器内的回车，返回是否已处理。
//   - 列表项：新建同级项；空项回车退出列表（删除标记）。
//   - 引用、提示块：在容器内新起段落；空段落回车退出容器。
//   - 表格单元格：不插入换行（单元格不能跨行）。
//   - 容器内的代码行：换行并保留容器前缀与本行缩进。
func (e *Editor) enterInRow(pos int, soft bool) bool {
	s, ok := e.segAt(pos)
	if !ok {
		return false
	}
	r := &e.entries[s.i].rows[s.k]
	if r.ncol > 0 {
		return true
	}
	prefix := e.rowPrefix(s)
	if prefix == "" && r.kind == rowText {
		return false
	}
	src := e.Doc.Source()
	m := parsePrefix(prefix)
	if r.kind == rowLine && !r.empty {
		indent := src[s.base:s.end]
		indent = indent[:len(indent)-len(strings.TrimLeft(indent, " \t"))]
		e.replaceSel("\n"+m.continuation()+indent, editOther)
		return true
	}
	if soft {
		e.replaceSel("\n"+m.continuation(), editOther)
		return true
	}
	empty := r.empty || strings.TrimSpace(src[s.base:s.end]) == ""
	ls := lineStart(src, s.base)
	switch {
	case empty && m.marker != "":
		// 空项：去掉标记，结束列表。
		e.applyEdit(e.Doc.Replace(ls+len(m.lead), s.end, "", editOther))
	case empty && strings.Contains(prefix, ">"):
		// 空段落：去掉一层“>”，退出引用。
		e.applyEdit(e.Doc.Replace(ls, s.end, strings.TrimRight(unquote(prefix), " "), editOther))
	case m.marker != "":
		e.replaceSel("\n"+m.lead+m.next(), editOther)
	case strings.Contains(prefix, ">"):
		e.replaceSel("\n"+strings.TrimRight(prefix, " ")+"\n"+prefix, editOther)
	default:
		e.replaceSel("\n\n"+prefix, editOther)
	}
	return true
}

// unquote 去掉前缀中最内层的一个“>”（及其后一个空格）。
func unquote(p string) string {
	k := strings.LastIndexByte(p, '>')
	if k < 0 {
		return p
	}
	z := k + 1
	if z < len(p) && p[z] == ' ' {
		z++
	}
	return p[:k] + p[z:]
}

// liftRow 处理行首退格：行首有容器前缀时去掉最内层（列表标记、一层“>”或续行缩进），
// 把内容提出容器，返回是否已处理。
func (e *Editor) liftRow(pos int) bool {
	s, ok := e.segAt(pos)
	if !ok || pos != s.base || e.entries[s.i].rows[s.k].ncol > 0 {
		return false
	}
	if r := &e.entries[s.i].rows[s.k]; s.kind != rowText && !r.empty {
		return false
	}
	prefix := e.rowPrefix(s)
	if prefix == "" {
		return false
	}
	ls := lineStart(e.Doc.Source(), s.base)
	m := parsePrefix(prefix)
	var keep string
	switch {
	case m.marker != "":
		keep = m.lead
	case strings.Contains(prefix, ">"):
		keep = unquote(prefix)
	default:
		keep = ""
	}
	e.applyEdit(e.Doc.Replace(ls, s.base, keep, editOther))
	e.Doc.hist.seal()
	return true
}

// tab 处理 Tab / Shift+Tab：表格中在单元格间移动，列表项中缩进或取消缩进。
// 返回是否已处理；未处理时 Tab 按默认行为移动焦点。
func (e *Editor) tab(back bool) bool {
	s, ok := e.segAt(e.Doc.Sel.Head)
	if !ok {
		return false
	}
	en := &e.entries[s.i]
	r := &en.rows[s.k]
	if r.ncol > 0 {
		k := s.k + 1
		if back {
			k = s.k - 1
		}
		if k >= 0 && k < len(en.rows) && en.rows[k].ncol > 0 {
			n := e.segOf(s.i, k)
			e.Doc.SetSelection(Selection{Anchor: n.base, Head: n.end})
			e.reveal, e.dirty = true, true
		}
		return true
	}
	item, ok := e.itemOf(s)
	if !ok {
		return false
	}
	return e.indentItem(item, back)
}

// itemOf 返回行 s 所在的最内层列表项（解析器序号）。
func (e *Editor) itemOf(s seg) (int, bool) {
	r := &e.entries[s.i].rows[s.k]
	if r.kind != rowText {
		return 0, false
	}
	for i := e.Doc.blocks[s.i].Index + r.rel; i > 0; i = e.Doc.md.Block(i).Parent {
		if e.Doc.md.Block(i).Kind == parser.Item {
			return i, true
		}
	}
	return 0, false
}

// contentOffset 返回列表项内容相对其标记起点的列数（“- ”为 2，“10. ”为 4）。
func (e *Editor) contentOffset(item int) int {
	b := e.Doc.md.Block(item)
	for ch := range e.Doc.md.Children(item) {
		return e.Doc.md.Block(ch).Start - b.Start
	}
	return b.End - b.Start + 1
}

// indentItem 缩进（嵌套到上一项之下）或取消缩进列表项的全部行，光标随内容移动。
func (e *Editor) indentItem(item int, back bool) bool {
	md := e.Doc.md
	b := md.Block(item)
	list := md.Block(b.Parent)
	width := 0
	if !back {
		// 嵌套到上一项：缩进到上一项内容的列。第一项没有上一项，不能缩进。
		prev := -1
		for ch := range md.Children(b.Parent) {
			if ch == item {
				break
			}
			prev = ch
		}
		if prev < 0 {
			return true
		}
		width = e.contentOffset(prev)
	} else {
		if list.Parent <= 0 || md.Block(list.Parent).Kind != parser.Item {
			// 顶层列表项：取消缩进即去掉标记。
			if s, ok := e.segAt(e.Doc.Sel.Head); ok {
				e.liftRowAt(s)
			}
			return true
		}
		width = e.contentOffset(list.Parent)
	}
	src := e.Doc.Source()
	start := lineStart(src, b.Start)
	text := src[start:b.End]
	var out strings.Builder
	caret := e.Doc.Sel.Head
	newCaret := caret
	pad := strings.Repeat(" ", width)
	for pos := 0; pos <= len(text); {
		end := strings.IndexByte(text[pos:], '\n')
		if end < 0 {
			end = len(text)
		} else {
			end += pos
		}
		line := text[pos:end]
		abs := start + pos
		if back {
			n := 0
			for n < width && n < len(line) && line[n] == ' ' {
				n++
			}
			line = line[n:]
			if abs+n <= caret {
				newCaret -= n
			} else if abs < caret {
				newCaret -= caret - abs
			}
		} else {
			line = pad + line
			if abs <= caret {
				newCaret += width
			}
		}
		out.WriteString(line)
		if end < len(text) {
			out.WriteByte('\n')
		}
		pos = end + 1
	}
	e.applyEdit(e.Doc.Replace(start, b.End, out.String(), editOther))
	e.Doc.Sel = Selection{Anchor: newCaret, Head: newCaret}
	e.Doc.hist.seal()
	return true
}

func (e *Editor) liftRowAt(s seg) {
	saved := e.Doc.Sel
	e.Doc.Sel = Selection{Anchor: s.base, Head: s.base}
	if !e.liftRow(s.base) {
		e.Doc.Sel = saved
	}
}

// toggleTask 切换任务框：at 为“[ ]”中间字符的绝对偏移。光标保持不动。
func (e *Editor) toggleTask(at int) {
	src := e.Doc.Source()
	if at <= 0 || at >= len(src) || src[at-1] != '[' {
		return
	}
	mark := "x"
	if src[at] == 'x' || src[at] == 'X' {
		mark = " "
	}
	saved := e.Doc.Sel
	e.applyEdit(e.Doc.Replace(at, at+1, mark, editOther))
	e.Doc.Sel = saved
	e.Doc.hist.seal()
	e.reveal = false
}

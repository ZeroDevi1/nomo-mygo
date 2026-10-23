package mdedit

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown/parser"
)

// ---- TextInputClient：坐标为原文的 UTF-16 码元 ----

var _ ui.TextInputClient = (*Editor)(nil)

func (e *Editor) TextForRange(r ui.TextInputRange) (string, ui.TextInputRange) {
	a, z := e.Doc.U16ToByte(r.Start), e.Doc.U16ToByte(r.End)
	if z < a {
		a, z = z, a
	}
	return e.Doc.Source()[a:z], ui.TextInputRange{Start: e.Doc.ByteToU16(a), End: e.Doc.ByteToU16(z)}
}

func (e *Editor) Selection() ui.TextInputSelection {
	ua, uh := e.Doc.ByteToU16(e.Doc.Sel.Anchor), e.Doc.ByteToU16(e.Doc.Sel.Head)
	return ui.TextInputSelection{Range: ui.TextInputRange{Start: min(ua, uh), End: max(ua, uh)}, Reversed: uh < ua}
}

func (e *Editor) MarkedRange() (ui.TextInputRange, bool) {
	m, ok := e.Doc.Marked()
	if !ok {
		return ui.TextInputRange{}, false
	}
	return ui.TextInputRange{Start: e.Doc.ByteToU16(m.Anchor), End: e.Doc.ByteToU16(m.Head)}, true
}

// target 返回替换目标的字节区间：显式区间优先，其次组字区间，最后是选区。
func (e *Editor) target(r *ui.TextInputRange) (int, int) {
	if r != nil {
		a, z := e.Doc.U16ToByte(r.Start), e.Doc.U16ToByte(r.End)
		return min(a, z), max(a, z)
	}
	if m, ok := e.Doc.Marked(); ok {
		return m.Anchor, m.Head
	}
	return e.Doc.Sel.Range()
}

func (e *Editor) ReplaceText(r *ui.TextInputRange, text string) {
	a, z := e.target(r)
	e.applyEdit(e.Doc.CommitMarked(a, z, text))
}

func (e *Editor) SetMarkedText(r *ui.TextInputRange, text string, selected ui.TextInputRange) {
	a, z := e.target(r)
	e.applyEdit(e.Doc.SetMarked(a, z, text, u16ToByte(text, selected.Start), u16ToByte(text, selected.End)))
	e.dirty = true
}

func (e *Editor) UnmarkText() {
	e.Doc.Unmark()
	e.dirty = true
}

func (e *Editor) BoundsForRange(r ui.TextInputRange) (ui.Rect, ui.TextInputRange, bool) {
	e.sync()
	a := e.Doc.U16ToByte(r.Start)
	rc, ok := e.caretRect(a)
	if !ok {
		return ui.Rect{}, r, false
	}
	if z := e.Doc.U16ToByte(r.End); z > a {
		if s, ok := e.segAt(a); ok {
			if v, ok := e.visibleSeg(s.i, s.k); ok {
				rects := s.layout.SelectionRects(ui.TextInputRange{Start: s.toDisp(a), End: s.toDisp(min(z, s.end))})
				if len(rects) > 0 {
					rc = rects[0]
					rc.X += e.left() + s.x
					rc.Y += v.y + s.padTop
				}
			}
		}
	}
	return rc, r, true
}

func (e *Editor) IndexForPoint(pt ui.Point) (int, bool) {
	return e.Doc.ByteToU16(e.hit(pt)), true
}

// ---- 选区移动 ----

func (e *Editor) setHead(pos int, extend bool) {
	s := e.Doc.Sel
	if extend {
		s.Head = pos
	} else {
		s = Selection{Anchor: pos, Head: pos}
	}
	e.Doc.SetSelection(s)
	e.reveal, e.dirty = true, true
}

// entryPos 返回光标从相邻行移入 s 时的落点：折叠投影中的第一个可见字符
// （跳过隐藏的标题前缀与提示块标记），或行末尾。
func (e *Editor) entryPos(s seg, atEnd bool) int {
	if atEnd {
		return s.end
	}
	if s.kind != rowText {
		return s.base
	}
	r := &e.entries[s.i].rows[s.k]
	top := e.Doc.blocks[s.i]
	p := projectLeaf(e.Doc, top.Index+r.rel, s.base, s.end, expansion{}, leafOpts{prefix: r.role == roleHeading, callout: r.callout, rawCode: e.opts.RawInlineCode})
	return s.base + p.toSrc(0, false)
}

// moveH 返回水平移动一个字素（word 为词、line 为视觉行边界）后的位置。
func (e *Editor) moveH(pos, dir int, word, line bool) int {
	src := e.Doc.Source()
	if line {
		return e.lineEdge(pos, dir)
	}
	if word {
		return wordEdge(src, pos, dir)
	}
	s, ok := e.segAt(pos)
	if !ok {
		_, after := e.Doc.BlockAt(pos)
		if dir < 0 {
			if after >= 0 {
				return e.Doc.blocks[after].End
			}
			return 0
		}
		if n := e.visibleNeighbor(after, 1); n >= 0 {
			return e.entryPos(e.firstSeg(n), false)
		}
		return len(src)
	}
	d := s.toDisp(pos)
	if (dir < 0 && d == 0) || (dir > 0 && d >= s.dispLen()) {
		if n, ok := e.neighbor(s, dir); ok {
			return e.entryPos(n, dir < 0)
		}
		return pos
	}
	if dir < 0 {
		return s.toSrc(s.layout.PreviousBoundary(d), false)
	}
	return s.toSrc(s.layout.NextBoundary(d), true)
}

// lineEdge 返回光标所在视觉行的行首（dir<0）或行尾。
func (e *Editor) lineEdge(pos, dir int) int {
	s, ok := e.segAt(pos)
	if !ok {
		return pos
	}
	r := e.caretIn(s, pos)
	x := float32(0)
	if dir > 0 {
		x = 1e6
	}
	return s.toSrc(s.layout.IndexAt(ui.Point{X: x, Y: r.Y + r.H/2}), dir > 0)
}

// moveV 返回上下移动一行后的位置，保持目标横坐标 goalX（内容区坐标）。
// 表格中移到上下一行的同列单元格。
func (e *Editor) moveV(pos, dir int) int {
	var target seg
	s, ok := e.segAt(pos)
	if ok {
		r := e.caretIn(s, pos)
		if !e.hasGoal {
			e.goalX, e.hasGoal = s.x+r.X, true
		}
		if y, ok := s.layout.adjacentY(r, dir); ok {
			return s.toSrc(s.layout.IndexAt(ui.Point{X: e.goalX - s.x, Y: y}), false)
		}
		if target, ok = e.neighborV(s, dir, e.goalX); !ok {
			if dir < 0 {
				return 0
			}
			return len(e.Doc.Source())
		}
	} else {
		// 间隙光标：上移到前一块末行，下移到后一块首行。
		_, after := e.Doc.BlockAt(pos)
		t := e.visibleNeighbor(after, 1)
		if dir < 0 {
			t = after
			if t >= 0 && e.detailsHidden(t) {
				t = e.visibleNeighbor(t, -1)
			}
		}
		if t < 0 {
			if dir < 0 {
				return 0
			}
			return len(e.Doc.Source())
		}
		if dir < 0 {
			target = e.lastSeg(t)
		} else {
			target = e.firstSeg(t)
		}
	}
	x := target.x
	if e.hasGoal {
		x = e.goalX
	}
	ty := float32(1)
	if dir < 0 {
		_, lh := target.layout.Size()
		ty = max(1, lh-1)
	}
	return target.toSrc(target.layout.IndexAt(ui.Point{X: x - target.x, Y: ty}), false)
}

// runeClass 是选词用的字符类别：0 空白，1 字母数字，2 标点符号，3 表意字符。
func runeClass(r rune) int {
	switch {
	case unicode.IsSpace(r):
		return 0
	case unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r):
		return 3
	case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
		return 1
	}
	return 2
}

// wordAt 返回双击 pos 时选中的区间：取 pos 之后（行尾时取之前）的字符类别，
// 在 [lo, hi) 内向两侧扩展同类字符。不跳过空白，因此不会越过换行选到相邻块；
// 连续表意字符整段选中（无分词词典，P0 不做中文分词）。
func wordAt(src string, pos, lo, hi int) (int, int) {
	pos = clamp(pos, lo, hi)
	r, _ := utf8.DecodeRuneInString(src[pos:hi])
	if pos == hi || runeClass(r) == 0 && pos > lo {
		if pr, _ := utf8.DecodeLastRuneInString(src[lo:pos]); pos > lo && runeClass(pr) != 0 {
			r = pr
		}
	}
	c := runeClass(r)
	a, z := pos, pos
	for a > lo {
		pr, n := utf8.DecodeLastRuneInString(src[lo:a])
		if runeClass(pr) != c {
			break
		}
		a -= n
	}
	for z < hi {
		nr, n := utf8.DecodeRuneInString(src[z:hi])
		if runeClass(nr) != c {
			break
		}
		z += n
	}
	return a, z
}

// wordEdge 返回 pos 向 dir 方向的词边界：先跳过空白，再跳过同类字符；
// 汉字等表意字符每字为一词。
func wordEdge(src string, pos, dir int) int {
	step := func(p int) (rune, int) {
		if dir < 0 {
			r, n := utf8.DecodeLastRuneInString(src[:p])
			return r, p - n
		}
		r, n := utf8.DecodeRuneInString(src[p:])
		return r, p + n
	}
	atEnd := func(p int) bool { return dir < 0 && p == 0 || dir > 0 && p >= len(src) }
	for !atEnd(pos) {
		r, next := step(pos)
		if runeClass(r) != 0 {
			break
		}
		pos = next
	}
	if atEnd(pos) {
		return pos
	}
	r, next := step(pos)
	c := runeClass(r)
	pos = next
	if c == 3 {
		return pos
	}
	for !atEnd(pos) {
		r, next := step(pos)
		if runeClass(r) != c {
			break
		}
		pos = next
	}
	return pos
}

// ---- 编辑命令 ----

func (e *Editor) replaceSel(text string, kind editKind) {
	a, z := e.Doc.Sel.Range()
	e.applyEdit(e.Doc.Replace(a, z, text, kind))
}

// deleteDir 删除选区；无选区时删除光标前（dir<0）或后的一个字素、词或到行边界。
// 在块首向后删除、块尾向前删除时删去块间隙，与相邻块合并。
func (e *Editor) deleteDir(dir int, word, line bool) {
	if !e.Doc.Sel.Empty() {
		e.replaceSel("", editDelete)
		return
	}
	pos := e.Doc.Sel.Head
	if dir < 0 && !word && !line && e.liftRow(pos) {
		return
	}
	src := e.Doc.Source()
	bi, after := e.Doc.BlockAt(pos)
	var a, z int
	switch {
	case bi < 0 && dir < 0:
		a, z = 0, pos
		if after >= 0 {
			a = e.Doc.blocks[after].End
		}
	case bi < 0:
		a, z = pos, len(src)
		if after+1 < len(e.Doc.blocks) {
			z = e.Doc.blocks[after+1].Start
		}
	case dir < 0 && pos == e.Doc.blocks[bi].Start:
		a, z = 0, pos
		if bi > 0 {
			a = e.Doc.blocks[bi-1].End
		}
	case dir > 0 && pos == e.Doc.blocks[bi].End:
		a, z = pos, len(src)
		if bi+1 < len(e.Doc.blocks) {
			z = e.Doc.blocks[bi+1].Start
		}
	default:
		t := e.moveH(pos, dir, word, line)
		a, z = min(pos, t), max(pos, t)
	}
	if a == z {
		return
	}
	e.applyEdit(e.Doc.Replace(a, z, "", editDelete))
}

// enter 在段落与标题中分段（插入空行），在列表、引用中延续结构（见 enterInRow），
// 在其他块中插入换行；soft 为 Shift+Enter，插入带续行前缀的换行。
func (e *Editor) enter(soft bool) {
	pos := e.Doc.Sel.Head
	bi, _ := e.Doc.BlockAt(pos)
	switch {
	case bi < 0:
		// 间隙中的空段落：Markdown 无对应语法，保持原文不变。
	case e.enterInRow(pos, soft):
	case soft:
		e.replaceSel("\n", editOther)
	case e.Doc.blocks[bi].Kind == parser.Paragraph || e.Doc.blocks[bi].Kind == parser.Heading:
		e.replaceSel("\n\n", editOther)
	default:
		e.replaceSel("\n", editOther)
	}
	e.Doc.hist.seal()
}

// toggleWrap 用 marker 包裹选区；选区两侧已有 marker 时去掉。选区跨多个块时
// 分别包裹各块内的部分（行内标记不能跨块），整体作为一步编辑与撤销。
func (e *Editor) toggleWrap(marker string) {
	src := e.Doc.Source()
	a, z := e.Doc.Sel.Range()
	m := len(marker)
	if parts := e.rowParts(a, z); len(parts) > 1 {
		var b strings.Builder
		pos := a
		for _, p := range parts {
			b.WriteString(src[pos:p[0]])
			b.WriteString(marker)
			b.WriteString(src[p[0]:p[1]])
			b.WriteString(marker)
			pos = p[1]
		}
		b.WriteString(src[pos:z])
		text := b.String()
		e.applyEdit(e.Doc.Replace(a, z, text, editOther))
		e.Doc.Sel = Selection{Anchor: a, Head: a + len(text)}
		e.Doc.hist.seal()
		return
	}
	if a >= m && z+m <= len(src) && src[a-m:a] == marker && src[z:z+m] == marker {
		inner := cloneString(src[a:z])
		e.applyEdit(e.Doc.Replace(a-m, z+m, inner, editOther))
		e.Doc.Sel = Selection{Anchor: a - m, Head: z - m}
	} else {
		e.applyEdit(e.Doc.Replace(a, z, marker+src[a:z]+marker, editOther))
		e.Doc.Sel = Selection{Anchor: a + m, Head: z + m}
	}
	e.Doc.hist.seal()
}

// rowParts 返回 [a, z) 与各文本行相交的非空字节区间（按文档顺序），不含块间隙与
// 隐藏的容器标记。
func (e *Editor) rowParts(a, z int) [][2]int {
	var parts [][2]int
	for i, b := range e.Doc.blocks {
		if b.Start >= z {
			break
		}
		if b.End <= a {
			continue
		}
		for _, r := range e.ensure(i).rows {
			if r.kind != rowText {
				continue
			}
			if pa, pz := max(a, b.Start+r.a), min(z, b.Start+r.b); pa < pz {
				parts = append(parts, [2]int{pa, pz})
			}
		}
	}
	return parts
}

func (e *Editor) copySel(cut bool) {
	a, z := e.Doc.Sel.Range()
	if a == z || !e.hasSvc {
		return
	}
	text := e.Doc.Source()[a:z]
	if e.opts.PlainCopy {
		text = plainText(text)
	}
	e.svc.WriteClipboard(text)
	if cut {
		e.replaceSel("", editOther)
	}
}

// undo 撤销或重做一步；相连的步骤（history.linkLast）一并处理：撤销时刚撤销的一步标记为相连则继续，重做时下一步
// 标记为相连则继续。
func (e *Editor) undo(redo bool) {
	h := &e.Doc.hist
	for {
		var ed Edit
		var ok bool
		if redo {
			ed, ok = e.Doc.Redo()
		} else {
			ed, ok = e.Doc.Undo()
		}
		if !ok {
			return
		}
		e.applyEdit(ed)
		if redo && (len(h.redo) == 0 || !h.redo[len(h.redo)-1].linked) || !redo && !h.redo[len(h.redo)-1].linked {
			return
		}
	}
}

func (e *Editor) command(name string) bool {
	switch name {
	case "copy":
		e.copySel(false)
	case "cut":
		e.copySel(true)
	case "paste", "pasteAndMatchStyle":
		if e.hasSvc {
			if e.PasteData != nil && slices.ContainsFunc(e.svc.ClipboardFormats(), func(f transfer.Format) bool {
				return f == transfer.PNG || f == transfer.FileList
			}) && e.PasteData(e.svc.ReadClipboardData(transfer.FileList, transfer.PNG)) {
				break
			}
			if s := e.svc.ReadClipboard(); s != "" {
				e.replaceSel(s, editOther)
				e.Doc.hist.seal()
			}
		}
	case "selectAll":
		e.Doc.SetSelection(Selection{Anchor: 0, Head: len(e.Doc.Source())})
		e.dirty = true
	case "delete":
		e.replaceSel("", editOther)
	case "undo":
		e.undo(false)
	case "redo":
		e.undo(true)
	case "bold":
		e.toggleWrap("**")
	case "italic":
		e.toggleWrap("*")
	case "strikethrough":
		e.toggleWrap("~~")
	default:
		return false
	}
	return true
}

func (e *Editor) key(ev ui.InputEvent) bool {
	shift := ev.Mods&ui.Shift != 0
	cmd := ev.Mods&ui.Cmd != 0
	alt := ev.Mods&ui.Alt != 0
	sel := e.Doc.Sel
	keepGoal := false
	switch ev.Key {
	case ui.KeyLeft, ui.KeyRight:
		dir := 1
		if ev.Key == ui.KeyLeft {
			dir = -1
		}
		if !shift && !sel.Empty() {
			a, z := sel.Range()
			if dir < 0 {
				e.setHead(a, false)
			} else {
				e.setHead(z, false)
			}
			break
		}
		e.setHead(e.moveH(sel.Head, dir, alt, cmd), shift)
	case ui.KeyUp, ui.KeyDown:
		dir := 1
		if ev.Key == ui.KeyUp {
			dir = -1
		}
		if cmd {
			if dir < 0 {
				e.setHead(0, shift)
			} else {
				e.setHead(len(e.Doc.Source()), shift)
			}
			break
		}
		keepGoal = true
		e.setHead(e.moveV(sel.Head, dir), shift)
	case ui.KeyHome, ui.KeyEnd:
		dir := 1
		if ev.Key == ui.KeyHome {
			dir = -1
		}
		e.setHead(e.lineEdge(sel.Head, dir), shift)
	case ui.KeyPageUp, ui.KeyPageDown:
		dy := e.viewH * 0.9
		if ev.Key == ui.KeyPageUp {
			dy = -dy
		}
		e.scrollBy(dy)
		if e.caretOK {
			c := e.caret
			e.setHead(e.hit(ui.Point{X: c.X, Y: c.Y + c.H/2}), shift)
		}
	case ui.KeyBackspace, ui.KeyDelete:
		dir := -1
		if ev.Key == ui.KeyDelete {
			dir = 1
		}
		e.deleteDir(dir, alt, cmd)
	case ui.KeyEnter:
		e.enter(shift)
	case ui.KeyTab:
		if cmd || alt || !e.tab(shift) {
			return false
		}
	case ui.KeyZ:
		if !cmd {
			return false
		}
		e.undo(shift)
	case ui.KeyY:
		if ev.Mods != ui.Ctrl {
			return false
		}
		e.undo(true)
	case ui.KeyA, ui.KeyB, ui.KeyI, ui.KeyC, ui.KeyX, ui.KeyV:
		if !cmd || shift || alt {
			return false
		}
		return e.command(map[ui.Key]string{ui.KeyA: "selectAll", ui.KeyB: "bold", ui.KeyI: "italic", ui.KeyC: "copy", ui.KeyX: "cut", ui.KeyV: "paste"}[ev.Key])
	default:
		return false
	}
	if !keepGoal {
		e.hasGoal = false
	}
	e.dirty = true
	return true
}

func (e *Editor) input(ev ui.InputEvent) bool {
	if e.Intercept != nil && ev.Kind == ui.InputKeyDown && e.Intercept(ev) {
		return true
	}
	switch ev.Kind {
	case ui.InputKeyDown:
		return e.key(ev)
	case ui.InputCommand:
		return e.command(ev.Text)
	case ui.InputScroll:
		// 按住 Ctrl 滚动交给外壳缩放（原版 ctrlWheelZoomEnabled），不滚动文档。
		if ev.Mods&ui.Ctrl != 0 && e.CtrlWheel != nil && e.CtrlWheel(ev.DY) {
			return true
		}
		// 横向滚轮（或 Shift+滚轮）在超宽表格上横向滚动表格，其余纵向滚动文档。
		dx := ev.DX
		if ev.Mods&ui.Shift != 0 && dx == 0 {
			dx = ev.DY
		}
		if dx != 0 && abs32(dx) >= abs32(ev.DY) || ev.Mods&ui.Shift != 0 {
			e.sync()
			if en, f := e.tableAt(ui.Point{X: ev.X, Y: ev.Y}); f != nil {
				e.scrollTable(en, f, dx)
				return true
			}
		}
		e.scrollBy(ev.DY)
		return true
	case ui.InputPointerDown:
		if ev.Button == 1 {
			// 右键：记下位置供 ContextTarget 判断目标；点在选区外时光标移到该处（与浏览器一致），菜单由外壳弹出。
			e.sync()
			pos := e.hit(ui.Point{X: ev.X, Y: ev.Y})
			e.ctxPos, e.ctxDetails = pos, -1
			if f, ok := e.detailsHeaderHit(ui.Point{X: ev.X, Y: ev.Y}); ok {
				e.ctxDetails = f.key
			}
			if a, z := e.Doc.Sel.Range(); pos < a || pos > z || a == z {
				e.setHead(pos, false)
			}
			return false
		}
		if ev.Button != 0 {
			return false
		}
		e.sync()
		if ev.X >= e.viewW-scrollbarHit {
			total, pos := e.scrollMetrics()
			if total > e.viewH {
				e.drag, e.dragY, e.dragPos = dragScrollbar, ev.Y, pos
				return true
			}
		}
		if at, ok := e.taskHit(ui.Point{X: ev.X, Y: ev.Y}); ok {
			e.toggleTask(at)
			return true
		}
		if f, ok := e.detailsToggleHit(ui.Point{X: ev.X, Y: ev.Y}); ok && ev.Mods&ui.Shift == 0 { // 连点每次都切换
			e.toggleDetails(f)
			return true
		}
		if at, ok := e.imageHit(ui.Point{X: ev.X, Y: ev.Y}); ok && ev.Clicks == 1 && ev.Mods&ui.Shift == 0 {
			if at < 0 { // 目录条目：跳到标题（imageHit 以 -1-pos 表示）
				e.JumpTo(-1 - at)
				return true
			}
			e.setHead(at, false)
			e.hasGoal = false
			return true
		}
		if def, ok := e.footnoteHit(ui.Point{X: ev.X, Y: ev.Y}); ok && ev.Clicks == 1 && ev.Mods&ui.Shift == 0 {
			e.JumpTo(def)
			return true
		}
		pos := e.hit(ui.Point{X: ev.X, Y: ev.Y})
		switch ev.Clicks {
		case 2:
			if s, ok := e.segAt(pos); ok {
				a, z := wordAt(e.Doc.Source(), pos, s.base, s.end)
				e.Doc.SetSelection(Selection{Anchor: a, Head: z})
			} else {
				e.Doc.SetSelection(Selection{Anchor: pos, Head: pos})
			}
			e.dirty = true
		case 3:
			if s, ok := e.segAt(pos); ok {
				e.Doc.SetSelection(Selection{Anchor: s.base, Head: s.end})
				e.dirty = true
			}
		default:
			e.setHead(pos, ev.Mods&ui.Shift != 0)
		}
		e.drag = dragText
		e.hasGoal = false
		return true
	case ui.InputPointerMove:
		switch {
		case e.drag == dragScrollbar && ev.Button == 0:
			total, _ := e.scrollMetrics()
			e.scrollTo(e.dragPos + (ev.Y-e.dragY)*total/max(1, e.viewH))
			return true
		case e.drag == dragText && ev.Button == 0:
			e.setHead(e.hit(ui.Point{X: ev.X, Y: ev.Y}), true)
			return true
		}
	case ui.InputPointerUp:
		if e.drag != dragNone {
			e.drag = dragNone
			return true
		}
	}
	return false
}

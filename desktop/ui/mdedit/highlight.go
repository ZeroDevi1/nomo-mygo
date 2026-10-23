package mdedit

import (
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// 代码块语法高亮：逐行调用 MyGo editor 的 Tokenizer，配色取 editor.DefaultTheme（与源码模式一致）。
// Tokenizer 的跨行状态（块注释、多行字符串）缓存在行上（row.hlIn/hlOut）：frame.hlUpTo 之前的
// 行状态已按当前原文求出。编辑后行按哈希继承旧状态，入口状态相同时直接复用 hlOut，不重新切分，
// 所以在长代码块中键入只重新切分改动行及其状态确实变化的后续行。

// codeTokenizer 返回代码块信息串语言的 Tokenizer；语言未知或该语言只有整篇解析器时返回 nil（不高亮）。
func codeTokenizer(lang string) editor.Tokenizer {
	if l := editor.LanguageNamed(lang); l != nil {
		return l.Tokenizer
	}
	return nil
}

// codeFrame 返回块内第 k 行所在的可高亮代码块装饰，没有时返回 nil。
func codeFrame(en *entry, k int) *frame {
	for f := range en.frames {
		fr := &en.frames[f]
		if fr.kind == frameCode && fr.first <= k && k <= fr.last {
			if fr.tok == nil {
				return nil
			}
			return fr
		}
	}
	return nil
}

// codeState 返回代码块 fr 中第 k 行开头的 Tokenizer 状态，并把 fr.hlUpTo 推进到 k。
// 围栏行不参与切分，状态原样传递。
func (e *Editor) codeState(i int, en *entry, fr *frame, k int) uint64 {
	src := e.Doc.Source()
	start := e.Doc.blocks[i].Start
	for j := max(fr.hlUpTo, fr.first); j < k; j++ {
		r := &en.rows[j]
		in := uint64(0)
		if j > fr.first {
			in = en.rows[j-1].hlOut
		}
		if r.hlKnown && r.hlIn == in {
			continue
		}
		out := in
		if !r.fence {
			e.tokBuf, out = fr.tok.Tokenize(src[start+r.a:start+r.b], in, e.tokBuf[:0])
		}
		r.hlIn, r.hlOut, r.hlKnown = in, out, true
	}
	fr.hlUpTo = max(fr.hlUpTo, k)
	if k == fr.first {
		return 0
	}
	return en.rows[k-1].hlOut
}

// codeSpans 把一行代码按 Token 切成着色片段，相邻同样式的 Token 合并；正文 Token 不设颜色，用行默认色。
// 字体由行字体（等宽）决定。
func (e *Editor) codeSpans(text string, toks []editor.Token) []ui.Span {
	spans := make([]ui.Span, 0, len(toks)+1)
	var cur editor.TokenStyle
	at, end := 0, 0 // [at, end) 为待写出、样式为 cur 的区间
	flush := func() {
		if end > at {
			sp := ui.Span{Text: text[at:end], Color: cur.Color, Italic: cur.Italic}
			if cur.Bold {
				sp.Weight = 700
			}
			spans = append(spans, sp)
		}
		at = end
	}
	for _, t := range toks {
		z := min(t.End, len(text))
		if z <= end {
			continue
		}
		var st editor.TokenStyle
		if e.syntax != nil {
			st = e.syntax.Tokens[t.Kind]
		}
		if st != cur {
			flush()
			cur = st
		}
		end = z
	}
	flush()
	if at < len(text) {
		spans = append(spans, ui.Span{Text: text[at:]})
	}
	return spans
}

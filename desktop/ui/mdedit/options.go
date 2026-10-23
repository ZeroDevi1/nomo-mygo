package mdedit

import (
	"html"
	"regexp"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown/parser"
)

// Options 是外壳设置中影响语义编辑区的项（原版 editor.updateOptions 与插入命令读取的偏好）。零值即原版默认，
// 外壳每帧调用 SetOptions，值未变时不做任何事。
type Options struct {
	CodeLanguage    string // 插入代码块的默认语言（defaultCodeBlockLanguage），空为不标语言
	DiagramCode     string // 插入 Mermaid 图表的模板（defaultDiagramType 对应的原版 DIAGRAM_TEMPLATES），空为流程图
	CodeIndent      string // 代码块中 Tab 插入的缩进（codeBlockIndent），空为两个空格
	HideLineNumbers bool   // 代码块不显示行号（codeBlockLineNumbersVisible 取反）
	RawInlineCode   bool   // 不渲染行内代码，显示原始反引号文本（inlineCodeRenderingEnabled 取反）
	PlainCopy       bool   // 复制、剪切时纯文本不保留 Markdown 语法（copyMarkdownSyntaxEnabled 取反）
	Typography      bool   // 精细排版（typographyEnabled，原版默认关闭）：段落按 Knuth–Plass 断行（kp.go）
}

// defaultDiagram 是原版 DIAGRAM_TEMPLATES 的流程图模板。
const defaultDiagram = "flowchart TD\n  A[Start] --> B{Continue?}\n  B -- Yes --> C[Process]\n  B -- No --> D[End]\n  C --> D"

// SetOptions 设置编辑区选项；行号与行内代码的显示变化时重新展开已展开的块并作废布局。
func (e *Editor) SetOptions(o Options) {
	if o == e.opts {
		return
	}
	relayout := o.HideLineNumbers != e.opts.HideLineNumbers || o.RawInlineCode != e.opts.RawInlineCode || o.Typography != e.opts.Typography
	e.opts = o
	if !relayout {
		return
	}
	for i := range e.entries {
		if en := &e.entries[i]; en.split {
			e.split(i, en.rows, en.frames)
		}
	}
	e.layoutGen++
	e.dirty = true
}

func (e *Editor) codeIndent() string {
	if e.opts.CodeIndent == "" {
		return "  "
	}
	return e.opts.CodeIndent
}

// codeTab 处理代码块行内的 Tab（原版 CodeBlockNodeView 按 codeBlockIndent 缩进）：Tab 在选区处插入一个缩进单位，
// Shift+Tab 去掉光标所在行开头的一个缩进单位。不在代码行内时返回 false。
func (e *Editor) codeTab(s seg, back bool) bool {
	r := &e.entries[s.i].rows[s.k]
	if r.role != roleCode || r.fence {
		return false
	}
	unit := e.codeIndent()
	if !back {
		e.replaceSel(unit, editOther)
		e.Doc.hist.seal()
		e.reveal, e.dirty = true, true
		return true
	}
	src := e.Doc.Source()
	line := s.base
	n := 0
	switch {
	case strings.HasPrefix(src[line:], "\t"):
		n = 1
	default:
		for n < len(unit) && line+n < len(src) && src[line+n] == ' ' {
			n++
		}
	}
	if n > 0 {
		head := e.Doc.Sel.Head
		e.applyEdit(e.Doc.Replace(line, line+n, "", editOther))
		p := max(line, head-n)
		e.Doc.Sel = Selection{Anchor: p, Head: p}
		e.Doc.hist.seal()
		e.reveal, e.dirty = true, true
	}
	return true
}

// insertCodeBlock 按默认语言插入代码块（原版 insertCodeBlock 的 defaultCodeBlockLanguage）。
func (e *Editor) insertCodeBlock() { e.wrapLines("```"+e.opts.CodeLanguage+"\n", "\n```") }

// insertDiagram 插入默认类型的 Mermaid 图表（原版 insertDiagramBlock）。
func (e *Editor) insertDiagram() {
	code := e.opts.DiagramCode
	if code == "" {
		code = defaultDiagram
	}
	e.insertBlock("```mermaid\n" + code + "\n```")
}

// InsertMermaid 插入 Mermaid 图表：code 为空时插入空白图表并把光标放进代码块（原版 insertMermaidBlock 进入编辑），
// 否则插入该模板（原版 insertDiagramBlock）。
func (e *Editor) InsertMermaid(code string) {
	if code != "" {
		e.insertBlock("```mermaid\n" + code + "\n```")
		e.dirty = true
		return
	}
	e.insertBlock("```mermaid\n\n```")
	p := e.Doc.Sel.Head - len("\n```")
	e.Doc.Sel = Selection{Anchor: p, Head: p}
	e.dirty = true
}

// insertCommentBlock 插入注释块（原版 insertCommentBlock），光标置于注释内。
func (e *Editor) insertCommentBlock() {
	e.insertBlock("<!--  -->")
	p := e.Doc.Sel.Head - len(" -->")
	e.Doc.Sel = Selection{Anchor: p, Head: p}
}

// lineNumberGutter 返回代码块行号栏宽度（容纳 n 行的位数）；不显示行号时为 0。
func (e *Editor) lineNumberGutter(n int) float32 {
	if e.opts.HideLineNumbers || n <= 0 {
		return 0
	}
	digits := len(strconv.Itoa(n))
	return (float32(digits)*0.62*13 + 14) * e.scale
}

// applyLineNumbers 为块中各代码块的代码行编号并让出行号栏：续行（超长行分段）与围栏行不编号。
func (e *Editor) applyLineNumbers(rows []row, frames []frame) {
	for _, f := range frames {
		if f.kind != frameCode {
			continue
		}
		n := 0
		for k := f.first; k <= f.last && k < len(rows); k++ {
			if r := &rows[k]; !r.fence && !r.cont {
				n++
				r.lineNo = int32(n)
			}
		}
		g := e.lineNumberGutter(n)
		for k := f.first; k <= f.last && k < len(rows); k++ {
			rows[k].indent += g
		}
	}
}

// drawLineNumber 在代码行左侧行号栏中右对齐绘制行号。
func (e *Editor) drawLineNumber(p *ui.Painter, r *row, x0, ty float32) {
	if r.lineNo == 0 || e.opts.HideLineNumbers {
		return
	}
	sp := ui.Span{Text: strconv.Itoa(int(r.lineNo)), Font: e.monoFont(), Size: 12 * e.scale, Color: e.pal.muted}
	w, _ := p.MeasureText(0, sp)
	p.RichText(x0+r.indent-w-8*e.scale, ty+1, 0, sp)
}

var plainTagRe = regexp.MustCompile(`<[^>]*>`)

// plainText 把 Markdown 片段转为不含语法的纯文本（复制时不保留 Markdown 语法）：按解析结果输出 HTML 后去掉
// 标签、解码实体，块之间保留换行。
func plainText(md string) string {
	doc := parser.New(parser.Default | parser.ExtDollarMath)
	doc.Set(md)
	out := doc.HTML()
	for _, r := range []struct{ old, new string }{{"<br>", "\n"}, {"<br />", "\n"}, {"</p>", "\n\n"}, {"</li>", "\n"},
		{"</h1>", "\n\n"}, {"</h2>", "\n\n"}, {"</h3>", "\n\n"}, {"</h4>", "\n\n"}, {"</h5>", "\n\n"}, {"</h6>", "\n\n"},
		{"</tr>", "\n"}, {"</td>", "\t"}, {"</th>", "\t"}, {"</pre>", "\n"}, {"</blockquote>", "\n"}} {
		out = strings.ReplaceAll(out, r.old, r.new)
	}
	out = html.UnescapeString(plainTagRe.ReplaceAllString(out, ""))
	return strings.TrimRight(out, "\n")
}

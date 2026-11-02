package typesetting

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// 静态签名直接交给原生编辑器显示，接受候选时只替换已输入的名称。
type syntaxHint struct{ name, signature, doc string }

var texHints = []syntaxHint{
	{"documentclass", `\documentclass[options]{class}`, "选择文档类，决定文章、报告或书籍的基本版式。"},
	{"usepackage", `\usepackage[options]{package}`, "加载宏包，可在方括号内传入选项。"},
	{"begin", `\begin{environment}`, "开始指定的环境，与同名的结束命令配对。"},
	{"end", `\end{environment}`, "结束指定的环境。"},
	{"section", `\section{title}`, "创建节标题，通常自动编号并加入目录。"},
	{"subsection", `\subsection{title}`, "在当前节下创建小节标题。"},
	{"subsubsection", `\subsubsection{title}`, "在当前小节下创建下一级标题。"},
	{"chapter", `\chapter{title}`, "在支持章的文档类中创建章标题。"},
	{"label", `\label{key}`, "为当前位置的编号对象设置交叉引用标签。"},
	{"ref", `\ref{key}`, "显示标签对应的编号。"},
	{"eqref", `\eqref{key}`, "引用公式编号并加上圆括号，需要 amsmath。"},
	{"cite", `\cite{key}`, "按文献键插入引用。"},
	{"caption", `\caption{text}`, "给图或表添加编号及说明文字。"},
	{"includegraphics", `\includegraphics[width=…]{file}`, "插入图片，可设置宽度，需要 graphicx。"},
	{"input", `\input{file}`, "在当前位置读入另一个源文件。"},
	{"include", `\include{file}`, "按独立单元载入文件，并在其前后分页。"},
	{"textbf", `\textbf{text}`, "以粗体排印参数中的文字。"},
	{"emph", `\emph{text}`, "强调文字，通常切换正体与斜体。"},
	{"frac", `\frac{numerator}{denominator}`, "排印分数，两个参数分别是分子和分母。"},
	{"sqrt", `\sqrt[index]{expression}`, "排印根式，方括号可指定根指数。"},
	{"mathbf", `\mathbf{symbol}`, "在数学模式中使用粗体罗马字母。"},
	{"mathbb", `\mathbb{symbol}`, "排印黑板粗体符号，通常需要 amsfonts。"},
	{"newcommand", `\newcommand{\name}[n]{definition}`, "定义新命令，可指定参数个数。"},
	{"bibliography", `\bibliography{database}`, "指定 BibTeX 文献数据库并排印参考文献。"},
}
var typHints = []syntaxHint{
	{"set", "#set element(arguments)", "为当前作用域设置指定元素的默认参数。"},
	{"show", "#show selector: transformation", "指定选择器和变换规则，自定义元素的显示方式。"},
	{"let", "#let name = value", "给值或函数命名，以便在后文复用。"},
	{"import", "#import \"path\": names", "从文件或包导入指定的定义。"},
	{"include", "#include \"path\"", "在当前位置插入另一份 Typst 文档的内容。"},
	{"text", "#text(font: …, size: …)[body]", "设置字体、字号或颜色并排印正文。"},
	{"heading", "#heading(level: …)[body]", "创建指定层级的标题，可出现在目录中。"},
	{"figure", "#figure(body, caption: …)", "将内容排为带题注、可引用的图表。"},
	{"image", "#image(path, width: …)", "从文件载入图片，可设置显示尺寸。"},
	{"table", "#table(columns: …, ..cells)", "按列组织表格数据并排印单元格。"},
	{"grid", "#grid(columns: …, ..cells)", "将内容按行列排列，用于布局。"},
	{"page", "#page(margin: …)[body]", "设置页面尺寸、页边距或页眉页脚。"},
	{"par", "#par(justify: true)[body]", "设置段落的对齐、缩进等属性。"},
	{"align", "#align(alignment)[body]", "将内容沿水平或垂直方向对齐。"},
	{"strong", "#strong[body]", "加强强调，通常以粗体排印。"},
	{"emph", "#emph[body]", "强调内容，通常以斜体排印。"},
	{"bibliography", "#bibliography(path, style: …)", "载入文献数据库并生成参考文献列表。"},
	{"cite", "#cite(<key>)", "按文献键插入引用。"},
	{"ref", "#ref(<label>)", "引用由标签标记的元素或文献。"},
	{"outline", "#outline(title: …)", "根据标题等元素生成目录。"},
	{"link", "#link(dest)[body]", "创建指向网址或文档位置的链接。"},
	{"pagebreak", "#pagebreak()", "从新页开始排印后续内容。"},
}
var envHints = []syntaxHint{
	{"document", "document", "包围文档正文。"},
	{"equation", "equation", "排印单个带编号的行间公式。"},
	{"equation*", "equation*", "排印不编号的行间公式。"},
	{"align", "align", "按对齐点排列多行公式，默认逐行编号。"},
	{"align*", "align*", "按对齐点排列多行公式，不编号。"},
	{"aligned", "aligned", "在数学环境内嵌入对齐的公式组。"},
	{"matrix", "matrix", "排印无括号的矩阵。"},
	{"pmatrix", "pmatrix", "排印带圆括号的矩阵。"},
	{"bmatrix", "bmatrix", "排印带方括号的矩阵。"},
	{"figure", "figure", "创建可浮动排版的插图。"},
	{"table", "table", "创建可浮动排版的表格。"},
	{"tabular", "tabular", "按列格式排印表格单元格。"},
	{"itemize", "itemize", "排印带项目符号的列表。"},
	{"enumerate", "enumerate", "排印自动编号的列表。"},
	{"proof", "proof", "排印证明正文，通常由 amsthm 提供。"},
	{"theorem", "theorem", "排印定理正文，需要预先定义该环境。"},
	{"abstract", "abstract", "排印文档摘要。"},
	{"center", "center", "使环境内的内容居中。"},
}

func syntaxWord(ch byte) bool { return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' }
func hintsAt(kind, line string) ([]syntaxHint, int) {
	at := len(line)
	for at > 0 && (syntaxWord(line[at-1]) || kind == kindTypst && strings.ContainsRune("0123456789_-", rune(line[at-1]))) {
		at--
	}
	before := line[:at]
	if kind == kindLaTeX {
		for i := 0; i < len(before); i++ {
			switch before[i] {
			case '\\':
				i++
			case '%':
				return nil, 0
			}
		}
		if strings.HasSuffix(before, `\begin{`) || strings.HasSuffix(before, `\end{`) {
			return envHints, at
		}
		n := len(before) - len(strings.TrimRight(before, `\`))
		if n%2 == 1 {
			return texHints, at
		}
	} else if kind == kindTypst {
		trim := strings.TrimRight(before, " \t")
		rule := len(trim) < len(before) && (strings.HasSuffix(trim, "#set") || strings.HasSuffix(trim, "#show"))
		if strings.HasSuffix(before, "#") || rule {
			toks, _ := typesetTokens(false, line, 0, nil)
			if len(toks) > 0 {
				switch toks[len(toks)-1].Kind {
				case editor.TokenString, editor.TokenComment, editor.TokenCode:
					return nil, 0
				}
			}
			return typHints, at
		}
	}
	return nil, 0
}

var texDeclaration = regexp.MustCompile(`^\s*\\(?:newcommand|renewcommand|providecommand|DeclareMathOperator)\*?\s*\{?\\([A-Za-z]+)\}?([[:blank:]]*\[[0-9]\])?`)
var typDeclaration = regexp.MustCompile(`^\s*#let\s+([A-Za-z][A-Za-z0-9_-]*)(\([^()]{0,100}\))?\s*=`)

// 缓存只归当前源码状态所有，最多 32 KiB 声明区、128 个名字，不持有旧快照。
type declaredHints struct {
	prefix  string
	items   []syntaxHint
	version uint64
	valid   bool
}

func (d *declaredHints) get(s editor.Snapshot, kind string) []syntaxHint {
	if d.valid && d.version == s.Version() {
		return d.items
	}
	capEnd := min(s.Len(), 32768)
	// 上一版导言区（含结束标记）完全相同时，正文编辑不再解析或复制它。
	end := capEnd
	if kind == kindLaTeX && strings.HasSuffix(d.prefix, `\begin{document}`) {
		end = min(end, len(d.prefix))
	}
	pos := 0
	equal := d.valid && end == len(d.prefix)
	if equal {
		s.Chunks(0, end, func(v string) bool { equal = d.prefix[pos:pos+len(v)] == v; pos += len(v); return equal })
	}
	d.valid, d.version = true, s.Version()
	if equal {
		return d.items
	}
	text := s.Slice(0, capEnd)
	if kind == kindLaTeX {
		if i := strings.Index(text, `\begin{document}`); i >= 0 {
			text = text[:i+len(`\begin{document}`)]
		}
	}
	d.prefix = strings.Clone(text)
	d.items = nil
	re := typDeclaration
	if kind == kindLaTeX {
		re = texDeclaration
	}
	for l := range strings.Lines(d.prefix) {
		m := re.FindStringSubmatch(l)
		if m == nil || len(m[1]) > 80 {
			continue
		}
		d.items = append(d.items, syntaxHint{m[1], strings.TrimSpace(m[0]), "在当前文件中声明；右侧显示其定义签名。"})
		if len(d.items) == 128 {
			break
		}
	}
	return d.items
}

func bindTypesetHints(st *editor.State, kind string) func() {
	var declarations declaredHints
	st.OnCompletion = func(req editor.CompletionRequest) {
		snap := st.Buffer.Snapshot()
		if req.Version != snap.Version() || req.Offset < 0 || req.Offset > snap.Len() {
			return
		}
		begin := snap.LineStart(snap.LineOf(req.Offset))
		if req.Offset-begin > 2048 {
			return
		}
		line := snap.Slice(begin, req.Offset)
		catalog, at := hintsAt(kind, line)
		if catalog == nil {
			return
		}
		var items []editor.Completion
		add := func(h syntaxHint) {
			if strings.HasPrefix(h.name, line[at:]) {
				items = append(items, editor.Completion{Label: h.name, Insert: h.name, Detail: h.signature, Documentation: h.doc, Kind: editor.CompletionFunction})
			}
		}
		for _, h := range catalog {
			add(h)
		}
		if at > 0 && (line[at-1] == '\\' || line[at-1] == '#') {
			for _, h := range declarations.get(snap, kind) {
				add(h)
			}
		}
		st.ShowCompletions(begin+at, items)
	}
	// MyGo 自动补全只识别单词和少数标点。保留原生输入与撤销，在字符提交后的下一帧请求候选。
	trigger := "#"
	if kind == kindLaTeX {
		trigger = "\\"
	}
	pending := false
	st.OnInput = func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputText {
			pending = ev.Text == trigger
		}
		return false
	}
	return func() {
		if !pending {
			return
		}
		pending = false
		at := st.Cursor()
		if st.Focused() && at > 0 && st.Buffer.Snapshot().Slice(at-1, at) == trigger {
			st.Run(editor.ShowCompletions)
		}
	}
}

var texLanguage = typesetLanguage(true)
var typLanguage = typesetLanguage(false)

func typesetLanguage(tex bool) *editor.Language {
	name, comment := "Typst", "//"
	imports := regexp.MustCompile(`^\s*#(?:import|include)\b`)
	if tex {
		name, comment = "LaTeX", "%"
		imports = regexp.MustCompile(`^\s*\\(?:usepackage|RequirePackage)\b`)
	}
	pairs := []editor.BracketPair{{Open: "{", Close: "}"}, {Open: "[", Close: "]"}, {Open: "(", Close: ")"}}
	return &editor.Language{Name: name, Imports: imports, LineComment: comment, Brackets: pairs, AutoClose: pairs, Tokenizer: editor.TokenizerFunc(func(l string, s uint64, t []editor.Token) ([]editor.Token, uint64) {
		return typesetTokens(tex, l, s, t)
	})}
}

// Typst 块注释可以嵌套；跨行状态由编辑器的增量高亮器保存。
func typesetTokens(tex bool, line string, state uint64, tokens []editor.Token) ([]editor.Token, uint64) {
	emit := func(end int, k editor.TokenKind) {
		if n := len(tokens); n > 0 && tokens[n-1].Kind == k {
			tokens[n-1].End = end
		} else {
			tokens = append(tokens, editor.Token{End: end, Kind: k})
		}
	}
	for i := 0; i < len(line); {
		k := editor.TokenText
		switch {
		case !tex && (state > 0 || strings.HasPrefix(line[i:], "/*")):
			if state == 0 {
				state++
				i += 2
			}
			for i < len(line) && state > 0 {
				switch {
				case strings.HasPrefix(line[i:], "/*"):
					state++
					i += 2
				case strings.HasPrefix(line[i:], "*/"):
					state--
					i += 2
				default:
					i++
				}
			}
			k = editor.TokenComment
		case tex && line[i] == '%' || !tex && strings.HasPrefix(line[i:], "//"):
			i = len(line)
			k = editor.TokenComment
		case !tex && (line[i] == '"' || line[i] == '`'):
			quote := line[i]
			i++
			for i < len(line) {
				ch := line[i]
				i++
				if ch == quote {
					break
				}
				if ch == '\\' && i < len(line) {
					i++
				}
			}
			k = editor.TokenString
		case line[i] == '\\' && tex || line[i] == '#' && !tex:
			i++
			a := i
			for i < len(line) && syntaxWord(line[i]) {
				i++
			}
			if tex && a == i && i < len(line) {
				_, n := utf8.DecodeRuneInString(line[i:])
				i += n
			}
			k = editor.TokenFunction
		case strings.IndexByte("{}[]()$", line[i]) >= 0:
			i++
			k = editor.TokenPunctuation
		default:
			i++
		}
		emit(i, k)
	}
	return tokens, state
}

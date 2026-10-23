package shell

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui/editor"

	"nomo/desktop/internal/docfs"
	"nomo/desktop/ui/mdedit"
)

// Tab 是一个打开的文档。
//
// 文档状态（Doc：原文、撤销历史、选区）与视图（Ed 的布局缓存、源码模式的 editor.State）
// 分离：标签转入后台时释放 Ed 的布局，Doc 不受影响。
//
// 源码模式使用 ui/editor（自带高亮、查找、多光标），其缓冲区是 Doc 的镜像：
// 源码缓冲区每次变化按最小差异写回 Doc（syncFromSource），进入源码模式时按差异
// 更新缓冲区（syncToSource）。srcVer/docVer 记录上次同步时两边的版本。
type Tab struct {
	ID       int
	Path     string // 空串表示未保存的新文档
	Untitled int    // 新文档的序号，用于“无标题 2.md”
	Encoding docfs.Encoding
	// eol 为文件的换行风格（"\n"、"\r\n" 或 "\r"）：编辑器内一律用 LF，保存时还原。mixedRaw 只在
	// 文件混用多种换行时保存原文（罕见、通常很小），未编辑就保存时原样写回，保证字节不变。
	eol      string
	mixedRaw string

	Doc *mdedit.Document
	Ed  *mdedit.Editor

	source         *editor.State
	srcVer, docVer uint64

	savedVersion uint64 // 最近一次保存（或打开）时 Doc 的版本
	savedText    string // 最近一次保存时的原文，用于判断撤销回原样后不再算未保存
	dirtyVer     uint64 // dirty 缓存对应的 Doc 版本
	dirty        bool
	diskModified int64 // 打开或保存时文件的修改时间（毫秒），用于外部修改检测
	saving       bool

	// autoVer/autoAt 记录最近一次修改的版本与时间，用于自动保存的延时。
	autoVer uint64
	autoAt  time.Time

	statsVer uint64
	stats    mdedit.Stats
	outVer   uint64
	outline  []mdedit.Heading

	lint lintState // Markdown 格式检查结果，见 lint.go
}

func newTab(id int, path, text string, enc docfs.Encoding) *Tab {
	t := &Tab{ID: id, Path: path, Encoding: enc}
	text = t.adoptLineEndings(text)
	doc := mdedit.NewDocument(text)
	t.Doc, t.Ed = doc, mdedit.NewEditor(doc)
	t.markSaved(text)
	if path != "" {
		t.diskModified = docfs.ModifiedAt(path)
	}
	return t
}

// adoptLineEndings 记录 raw 的换行风格并返回统一为 LF 的文本（编辑器、解析器与结构编辑只处理 LF）。
func (t *Tab) adoptLineEndings(raw string) string {
	crlf := strings.Count(raw, "\r\n")
	cr := strings.Count(raw, "\r") - crlf
	lf := strings.Count(raw, "\n") - crlf
	t.eol, t.mixedRaw = "\n", ""
	switch {
	case crlf == 0 && cr == 0:
		return raw
	case crlf > 0 && cr == 0 && lf == 0:
		t.eol = "\r\n"
	case cr > 0 && crlf == 0 && lf == 0:
		t.eol = "\r"
	default:
		// 混用：保存编辑后的内容时取出现最多的风格；未编辑时原样写回。
		t.mixedRaw = raw
		if crlf >= lf && crlf >= cr {
			t.eol = "\r\n"
		} else if cr > lf {
			t.eol = "\r"
		}
	}
	return strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
}

// diskText 返回要写入磁盘的文本：LF 换回文件原有的换行风格；混用换行且内容未改时返回原文。
func (t *Tab) diskText(text string) string {
	if t.mixedRaw != "" && text == t.savedText {
		return t.mixedRaw
	}
	if t.eol == "\n" || t.eol == "" {
		return text
	}
	return strings.ReplaceAll(text, "\n", t.eol)
}

func (t *Tab) markSaved(text string) {
	t.savedVersion, t.savedText = t.Doc.Version(), text
	t.dirtyVer, t.dirty = t.Doc.Version(), false
}

// 文档类型与原版 getDocumentKindFromPath 一致：.txt、.json 只用源码编辑器编辑（无语义模式、
// 格式检查、快捷插入与导出），其余（含未保存的新文档）按 Markdown。
const (
	kindMarkdown = "markdown"
	kindText     = "text"
	kindJSON     = "json"
)

// Kind 返回文档类型（按路径扩展名）。
func (t *Tab) Kind() string { return documentKind(t.Path) }

func documentKind(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".txt":
		return kindText
	case ".json":
		return kindJSON
	}
	return kindMarkdown
}

// Name 返回标签显示的文件名。
func (t *Tab) Name(untitled func(n int) string) string {
	if t.Path == "" {
		return untitled(t.Untitled)
	}
	return filepath.Base(t.Path)
}

// Dirty 报告是否有未保存修改。版本变化后比较一次原文（撤销回保存时的内容不算修改），
// 结果按版本缓存，不会每帧比较全文。
func (t *Tab) Dirty() bool {
	if v := t.Doc.Version(); v != t.dirtyVer {
		t.dirtyVer = v
		t.dirty = v != t.savedVersion && t.Doc.Source() != t.savedText
	}
	return t.dirty
}

// Stats 返回按版本缓存的写作统计。
func (t *Tab) Stats() mdedit.Stats {
	if v := t.Doc.Version(); v != t.statsVer || t.statsVer == 0 {
		t.statsVer, t.stats = v, t.Doc.Stats()
	}
	return t.stats
}

// Outline 返回按版本缓存的大纲。
func (t *Tab) Outline() []mdedit.Heading {
	if v := t.Doc.Version(); v != t.outVer || t.outline == nil {
		t.outVer, t.outline = v, t.Doc.Headings()
	}
	return t.outline
}

// Source 返回源码模式的编辑器状态，首次使用时创建。
func (t *Tab) Source() *editor.State {
	if t.source == nil {
		buf := editor.NewBuffer(t.Doc.Source())
		label, lang, numbers := "Markdown", "document.md", editor.LineNumbersOff
		switch t.Kind() {
		case kindJSON:
			label, lang, numbers = "JSON", "document.json", editor.LineNumbersOn
		case kindText:
			label, lang = "Text", "document.txt"
		}
		buf.SetLanguage(editor.LanguageFor(lang))
		t.source = &editor.State{Buffer: buf, SoftWrap: true, LineNumbers: numbers, Label: label}
		t.srcVer, t.docVer = buf.Version(), t.Doc.Version()
	}
	return t.source
}

// syncToSource 在进入源码模式前，把语义编辑期间的改动按差异带到源码缓冲区。
func (t *Tab) syncToSource() {
	st := t.Source()
	if t.Doc.Version() == t.docVer {
		return
	}
	buf := st.Buffer
	old, text := buf.String(), t.Doc.Source()
	if old != text {
		p, s := diffBounds(old, text)
		buf.Edit(editor.Edit{Start: p, End: len(old) - s, Text: text[p : len(text)-s]})
	}
	t.srcVer, t.docVer = buf.Version(), t.Doc.Version()
}

// syncFromSource 把源码缓冲区的改动写回 Doc；缓冲区未变时不做任何事（每帧调用）。
func (t *Tab) syncFromSource() {
	if t.source == nil || t.source.Buffer.Version() == t.srcVer {
		return
	}
	t.Ed.SetSource(t.source.Buffer.String())
	t.srcVer, t.docVer = t.source.Buffer.Version(), t.Doc.Version()
}

// diffBounds 返回 a、b 的公共前缀与公共后缀长度（字节，落在 UTF-8 字符边界）。
func diffBounds(a, b string) (prefix, suffix int) {
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	for prefix > 0 && prefix < len(a) && a[prefix]&0xC0 == 0x80 {
		prefix--
	}
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	for suffix > 0 && a[len(a)-suffix]&0xC0 == 0x80 {
		suffix--
	}
	return prefix, suffix
}

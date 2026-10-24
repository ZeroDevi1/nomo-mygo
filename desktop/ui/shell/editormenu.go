package shell

import (
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"nomo/desktop/ui/mdedit"
)

// editorView 构建 tab 的语义编辑区，并挂上编辑区右键菜单。所有显示语义编辑区的位置（单栏、双栏、组合标签、
// 小窗）都经由这里，保证右键行为一致。
func (s *Shell) editorView(c *ui.Context, tab *Tab) ui.Element {
	return tab.Ed.View(c).ContextMenu(func(m *ui.Menu) { s.editorMenu(c, m, tab) })
}

// editorMenu 填充语义编辑区的右键菜单，按右键位置的目标给出不同的项（原版 App.svelte 的
// buildEditorContextMenuItems）：选区、折叠块标题、表格、代码块、公式、图表、链接、标题与普通文本。
func (s *Shell) editorMenu(c *ui.Context, m *ui.Menu, tab *Tab) {
	tg := tab.Ed.ContextTarget()
	run := func(m *ui.Menu, label, cmd string) {
		if m.Item(label).Chosen() {
			tab.Ed.Run(cmd)
			s.focusEditor = true
		}
	}
	tail := func() {
		m.Separator()
		if m.Item(s.tr("selectAll")).Chosen() {
			tab.Ed.Run("selectAll")
		}
		if m.Item(s.tr("find")).Chosen() {
			s.openFind(false)
		}
	}
	switch tg.Kind {
	case "table", "code", "math", "mermaid":
		s.blockMenu(c, m, tab, tg)
		m.Separator()
		m.EditItems()
		tail()
		return
	case "details":
		if m.Item(s.tr("detailsDefaultOpen")).Checked(tg.DetailsOpen).Chosen() {
			tab.Ed.SetDetailsDefaultOpen(tg.DetailsKey, !tg.DetailsOpen)
			s.focusEditor = true
		}
		if m.Item(s.tr("unwrapDetails")).Chosen() {
			tab.Ed.UnwrapDetails(tg.DetailsKey)
			s.focusEditor = true
		}
		return
	case "selection":
		m.EditItems()
		m.Separator()
		m.Submenu(s.tr("format"), func(sub *ui.Menu) {
			for _, it := range [][2]string{{"bold", "bold"}, {"italic", "italic"}, {"underline", "underline"},
				{"strikethrough", "strikethrough"}, {"inlineCode", "inlineCode"}, {"highlight", "highlight"}} {
				run(sub, s.tr(it[0]), it[1])
			}
			sub.Separator()
			run(sub, s.tr("clearStyle"), "clearFormat")
		})
		if m.Item(s.tr("createLink")).Chosen() {
			s.openLinkEditor()
		}
		tail()
		return
	case "link":
		if m.Item(s.tr("openLink")).Chosen() {
			s.openLink(c, tab, tg.Href)
		}
		if m.Item(s.tr("editLink")).Chosen() {
			tab.Ed.Select(tg.Pos, tg.Pos)
			s.openLinkEditor()
		}
		if m.Item(s.tr("copyLinkAddress")).Chosen() {
			c.Services().WriteClipboard(tg.Href)
		}
		m.Separator()
	case "heading":
		m.Submenu(s.tr("heading"), func(sub *ui.Menu) {
			for lv := 1; lv <= 6; lv++ {
				if sub.Item(s.tr("headingLevel", "level", strconv.Itoa(lv))).Checked(tg.Level == lv).Chosen() {
					tab.Ed.Run("heading" + strconv.Itoa(lv))
					s.focusEditor = true
				}
			}
		})
		run(m, s.tr("paragraph"), "paragraph")
		m.Separator()
	}
	run(m, s.tr("undo"), "undo")
	run(m, s.tr("redo"), "redo")
	m.EditItems()
	m.Separator()
	m.Submenu(s.tr("insert"), func(sub *ui.Menu) {
		for lv := 1; lv <= 6; lv++ {
			run(sub, s.tr("headingLevel", "level", strconv.Itoa(lv)), "heading"+strconv.Itoa(lv))
		}
		sub.Separator()
		for _, it := range [][2]string{{"unorderedList", "unorderedList"}, {"orderedList", "orderedList"}, {"taskList", "taskList"},
			{"quote", "quote"}, {"callout", "callout"}, {"details", "details"}} {
			run(sub, s.tr(it[0]), it[1])
		}
		sub.Separator()
		run(sub, s.tr("codeBlock"), "codeBlock")
		run(sub, s.tr("table"), "table")
		if sub.Item(s.tr("image")).Chosen() {
			s.pickImages()
		}
		run(sub, s.tr("mathBlock"), "mathBlock")
		run(sub, s.tr("mermaidDiagram"), "mermaid")
		run(sub, s.tr("comment"), "comment")
		run(sub, s.tr("horizontalRule"), "horizontalRule")
	})
	tail()
}

// codeMenuLanguages 是代码块右键“选择语言”中列出的常用语言（与快捷插入、设置中的语言标识一致）。
var codeMenuLanguages = []string{"ts", "js", "go", "python", "rust", "java", "c", "cpp", "json", "yaml", "html", "css", "sql", "bash"}

// blockMenu 是表格、代码块、公式与图表的专属菜单项（原版 contextMenu.ts 的 table / code / math / mermaid 目标）。
func (s *Shell) blockMenu(c *ui.Context, m *ui.Menu, tab *Tab, tg mdedit.ContextTarget) {
	done := func() { s.focusEditor = true }
	copyContent := func(label string) {
		if m.Item(label).Chosen() {
			if text, ok := tab.Ed.BlockContent(tg.Pos); ok {
				c.Services().WriteClipboard(text)
			}
		}
	}
	switch tg.Kind {
	case "table":
		row, _, rows, _, ok := tab.Ed.TableCell(tg.Pos)
		if !ok {
			return
		}
		table := func(m *ui.Menu, label, op string, disabled bool) {
			if m.Item(label).Disabled(disabled).Chosen() {
				tab.Ed.TableCommand(tg.Pos, op)
				done()
			}
		}
		table(m, s.tr("addRowBefore"), "addRowBefore", row == 0)
		table(m, s.tr("addRowAfter"), "addRowAfter", false)
		table(m, s.tr("deleteRow"), "deleteRow", row == 0 || rows <= 1)
		m.Separator()
		table(m, s.tr("addColumnBefore"), "addColumnBefore", false)
		table(m, s.tr("addColumnAfter"), "addColumnAfter", false)
		table(m, s.tr("deleteColumn"), "deleteColumn", false)
		m.Submenu(s.tr("columnAlignment"), func(sub *ui.Menu) {
			table(sub, s.tr("alignDefault"), "alignNone", false)
			table(sub, s.tr("alignLeft"), "alignLeft", false)
			table(sub, s.tr("alignCenter"), "alignCenter", false)
			table(sub, s.tr("alignRight"), "alignRight", false)
		})
		m.Separator()
		table(m, s.tr("deleteTable"), "deleteTable", false)
		return
	case "code":
		copyContent(s.tr("copyCode"))
		m.Submenu(s.tr("chooseLanguage"), func(sub *ui.Menu) {
			if sub.Item(s.tr("plainText")).Checked(tg.Lang == "").Chosen() {
				tab.Ed.SetCodeLanguage(tg.Pos, "")
				done()
			}
			sub.Separator()
			for _, l := range codeMenuLanguages {
				if sub.Item(l).Checked(strings.EqualFold(tg.Lang, l)).Chosen() {
					tab.Ed.SetCodeLanguage(tg.Pos, l)
					done()
				}
			}
		})
	case "math":
		copyContent(s.tr("copyFormulaSource"))
	case "mermaid":
		copyContent(s.tr("copyDiagramSource"))
	}
	m.Separator()
	if m.Item(s.tr("deleteBlock")).Chosen() {
		tab.Ed.DeleteBlock(tg.Pos)
		done()
	}
}

// openLink 打开链接：带协议的地址交给系统；相对路径按文档目录解析，Markdown 文件在新标签中打开，其余交给系统。
func (s *Shell) openLink(c *ui.Context, tab *Tab, href string) {
	if href == "" || strings.HasPrefix(href, "#") {
		return
	}
	if u, err := url.Parse(href); err == nil && u.Scheme != "" {
		c.Services().OpenURL(href)
		return
	}
	p, err := url.PathUnescape(strings.SplitN(href, "#", 2)[0])
	if err != nil || tab.Path == "" {
		return
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(filepath.Dir(tab.Path), p)
	}
	if _, err := os.Stat(p); err != nil {
		return
	}
	switch strings.ToLower(filepath.Ext(p)) {
	case ".md", ".markdown", ".txt":
		s.OpenFiles([]string{p})
	default:
		c.Services().OpenURL((&url.URL{Scheme: "file", Path: p}).String())
	}
}

// insertSourceDetails 在源码模式下插入折叠块（原版 editorInteractionController 的源码分支）：选中内容作为正文，
// 前后不足一个空行时补齐，光标置于标题中。
func (s *Shell) insertSourceDetails(t *Tab) {
	src := t.Source()
	text, sel := src.Buffer.String(), src.Selection()
	a, z := min(sel.Anchor, sel.Head), max(sel.Anchor, sel.Head)
	before, after := "", ""
	if a > 0 && !strings.HasSuffix(text[:a], "\n\n") {
		before = "\n\n"
	}
	if z < len(text) && !strings.HasPrefix(text[z:], "\n\n") {
		after = "\n\n"
	}
	const head = "<details>\n<summary>"
	src.Insert(before + head + "</summary>\n\n" + text[a:z] + "\n\n</details>" + after)
	p := a + len(before) + len(head)
	src.SetSelections(editor.Selection{Anchor: p, Head: p})
}

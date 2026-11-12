package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"nomo/desktop/typeset"
	"nomo/desktop/ui/mdedit"
)

type manuscriptView struct {
	tab          *Tab
	path         string
	buffer       *editor.Buffer
	editor       *mdedit.Editor
	mainBuffer   *editor.Buffer
	mainVersion  uint64
	mainPreamble string
	author       manuscriptAuthorForm
	styleVersion uint64
	styleCached  bool
	canvas       typeset.AuthorCanvasStyle
}

// 只保留当前画布的块索引与布局；大稿件仍可通过分段源码编辑器写作。
func canEditManuscript(tab *Tab) bool {
	if !isManuscript(tab) || tab.partner != 0 || tab.Source().ReadOnly {
		return false
	}
	snap := tab.Source().Buffer.Snapshot()
	return snap.Len() <= 4<<20 && snap.LineCount() <= 32000
}

func (s *Shell) manuscriptEditor(c *ui.Context, tab *Tab) ui.Element {
	s.manuscriptVisible = true
	st := tab.Source()
	v := s.manuscript
	if v == nil || v.tab != tab || v.path != tab.Path || v.buffer != st.Buffer {
		s.closeManuscriptEditor()
		ed := mdedit.NewManuscriptEditor(st.Buffer, typeset.IsLaTeXPath(tab.Path))
		sel := st.Selection()
		ed.Select(sel.Anchor, sel.Head)
		first, _ := st.VisibleLines()
		ed.ScrollToSource(st.Buffer.Snapshot().LineStart(first))
		v = &manuscriptView{tab: tab, path: tab.Path, buffer: st.Buffer, editor: ed}
		view := v
		ed.OnManuscriptObject = func(at int) bool {
			sel := ed.Doc.Sel
			ed.Doc.SetSelection(mdedit.Selection{Anchor: at, Head: at})
			kind := ed.ManuscriptObjectKind()
			if kind != "" {
				s.startAuthoring(view, kind)
			}
			ed.Doc.SetSelection(sel)
			return kind != ""
		}
		s.manuscript = v
	}
	ed := v.editor
	ed.SyncManuscript()
	var language *editor.Language
	if s.prefs.ProviderSyntax {
		if p := s.enabledProvider(tab); p != nil {
			language = p.Language
		}
	}
	ed.SetManuscriptLanguage(language)
	ed.SetManuscriptProject(s.manuscriptBlockProject(v))
	author := v.canvasStyle(typeset.IsLaTeXPath(v.path))
	size, height, family := float32(s.prefs.FontSize), float32(s.prefs.LineHeight), s.prefs.DocumentFontFamily
	if author.Size > 0 {
		size = author.Size * 4 / 3
	}
	if author.LineSpacing > 0 {
		height = author.LineSpacing
	}
	if author.Font != "" {
		family = author.Font
	}
	zoom := float32(s.prefs.ZoomPercent)
	ed.SetStyle(size, zoom, float32(s.documentWidth(tab)))
	ed.SetLineHeight(height)
	ed.SetManuscriptParagraphMetrics(author.ParagraphGap*zoom/75, author.Indent*zoom/75, zoom/75, author.Alignment)
	classic := s.prefs.DocumentStyleID == "nomo-classic"
	colors := editorColors(s.currentTheme(), c.Theme().Dark, classic)
	if author.Background != "" {
		bg := ui.Hex(author.Background)
		colors.Background = bg
		fg := ui.Hex("#202124")
		if 299*int(bg.R)+587*int(bg.G)+114*int(bg.B) < 128000 {
			fg = ui.Hex("#f3f4f5")
		}
		colors.Text, colors.Heading, colors.Muted = fg, fg, fg.Mix(bg, .35)
	}
	if author.TextColor != "" {
		colors.Text, colors.Heading = ui.Hex(author.TextColor), ui.Hex(author.TextColor)
	}
	ed.SetColors(colors, classic)
	ed.SetFonts(family, s.prefs.MonoFontFamily)
	ed.SetImages(s.images, "")
	ed.CtrlWheel = s.ctrlWheel
	var canvas ui.Element
	ui.Column(c.Key("manuscript-workspace")).Fill().MinHeight(0).Children(func() {
		s.manuscriptAuthorToolbar(c.Key("manuscript-toolbar"), v)
		canvas = ed.View(c.Key("manuscript-editor")).Basis(0).Grow(1).MinHeight(0).ContextMenu(func(m *ui.Menu) { m.EditItems() })
		s.manuscriptAuthorDialog(c, v)
	})
	return canvas
}

func (s *Shell) commitManuscript(tab *Tab) {
	if v := s.manuscript; v != nil && v.tab == tab && v.buffer == tab.Source().Buffer {
		v.editor.SyncManuscript()
		v.editor.UnmarkText()
	}
}

func (s *Shell) closeManuscriptEditor() {
	v := s.manuscript
	if v == nil {
		return
	}
	v.editor.SyncManuscript()
	top := v.editor.TopSourceOffset()
	v.editor.CloseManuscript()
	v.editor.OnManuscriptObject = nil
	s.manuscript = nil
	st := v.tab.Source()
	if st.Buffer == v.buffer {
		sel := v.editor.Doc.Sel
		st.Select(sel.Anchor, sel.Head)
		st.ScrollTo(st.Buffer.Snapshot().LineOf(top))
	}
}

func (s *Shell) manuscriptBlockProject(v *manuscriptView) mdedit.ManuscriptProject {
	if !s.canPreviewTypeset(v.tab) {
		return mdedit.ManuscriptProject{}
	}
	project := mdedit.ManuscriptProject{Path: v.path, SetupTitle: s.tr("author-templateSummary"),
		ListingTitle: s.tr("author-externalCode"), PreviewUnavailable: s.tr("author-previewUnavailable"),
		FigureTitle: s.tr("author-edit-figure"), TableTitle: s.tr("author-edit-table")}
	if p := s.scholarForTab(v.tab); p != nil {
		project.Main, project.Root, project.DiskRevision = p.project.Main, p.project.Root, p.previewDisk
	}
	if p := s.compile; p != nil && p.tab == v.tab && p.fullVersion == p.version && p.sourceVersion == v.buffer.Version() {
		project.References = p.blockReferences
		project.Index = p.referenceIndex
	}
	if typeset.IsLaTeXPath(v.path) && !samePath(project.Main, v.path) {
		if main := s.scholarTab(project.Main); main != nil && main.Dirty() {
			buf := main.Source().Buffer
			if v.mainBuffer != buf || v.mainVersion != buf.Version() {
				snap := buf.Snapshot()
				v.mainBuffer, v.mainVersion = buf, snap.Version()
				v.mainPreamble = mdedit.ManuscriptLaTeXPreamble(snap.Slice(0, min(snap.Len(), 64<<10)))
			}
			project.MainPreamble = v.mainPreamble
		} else {
			v.mainBuffer, v.mainPreamble = nil, ""
		}
	}
	return project
}

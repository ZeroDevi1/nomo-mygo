package shell

import (
	"fmt"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

const editorGroupLimit = 4

type workspaceItem struct {
	doc       int
	reference *scholarReferenceTab
	catalog   bool
}

type editorGroup struct {
	first, second *editorGroup
	vertical      bool
	percent       float32
	tabs          []workspaceItem
	selected      workspaceItem
}

type editorGroups struct {
	root, focused *editorGroup
	live          []workspaceItem
	prunePending  bool
	focusPane     bool
	paneWidth     float32 // Width during the current library pane build, not retained layout state.
}

func (g *editorGroup) eachLeaf(fn func(*editorGroup)) {
	if g == nil {
		return
	}
	if g.first == nil {
		fn(g)
		return
	}
	g.first.eachLeaf(fn)
	g.second.eachLeaf(fn)
}

func (g *editorGroup) containing(item workspaceItem) (found *editorGroup) {
	g.eachLeaf(func(leaf *editorGroup) {
		if slices.Contains(leaf.tabs, item) {
			found = leaf
		}
	})
	return found
}

func (g *editorGroup) removeTab(item workspaceItem) {
	i := slices.Index(g.tabs, item)
	g.tabs = slices.Delete(g.tabs, i, i+1)
	if g.selected == item {
		g.selected = workspaceItem{}
		if len(g.tabs) > 0 {
			g.selected = g.tabs[max(0, i-1)]
		}
	}
}

func (g *editorGroup) withoutEmptyLeaves() *editorGroup {
	if g.first == nil {
		if len(g.tabs) == 0 {
			return nil
		}
		return g
	}
	first, second := g.first.withoutEmptyLeaves(), g.second.withoutEmptyLeaves()
	if first == nil {
		return second
	}
	if second == nil {
		return first
	}
	g.first, g.second = first, second
	return g
}

func (s *Shell) workspaceDocumentItem(t *Tab) workspaceItem {
	if left, _, ok := s.comparisonOf(t); ok {
		t = left
	}
	if t == nil {
		return workspaceItem{}
	}
	return workspaceItem{doc: t.ID}
}

func (s *Shell) eachWorkspaceItem(fn func(workspaceItem)) {
	for _, t := range s.tabs {
		if !s.tabHidden(t) {
			fn(workspaceItem{doc: t.ID})
		}
	}
	if p := s.scholarForTab(s.activeTab()); p != nil {
		if p.references.catalogOpen || p.references.catalog {
			fn(workspaceItem{catalog: true})
		}
		for _, t := range p.references.tabs {
			fn(workspaceItem{reference: t})
		}
	}
}

func (s *Shell) workspaceItems() []workspaceItem {
	items := make([]workspaceItem, 0, len(s.tabs)+4)
	s.eachWorkspaceItem(func(item workspaceItem) { items = append(items, item) })
	return items
}

func (s *Shell) workspaceSelection() workspaceItem {
	if p := s.scholarForTab(s.activeTab()); p != nil {
		if p.references.catalog {
			return workspaceItem{catalog: true}
		}
		if p.references.focused != nil {
			return workspaceItem{reference: p.references.focused}
		}
	}
	return s.workspaceDocumentItem(s.activeTab())
}

// Reconcile IDs with the existing tab owners. Groups never own a second copy of
// a document, reference, editor buffer, or workspace recovery record.
func (s *Shell) syncEditorGroups() {
	gs := &s.groups
	if gs.root == nil {
		return
	}
	i, same := 0, true
	s.eachWorkspaceItem(func(item workspaceItem) {
		if i >= len(gs.live) || gs.live[i] != item {
			same = false
		}
		i++
	})
	if i == 0 {
		*gs = editorGroups{}
		return
	}
	if !same || i != len(gs.live) {
		gs.live = s.workspaceItems()
		gs.root.eachLeaf(func(g *editorGroup) {
			hadTabs := len(g.tabs) > 0
			g.tabs = slices.DeleteFunc(g.tabs, func(item workspaceItem) bool { return !slices.Contains(gs.live, item) })
			if hadTabs && len(g.tabs) == 0 {
				gs.prunePending = true
			}
			if !slices.Contains(g.tabs, g.selected) {
				g.selected = workspaceItem{}
				if len(g.tabs) > 0 {
					g.selected = g.tabs[0]
				}
			}
		})
		for _, item := range gs.live {
			if gs.root.containing(item) == nil {
				gs.focused.tabs = append(gs.focused.tabs, item)
			}
		}
	}
	if gs.prunePending {
		s.collapseEmptyEditorGroups()
		if gs.root == nil {
			return
		}
	}

	selected := s.workspaceSelection()
	if group := gs.root.containing(selected); group != nil {
		if gs.focused != group || group.selected != selected {
			gs.focused, group.selected = group, selected
			gs.focusPane = selected.doc == 0
		}
	}
}

func (s *Shell) activateWorkspaceItem(g *editorGroup, item workspaceItem) {
	if g == nil || item == (workspaceItem{}) {
		return
	}
	s.groups.focused, g.selected = g, item
	s.groups.focusPane = item.doc == 0
	if item.doc != 0 {
		if i := s.findTabIndex(item.doc); i >= 0 {
			if i == s.active {
				if p := s.scholarForTab(s.activeTab()); p != nil && (p.references.catalog || p.references.focused != nil) {
					s.selectScholarPDF(p, nil)
				}
			} else {
				s.activate(i)
			}
			s.focusEditor = true
		}
	} else if p := s.scholarForTab(s.activeTab()); p != nil {
		s.focusEditor = false
		if item.catalog {
			s.showScholarCatalog(p)
		} else if item.reference != nil {
			s.selectScholarPDF(p, item.reference)
		}
	}
}

func (s *Shell) splitWorkspaceItem(item workspaceItem, vertical, before bool) {
	gs := &s.groups
	if gs.root == nil {
		gs.root = &editorGroup{tabs: s.workspaceItems(), selected: s.workspaceSelection()}
		gs.focused = gs.root
	}
	group := gs.root.containing(item)
	if group == nil {
		return
	}
	count := 0
	gs.root.eachLeaf(func(*editorGroup) { count++ })
	if count >= editorGroupLimit {
		return
	}
	group.removeTab(item)
	kept := &editorGroup{tabs: group.tabs, selected: group.selected}
	moved := &editorGroup{tabs: []workspaceItem{item}, selected: item}
	*group = editorGroup{first: kept, second: moved, vertical: vertical, percent: 50}
	if before {
		group.first, group.second = moved, kept
	}
	s.closeCompile()
	s.activateWorkspaceItem(moved, item)
}

func (s *Shell) moveWorkspaceItem(item workspaceItem, dest *editorGroup) {
	gs := &s.groups
	if gs.root == nil {
		return
	}
	from := gs.root.containing(item)
	if from == nil || from == dest {
		return
	}
	from.removeTab(item)
	if len(from.tabs) == 0 {
		gs.prunePending = true
	}
	dest.tabs = append(dest.tabs, item)
	s.activateWorkspaceItem(dest, item)
}

func (s *Shell) collapseEmptyEditorGroups() {
	gs := &s.groups
	gs.prunePending = false
	if len(gs.focused.tabs) == 0 {
		// Prefer the closest preceding occupied group, or the next if there is none.
		var neighbor *editorGroup
		pastFocus := false
		gs.root.eachLeaf(func(g *editorGroup) {
			if g == gs.focused {
				pastFocus = true
			} else if len(g.tabs) > 0 && (!pastFocus || neighbor == nil) {
				neighbor = g
			}
		})
		if neighbor != nil {
			s.activateWorkspaceItem(neighbor, neighbor.selected)
		}
	}
	gs.root = gs.root.withoutEmptyLeaves()
	if gs.root == nil || gs.root.first == nil {
		s.mergeEditorGroups()
	}
}

func (s *Shell) mergeEditorGroups() {
	s.groups = editorGroups{}
	s.focusEditor = s.workspaceSelection().doc != 0
}

func (s *Shell) editorGroupCount() (count int) {
	s.groups.root.eachLeaf(func(*editorGroup) { count++ })
	return
}

func (s *Shell) editorSplitButtons(c *ui.Context, item workspaceItem) {
	disabled := item == (workspaceItem{}) || s.mini.active || s.editorGroupCount() >= editorGroupLimit
	if s.scholarCompactIcon(c.Key("split-right"), "columns-2", s.tr("splitGroupRight"), false).Disabled(disabled).Clicked() {
		s.splitWorkspaceItem(item, false, false)
	}
	if s.scholarCompactIcon(c.Key("split-down"), "rows-2", s.tr("splitGroupDown"), false).Disabled(disabled).Clicked() {
		s.splitWorkspaceItem(item, true, false)
	}
}

func (s *Shell) editorGroupMenu(m *ui.Menu, item workspaceItem) {
	gs := &s.groups
	count := s.editorGroupCount()
	for _, action := range []struct {
		label            string
		vertical, before bool
	}{
		{"splitGroupRight", false, false}, {"splitGroupDown", true, false}, {"splitGroupLeft", false, true}, {"splitGroupUp", true, true},
	} {
		if m.Item(s.tr(action.label)).Disabled(count >= editorGroupLimit || s.mini.active).Chosen() {
			s.splitWorkspaceItem(item, action.vertical, action.before)
		}
	}
	if gs.root != nil {
		m.Submenu(s.tr("moveToEditorGroup"), func(sub *ui.Menu) {
			i := 0
			gs.root.eachLeaf(func(group *editorGroup) {
				i++
				if sub.Item(fmt.Sprintf(s.tr("editorGroupNumber"), i)).Disabled(slices.Contains(group.tabs, item)).Chosen() {
					s.moveWorkspaceItem(item, group)
				}
			})
		})
		if m.Item(s.tr("mergeEditorGroups")).Chosen() {
			s.mergeEditorGroups()
		}
	}
	m.Separator()
}

func (s *Shell) workspaceGroupDocument(c *ui.Context, p *scholarState, tab *Tab) {
	if s.compile != nil {
		s.closeCompile()
	}
	total, _ := c.Size()
	inspector := p != nil && s.scholarInspectorVisible(p, total)
	if p == nil && !s.prefs.ToolbarHidden {
		s.toolbar(c)
	}
	if s.workspaceSelection().doc != 0 && s.mode() == modeSemantic {
		s.findBar(c, tab)
	}
	ui.Row(c).Grow(1).FillWidth().MinHeight(0).AlignItems(ui.Stretch).Clip().Children(func() {
		if inspector && s.scholarInspectorPrimary(p, total) {
			p.references.releaseHidden()
			s.scholarInspector(c, p, s.scholarWorkspaceWidth(c), true)
			return
		}
		var shown [editorGroupLimit]*scholarReferenceTab
		n := 0
		s.groups.root.eachLeaf(func(group *editorGroup) {
			if group.selected.reference != nil && n < len(shown) {
				shown[n] = group.selected.reference
				n++
			}
		})
		if p != nil {
			p.references.releaseHidden(shown[:n]...)
		}
		width := s.scholarWorkspaceWidth(c)
		if inspector {
			width -= s.scholarInspectorWidth(p, width) + scholarInspectorGripWidth
		}
		ui.Column(c.Key("editor-groups")).Grow(1).Fill().MinWidth(0).MinHeight(0).Children(func() { s.editorGroupView(c, p, s.groups.root, width) })
		if inspector {
			s.scholarInspector(c, p, s.scholarWorkspaceWidth(c), false)
		} else if p == nil && s.prefs.OutlineVisible && s.workspaceSelection().doc != 0 {
			s.outline(c, tab)
		}
	})
	if p != nil {
		s.scholarStatus(c, p, tab)
	} else if s.prefs.WritingStatsVisible {
		s.statusBar(c, tab)
	}
}

func (s *Shell) editorGroupView(c *ui.Context, p *scholarState, g *editorGroup, width float32) {
	if g == nil {
		return
	}
	if g.first != nil {
		first, second := width, width
		if !g.vertical {
			first = width * g.percent / 100
			second = width - first
		}
		split := ui.SplitPercent
		if g.vertical {
			split = ui.SplitVerticalPercent
		}
		split(c.Key(g), &g.percent, func() { s.editorGroupView(c, p, g.first, first) }, func() { s.editorGroupView(c, p, g.second, second) }).Fill().MinWidth(0).MinHeight(0)
		return
	}
	pane := ui.Column(c.Key(g)).Fill().MinWidth(0).MinHeight(0).Clip().Focusable().Label(s.tr("editorGroup"))
	if s.groups.focused == g && s.groups.focusPane {
		pane.Focus()
		s.groups.focusPane = false
	}
	pane.Children(func() {
		bar := ui.Row(c.Key("group-tabs")).Height(tabBarH - 4).FillWidth().AlignItems(ui.Stretch).Background(s.ch.tabBar)
		if _, over := ui.DragOver[tabDrag](bar); over {
			bar.Background(s.ch.hover)
		}
		if _, over := ui.DragOver[workspaceItem](bar); over {
			bar.Background(s.ch.hover)
		}
		bar.Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).FillHeight().Clip().AlignItems(ui.Stretch).Children(func() {
				for _, item := range slices.Clone(g.tabs) {
					s.editorGroupTab(c.Key(item), p, g, item)
				}
			})
			ui.Row(c.Key("group-controls")).FillHeight().Shrink(0).AlignItems(ui.Center).Children(func() {
				pdf := g.selected.reference
				if p == nil || (pdf != nil && pdf.web != nil) {
					pdf = nil
				}
				// Reserve room for two tab labels and the existing group controls.
				compact := width < 340
				if pdf != nil && !compact {
					s.scholarPDFTabControls(c, p, pdf)
					ui.Box(c).Size(1, 14).Margin(0, 4).Background(s.ch.separator)
				}
				s.editorSplitButtons(c.Key("split-buttons"), g.selected)
				s.scholarCompactIcon(c.Key("group-menu"), "ellipsis", s.tr("editorGroup"), false).Menu(func(m *ui.Menu) {
					if pdf != nil && compact {
						s.scholarPDFMenu(c, m, p, pdf)
						m.Separator()
					}
					for _, item := range g.tabs {
						_, title := s.workspaceItemLabel(item)
						if m.Item(title).Radio(g.selected == item).Chosen() {
							s.activateWorkspaceItem(g, item)
						}
					}
					m.Separator()
					if m.Item(s.tr("mergeEditorGroups")).Chosen() {
						s.mergeEditorGroups()
					}
				})
			})
		})
		s.editorGroupDrop(bar, g)
		ui.Box(c).Height(1).FillWidth().Background(s.ch.separator)
		item := g.selected
		switch {
		case item.reference != nil && p != nil:
			s.scholarReferencePage(c, p, item.reference)
		case item.catalog && p != nil:
			s.groups.paneWidth = max(1, width)
			s.scholarLibraryView(c.Key("library"), p)
			s.groups.paneWidth = 0
		case item.doc != 0:
			s.editorGroupSource(c, p, g, item)
		default:
			ui.Text(c, s.tr("emptyEditorGroup")).FontSize(12).TextColor(s.ch.muted).Padding(14).Wrap()
		}
	})
}

func (s *Shell) workspaceItemLabel(item workspaceItem) (glyph, title string) {
	if item.reference != nil {
		glyph = "file-text"
		if item.reference.web != nil {
			glyph = "globe"
		}
		return glyph, item.reference.title
	}
	if item.catalog {
		return "book-open", s.tr("scholarCatalog")
	}
	if tab := s.tabWithID(item.doc); tab != nil {
		return "file-text", s.tabLabel(tab)
	}
	return "file-text", ""
}

func (s *Shell) editorGroupDrop(el ui.Element, g *editorGroup) {
	if d, ok := ui.Drop[tabDrag](el); ok {
		if d.from == s {
			s.moveWorkspaceItem(s.workspaceDocumentItem(s.tabWithID(d.id)), g)
		} else {
			s.groups.focused = g
			s.adoptTabs(d.from, d.id, 0)
		}
	}
	if item, ok := ui.Drop[workspaceItem](el); ok {
		s.moveWorkspaceItem(item, g)
	}
}

func (s *Shell) editorGroupTab(c *ui.Context, p *scholarState, g *editorGroup, item workspaceItem) {
	glyph, title := s.workspaceItemLabel(item)
	if title == "" {
		return
	}
	el := s.scholarWorkspaceTab(c, glyph, title, s.tr("closeTab"), g.selected == item, func() { s.activateWorkspaceItem(g, item) }, func() {
		if item.doc != 0 {
			if t := s.tabWithID(item.doc); t != nil {
				s.requestClose(s.tabIDsOf(t), false)
			}
		} else if p != nil {
			if item.catalog {
				p.references.catalogOpen = false
				if p.references.catalog {
					s.selectScholarPDF(p, nil)
				}
			} else {
				s.closeScholarPDF(p, item.reference)
			}
		}
	})
	if t := s.tabWithID(item.doc); t != nil {
		s.tabDragSource(el, t)
		s.tabMenu(el, t)
	} else {
		el.Drag(item)
		el.ContextMenu(func(m *ui.Menu) {
			if item.reference != nil && p != nil {
				s.scholarReferenceActions(c, m, p, item.reference)
			} else {
				s.editorGroupMenu(m, item)
			}
		})
	}
	s.editorGroupDrop(el, g)
}

func (s *Shell) editorGroupSource(c *ui.Context, p *scholarState, g *editorGroup, item workspaceItem) {
	t := s.tabWithID(item.doc)
	if t == nil {
		return
	}
	ui.Column(c.Key(t.ID)).Grow(1).Fill().MinWidth(0).MinHeight(0).Children(func() {
		var el ui.Element
		source := t.Kind() != kindMarkdown || s.prefs.EditorMode == modeSource || s.prefs.EditorMode == modeSplit && s.splitActive == modeSource
		s.applyProviderSyntax(t)
		s.applyEditorStyle(t)
		focused := false
		if left, right, ok := s.comparisonOf(t); ok {
			el = s.comparisonView(c, left, right)
			focused = el.Focused()
		} else if source {
			t.syncToSource()
			t.syncFromSource()
			st := t.Source()
			if st.OnCompletion == nil && t.Kind() == kindMarkdown {
				st.OnCompletion = func(req editor.CompletionRequest) { s.sourceCompletion(t, req) }
			}
			focused = st.Focused()
			ui.Row(c).Grow(1).FillWidth().MinHeight(0).Justify(ui.Center).Children(func() { el = s.sourceEditor(c, t) })
		} else {
			el = s.editorView(c, t)
			focused = t.Ed.Focused()
		}
		if focused && s.groups.focused != g && !s.focusEditor && !s.groups.focusPane {
			s.activateWorkspaceItem(g, item)
			s.focusEditor = false
		}
		if s.groups.focused == g && s.workspaceSelection().doc != 0 {
			if !source {
				if r := el.Bounds(); r.W > 0 {
					s.editorOrigin = ui.Point{X: r.X, Y: r.Y}
				}
				s.quickInsert(c, t, el)
				s.linkPopover(c, t, el)
				s.emojiSuggest(c, t, el)
			}
			if p != nil && s.scholarForTab(t) == p {
				p.agent.anchor = el.Bounds()
				s.scholarInlineCitations(c, p, t, el)
				s.scholarInlineReview(c, p, t, el)
				s.scholarChangePeek(c, p, t, el)
			}
			if s.focusEditor {
				el.Focus()
				s.focusEditor = false
			}
		}
		if files := el.DroppedFiles(); len(files) > 0 {
			s.dropFiles(t, files)
		}
	})
}

func (s *Shell) editorGroupsToolbar(c *ui.Context) {
	if s.scholarForTab(s.activeTab()) == nil {
		return
	}
	ui.Row(c).Height(tabBarH-4).FillWidth().Padding(0, 4).Gap(4).AlignItems(ui.Center).Background(s.ch.tabBar).Children(func() {
		ui.Spacer(c)
		s.documentTools(c)
	})
	ui.Box(c).Height(1).FillWidth().Background(s.ch.separator)
}

func (s *Shell) editorGroupsVisible(p *scholarState) bool {
	return s.groups.root != nil && (p == nil || !p.agent.trial.show && !p.changes.compare)
}

func (s *Shell) editorGroupShowsDocument(t *Tab) bool {
	if t == nil || s.groups.root == nil {
		return false
	}
	item := s.workspaceDocumentItem(t)
	group := s.groups.root.containing(item)
	return group != nil && group.selected == item
}

func (s *Shell) editorGroupsShowCatalog() (shown bool) {
	s.groups.root.eachLeaf(func(group *editorGroup) { shown = shown || group.selected.catalog })
	return
}

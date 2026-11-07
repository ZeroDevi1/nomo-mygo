package shell

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"nomo/desktop/ui/mdedit"
)

type scholarNavigation struct {
	reveal      bool
	files       scholarFileSearch
	search      scholarQueryState
	open, focus bool
	selected    int
	query       string
	figures     bool
	index       *scholarOutlineIndex
	list        ui.ListState
}

type scholarOutlineIndex struct {
	tabID       int
	version     uint64
	items       []mdedit.Heading
	unavailable bool
}

func (s *Shell) openScholarPalette(p *scholarState) {
	p.navigation.open, p.navigation.focus = true, true
}

func (s *Shell) scholarPalette(c *ui.Context, p *scholarState) {
	n := &p.navigation
	if !n.open {
		return
	}
	tab := s.activeTab()
	ui.Modal(c, &n.open, func() {
		panel := ui.Column(c).Width(480).Height(360).Padding(12).Gap(6)
		panel.Children(func() {
			ui.Text(c, s.tr("scholarCommandPalette")).Bold()
			input := ui.TextInput(c, &n.query).FillWidth().Placeholder(s.tr("scholarCommandQuery"))
			if n.focus {
				input.Focus()
				n.focus = false
			}
			query := strings.TrimSpace(n.query)
			closePalette := func() { n.open = false; s.focusEditor = true }
			contains := func(label string) bool {
				return strings.Contains(strings.ToLower(label), strings.ToLower(strings.TrimSpace(query)))
			}
			type action struct {
				title string
				run   func()
			}
			var entries []action
			switch {
			case strings.HasPrefix(query, "@"):
				query = query[1:]
				for _, e := range s.queryScholar(c, p, &n.search, query, 24) {
					entries = append(entries, action{e.Key + " · " + e.Title, func() {
						s.insertScholarCitations(p, []string{e.Key}, "")
					}})
				}
			case strings.HasPrefix(query, "#"):
				query = query[1:]
				for _, heading := range tab.Outline() {
					if contains(heading.Text) {
						entries = append(entries, action{heading.Text, func() { s.jumpScholarOutline(tab, heading.Pos) }})
						if len(entries) == 30 {
							break
						}
					}
				}
			default:
				commandsOnly := strings.HasPrefix(query, ">")
				query = strings.TrimPrefix(query, ">")
				if !commandsOnly {
					for _, path := range s.scholarSearchFiles(c, p, query) {
						name, _ := filepath.Rel(p.project.Root, path)
						if samePath(path, p.project.Main) {
							name = "◆ " + name
						}
						entries = append(entries, action{name, func() { s.OpenFiles([]string{path}) }})
					}
					if query != "" {
						for _, e := range s.queryScholar(c, p, &n.search, query, 12) {
							entries = append(entries, action{"@" + e.Key + " · " + e.Title, func() { s.insertScholarCitations(p, []string{e.Key}, "") }})
						}
					}
				}
				for _, cmd := range []action{
					{s.tr("scholarInsertLocalCitations"), func() {
						if isManuscript(tab) {
							p.literature.picker, p.literature.focusPicker = true, true
						}
					}},
					{s.tr("scholarViewRecentEdit"), func() { s.openScholarChange(p, 0) }},
					{s.tr("scholarOpenDiagnostics"), func() { s.showScholarDiagnostics(p) }},
					{s.tr("scholarOpenAgent"), func() { s.openScholarAgent(p) }},
				} {
					if contains(cmd.title) {
						entries = append(entries, cmd)
					}
				}
			}
			if input.Changed() {
				n.selected = 0
			}
			n.selected = min(n.selected, max(0, len(entries)-1))
			if len(entries) > 0 {
				if panel.OverlayShortcut(0, ui.KeyDown) {
					n.selected = (n.selected + 1) % len(entries)
					n.reveal = true
				}
				if panel.OverlayShortcut(0, ui.KeyUp) {
					n.selected = (n.selected + len(entries) - 1) % len(entries)
					n.reveal = true
				}
			}
			ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
				ui.Column(c).FillWidth().Gap(3).Children(func() {
					for i, entry := range entries {
						ck := c.Key(i)
						b := ui.ButtonBase(ck).FillWidth().Justify(ui.Start).Padding(6).Tooltip(entry.title)
						if i == n.selected {
							if n.reveal {
								b.ScrollIntoView()
							}
							b.Background(s.ch.pressed)
						} else if b.Hovered() {
							b.Background(s.ch.hover)
						}
						b.Children(func() { ui.Text(ck, entry.title).SingleLine() })
						if b.Clicked() || i == n.selected && input.Submitted() {
							closePalette()
							entry.run()
						}
					}
				})
			})
			n.reveal = false
			if len(entries) == 0 {
				ui.Text(c, s.tr("scholarNoMatches")).FontSize(11).TextColor(s.ch.muted)
			}
			if panel.OverlayShortcut(0, ui.KeyEscape) {
				closePalette()
			}
		})
	})
}

func (s *Shell) jumpScholarOutline(tab *Tab, offset int) {
	st := tab.Source()
	st.Select(offset, offset)
	st.ScrollTo(st.Buffer.Snapshot().LineOf(offset))
	if tab.typesetMode == typesetPreview {
		tab.typesetMode = typesetSplit
	}
	if p := s.scholar; p != nil {
		p.changes.compare = false
		p.compactPDF = false
	}
	s.focusEditor = true
}

func (s *Shell) scholarOutlineView(c *ui.Context, p *scholarState, tab *Tab) {
	n := &p.navigation
	ui.Row(c).Padding(4, 8).Gap(4).Children(func() {
		if ui.Button(c, s.tr("scholarSections")).Clicked() {
			n.figures = false
		}
		if ui.Button(c, s.tr("scholarObjects")).Clicked() {
			n.figures = true
		}
	})
	if !n.figures {
		s.outlineContent(c, tab)
		return
	}
	snap := tab.Source().Buffer.Snapshot()
	if n.index == nil || n.index.tabID != tab.ID || n.index.version != snap.Version() {
		if !p.busy {
			index := &scholarOutlineIndex{tabID: tab.ID, version: snap.Version(), unavailable: snap.Len() > 1<<20}
			typst := strings.EqualFold(tab.Kind(), "typst")
			labels := [3]string{s.tr("scholarEquation"), s.tr("scholarFigure"), s.tr("scholarTable")}
			s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
				if !index.unavailable {
					index.items = scholarObjects(ctx, snap, typst, labels)
				}
				return func() { n.index = index }, ctx.Err()
			})
		}
		ui.Text(c, s.tr("scholarReadingObjects")).Padding(4, 8).FontSize(11)
		return
	}
	if n.index.unavailable {
		ui.Text(c, s.tr("scholarObjectLimit")).Padding(4, 8).FontSize(11).Wrap()
		return
	}
	if len(n.index.items) == 0 {
		ui.Text(c, s.tr("scholarNoObjects")).Padding(4, 8).FontSize(11).Wrap()
		return
	}
	ui.List(c, &n.list, len(n.index.items), func(i int) {
		item := n.index.items[i]
		if ui.Button(c.Key(i), item.Text).FillWidth().Clicked() {
			s.jumpScholarOutline(tab, item.Pos)
		}
	}).FillWidth().Grow(1).MinHeight(0)
}

func scholarObjects(ctx context.Context, snap editor.Snapshot, typst bool, labels [3]string) []mdedit.Heading {
	var items []mdedit.Heading
	math := false
	for line := 0; line < snap.LineCount() && ctx.Err() == nil; line++ {
		text := strings.TrimSpace(snap.Line(line))
		label := ""
		if typst {
			switch {
			case strings.HasPrefix(text, "$") && !math:
				label = labels[0]
				math = strings.Count(text, "$")%2 != 0
			case strings.HasPrefix(text, "$") && math:
				math = false
			case strings.HasPrefix(text, "#figure("):
				label = labels[1]
			case strings.HasPrefix(text, "#table("):
				label = labels[2]
			}
		} else {
			switch {
			case strings.HasPrefix(text, `\[`), strings.HasPrefix(text, `\begin{equation`),
				strings.HasPrefix(text, `\begin{align`), strings.HasPrefix(text, `\begin{gather`):
				label = labels[0]
			case strings.HasPrefix(text, `\begin{figure`):
				label = labels[1]
			case strings.HasPrefix(text, `\begin{table`):
				label = labels[2]
			}
		}
		if label != "" {
			items = append(items, mdedit.Heading{Text: fmt.Sprintf("%s · L%d", label, line+1), Pos: snap.LineStart(line)})
			if len(items) >= 4096 {
				break
			}
		}
	}
	return items
}

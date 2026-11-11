package shell

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"nomo/desktop/internal/scholar"
)

type scholarInlineCitation struct {
	results                     scholarQueryState
	open, focus                 bool
	zotero                      bool
	tabID, start, end, selected int
	version                     uint64
	query                       string
	checked                     []string
	list                        ui.ListState
}

func (s *Shell) scholarInlineCitations(c *ui.Context, p *scholarState, t *Tab, el ui.Element) {
	q := &p.literature.inline
	if !q.open {
		return
	}
	st := t.Source()
	if t.ID != q.tabID || st.Buffer.Version() != q.version {
		q.open = false
		q.results.clear()
		return
	}
	r := el.Bounds()
	first, _ := st.VisibleLines()
	line := st.Buffer.Snapshot().LineOf(q.end)
	w, h := c.Size()
	width := min(float32(460), w-24)
	x := max(float32(12), min(r.X+36, w-width-12))
	y := max(float32(48), min(r.Y+float32(max(0, line-first)+1)*21, h-330))
	closePicker := func() { q.open = false; q.results.clear(); q.checked = nil; s.focusEditor = true }
	ui.Overlay(c, func() {
		panel := ui.Column(c).Absolute().Left(x).Top(y).Width(width).Height(300).Padding(8).Gap(4).Radius(c.Theme().Radius).Background(c.Theme().Background).Border(1, scholarInk(c, "citation"))
		panel.Children(func() {
			in := ui.TextInput(c, &q.query).FillWidth().Placeholder(s.tr("scholarCitationSearch"))
			if q.results.pending {
				ui.Text(c, s.tr("scholarSearching")).FontSize(11)
			}
			if q.focus {
				in.Focus()
				q.focus = false
			}
			if q.zotero && !p.literature.connected {
				q.zotero = false
				q.results.clear()
				q.selected = 0
			}
			if p.literature.connected && ui.Checkbox(c, &q.zotero, "Zotero").Changed() {
				q.results.clear()
				q.checked = nil
				q.selected = 0
			}
			var items []scholar.Entry
			if q.zotero {
				items = s.queryScholarZotero(p, &q.results, q.query)
			} else {
				items = s.queryScholar(c, p, &q.results, q.query, 100)
			}
			if in.Changed() {
				q.selected = 0
			}
			q.selected = min(q.selected, max(0, len(items)-1))
			if len(items) > 0 {
				if panel.OverlayShortcut(0, ui.KeyDown) {
					q.selected = (q.selected + 1) % len(items)
					q.list.ScrollTo(q.selected, ui.Center)
				}
				if panel.OverlayShortcut(0, ui.KeyUp) {
					q.selected = (q.selected + len(items) - 1) % len(items)
					q.list.ScrollTo(q.selected, ui.Center)
				}
				if !q.zotero && panel.OverlayShortcut(0, ui.KeyTab) {
					key := items[q.selected].Key
					if slices.Contains(q.checked, key) {
						q.checked = slices.DeleteFunc(q.checked, func(k string) bool { return k == key })
					} else if len(q.checked) < 32 {
						q.checked = append(q.checked, key)
					}
				}
			}
			insert := func() {
				keys := q.checked
				if q.zotero && len(items) > 0 {
					key := scholarReferenceKey(p.library, items[q.selected])
					if key == "" {
						p.err = s.tr("scholarAltImport")
						return
					}
					keys = []string{key}
				}
				if len(keys) == 0 && len(items) > 0 {
					keys = []string{items[q.selected].Key}
				}
				if len(keys) == 0 {
					return
				}
				if st.Buffer.Version() != q.version {
					return
				}
				// Preserve the original source trigger and closing delimiter.
				st.Select(q.end, q.end)
				s.insertScholarCitations(p, keys, "")
				closePicker()
			}
			ui.List(c, &q.list, len(items), func(i int) {
				e := items[i]
				rowKey := e.Key
				if q.zotero {
					rowKey = e.RemoteID
				}
				ck := c.Key(rowKey)
				b := ui.ButtonBase(ck).FillWidth().Padding(4).Justify(ui.Start)
				if i == q.selected {
					b.Background(s.ch.pressed)
				}
				b.Children(func() {
					ui.Column(ck).FillWidth().Children(func() {
						key := e.Key
						if key == "" {
							key = e.RemoteID
						}
						if slices.Contains(q.checked, key) {
							key = "✓ " + key
						}
						ui.Text(ck, key+" · "+e.Title).SingleLine().FontSize(12)
						ui.Text(ck, strings.TrimSpace(e.Author+" · "+e.Year+" · "+e.Venue)).SingleLine().FontSize(11)
					})
				})
				if b.Clicked() {
					q.selected = i
					insert()
				}
			}).Grow(1).FillWidth().MinHeight(0)
			if q.zotero {
				ui.Text(c, s.tr("scholarAltImport")).FontSize(11).Wrap()
				if p.err != "" {
					ui.Text(c, p.err).FontSize(11).Wrap()
				}
				if len(p.bibNames) > 0 {
					target := &p.literature.target
					if len(p.bibNames) == 1 {
						*target = 0
					} else {
						scholarSelectIndex(c, target, p.bibNames, false)
					}
					if len(items) > 0 && !p.busy && panel.OverlayShortcut(ui.Alt, ui.KeyEnter) {
						selected := items[q.selected]
						st.Select(q.end, q.end)
						closePicker()
						s.importScholarEntry(p, p.bibNames[*target], selected)
					}
				}
			} else {
				ui.Text(c, s.tr("scholarPickerKeys")).FontSize(11)
			}
			if q.open && (in.Submitted() || panel.OverlayShortcut(0, ui.KeyEnter)) {
				insert()
			}
		})
		if panel.OverlayShortcut(0, ui.KeyEscape) || panel.PressedOutside() {
			closePicker()
		}
	})
}

func (s *Shell) showScholarReference(st *editor.State, p *scholarState, e scholar.Entry, start, end int) {
	st.ShowHover(start, end, e.Title+"\n"+e.Author+" · "+e.Year, func(c *ui.Context) {
		if s.scholar != p {
			return
		}
		ui.Text(c, e.Venue+" "+e.DOI).FontSize(11).Wrap()
		if e.Abstract != "" {
			ui.Text(c, e.Abstract).Wrap().FontSize(12).MaxHeight(92).Clip()
		}
		ui.Row(c).Gap(4).Children(func() {
			if ui.Button(c, s.tr("scholarReferenceDetails")).Clicked() {
				s.selectScholarEntry(p, e)
			}
			if ui.Button(c, s.tr("scholarCopyBib")).Disabled(p.busy).Clicked() {
				s.copyScholarBib(p, e, c.Services())
			}
		})
		if ui.Button(c, s.tr("scholarDiscussPaper")).Disabled(p.busy || p.agent.patch != nil || p.agent.batch != nil).Clicked() {
			s.captureScholarTarget(p)
			if p.agent.target.tabID == 0 {
				p.agent.multi = true
				p.err = ""
			}
			s.showScholarInspector(p, scholarCodex)
			p.agent.parts[2] = fmt.Sprintf("%s\n%s\n%s\n%s", e.Key, e.Title, e.Author, e.Abstract)
			p.agent.include[2] = true
		}
		glyph, label := s.scholarReferenceOpenLabel(p, e)
		if s.scholarReaderCommand(c.Key("open-reference"), glyph, label, false).Disabled(p.busy).Clicked() {
			s.openScholarReference(p, e)
		}
	})
}

func (s *Shell) pinScholarReference(p *scholarState) {
	t := s.activeTab()
	if !isManuscript(t) {
		return
	}
	st := t.Source()
	head := st.Selection().Head
	for _, ref := range p.citations.ranges {
		if head >= ref.Start && head <= ref.End {
			s.showScholarReferenceAt(st, p, ref, true)
			return
		}
	}
}

package shell

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

func (s *Shell) scholarChangePeek(c *ui.Context, p *scholarState, t *Tab, el ui.Element) {
	if p.agent.trial.show && p.agent.trial.compare {
		return
	}
	d := &p.changes
	change := d.peek
	if change == nil {
		return
	}
	if change.tabID != t.ID {
		d.peek = nil
		d.peekView = nil
		return
	}
	if d.peekView == nil {
		d.peekView = scholarDiffView(change.before, change.after)
	}
	r := el.Bounds()
	w, h := c.Size()
	width := min(float32(540), w-24)
	ui.Overlay(c, func() {
		box := ui.Column(c).Absolute().Left(max(12, min(r.X+16, w-width-12))).Top(max(48, min(r.Y+40, h-280))).Width(width).Padding(8).Gap(5).Background(c.Theme().Background).Border(1, scholarInk(c, "diff"))
		box.Children(func() {
			ui.Text(c, "│ "+s.scholarChangeOrigin(change.origin)+fmt.Sprintf(" · L%d", change.line+1)).FontSize(11)
			scholarDiffElement(c, d.peekView).FillWidth().Height(180)
			ui.Row(c).Gap(4).Children(func() {
				if ui.Button(c, s.tr("scholarViewRecentEdit")).Clicked() {
					s.openScholarChange(p, change.id)
					d.peek = nil
					d.peekView = nil
				}
				if ui.Button(c, s.tr("scholarClose")).Clicked() {
					d.peek = nil
					d.peekView = nil
				}
			})
		})
		if box.OverlayShortcut(0, ui.KeyEscape) || box.PressedOutside() {
			d.peek = nil
			d.peekView = nil
			s.focusEditor = true
		}
	})
}

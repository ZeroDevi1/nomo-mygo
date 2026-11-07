package shell

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

const (
	scholarChangeDelay       = 450 * time.Millisecond
	scholarChangeBudget      = 192 << 10
	scholarChangeFragment    = 24 << 10
	scholarChangeQueueBudget = 64 << 20
)

type scholarChange struct {
	id            uint64
	tabID         int
	path          string
	line          int
	lines         []int
	start, end    int // Old and new byte counts for the changed span.
	before, after string
	origin        string
	truncated     bool
}

type scholarChangeBatch struct {
	change        *scholarChange
	before, after editor.Snapshot
}

type scholarChangeState struct {
	tabID              int
	path               string
	baseline, observed editor.Snapshot
	due                time.Time
	running            bool
	waiting            []scholarChangeBatch
	origin, nextOrigin string
	missed             bool
	nextID             uint64
	records            []*scholarChange
	bytes              int
	compare, lineDiff  bool
	selected           uint64
	view               *scholarDiff
	viewID             uint64
	hunk               int
	peek               *scholarChange
	pdfRevision        *scholarChangeBatch
	peekView           *scholarDiff
}

func (d *scholarChangeState) latest(tabID int) *scholarChange {
	for _, change := range d.records {
		if change.tabID == tabID {
			return change
		}
	}
	return nil
}

func (d *scholarChangeState) push(change *scholarChange) {
	if change == nil {
		return
	}
	at := 0
	for at < len(d.records) && d.records[at].id > change.id {
		at++
	}
	d.records = append(d.records, nil)
	copy(d.records[at+1:], d.records[at:])
	d.records[at] = change
	d.bytes += len(change.before) + len(change.after) + len(change.lines)*8
	for len(d.records) > 16 || d.bytes > scholarChangeBudget {
		last := d.records[len(d.records)-1]
		d.bytes -= len(last.before) + len(last.after) + len(last.lines)*8
		if d.peek != nil && d.peek.id == last.id {
			d.peek = nil
			d.peekView = nil
		}
		if d.viewID == last.id {
			d.view, d.viewID = nil, 0
		}
		d.records[len(d.records)-1] = nil
		d.records = d.records[:len(d.records)-1]
	}
}

func (s *Shell) observeScholarChanges(c *ui.Context, p *scholarState, tab *Tab) {
	d := &p.changes
	snap := tab.Source().Buffer.Snapshot()
	if d.tabID != tab.ID || d.path != tab.Path {
		s.flushScholarChanges(p)
		d.tabID, d.path, d.baseline, d.observed = tab.ID, tab.Path, snap, snap
		d.due, d.view, d.viewID, d.selected, d.compare = time.Time{}, nil, 0, 0, false
		d.origin, d.nextOrigin = "", ""
		return
	}
	s.captureScholarChangeSnapshot(p, snap, c.Now())
	if d.due.IsZero() {
		return
	}
	if remaining := d.due.Sub(c.Now()); remaining > 0 {
		c.After(remaining)
		return
	}
	s.flushScholarChanges(p)
}

func (s *Shell) captureScholarChangeSnapshot(p *scholarState, snap editor.Snapshot, now time.Time) {
	d := &p.changes
	if snap.Version() == d.observed.Version() {
		return
	}
	if d.due.IsZero() {
		d.origin = d.nextOrigin
	}
	d.nextOrigin = ""
	d.observed, d.due = snap, now.Add(scholarChangeDelay)
}

func (s *Shell) tagScholarChange(tab *Tab, origin string) {
	p := s.scholar
	if p == nil || p.changes.tabID != tab.ID {
		return
	}
	buf := tab.Source().Buffer
	if origin == "undo" && !buf.CanUndo() || origin == "redo" && !buf.CanRedo() {
		return
	}
	s.captureScholarChangeSnapshot(p, buf.Snapshot(), time.Now())
	s.flushScholarChanges(p)
	p.changes.nextOrigin = origin
}

func (s *Shell) flushScholarChanges(p *scholarState) {
	d := &p.changes
	if d.due.IsZero() {
		return
	}
	d.nextID++
	batch := scholarChangeBatch{change: &scholarChange{id: d.nextID, tabID: d.tabID, path: d.path, origin: d.origin},
		before: d.baseline, after: d.observed}
	d.baseline, d.due, d.origin = d.observed, time.Time{}, ""
	// Ropes share storage. Count their logical sizes as a conservative budget
	// for deferred snapshots, and let a single oversized edit run on its own.
	bytes := batch.before.Len() + batch.after.Len()
	for _, queued := range d.waiting {
		bytes += queued.before.Len() + queued.after.Len()
	}
	if len(d.waiting) >= 12 || bytes > scholarChangeQueueBudget {
		for i := len(d.waiting) - 1; i >= 0; i-- {
			prev := &d.waiting[i]
			if prev.change.tabID == batch.change.tabID && prev.change.path == batch.change.path && prev.after.Version() == batch.before.Version() {
				mergedBytes := bytes - prev.after.Len() - batch.before.Len()
				if mergedBytes > scholarChangeQueueBudget && len(d.waiting) > 1 {
					break
				}
				prev.after = batch.after
				prev.change.id = batch.change.id
				if prev.change.origin != batch.change.origin {
					prev.change.origin = "mixed"
				}
				s.startScholarChangeWorker(p)
				return
			}
		}
		for len(d.waiting) > 0 && (len(d.waiting) >= 12 || bytes > scholarChangeQueueBudget) {
			old := d.waiting[0]
			bytes -= old.before.Len() + old.after.Len()
			d.waiting[0] = scholarChangeBatch{}
			d.waiting = d.waiting[1:]
			d.missed = true
		}
	}
	d.waiting = append(d.waiting, batch)
	s.startScholarChangeWorker(p)
}

func (s *Shell) startScholarChangeWorker(p *scholarState) {
	d := &p.changes
	if d.running || len(d.waiting) == 0 {
		return
	}
	batch := d.waiting[0]
	d.waiting[0] = scholarChangeBatch{}
	d.waiting = d.waiting[1:]
	d.running = true
	go func() {
		matchScholarChange(p.ctx, batch.change, batch.before, batch.after)
		scholarChangeLines(batch.change)
		s.post(func() {
			if s.scholar != p {
				return
			}
			d.running = false
			if batch.change.start != 0 || batch.change.end != 0 {
				d.push(batch.change)
				if batch.before.Len()+batch.after.Len() <= 16<<20 {
					saved := batch
					d.pdfRevision = &saved
				} else {
					d.pdfRevision = nil
				}
			}
			s.startScholarChangeWorker(p)
		})
	}()
}

func (s *Shell) scholarChangeOrigin(origin string) string {
	switch origin {
	case "codex":
		return s.tr("scholarAppliedCodex")
	case "undo":
		return s.tr("scholarUndo")
	case "redo":
		return s.tr("scholarRedo")
	case "mixed":
		return s.tr("scholarMixedEdits")
	default:
		return s.tr("scholarEditBuffer")
	}
}

func matchScholarChange(ctx context.Context, change *scholarChange, a, b editor.Snapshot) {
	if ctx.Err() != nil {
		return
	}
	prefix := changedOffset(a, b)
	if prefix == a.Len() && prefix == b.Len() {
		return
	}
	endA, endB := a.Len(), b.Len()
	for endA > prefix && endB > prefix && ctx.Err() == nil {
		n := min(4096, endA-prefix, endB-prefix)
		x, y := a.Slice(endA-n, endA), b.Slice(endB-n, endB)
		if x == y {
			endA, endB = endA-n, endB-n
			continue
		}
		i := n - 1
		for i >= 0 && x[i] == y[i] {
			i--
		}
		endA, endB = endA-(n-1-i), endB-(n-1-i)
		break
	}
	if ctx.Err() != nil {
		return
	}
	for endA < a.Len() && a.Slice(endA, endA+1)[0]&0xc0 == 0x80 {
		endA++
	}
	for endB < b.Len() && b.Slice(endB, endB+1)[0]&0xc0 == 0x80 {
		endB++
	}
	change.line = b.LineOf(prefix)
	change.start, change.end = endA-prefix, endB-prefix
	change.before, change.truncated = scholarDiffFragment(a, prefix, endA)
	var limited bool
	change.after, limited = scholarDiffFragment(b, prefix, endB)
	change.truncated = change.truncated || limited
}

func scholarDiffFragment(snap editor.Snapshot, start, end int) (string, bool) {
	// A bounded amount of surrounding prose makes word-level edits legible.
	begin := max(snap.LineStart(snap.LineOf(start)), start-256)
	last := min(snap.LineEnd(snap.LineOf(end)), end+256)
	if last < end {
		last = end
	}
	limited := last-begin > scholarChangeFragment
	if limited {
		last = begin + scholarChangeFragment
	}
	for begin < start && snap.Slice(begin, begin+1)[0]&0xc0 == 0x80 {
		begin++
	}
	for last > begin && last < snap.Len() && snap.Slice(last, last+1)[0]&0xc0 == 0x80 {
		last--
	}
	return strings.Clone(snap.Slice(begin, last)), limited
}

func (s *Shell) recordScholarPatch(p *scholarState, patch *scholarPatch, before, after editor.Snapshot) {
	d := &p.changes
	if d.tabID == patch.target.tabID {
		s.captureScholarChangeSnapshot(p, before, time.Now())
		s.flushScholarChanges(p)
	}
	d.nextID++
	change := &scholarChange{id: d.nextID, tabID: patch.target.tabID, path: patch.target.path, origin: "codex",
		line: patch.target.line, start: len(patch.target.before), end: len(patch.next)}
	var limited bool
	change.before, change.truncated = scholarDiffFragment(before, patch.target.start, patch.target.end)
	change.after, limited = scholarDiffFragment(after, patch.target.start, patch.target.start+len(patch.next))
	change.truncated = change.truncated || limited
	scholarChangeLines(change)
	d.push(change)
	if d.tabID == patch.target.tabID {
		d.baseline, d.observed, d.due = after, after, time.Time{}
	}
	d.selected, d.compare = 0, false
}

func (s *Shell) scholarChangesView(c *ui.Context, p *scholarState) {
	tab := s.activeTab()
	d := &p.changes
	ui.Column(c).FillWidth().Grow(1).MinHeight(0).Padding(8).Gap(6).Children(func() {
		if set := p.agent.batch; set != nil {
			ui.Text(c, "┆ "+s.tr("scholarPendingProposal")).Bold().TextColor(scholarInk(c, "agent"))
			for i, h := range set.hunks {
				if ui.Button(c.Key("pending-"+fmt.Sprint(i)), fmt.Sprintf("%s · L%d", filepath.Base(h.target.path), h.target.line+1)).FillWidth().Clicked() {
					set.selected = i
					p.agent.reviewHidden = false
					p.drawer, p.compactNav = scholarCodex, false
					s.jumpScholarPatch(h)
				}
			}
		}
		ui.Text(c, "│ "+s.tr("scholarAppliedChanges")).Bold().TextColor(scholarInk(c, "diff"))
		if d.missed {
			ui.Text(c, s.tr("scholarDroppedBatches")).Wrap().FontSize(11)
		}
		if d.running || len(d.waiting) > 0 || !d.due.IsZero() {
			ui.Text(c, s.tr("scholarWaitingForEdits")).FontSize(11)
		}
		if ui.Button(c, s.tr("scholarViewRecentEdit")).Disabled(d.latest(tab.ID) == nil && d.due.IsZero() && !d.running && len(d.waiting) == 0).Clicked() {
			s.openScholarChange(p, 0)
		}
		ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
			for _, change := range d.records {
				if change.tabID != tab.ID {
					continue
				}
				origin := s.scholarChangeOrigin(change.origin)
				if ui.Button(c.Key(change.id), fmt.Sprintf(s.tr("scholarLineLabel"), origin, change.line+1)).FillWidth().Clicked() {
					s.openScholarChange(p, change.id)
				}
			}
		})
	})
}

func (s *Shell) openScholarChange(p *scholarState, id uint64) {
	s.flushScholarChanges(p)
	d := &p.changes
	d.selected, d.compare = id, true
	s.closeCompile()
}

func (s *Shell) scholarCompare(c *ui.Context, p *scholarState, tab *Tab) bool {
	d := &p.changes
	if !d.compare {
		return false
	}
	change := d.latest(tab.ID)
	if d.selected != 0 {
		for _, item := range d.records {
			if item.id == d.selected && item.tabID == tab.ID {
				change = item
				break
			}
		}
	}
	ui.Column(c).Fill().MinHeight(0).Gap(4).Children(func() {
		ui.Row(c).FillWidth().Padding(4, 8).Gap(4).Children(func() {
			if ui.Button(c, s.tr("scholarBackToSource")).Clicked() {
				d.compare = false
				d.view, d.viewID = nil, 0
			}
			ui.Checkbox(c, &d.lineDiff, s.tr("scholarLineDiff"))
			if ui.Button(c, "↑").Clicked() {
				s.navigateScholarChange(p, -1)
			}
			if ui.Button(c, "↓").Clicked() {
				s.navigateScholarChange(p, 1)
			}
		})
		if change == nil {
			ui.Text(c, s.tr("scholarPreparingDiff")).FontSize(11)
			return
		}
		origin := s.scholarChangeOrigin(change.origin)
		ui.Text(c, fmt.Sprintf(s.tr("scholarDiffSummary"), origin, change.line+1, change.start, change.end)).
			FontSize(12).TextColor(scholarInk(c, "diff"))
		if rev := d.pdfRevision; rev != nil && rev.change.id == change.id {
			if ui.Button(c, s.tr("scholarTrialComparison")).Disabled(p.busy).Clicked() {
				s.trialScholarEdit(p, rev)
			}
		}
		if change.truncated {
			ui.Text(c, s.tr("scholarTruncatedDiff")).FontSize(11)
		}
		if d.viewID != change.id || d.view == nil {
			d.view, d.viewID = scholarDiffView(change.before, change.after), change.id
		}
		d.view.Inline = editor.DiffInlineWord
		if d.lineDiff {
			d.view.Inline = editor.DiffInlineNone
		}
		scholarDiffElement(c, d.view).Grow(1).FillWidth().MinHeight(0)
	})
	return true
}

func (s *Shell) scholarGutter(c *ui.Context, tab *Tab) {
	p := s.scholar
	if !isManuscript(tab) && (p == nil || p.changes.tabID != tab.ID) {
		return
	}
	st := tab.Source()
	if p == nil {
		st.GutterMarks, st.OnGutterClick = nil, nil
		return
	}
	st.GutterMarks = st.GutterMarks[:0]
	d := &p.changes
	if latest := d.latest(tab.ID); latest != nil && d.due.IsZero() && !d.running && len(d.waiting) == 0 {
		for _, line := range latest.lines {
			st.GutterMarks = append(st.GutterMarks, editor.GutterMark{Line: line, Color: scholarInk(c, "diff")})
		}
	}
	if set := p.agent.batch; set != nil {
		for _, h := range set.hunks {
			if h.target.tabID == tab.ID && h.target.version == st.Buffer.Version() {
				st.GutterMarks = append(st.GutterMarks, editor.GutterMark{Line: h.target.line, Color: scholarInk(c, "agent")})
			}
		}
	}
	if patch := p.agent.patch; patch != nil && patch.target.tabID == tab.ID && patch.target.version == st.Buffer.Version() {
		st.GutterMarks = append(st.GutterMarks, editor.GutterMark{Line: patch.target.line, Color: scholarInk(c, "agent")})
	}
	if st.OnGutterClick != nil {
		return
	}
	st.OnGutterClick = func(line int) {
		p := s.scholar
		if p == nil || s.activeTab() != tab {
			return
		}
		if set := p.agent.batch; set != nil {
			for i, h := range set.hunks {
				if h.target.tabID == tab.ID && h.target.line == line && h.target.version == st.Buffer.Version() {
					set.selected = i
					p.agent.reviewHidden = false
					p.drawer, p.compactNav = scholarCodex, false
					return
				}
			}
		}
		if patch := p.agent.patch; patch != nil && patch.target.tabID == tab.ID && patch.target.line == line &&
			patch.target.version == st.Buffer.Version() {
			p.drawer, p.compactNav = scholarCodex, false
			return
		}
		if latest := p.changes.latest(tab.ID); latest != nil && slices.Contains(latest.lines, line) {
			p.changes.peek = latest
			p.changes.peekView = nil
		}
	}
}

func (s *Shell) navigateScholarChange(p *scholarState, delta int) {
	d := &p.changes
	if !d.compare {
		s.openScholarChange(p, 0)
		return
	}
	if d.view == nil {
		return
	}
	hunks := d.view.Hunks()
	if len(hunks) == 0 {
		return
	}
	d.hunk = (d.hunk + len(hunks) + delta) % len(hunks)
	d.view.GoToHunk(d.hunk)
}

func scholarChangeLines(change *scholarChange) {
	change.lines = nil
	for _, hunk := range scholarDiffView(change.before, change.after).Hunks() {
		change.lines = append(change.lines, change.line+hunk.NewStart)
		if len(change.lines) >= 128 {
			break
		}
	}
	if len(change.lines) == 0 {
		change.lines = []int{change.line}
	}
}

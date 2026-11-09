package shell

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"nomo/desktop/internal/scholar"
)

func (s *Shell) scholarContextActions(c *ui.Context, p *scholarState) {
	a := &p.agent
	tab, report := s.activeTab(), p.diagnostics
	if pane := s.compile; pane != nil && pane.tab == tab && !pane.busy {
		report = pane.report()
	}
	hasDiagnostics := tab != nil && report.tabID == tab.ID && samePath(report.source, tab.Path) &&
		(report.message != "" || len(report.warnings) > 0)
	ui.Row(c).Gap(4).Children(func() {
		if ui.Button(c, "+ @preamble").Disabled(p.busy).Clicked() {
			s.loadScholarPreamble(p)
		}
		if ui.Button(c, "+ @zotero").Disabled(p.busy || p.library == nil).Clicked() {
			s.loadScholarReferenceContext(p)
		}
		if ui.Button(c, "+ @diag").Disabled(p.busy || !hasDiagnostics).Clicked() {
			text := report.message
			for _, note := range report.warnings {
				if len(text)+len(note.Text) > 8<<10 {
					break
				}
				text += "\n" + note.Text
			}
			a.parts[3], a.include[3] = text, true
			a.attachments = false
			a.resetSession()
		}
	})
	ui.Text(c, s.tr("scholarContextBounds")).FontSize(11).Wrap()
}

func (s *Shell) loadScholarPreamble(p *scholarState) {
	project := p.project
	var snap editor.Snapshot
	opened := s.scholarTab(project.Main)
	if opened != nil {
		snap = opened.Source().Buffer.Snapshot()
	}
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		var text string
		if opened != nil {
			text = snap.Slice(0, min(snap.Len(), 8<<10))
		} else {
			name, err := scholar.Relative(project.Root, project.Main)
			if err != nil {
				return nil, err
			}
			path, err := project.Path(name)
			if err != nil {
				return nil, err
			}
			f, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			info, err := f.Stat()
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("preamble requires a regular file")
			}
			data, err := io.ReadAll(io.LimitReader(f, 8<<10))
			if err != nil {
				return nil, err
			}
			text = string(data)
		}
		if at := strings.Index(text, `\begin{document}`); at >= 0 {
			text = text[:at]
		}
		return func() {
			if opened != nil && (s.tabWithID(opened.ID) != opened || opened.Source().Buffer.Version() != snap.Version()) {
				p.err = s.tr("scholarStaleBaseline")
				return
			}
			p.agent.parts[1], p.agent.include[1] = text, true
			p.agent.resetSession()
		}, ctx.Err()
	})
}
func (s *Shell) loadScholarReferenceContext(p *scholarState) {
	library := p.library
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		entries, err := library.Query(ctx, "", 512)
		var text strings.Builder
		for _, e := range entries {
			line := e.Key + " — " + e.Title + "\n"
			if text.Len()+len(line) > 8<<10 {
				break
			}
			text.WriteString(line)
		}
		return func() {
			if p.library == library {
				p.agent.parts[2], p.agent.include[2] = text.String(), true
				p.agent.resetSession()
			}
		}, err
	})
}

package shell

import (
	"context"

	"nomo/desktop/internal/scholar"
	"nomo/desktop/typeset"
)

type scholarBodySource struct {
	tab     *Tab
	path    string
	version uint64
}
type scholarBodyConfirmation struct {
	pane    *compilePane
	serial  uint64
	sources []scholarBodySource
	disk    string
}

func (s *Shell) confirmScholarBody(p *scholarState) {
	p.bodyConfirmed = nil
	pane := s.compile
	if pane == nil || pane.busy || pane.err != "" || pane.pages == 0 || pane.fullVersion != pane.version || pane.sourceVersion != pane.tab.Source().Buffer.Version() || !samePath(pane.path, p.project.Main) {
		p.err = s.tr("scholarBodyAwaiting")
		return
	}
	confirmation := &scholarBodyConfirmation{pane: pane, serial: pane.fullSerial}
	for _, t := range s.tabs {
		if _, err := scholar.Relative(p.project.Root, t.Path); err == nil {
			confirmation.sources = append(confirmation.sources, scholarBodySource{t, t.Path, scholarVersion(t)})
		}
	}
	project := p.project
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		stamp, err := project.InputStamp(ctx)
		return func() {
			if err != nil {
				return
			}
			if s.compile != pane || pane.busy || pane.fullSerial != confirmation.serial || (pane.projectInput != nil && stamp != p.previewStamp) {
				p.err = s.tr("scholarBodyAwaiting")
				return
			}
			confirmation.disk = stamp
			for _, source := range confirmation.sources {
				if s.tabWithID(source.tab.ID) != source.tab || !samePath(source.tab.Path, source.path) || scholarVersion(source.tab) != source.version {
					p.err = s.tr("scholarBodyAwaiting")
					return
				}
			}
			p.bodyConfirmed = confirmation
		}, err
	})
}
func (s *Shell) scholarBodyPages(p *scholarState, pane *compilePane, endPage int) (int, bool) {
	confirmed := p.bodyConfirmed
	if confirmed == nil {
		return 0, false
	}
	if pane != confirmed.pane || pane.fullSerial != confirmed.serial || pane.fullVersion != pane.version || pane.sourceVersion != pane.tab.Source().Buffer.Version() {
		p.bodyConfirmed = nil
		return 0, false
	}
	if p.agent.trial.show {
		return 0, false
	}
	for _, source := range confirmed.sources {
		if s.tabWithID(source.tab.ID) != source.tab || !samePath(source.tab.Path, source.path) || scholarVersion(source.tab) != source.version {
			p.bodyConfirmed = nil
			return 0, false
		}
	}
	// Opening another unsaved input also invalidates the observation.
	for _, t := range s.tabs {
		if _, err := scholar.Relative(p.project.Root, t.Path); err != nil {
			continue
		}
		if !t.Dirty() {
			continue
		}
		found := false
		for _, source := range confirmed.sources {
			if source.tab == t {
				found = true
				break
			}
		}
		if !found {
			p.bodyConfirmed = nil
			return 0, false
		}
	}
	return typeset.BodyPages(pane.pages, endPage)
}

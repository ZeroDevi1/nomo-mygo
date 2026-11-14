package shell

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"nomo/desktop/internal/scholar"
	"nomo/desktop/typeset"
)

type scholarPreviewInput struct {
	tabID   int
	version uint64
	snap    editor.Snapshot
}

type scholarPreviewRevision struct {
	disk   uint64
	inputs map[string]scholarPreviewInput
	err    error
}

func (r *scholarPreviewRevision) readers() map[string]typeset.ProjectBuffer {
	out := make(map[string]typeset.ProjectBuffer, len(r.inputs))
	for name, input := range r.inputs {
		out[name] = typeset.ProjectBuffer{
			Reader: &scholarSnapshotReader{snap: input.snap}, TabID: input.tabID, Version: input.version,
		}
	}
	return out
}

func (r *scholarPreviewRevision) onlySourceChanged(base *scholarPreviewRevision, source string) bool {
	if base == nil || r.disk != base.disk || r.err != nil {
		return false
	}
	for name, input := range r.inputs {
		if name != source {
			old, ok := base.inputs[name]
			if !ok || old.tabID != input.tabID || old.version != input.version {
				return false
			}
		}
	}
	for name := range base.inputs {
		if _, ok := r.inputs[name]; name != source && !ok {
			return false
		}
	}
	return true
}

func (s *Shell) typesetProject(t *Tab) scholar.Project {
	p := s.scholarForTab(t)
	if p == nil || !isManuscript(t) {
		return scholar.Project{}
	}
	if p.mainSelected || !samePath(t.Path, p.project.Main) || len(p.bibNames) > 0 {
		return p.project
	}
	for _, candidate := range s.tabs {
		if candidate != t && isManuscript(candidate) {
			if _, err := scholar.Relative(p.project.Root, candidate.Path); err == nil {
				return p.project
			}
		}
	}
	return scholar.Project{}
}

func scholarPreviewInputFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tex", ".ltx", ".typ", ".bib", ".biblatex", ".yaml", ".yml", ".json", ".sty", ".cls", ".bst", ".csv", ".txt", ".md":
		return true
	}
	return false
}

func (s *Shell) projectPreviewRevision(p *compilePane) *scholarPreviewRevision {
	disk := uint64(0)
	if s.scholar != nil {
		disk = s.scholar.previewDisk
	}
	old := p.projectInput
	count := 0
	changed := old == nil || old.disk != disk
	total := 0
	for _, t := range s.tabs {
		if !scholarPreviewInputFile(t.Path) || !t.Dirty() {
			continue
		}
		name, err := scholar.Relative(p.project.Root, t.Path)
		if err != nil {
			continue
		}
		count++
		if !changed {
			prior, ok := old.inputs[name]
			changed = !ok || prior.tabID != t.ID || prior.version != scholarVersion(t)
		}
	}
	if !changed && count == len(old.inputs) {
		return old
	}
	r := &scholarPreviewRevision{disk: disk, inputs: make(map[string]scholarPreviewInput, count)}
	for _, t := range s.tabs {
		if !scholarPreviewInputFile(t.Path) || !t.Dirty() {
			continue
		}
		name, err := scholar.Relative(p.project.Root, t.Path)
		if err != nil {
			continue
		}
		// Markdown-like project inputs use their semantic document as the source of truth.
		if !t.plain {
			t.syncToSource()
		}
		buf := t.Source().Buffer
		total += buf.Len()
		if total > 16<<20 {
			r.err = errors.New(s.tr("scholarTrialBufferLimit"))
			r.inputs[name] = scholarPreviewInput{tabID: t.ID, version: scholarVersion(t)}
			continue
		}
		r.inputs[name] = scholarPreviewInput{tabID: t.ID, version: scholarVersion(t), snap: buf.Snapshot()}
	}
	return r
}

func (s *Shell) queuePreview(p *compilePane, revision previewRevision) {
	select {
	case <-p.liveUpdates:
	default:
	}
	p.cancelBuild()
	revision.semantic = p.semantic
	revision.ctx, p.cancelBuild = context.WithCancel(p.lifetime)
	p.version, p.projectInput, p.sourceVersion = revision.version, revision.project, revision.snap.Version()
	p.busy = true
	p.loading = 0
	p.anchor, p.paragraph, p.inlineText = typeset.PageAnchor{}, nil, ""
	p.liveUpdates <- revision
}

func (s *Shell) updateProjectPreview(p *compilePane) {
	if p.liveUpdates == nil || p.stopped {
		return
	}
	inputs := s.projectPreviewRevision(p)
	buf := p.tab.Source().Buffer
	if inputs != p.projectInput || buf.Version() != p.sourceVersion {
		s.queuePreview(p, previewRevision{snap: buf.Snapshot(), version: p.version + 1, project: inputs})
	}
}

func (s *Shell) previewBuffersCurrent(p *compilePane) bool {
	return p.tab.Source().Buffer.Version() == p.sourceVersion &&
		(p.projectInput == nil || s.projectPreviewRevision(p) == p.projectInput)
}

func (s *Shell) hideTypesetPreview(workspace *scholarState, tab *Tab) {
	p := s.compile
	if p == nil {
		return
	}
	p.pageEdit = nil
	covered := workspace != nil && (workspace.drawer != scholarClosed || workspace.references.catalog ||
		workspace.references.focused != nil || workspace.accountOpen || workspace.winSettings != nil ||
		workspace.winCompactProject || tab != nil && (tab.typesetMode == typesetSplit && !tab.typesetCompactPDF ||
		s.scholarReviewPatch(workspace, tab) != nil))
	if !covered ||
		workspace.agent.trial.show ||
		p.tab != tab || tab.typesetMode == typesetSource || tab.typesetMode == typesetSemantic || tab.typesetPaused || p.reuseChecks == nil ||
		p.project != s.typesetProject(tab) || s.previewProvider(&Tab{Path: p.path}) != p.provider ||
		p.busy || p.stopped || p.err != "" || p.image == nil || p.fullVersion != p.version || !s.previewBuffersCurrent(p) {
		s.closeCompile()
		return
	}
	if p.parkedPage == 0 {
		first, last := p.list.Visible()
		p.parkedPage = p.page
		if first <= last {
			p.parkedPage = first + 1
		}
		tab.typesetPage, tab.typesetPagePath = p.parkedPage, p.path
		p.pageNavigation.open = false
		p.sync.linked = false
		p.sync.invalidate()
		p.reuseChecks <- nil
	}
}

func (s *Shell) restoreTypesetPreview(p *compilePane) {
	if p.stopped {
		s.closeCompile()
		return
	}
	if p.checkingPreview {
		return
	}
	s.checkExternalChanges()
	if !s.previewBuffersCurrent(p) {
		s.closeCompile()
		return
	}
	p.checkingPreview = true
	p.reuseChecks <- func(current bool) {
		s.post(func() {
			if s.compile != p {
				return
			}
			p.checkingPreview = false
			if !current || s.activeTab() != p.tab || p.tab.Path != p.sourcePath || !s.previewBuffersCurrent(p) {
				s.closeCompile()
				return
			}
			if s.scholarForTab(p.tab) != nil && p.parkedPage != 0 {
				p.list.ScrollTo(p.parkedPage-1, ui.Start)
				p.parkedPage = 0
			}
		})
	}
}

func (s *Shell) jumpProjectPreview(p *compilePane, location typeset.ProjectLocation, page int, scroll bool) {
	if location.Path == "" || s.compile != p || p.busy || p.fullVersion != p.version {
		return
	}
	name, err := scholar.Relative(p.project.Root, location.Path)
	if err != nil {
		return
	}
	if _, err := p.project.Path(name); err != nil {
		return
	}
	if t := s.scholarTab(location.Path); t == nil || t != s.activeTab() {
		if scroll {
			return
		}
		s.OpenFiles([]string{location.Path})
	}
	t := s.activeTab()
	if !isManuscript(t) || !samePath(t.Path, location.Path) {
		return
	}
	snap := t.Source().Buffer.Snapshot()
	if location.Line < 0 || location.Line >= snap.LineCount() ||
		strings.TrimSuffix(strings.TrimPrefix(snap.Line(location.Line), "\ufeff"), "\r") != location.Text {
		return
	}
	p.sync.ignoreSource = 2
	if scroll {
		t.Source().ScrollTo(location.Line)
		return
	}
	if t != p.tab {
		t.typesetPage, t.typesetPagePath = page, p.path
	}
	t.typesetMode = typesetSplit
	s.jumpScholarOutline(t, snap.LineStart(location.Line))
}

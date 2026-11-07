package shell

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"nomo/desktop/internal/scholar"
	"nomo/desktop/typeset"
)

const (
	scholarFiles = iota
	scholarLiterature
	scholarChanges
	scholarOutline
)

const (
	scholarClosed = iota
	scholarReader
	scholarCodex
	scholarDiagnostics
)

// scholarSelectIndex keeps distinct choices selectable when display names repeat.
func scholarSelectIndex(c *ui.Context, selected *int, labels []string, disabled bool) bool {
	if len(labels) == 0 {
		return false
	}
	if *selected < 0 || *selected >= len(labels) {
		*selected = 0
	}
	seen := make(map[string]bool, len(labels))
	for _, label := range labels {
		if seen[label] {
			labels = slices.Clone(labels)
			for i, name := range labels {
				labels[i] = strconv.Itoa(i+1) + ". " + name
			}
			break
		}
		seen[label] = true
	}
	current := labels[*selected]
	if ui.Select(c, &current, labels).FillWidth().Disabled(disabled).Changed() {
		if i := slices.Index(labels, current); i >= 0 {
			*selected = i
			return true
		}
	}
	return false
}

type scholarState struct {
	project      scholar.Project
	activeTabID  int
	mainSelected bool
	previewDisk  uint64
	previewStamp string
	nav, drawer  int
	compactNav   bool
	compactPDF   bool
	drawerSize   float32
	drawerSave   time.Time

	ctx               context.Context
	cancel, cancelJob context.CancelFunc
	job               uint64
	busy              bool
	err               string

	library             *scholar.Library
	libraryReady        bool
	libraryError        string
	bibNames            []string
	mainVersion         uint64
	mainTabID           int
	bibVersions         map[int]uint64
	reloadAt            time.Time
	citations           scholarCitations
	literature          scholarLiteratureState
	changes             scholarChangeState
	navigation          scholarNavigation
	agent               scholarAgentState
	budget              string
	bodyBoundary        string
	bodyConfirmed       *scholarBodyConfirmation
	excludeBibliography bool
	watchAt, probeAt    time.Time
	watchPending        bool
	watchSignature      string
}

type scholarCitations struct {
	tabID          int
	version        uint64
	library        *scholar.Library
	counts         map[string]int
	pending        bool
	unknown        int
	retry          time.Time
	ranges         []scholar.Citation
	ready, counted bool
	words, chars   int
}

func isManuscript(tab *Tab) bool {
	if tab == nil {
		return false
	}
	ext := strings.ToLower(filepath.Ext(tab.Path))
	return typeset.IsLaTeXPath(tab.Path) || ext == ".typ"
}

func (s *Shell) closeScholar() {
	if p := s.scholar; p != nil {
		p.cancel()
		s.persistScholarDrawer(p)
		s.clearScholarEditor(p)
		go p.library.Close()
		s.scholar = nil
	}
}

func (s *Shell) clearScholarEditor(p *scholarState) {
	if t := s.tabWithID(p.citations.tabID); t != nil && t.source != nil {
		t.source.SetDiagnostics(t.source.Buffer.Version(), nil)
		t.source.ShowCompletions(-1, nil)
		t.source.ShowHover(0, 0, "", nil)
		t.source.Run(editor.Cancel)
		t.source.GutterMarks, t.source.OnGutterClick = nil, nil
	}
}

func (s *Shell) updateScholar(c *ui.Context, tab *Tab) {
	if s.post == nil {
		return
	}
	p := s.scholar
	if tab == nil || s.mini.active || s.hiddenToTray || tab.partner != 0 {
		s.closeScholar()
		return
	}
	root := filepath.Dir(tab.Path)
	folderProject := false
	if s.folder != nil {
		if _, err := scholar.Relative(s.folder.path, tab.Path); err == nil {
			root, folderProject = s.folder.path, true
		}
	}
	if !folderProject {
		hasActiveRoot := false
		if p != nil {
			if _, err := scholar.Relative(p.project.Root, tab.Path); err == nil {
				root, hasActiveRoot = p.project.Root, true
			}
		}
		if !hasActiveRoot && isManuscript(tab) {
			best := ""
			for candidate, pref := range s.prefs.ScholarProjects {
				if !filepath.IsLocal(pref.Main) || len(candidate) <= len(best) {
					continue
				}
				if _, err := scholar.Relative(candidate, tab.Path); err == nil {
					best = candidate
				}
			}
			if best != "" {
				root = best
			}
		}
	}
	if p != nil && !samePath(root, p.project.Root) {
		s.closeScholar()
		p = nil
	}
	if !isManuscript(tab) {
		if p != nil {
			for _, name := range p.bibNames {
				if samePath(filepath.Join(p.project.Root, name), tab.Path) {
					p.activeTabID = tab.ID
					s.observeScholarChanges(c, p, tab)
					return
				}
			}
			s.closeScholar()
		}
		return
	}
	if p == nil {
		ctx, cancel := context.WithCancel(context.Background())
		p = &scholarState{project: scholar.Project{Root: root, Main: tab.Path}, ctx: ctx, cancel: cancel}
		pref := s.prefs.ScholarProjects[root]
		if filepath.IsLocal(pref.Main) {
			main := filepath.Join(root, pref.Main)
			if isManuscript(&Tab{Path: main}) {
				p.project.Main = main
			}
		}
		p.mainSelected = pref.MainSelected
		p.budget = strconv.Itoa(p.pageBudget(s.prefs))
		p.bodyBoundary = strconv.Itoa(pref.BodyEndPage)
		p.excludeBibliography = pref.ExcludeBibliography
		s.scholar = p
	}
	p.activeTabID = tab.ID
	if p.agent.trial.pending && (p.agent.trial.ownerPatch != nil && p.agent.trial.ownerPatch != p.agent.patch || p.agent.trial.ownerBatch != nil && p.agent.trial.ownerBatch != p.agent.batch) {
		p.agent.trial = scholarTrialView{}
	}
	if p.agent.trial.show {
		for id, v := range p.agent.trial.versions {
			t := s.tabWithID(id)
			if t == nil || t != p.agent.trial.identities[id] || !samePath(t.Path, p.agent.trial.paths[id]) || t.Source().Buffer.Version() != v {
				p.agent.trial = scholarTrialView{}
				break
			}
		}
	}

	if !p.busy {
		main := s.scholarTab(p.project.Main)
		mainChanged := main != nil && (p.mainTabID != main.ID || p.mainVersion != main.Source().Buffer.Version())
		bibChanged := false
		for id, v := range p.bibVersions {
			if t := s.tabWithID(id); t == nil || scholarVersion(t) != v {
				bibChanged = true
				break
			}
		}
		if !bibChanged {
			for _, name := range p.bibNames {
				if t := s.scholarTab(filepath.Join(p.project.Root, name)); t != nil {
					if _, tracked := p.bibVersions[t.ID]; !tracked {
						bibChanged = true
						break
					}
				}
			}
		}
		if (mainChanged || bibChanged) && p.libraryReady {
			if p.reloadAt.IsZero() {
				p.reloadAt = c.Now().Add(450 * time.Millisecond)
			}
			if c.Now().Before(p.reloadAt) {
				c.After(p.reloadAt.Sub(c.Now()))
			} else {
				s.loadScholarLibrary(p, bibChanged)
			}
		} else if !p.libraryReady {
			s.loadScholarLibrary(p, true)
		}
	}
	s.observeScholarSources(c, p)
	if !p.busy && p.literature.refreshPending && p.nav == scholarLiterature && p.literature.mode == 1 {
		l := &p.literature
		l.refreshPending = false
		start := l.page
		if l.fulltext {
			start = 0
		}
		s.loadZotero(p, false, l.collections, l.collection, l.sentQuery, start)
	}
	s.observeScholarChanges(c, p, tab)
	s.scholarCitationDiagnostics(c, p, tab)
}

func scholarVersion(t *Tab) uint64 {
	if t.plain {
		return t.Source().Buffer.Version()
	}
	return t.Doc.Version()
}

func (s *Shell) scholarTab(path string) *Tab {
	for _, t := range s.tabs {
		if t.Path != "" && samePath(t.Path, path) {
			return t
		}
	}
	return nil
}

func (p *scholarState) cancelCurrent() {
	if p.cancelJob != nil {
		p.cancelJob()
	}
	p.job++
	p.busy = false
	p.cancelJob = nil
}

// A project's single foreground task owns its result. Closing the project or
// cancelling the task invalidates any result still queued on the UI thread.
func (s *Shell) scholarJob(p *scholarState, work func(context.Context, uint64) (func(), error)) {
	s.scholarOwnedJob(p, func(ctx context.Context, id uint64) (func(), func(), error) {
		apply, err := work(ctx, id)
		return apply, nil, err
	})
}

func (s *Shell) scholarOwnedJob(p *scholarState, work func(context.Context, uint64) (func(), func(), error)) {
	if p.busy {
		return
	}
	p.busy, p.err = true, ""
	p.job++
	job := p.job
	ctx, cancel := context.WithCancel(p.ctx)
	p.cancelJob = cancel
	go func() {
		apply, discard, err := work(ctx, job)
		var claimed atomic.Bool
		release := func() {
			if claimed.CompareAndSwap(false, true) && discard != nil {
				discard()
			}
		}
		stopRelease := func() bool { return false }
		if discard != nil {
			stopRelease = context.AfterFunc(ctx, release)
		}
		s.post(func() {
			defer cancel()
			defer stopRelease()
			if s.scholar != p || p.job != job {
				release()
				return
			}
			p.busy, p.cancelJob = false, nil
			if err != nil {
				p.err = err.Error()
			}
			if discard != nil && !claimed.CompareAndSwap(false, true) {
				return
			}
			if apply != nil {
				apply()
			}
		})
	}()
}

func (s *Shell) loadScholarLibrary(p *scholarState, force bool) {
	if p.busy {
		return
	}
	project := p.project
	priorNames, priorReady := p.bibNames, p.libraryReady
	var main editor.Snapshot
	hasMain := false
	if t := s.scholarTab(project.Main); t != nil {
		main, hasMain = t.Source().Buffer.Snapshot(), true
		p.mainTabID, p.mainVersion = t.ID, main.Version()
	}
	p.reloadAt = time.Time{}
	versions := make(map[string]struct {
		id      int
		version uint64
	})
	// Capture immutable open buffers before handing off filesystem parsing.
	type input struct {
		snap  editor.Snapshot
		text  string
		plain bool
	}
	inputs := make(map[string]input)
	for _, t := range s.tabs {
		switch strings.ToLower(filepath.Ext(t.Path)) {
		case ".bib", ".biblatex", ".json", ".yml", ".yaml":
		default:
			continue
		}
		name, err := scholar.Relative(project.Root, t.Path)
		if err != nil {
			continue
		}
		versions[name] = struct {
			id      int
			version uint64
		}{t.ID, scholarVersion(t)}
		if t.plain {
			snap := t.Source().Buffer.Snapshot()
			inputs[name] = input{snap: snap, plain: true}
		} else {
			text := t.Doc.Source()
			inputs[name] = input{text: text}
		}
	}
	s.scholarOwnedJob(p, func(ctx context.Context, _ uint64) (func(), func(), error) {
		var source string
		var names []string
		var library *scholar.Library
		var err error
		if hasMain {
			if main.Len() > scholar.IndexBudget {
				err = fmt.Errorf("主文件超过文献扫描上限")
			} else {
				source = main.String()
			}
		} else {
			name, pathErr := scholar.Relative(project.Root, project.Main)
			if pathErr == nil {
				var path string
				path, pathErr = project.Path(name)
				if pathErr == nil {
					var data []byte
					data, pathErr = scholar.Read(path, scholar.IndexBudget)
					source = string(data)
				}
			}
			err = pathErr
		}
		if err == nil {
			names, err = project.Bibliographies(source)
		}
		if err == nil && !force && priorReady && slices.Equal(names, priorNames) {
			return nil, nil, nil
		}
		if err == nil {
			buffers := make(map[string]string)
			remaining := 64 << 20
			for _, name := range names {
				in, ok := inputs[name]
				if !ok {
					continue
				}
				size := len(in.text)
				if in.plain {
					size = in.snap.Len()
				}
				if size > remaining {
					err = fmt.Errorf("打开的文献缓冲超过 64 MiB 读取上限")
					break
				}
				remaining -= size
				if in.plain {
					buffers[name] = in.snap.String()
				} else {
					buffers[name] = in.text
				}
			}
			if err == nil {
				library, err = project.Library(ctx, names, buffers)
			}
		}
		return func() {
			if p.library != library {
				p.literature.search.clear()
				p.literature.inline.results.clear()
				p.navigation.search.clear()
				old := p.library
				go old.Close()
			}
			p.library, p.bibNames, p.libraryReady = library, names, true
			p.bibVersions = make(map[int]uint64)
			for _, name := range names {
				if version, ok := versions[name]; ok {
					p.bibVersions[version.id] = version.version
				}
			}
			p.literature.visible = nil
			p.literature.local = scholarLocalSearch{enabled: p.literature.local.enabled}
			p.citations.library = nil
			p.citations.ready = false
			p.libraryError = ""
			if err != nil {
				p.libraryError = err.Error()
			}
			p.literature.checked = nil
			p.citations.retry = time.Time{}
			if library == nil {
				s.clearScholarEditor(p)
				p.citations = scholarCitations{}
			}
		}, func() { library.Close() }, nil
	})
}

func (s *Shell) scholarSidebarHidden(c *ui.Context) bool {
	w, _ := c.Size()
	if p := s.scholarForTab(s.activeTab()); p != nil && w < scholarCompactWidth {
		return !p.compactNav
	}
	return s.prefs.SidebarHidden
}

func (s *Shell) scholarToggleSidebar(c *ui.Context) {
	w, _ := c.Size()
	if p := s.scholarForTab(s.activeTab()); p != nil && w < scholarCompactWidth {
		p.compactNav = !p.compactNav
		return
	}
	s.toggleSidebar()
}

func scholarInk(c *ui.Context, channel string) ui.Color {
	if c.Theme().Dark {
		switch channel {
		case "citation":
			return ui.RGB(240, 184, 110)
		case "agent":
			return ui.RGB(159, 178, 255)
		case "diff":
			return ui.RGB(128, 198, 255)
		case "addition":
			return ui.RGB(126, 226, 168)
		case "deletion":
			return ui.RGB(255, 155, 142)
		case "warning":
			return ui.RGB(245, 196, 81)
		default:
			return c.Theme().Accent
		}
	}
	switch channel {
	case "citation":
		return ui.RGB(138, 67, 14)
	case "agent":
		return ui.RGB(54, 73, 168)
	case "diff":
		return ui.RGB(11, 92, 173)
	case "addition":
		return ui.RGB(20, 99, 58)
	case "deletion":
		return ui.RGB(166, 36, 25)
	case "warning":
		return ui.RGB(133, 82, 0)
	default:
		return c.Theme().Accent
	}
}

func (s *Shell) scholarForTab(tab *Tab) *scholarState {
	if p := s.scholar; p != nil && tab != nil && p.activeTabID == tab.ID {
		return p
	}
	return nil
}

var scholarDestinations = [...]struct{ icon, label string }{
	{"folder", "scholarFiles"}, {"book-open", "scholarLiterature"},
	{"file-diff", "scholarChanges"}, {"list-tree", "scholarOutline"},
}

func (s *Shell) showScholarNavigation(p *scholarState, destination int, width float32) {
	p.nav = destination
	if width < scholarCompactWidth {
		p.compactNav = true
	} else if s.prefs.SidebarHidden {
		s.toggleSidebar()
	}
}

func (s *Shell) scholarNav(c *ui.Context, rail bool) {
	p := s.scholar
	showLabels := !rail && s.sidebarW >= 144
	w, _ := c.Size()
	for i, item := range scholarDestinations {
		ck := c.Key(i)
		title, selected := s.tr(item.label), p.nav == i
		b := ui.ButtonBase(ck).Height(s.pf.scholarNavHeight()).FillWidth().
			Radius(c.Theme().Radius).FocusRing(true).Label(title).Selected(selected).Tooltip(title)
		if selected {
			b.Background(s.ch.pressed)
		} else if b.Hovered() {
			b.Background(s.ch.hover)
		}
		b.Children(func() {
			row := ui.Row(ck).FillWidth().AlignItems(ui.Center).Gap(8).Padding(0, 8)
			if !showLabels {
				row.Justify(ui.Center)
			}
			row.Children(func() {
				if s.pf.windows && selected {
					ui.Box(ck).Absolute().Left(0).Width(3).Height(16).Radius(2).Background(s.ch.accent)
				}
				color := s.ch.muted
				if selected {
					color = s.ch.accent
				}
				ui.Icon(ck, s.icon(item.icon)).FontSize(16).TextColor(color)
				if showLabels {
					label := ui.Text(ck, title).FontSize(13).SingleLine().MinWidth(0).Shrink(1)
					if selected {
						label.Bold()
					}
				}
			})
		})
		if b.Clicked() {
			s.showScholarNavigation(p, i, w)
		}
	}
}

func (s *Shell) scholarSidebar(c *ui.Context, p *scholarState) bool {
	ui.Column(c).FillWidth().Gap(2).Padding(4, 8, 6, 8).Children(func() { s.scholarNav(c, false) })
	ui.Box(c).FillWidth().Height(1).Background(s.ch.separator)
	switch p.nav {
	case scholarLiterature:
		s.scholarLibraryView(c, p)
	case scholarChanges:
		s.scholarChangesView(c, p)
	case scholarOutline:
		if tab := s.activeTab(); tab != nil {
			s.scholarOutlineView(c, p, tab)
		}
	default:
		s.scholarMainStatus(c, p)
		return false
	}
	return true
}

func (s *Shell) scholarMainStatus(c *ui.Context, p *scholarState) {
	menu := ui.ButtonBase(c).Height(sidebarHd).FillWidth().Padding(0, 8).Margin(4, 6, 0, 6).
		Radius(c.Theme().Radius).FocusRing(true).Tooltip(s.tr("scholarCurrentMain") + "\n" + p.project.Main)
	if menu.Hovered() {
		menu.Background(s.ch.hover)
	}
	menu.Children(func() {
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, s.icon("file-text")).FontSize(14).TextColor(s.ch.muted)
			if s.sidebarW >= 196 {
				ui.Text(c, s.tr("scholarCurrentMain")).FontSize(11).TextColor(s.ch.muted).SingleLine()
			}
			ui.Text(c, filepath.Base(p.project.Main)).FontSize(12).SingleLine().MinWidth(0).Grow(1)
			ui.Icon(c, s.icon("chevron-down")).FontSize(12).TextColor(s.ch.muted)
		})
	})
	menu.Menu(func(m *ui.Menu) {
		tab := s.activeTab()
		m.Item(s.tr("scholarCurrentMain") + " · " + filepath.Base(p.project.Main)).Disabled(true)
		m.Separator()
		if m.Item(s.tr("scholarOpenMain")).Disabled(s.scholarForTab(tab) != p).Chosen() {
			s.OpenFiles([]string{p.project.Main})
		}
		canSet := s.scholarForTab(tab) == p && isManuscript(tab) && !p.busy &&
			(!p.mainSelected || !samePath(tab.Path, p.project.Main))
		if m.Item(s.tr("scholarSetCurrentMain")).Disabled(!canSet).Chosen() {
			if _, err := scholar.Relative(p.project.Root, tab.Path); err != nil {
				return
			}
			p.project.Main, p.mainSelected = tab.Path, true
			s.closeCompile()
			tab.typesetPaused = false
			p.libraryReady = false
			p.agent.resetSession()
			p.agent = scholarAgentState{}
			s.storeScholarProject(p, p.pageBudget(s.prefs))
		}
		if s.folder == nil && m.Item(s.tr("scholarOpenProjectFolder")).Chosen() {
			s.openFolder(p.project.Root)
		}
	})
}

func (p *scholarState) pageBudget(prefs *Prefs) int {
	return min(10000, max(0, prefs.ScholarProjects[p.project.Root].PageBudget))
}

func (s *Shell) storeScholarProject(p *scholarState, budget int) {
	if s.prefs.ScholarProjects == nil {
		s.prefs.ScholarProjects = make(map[string]scholarProjectPrefs)
	}
	main, _ := scholar.Relative(p.project.Root, p.project.Main)
	pref := s.prefs.ScholarProjects[p.project.Root]
	pref.Main, pref.PageBudget, pref.DrawerWidth = main, budget, p.drawerWidth(s.prefs)
	pref.MainSelected = p.mainSelected
	s.prefs.ScholarProjects[p.project.Root] = pref
	s.setPref("scholarProjects")
}

func (s *Shell) scholarInspectorVisible(p *scholarState, width float32) bool {
	return p.drawer != scholarClosed && !(width < scholarCompactWidth && p.compactNav)
}

func (s *Shell) showScholarDiagnostics(p *scholarState) {
	// 打开检查器可能让双栏降为紧凑模式。保留当前 PDF 会话，使错误和定位仍可查看。
	if pane := s.compile; pane != nil && pane.tab == s.activeTab() && pane.tab.typesetMode == typesetSplit {
		p.compactPDF = true
	}
	p.drawer, p.compactNav = scholarDiagnostics, false
}

func (s *Shell) scholarToggleDrawer(p *scholarState, drawer int) {
	width := scholarCompactWidth
	if s.win != nil {
		width, _ = s.win.ContentSize()
	}
	if p.drawer == drawer && s.scholarInspectorVisible(p, float32(width)) {
		s.persistScholarDrawer(p)
		p.drawer = scholarClosed
	} else if drawer == scholarDiagnostics {
		s.showScholarDiagnostics(p)
	} else {
		p.drawer, p.compactNav = drawer, false
	}
}

func (p *scholarState) drawerWidth(prefs *Prefs) int {
	width := prefs.ScholarProjects[p.project.Root].DrawerWidth
	if width == 0 {
		return 320
	}
	return max(320, min(width, 420))
}

func (s *Shell) scholarInspectorWidth(p *scholarState, window float32) float32 {
	width := p.drawerSize
	if width == 0 {
		width = float32(p.drawerWidth(s.prefs))
	}
	return min(width, max(240, window-340))
}

func (s *Shell) scholarInspector(c *ui.Context, p *scholarState) {
	w, _ := c.Size()
	if !s.scholarInspectorVisible(p, w) {
		return
	}
	if p.drawerSize == 0 {
		p.drawerSize = float32(p.drawerWidth(s.prefs))
	}
	grip := ui.Box(c).Width(5).FillHeight().Focusable().Cursor(ui.CursorResizeEW).Background(s.ch.separator)
	dx, _, held := grip.Dragged()
	if held && dx != 0 {
		p.drawerSize = max(320, min(420, p.drawerSize-dx))
		p.drawerSave = c.Now().Add(splitSaveDelay)
	}
	if grip.Shortcut(0, ui.KeyLeft) {
		p.drawerSize = min(420, p.drawerSize+10)
		p.drawerSave = c.Now().Add(splitSaveDelay)
	}
	if grip.Shortcut(0, ui.KeyRight) {
		p.drawerSize = max(320, p.drawerSize-10)
		p.drawerSave = c.Now().Add(splitSaveDelay)
	}
	if !p.drawerSave.IsZero() {
		if c.Now().Before(p.drawerSave) {
			c.After(p.drawerSave.Sub(c.Now()))
		} else {
			pref := s.prefs.ScholarProjects[p.project.Root]
			pref.DrawerWidth = int(p.drawerSize)
			if s.prefs.ScholarProjects == nil {
				s.prefs.ScholarProjects = make(map[string]scholarProjectPrefs)
			}
			s.prefs.ScholarProjects[p.project.Root] = pref
			s.setPref("scholarProjects")
			p.drawerSave = time.Time{}
		}
	}
	ui.Column(c).Width(s.scholarInspectorWidth(p, w)).FillHeight().MinHeight(0).Clip().Background(s.ch.content).Children(func() {
		ui.Row(c).Height(s.pf.scholarNavHeight()).FillWidth().Padding(0, 8).Gap(4).AlignItems(ui.Center).Children(func() {
			label := [...]string{"", "scholarLiterature", "scholarAgentName", "scholarDiagnostics"}[p.drawer]
			ui.Text(c, s.tr(label)).FontSize(12).Bold().SingleLine().Grow(1).MinWidth(0)
			if s.toolButton(c, "x", s.tr("scholarCloseDrawer"), false).Clicked() {
				s.scholarToggleDrawer(p, p.drawer)
				s.focusEditor = true
			}
		})
		ui.Box(c).Height(1).FillWidth().Background(s.ch.separator)
		ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
			ui.Column(c).FillWidth().Padding(8).Gap(6).Children(func() {
				switch p.drawer {
				case scholarReader:
					s.scholarReaderView(c, p)
				case scholarCodex:
					s.scholarAgentView(c, p)
				case scholarDiagnostics:
					s.scholarDiagnosticsView(c, p)
				}
			})
		})
	})
}

func (s *Shell) scholarStatus(c *ui.Context, p *scholarState, tab *Tab) {
	c = c.Key(tab.ID)
	width := s.scholarWorkspaceWidth(c)
	ui.Box(c).Height(1).FillWidth().Background(s.ch.separator)
	ui.Row(c).Height(statusH).FillWidth().Padding(0, 10).Gap(10).AlignItems(ui.Center).Children(func() {
		part := func(label, channel string) {
			ui.Text(c, label).FontSize(11).TextColor(scholarInk(c, channel)).SingleLine().MinWidth(0).Shrink(1)
		}
		if pane := s.compile; pane != nil && pane.tab == tab {
			switch {
			case pane.busy:
				s.scholarStatusButton(c, "loader-circle", "brand", s.tr("scholarCompiling"), s.tr("scholarStopTypeset")+" · "+pane.path).
					Menu(func(m *ui.Menu) { s.scholarCompileActions(m, tab) })
			case pane.err != "":
				if s.scholarStatusButton(c, "triangle-alert", "warning", s.tr("scholarCompileFailed"), s.tr("scholarOpenDiagnostics")+" · "+pane.path).Clicked() {
					s.showScholarDiagnostics(p)
				}
			case pane.pages > 0:
				label, channel := fmt.Sprintf(s.tr("scholarTotalPages"), pane.pages), "brand"
				measured, known := pane.pages, true
				if pref := s.prefs.ScholarProjects[p.project.Root]; pref.ExcludeBibliography {
					measured, known = s.scholarBodyPages(p, pane, pref.BodyEndPage)
					if known {
						label = fmt.Sprintf(s.tr("scholarBodyPages"), measured)
					} else {
						label = s.tr("scholarBodyUnconfirmed")
						channel = "warning"
					}
				}
				if limit := p.pageBudget(s.prefs); limit > 0 && known && samePath(pane.path, p.project.Main) {
					label += fmt.Sprintf(" / %d", limit)
					if measured > limit {
						channel = "warning"
					}
				}
				engine := "XeLaTeX"
				if strings.EqualFold(filepath.Ext(pane.path), ".typ") {
					engine = "Typst"
				}
				status := fmt.Sprintf("%s · %d ms · %s", engine, pane.elapsed.Milliseconds(), label)
				if len(pane.warnings) > 0 {
					if s.scholarStatusButton(c, "triangle-alert", "warning", status, s.tr("scholarOpenDiagnostics")+" · "+pane.path).Clicked() {
						s.showScholarDiagnostics(p)
					}
				} else {
					ui.Text(c, status).FontSize(11).TextColor(scholarInk(c, channel)).SingleLine().MinWidth(0).Shrink(1).
						Tooltip(s.tr("scholarTypesetSuccess") + " · " + pane.path)
				}
			}
		} else if tab.typesetPaused && s.canPreviewTypeset(tab) && tab.typesetMode != typesetSource &&
			(tab.typesetMode != typesetSplit || p.compactPDF || s.scholarCanvasWidth(c) >= 620) {
			if s.scholarStatusButton(c, "refresh-cw", "brand", s.tr("scholarTypesetPaused"), s.tr("scholarRebuildPreview")).Clicked() {
				tab.typesetPaused = false
			}
		}
		// 窄窗口只保留排版状态、未保存提示和缩放。各类详情仍可从对应导航与检查器打开。
		if width >= 660 {
			if p.changes.latest(tab.ID) != nil {
				b := ui.ButtonBase(c).Padding(2, 4).Tooltip(s.tr("scholarViewRecentEdit"))
				b.Children(func() { part(s.tr("scholarRecentEdit"), "diff") })
				if b.Clicked() {
					s.openScholarChange(p, 0)
				}
			}
			if p.literature.connected {
				part("Zotero", "citation")
			}
			if p.library != nil && len(p.bibNames) > 0 {
				part(fmt.Sprintf(s.tr("scholarReferenceCount"), p.library.Count()), "citation")
			}
			if p.citations.unknown > 0 {
				part(fmt.Sprintf(s.tr("scholarUnknownCount"), p.citations.unknown), "warning")
			}
			if set := p.agent.batch; set != nil {
				part(fmt.Sprintf(s.tr("scholarPendingCount"), len(set.hunks)), "agent")
			}
			if p.agent.patch != nil {
				part(s.tr("scholarOnePending"), "agent")
			}
		}
		ui.Spacer(c)
		if width >= 860 && p.citations.tabID == tab.ID && p.citations.counted {
			ui.Text(c, s.statsText(p.citations.words, p.citations.chars, tab.Source().Buffer.LineCount())).
				FontSize(11).TextColor(s.ch.muted).SingleLine().MinWidth(0).Shrink(1)
		}
		if tab.Dirty() {
			part(s.tr("scholarManuscriptUnsaved"), "warning")
		}
		s.zoomControl(c)
	})
}

func (s *Shell) scholarStatusButton(c *ui.Context, icon, channel, label, tip string) ui.Element {
	color := scholarInk(c, channel)
	b := ui.ButtonBase(c).Padding(2, 4).Radius(c.Theme().Radius).Label(label).Tooltip(tip).FocusRing(true).Shrink(0).
		MaxWidth(max(52, min(230, s.scholarWorkspaceWidth(c)-84)))
	if b.Hovered() {
		b.Background(s.ch.hover)
	}
	b.Children(func() {
		ui.Row(c).MinWidth(0).Gap(4).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, s.icon(icon)).FontSize(12).TextColor(color).Shrink(0)
			ui.Text(c, label).FontSize(11).TextColor(color).SingleLine().MinWidth(0).Shrink(1)
		})
	})
	return b
}

// Reserve closed targets between the UI preflight and the disk operation.
// The worker releases the reservation even if the window closes mid-operation.
var scholarWrites = struct {
	sync.Mutex
	paths map[string]bool
}{paths: make(map[string]bool)}

func scholarPathKey(path string) string {
	if path == "" {
		return ""
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	if canonical, err := filepath.EvalSymlinks(path); err == nil {
		path = canonical
	}
	return path
}

func scholarPathReserved(path string) bool {
	scholarWrites.Lock()
	defer scholarWrites.Unlock()
	if len(scholarWrites.paths) == 0 {
		return false
	}
	key := scholarPathKey(path)
	for name := range scholarWrites.paths {
		if samePath(key, name) {
			return true
		}
	}
	return false
}

func reserveScholarPaths(paths []string) (func(), bool) {
	scholarWrites.Lock()
	defer scholarWrites.Unlock()
	keys := make([]string, 0, len(paths))
	for _, path := range paths {
		key := scholarPathKey(path)
		for held := range scholarWrites.paths {
			if samePath(held, key) {
				return nil, false
			}
		}
		keys = append(keys, key)
	}
	for _, key := range keys {
		scholarWrites.paths[key] = true
	}
	return func() {
		scholarWrites.Lock()
		defer scholarWrites.Unlock()
		for _, key := range keys {
			delete(scholarWrites.paths, key)
		}
	}, true
}

func scholarFileOpen(path string) bool {
	key := scholarPathKey(path)
	for _, shell := range registry.shells {
		for _, tab := range shell.tabs {
			if tab.Path != "" && samePath(scholarPathKey(tab.Path), key) {
				return true
			}
		}
	}
	return false
}

func (s *Shell) scholarShortcuts(root ui.Element) {
	p := s.scholarForTab(s.activeTab())
	if p == nil || p.literature.picker || p.navigation.open {
		return
	}
	if root.Shortcut(ui.Cmd, ui.KeyK) {
		s.openScholarInlinePrompt(p)
	}
	if root.Shortcut(ui.Alt, ui.KeySpace) {
		s.pinScholarReference(p)
	}
	if root.Shortcut(ui.Alt, ui.KeyDown) {
		s.navigateScholarHunk(p, 1)
	}
	if root.Shortcut(ui.Alt, ui.KeyUp) {
		s.navigateScholarHunk(p, -1)
	}
	if p.agent.batch != nil {
		set := p.agent.batch
		if len(set.hunks) > 0 && root.Shortcut(ui.Cmd, ui.KeyEnter) {
			s.acceptScholarHunks(p, []*scholarPatch{set.hunks[min(set.selected, len(set.hunks)-1)]})
		}
		if root.Shortcut(ui.Cmd, ui.KeyBackspace) {
			s.rejectScholarHunk(p)
		}
		return
	}
	if p.agent.patch == nil {
		return
	}
	if root.Shortcut(ui.Cmd, ui.KeyEnter) {
		s.acceptScholarPatch(p, p.agent.patch)
	}
	if root.Shortcut(ui.Cmd, ui.KeyBackspace) && !p.busy {
		s.rejectScholarSingle(p)
	}
}

func (s *Shell) persistScholarDrawer(p *scholarState) {
	if p.drawerSize == 0 || p.drawerSave.IsZero() {
		return
	}
	if s.prefs.ScholarProjects == nil {
		s.prefs.ScholarProjects = make(map[string]scholarProjectPrefs)
	}
	pref := s.prefs.ScholarProjects[p.project.Root]
	pref.DrawerWidth = int(p.drawerSize)
	s.prefs.ScholarProjects[p.project.Root] = pref
	s.setPref("scholarProjects")
	p.drawerSave = time.Time{}
}
func (s *Shell) scholarWorkspaceWidth(c *ui.Context) float32 {
	w, _ := c.Size()
	if s.scholarForTab(s.activeTab()) == nil {
		return w
	}
	if !s.scholarSidebarHidden(c) {
		return w - s.sidebarW
	}
	if s.pf.windows {
		return w - scholarRailWidth
	}
	return w
}

func (s *Shell) scholarCanvasWidth(c *ui.Context) float32 {
	w := s.scholarWorkspaceWidth(c)
	total, _ := c.Size()
	if p := s.scholarForTab(s.activeTab()); p != nil && s.scholarInspectorVisible(p, total) {
		w -= s.scholarInspectorWidth(p, total) + 5
	}
	return w
}

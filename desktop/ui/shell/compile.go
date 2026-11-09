package shell

import (
	"context"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"nomo/desktop/internal/scholar"
	"nomo/desktop/provider"
	"nomo/desktop/typeset"
	"nomo/desktop/ui/mdedit"
)

const (
	typesetSource = iota
	typesetPreview
	typesetSplit
)

type pageRequest struct {
	number, edge int
	dark         bool
}

type sourcePageRequest struct {
	line    int
	text    string
	version uint64
}

type pageClick struct {
	page    int
	version uint64
	x, y    float64
}
type previewRevision struct {
	snap    editor.Snapshot
	ctx     context.Context
	version uint64
	project *scholarPreviewRevision
}

// 工作协程拥有编译会话；UI 只保留有限页缓存与一张临时段落位图。
type compilePane struct {
	provider                *provider.Descriptor
	liveUpdates             chan previewRevision
	lifetime                context.Context
	cancelBuild             context.CancelFunc
	anchor                  typeset.PageAnchor
	shown                   uint64
	fullVersion, fullSerial uint64
	inlineText              string
	paragraph               *ui.Bitmap
	stopped                 bool
	tab                     *Tab
	path, sourcePath        string
	project                 scholar.Project
	previewRoot             string
	projectInput            *scholarPreviewRevision
	sourceVersion           uint64
	version                 uint64
	cancel                  context.CancelFunc
	list                    ui.ListState
	cached                  map[int]*ui.Bitmap
	cacheDark               bool
	loading                 int
	bookmarks               []mdedit.Heading
	cacheSlots              int
	requests                chan pageRequest
	sections                chan sourcePageRequest
	clicks                  chan pageClick
	jump                    func(int)
	jumpProject             func(typeset.ProjectLocation)
	page, pages, resumePage int
	parkedPage              int
	checkingPreview         bool
	reuseChecks             chan func(bool) // nil 请求释放段落快览，有回调的请求校验当前 PDF。
	busy                    bool
	pageJumpOpen            bool
	pageJumpInput           string
	elapsed                 time.Duration
	warnings                []typeset.Diagnostic
	err                     string
	image                   *ui.Bitmap
}

func (s *Shell) closeCompile() {
	if p := s.compile; p != nil {
		p.cancel()
		if isManuscript(p.tab) && samePath(p.sourcePath, p.tab.Path) {
			first, last := p.list.Visible()
			if first <= last && p.parkedPage == 0 {
				p.tab.typesetPage, p.tab.typesetPagePath = first+1, p.path
			}
			if workspace := s.scholar; workspace != nil {
				workspace.budgetOpen = false
				if !p.busy && (p.err != "" || p.pages > 0) {
					workspace.diagnostics = p.report()
				}
			}
		}
		if s.scholar != nil && s.scholar.bodyConfirmed != nil && s.scholar.bodyConfirmed.pane == p {
			s.scholar.bodyConfirmed = nil
		}
		p.tab.outRows = nil
		s.compile = nil
	}
}

func (s *Shell) sourceDocument(c *ui.Context, t *Tab) (el ui.Element) {
	st := t.Source()
	workspace := s.scholarForTab(t)
	mode := t.typesetMode
	if workspace != nil {
		mode = s.scholarPreviewMode(c, workspace, t)
	}
	wantPreview := s.canPreviewTypeset(t) && mode != typesetSource && (s.scholar == nil || workspace != nil)
	trial := wantPreview && workspace != nil && workspace.agent.trial.show
	if workspace != nil && (!wantPreview || trial) {
		workspace.budgetOpen = false
	}
	if trial && workspace.agent.trial.compare {
		s.closeCompile()
		el = ui.Column(c).Fill().Focusable().Label(s.tr("scholarComparePDFs"))
		el.Children(func() { s.scholarTrialPaper(c, workspace) })
		return
	}
	if st.OnCompletion == nil && t.Kind() == kindMarkdown {
		st.OnCompletion = func(req editor.CompletionRequest) { s.sourceCompletion(t, req) }
	}
	var project scholar.Project
	if wantPreview && !trial {
		project = s.typesetProject(t)
	}
	if p := s.compile; p != nil {
		switch {
		case p.tab != t || t.typesetPaused || trial:
			s.closeCompile()
		case !wantPreview:
			s.hideTypesetPreview(workspace, t)
		case p.project != project:
			s.closeCompile()
		case p.parkedPage != 0:
			s.restoreTypesetPreview(p)
		}
	}
	if wantPreview && !t.typesetPaused && !trial && s.compile == nil {
		s.openTypeset(t, true)
	}
	if p := s.compile; p != nil && p.parkedPage == 0 {
		if p.project.Root != "" {
			s.updateProjectPreview(p)
		} else if p.liveUpdates != nil && !p.stopped && p.version != st.Buffer.Version() {
			s.queuePreview(p, previewRevision{snap: st.Buffer.Snapshot(), version: st.Buffer.Version()})
		}
	}

	var paper ui.Element
	source := func() {
		ui.Row(c).Grow(1).Fill().Justify(ui.Center).Padding(16, 0, 0, 0).Children(func() {
			el = s.sourceEditor(c, t)
		})
	}
	p := s.compile
	if p != nil && p.parkedPage != 0 {
		p = nil
	}
	if p != nil || wantPreview && (workspace != nil || t.typesetPaused) {
		preview := func() {
			canvas := c.Theme().SurfaceHover
			if c.Theme().Dark || t.typesetDarkReading {
				canvas = ui.Hex("#1a1b1e")
			}
			paper = ui.Column(c).Fill().MinWidth(0).Clip().Focusable().Label("PDF").Background(canvas)
			paper.Children(func() {
				if trial && s.scholarTrialPaper(c, workspace) {
					return
				}
				if workspace != nil {
					s.scholarPDFHeader(c, workspace, t, p)
				}
				if p == nil {
					return
				}
				// 学术工作区的排版状态与错误详情由状态栏和诊断检查器呈现。
				if workspace == nil && p.busy && p.image == nil {
					ui.Text(c, s.tr("compiling"))
				}
				if workspace == nil && p.err != "" {
					el := ui.Scroll(c).FillWidth().MaxHeight(150)
					el.Children(func() { ui.Text(c, p.err).Wrap() })
				}

				if p.image == nil {
					return
				}
				ui.Column(c).Fill().Grow(1).MinHeight(0).Padding(8).Children(func() {
					s.previewPages(c, p)
				})
				if p.paragraph != nil && p.anchor.Page == 0 {
					ui.Column(c).Absolute().Bottom(12).Left(12).Right(12).Background(c.Theme().Surface).Padding(6).Gap(3).Children(func() {
						ui.Text(c, s.tr("liveParagraph")).FontSize(12)
						_, h := p.paragraph.Size()
						ui.Image(c, p.paragraph).FillWidth().Height(min(180, float32(h)))
					})
				}
			})
		}
		if mode == typesetPreview {
			preview()
			el = paper
		} else if !trial {
			if t.typesetSplitPercent == 0 {
				t.typesetSplitPercent = 50
			}
			ui.SplitPercent(c, &t.typesetSplitPercent, source, preview).Fill()
		} else {
			v := &workspace.agent.trial
			if v.split == 0 {
				v.split = 50
			}
			ui.SplitPercent(c, &v.split, source, preview).Fill()
		}
	} else {
		source()
	}
	if workspace != nil && paper.Valid() {
		if s.focusEditor && (mode == typesetPreview || t.typesetFocusPDF) {
			paper.Focus()
			s.focusEditor = false
		} else if paper.FocusWithin() {
			t.typesetFocusPDF = true
		} else if el.FocusWithin() {
			t.typesetFocusPDF = false
		}
	}
	return
}

func (s *Shell) scholarPDFHeader(c *ui.Context, workspace *scholarState, tab *Tab, pane *compilePane) {
	path := tab.Path
	if pane != nil {
		path = pane.path
	} else if project := s.typesetProject(tab); project.Root != "" {
		path = project.Main
	}
	ui.Row(c).Height(sidebarHd).FillWidth().Padding(0, 6, 0, 10).Gap(6).AlignItems(ui.Center).Background(s.ch.content).Children(func() {
		ui.Icon(c, s.icon("file-text")).FontSize(14).TextColor(s.ch.muted)
		ui.Text(c, "PDF · "+filepath.Base(path)).FontSize(11).SingleLine().Grow(1).MinWidth(0).Tooltip(path)
		if pane != nil && pane.pages > 0 {
			s.scholarPDFPageControl(c, pane)
		}
		reading := s.tr("scholarPDFDarkReading")
		if s.toolButton(c.Key("pdf-dark-reading"), "moon", reading, tab.typesetDarkReading).
			Label(reading).Role(ui.RoleToggleButton).Checked(tab.typesetDarkReading).FocusRing(true).
			Disabled(pane != nil && pane.err != "" && pane.image != nil).Clicked() {
			tab.typesetDarkReading = !tab.typesetDarkReading
			c.Invalidate()
		}
		label := s.tr("scholarPageBudgetSettings")
		b := s.toolButton(c.Key("pdf-budget"), "sliders-horizontal", label, workspace.budgetOpen).Label(label).FocusRing(true)
		if b.Clicked() {
			workspace.budgetOpen = !workspace.budgetOpen
		}
		ui.Popover(c, b, &workspace.budgetOpen, func() {
			ui.Column(c).Width(270).Padding(12).Gap(6).Role(ui.RoleDialog).Label(label).Children(func() {
				s.scholarBudgetControls(c, workspace)
			})
		})
	})
	ui.Box(c).Height(1).FillWidth().Background(s.ch.separator)
}

func (s *Shell) scholarPDFPageControl(c *ui.Context, pane *compilePane) {
	first, _ := pane.list.Visible()
	current := first + 1
	label := strconv.Itoa(current) + " / " + strconv.Itoa(pane.pages)
	b := s.scholarMenuButton(c.Key("pdf-page"), "list-tree", label, true)
	if b.Clicked() {
		pane.pageJumpOpen = !pane.pageJumpOpen
		pane.pageJumpInput = strconv.Itoa(current)
	}
	ui.Popover(c, b, &pane.pageJumpOpen, func() {
		label := s.tr("scholarGoToPage")
		ui.Row(c).Padding(8).Gap(6).AlignItems(ui.Center).Children(func() {
			in := ui.TextInput(c, &pane.pageJumpInput).Width(60).Label(label).AutoFocus()
			page, err := strconv.Atoi(strings.TrimSpace(pane.pageJumpInput))
			valid := err == nil && page >= 1 && page <= pane.pages
			if (ui.Button(c, label).Disabled(!valid).Clicked() || in.Submitted()) && valid {
				pane.list.ScrollTo(page-1, ui.Start)
				pane.pageJumpOpen = false
			}
		})
	})
}

// 整页缓存的原始 RGBA 像素预算为 128 MiB；拖宽窗口时保留现有位图，待后台从 PDF 补绘。
func (s *Shell) previewPages(c *ui.Context, p *compilePane) {
	dark := p.tab.typesetDarkReading
	if p.cacheDark != dark {
		p.cached = nil
		p.cacheDark = dark
	}
	first, last := p.list.Visible()
	slots := max(2, last-first+1)
	if slots != p.cacheSlots && p.loading == 0 {
		p.cached = nil
		p.cacheSlots = slots
	}
	ui.List(c, &p.list, p.pages, func(row int) {
		number := row + 1
		bmp := p.cached[number]
		if bmp == nil && number == p.page && !dark {
			bmp = p.image
		}
		w, h := p.image.Size()
		if bmp != nil {
			w, h = bmp.Size()
		}
		ui.Column(c).FillWidth().AlignItems(ui.Center).Padding(0, 0, 8, 0).Children(func() {
			frame := ui.Box(c).WidthPercent(float32(s.prefs.ContentWidthPercent)).AspectRatio(float32(w) / float32(h)).Clip()
			if dark {
				frame.Background(ui.Hex("#202020"))
			}
			frame.Draw(func(painter *ui.Painter, rect ui.Rect) {
				if p.busy || p.err != "" || p.loading != 0 || row < first || row > last {
					return
				}
				// Painter.Scale 已包含当前屏幕的 backing scale 与应用缩放。
				pixels := int(math.Ceil(float64(max(rect.W, rect.H) * painter.Scale())))
				limit := int(math.Sqrt(float64((128 << 20) / 4 / p.cacheSlots)))
				edge := min(typeset.MaxPageEdge, limit, ((pixels+127)/128)*128)
				if edge <= 0 {
					return
				}
				if bmp != nil {
					bw, bh := bmp.Size()
					if max(bw, bh)+1 >= edge {
						return
					}
				}
				select {
				case p.requests <- pageRequest{number, edge, dark}:
					p.loading = number
				default:
				}
			})
			frame.Children(func() {
				if bmp == nil {
					return
				}
				pageImage := ui.Image(c, bmp).Fill().Label(s.tr("compiledPage"))
				if pageImage.Clicked() && !p.busy && p.err == "" && p.fullVersion == p.version {
					x, y, over := pageImage.PointerPosition()
					box := pageImage.Bounds()
					x, y = x/box.W, y/box.H
					if over && x >= 0 && y >= 0 && x <= 1 && y <= 1 {
						select {
						case p.clicks <- pageClick{number, p.fullVersion, float64(x), float64(y)}:
						default:
						}
					}
				}
				if a := p.anchor; (p.paragraph != nil || p.inlineText != "") && a.Page == number {
					box := frame.Bounds()
					pw, ph := box.W, box.H
					x, y := pw*float32(a.X/a.PageW), ph*float32(a.Y/a.PageH)
					bg, fg := ui.Hex("#ffffff"), ui.Hex("#111111")
					if dark && p.inlineText != "" {
						bg, fg = ui.Hex("#202020"), ui.Hex("#e0e0e0")
					}
					patch := ui.Box(c).Absolute().Left(x).Top(y).Size(pw*float32(a.W/a.PageW), ph*float32(a.H/a.PageH)).Background(bg).Clip().Label(s.tr("inlineDraft"))
					patch.Children(func() {
						if p.inlineText != "" {
							scale := ph / float32(a.PageH)
							ui.Text(c, p.inlineText).Font("serif").FontSize(float32(a.FontPt) * scale).FixedLineHeight(float32(a.LinePt) * scale).TextColor(fg).FillWidth().Wrap()
						} else {
							ui.Image(c, p.paragraph).Fill()
						}
					})
				}
			})
		})
	}).Grow(1).FillWidth().MinHeight(0)
}
func (p *compilePane) keepPage(n int, bmp *ui.Bitmap) {
	if p.cached == nil {
		p.cached = make(map[int]*ui.Bitmap)
	}
	if len(p.cached) >= p.cacheSlots && p.cached[n] == nil {
		farthest := n
		for k := range p.cached {
			if max(k-n, n-k) > max(farthest-n, n-farthest) {
				farthest = k
			}
		}
		delete(p.cached, farthest)
	}
	p.cached[n] = bmp
}

func (s *Shell) sourceEditor(c *ui.Context, t *Tab) ui.Element {
	st := t.Source()
	s.bindScholarCitations(t)
	s.scholarGutter(c, t)
	if t.afterSourceInput != nil {
		t.afterSourceInput()
	}
	return editor.View(c, st).FillHeight().WidthPercent(float32(s.prefs.ContentWidthPercent))
}

func (s *Shell) openTypeset(t *Tab, live bool) {
	project := s.typesetProject(t)
	path := t.Path
	if project.Root != "" {
		path = project.Main
	}
	pr := s.previewProvider(&Tab{Path: path})
	if pr == nil || pr.Open == nil {
		return
	}
	s.closeCompile()
	ctx, cancel := context.WithCancel(context.Background())
	initial := t.Source().Buffer.Snapshot()
	p := &compilePane{provider: pr, tab: t, path: path, sourcePath: t.Path, project: project, version: initial.Version(), sourceVersion: initial.Version(), cancel: cancel, cacheSlots: 2, requests: make(chan pageRequest, 1), sections: make(chan sourcePageRequest, 1), clicks: make(chan pageClick, 1), busy: true}
	if workspace := s.scholarForTab(t); workspace != nil && live {
		p.previewRoot = workspace.project.Root
		p.reuseChecks = make(chan func(bool), 2)
	}
	if t.typesetPagePath != "" && samePath(t.typesetPagePath, path) {
		p.resumePage = t.typesetPage
	}
	buildCtx, cancelBuild := context.WithCancel(ctx)
	p.lifetime = ctx
	p.cancelBuild = cancelBuild
	if live {
		p.liveUpdates = make(chan previewRevision, 1)
	}
	p.jump = func(line int) {
		snap := t.Source().Buffer.Snapshot()
		if line >= 0 && line < snap.LineCount() {
			s.jumpScholarOutline(t, snap.LineStart(line))
		}
	}
	if project.Root != "" {
		p.jumpProject = func(location typeset.ProjectLocation) { s.jumpProjectPreview(p, location) }
		p.projectInput = s.projectPreviewRevision(p)
	}
	revision := previewRevision{snap: initial, ctx: buildCtx, version: p.version, project: p.projectInput}
	s.compile = p
	go func() {
		err := p.runPreview(ctx, revision, func(v uint64, draft bool, apply func()) {
			s.post(func() {
				if s.compile != p || s.activeTab() != t || t.Path != p.sourcePath {
					return
				}
				if project.Root != "" {
					if p.parkedPage == 0 {
						s.updateProjectPreview(p)
					}
					if v != p.version {
						return
					}
				} else if live && t.Source().Buffer.Version() != v && !(draft && v >= p.shown) {
					return
				}
				p.shown = v
				apply()
			})
		})
		if err != nil && ctx.Err() == nil {
			s.post(func() {
				if s.compile == p {
					p.err = err.Error()
					p.busy = false
					p.stopped = true
					p.paragraph = nil
					p.inlineText = ""
				}
			})
		}
	}()
}

// 工作协程独占编译器与临时文件。界面只投递最新的只读 rope 快照，过期结果不能覆盖当前版本。
func (p *compilePane) runPreview(ctx context.Context, revision previewRevision, publish func(uint64, bool, func())) error {
	var session provider.Session
	var projectSession *typeset.Session
	var disk uint64
	if revision.project == nil {
		var err error
		session, err = p.provider.Open(p.path)
		if err != nil {
			return err
		}
	}
	defer func() {
		if session != nil {
			session.Close()
		}
	}()
	snap, buildCtx := revision.snap, revision.ctx
	sourceName, _ := scholar.Relative(p.project.Root, p.sourcePath)
	var base editor.Snapshot
	var baseVersion uint64
	var baseProject *scholarPreviewRevision
	var standaloneStamp string
	fragment := func(input editor.Snapshot, offset int) (string, string, int, bool) {
		if projectSession == nil || samePath(p.sourcePath, p.path) {
			return p.provider.Fragment(input, offset)
		}
		if p.provider.IncludedFragment == nil {
			return "", "", 0, false
		}
		body, start, ok := p.provider.IncludedFragment(input, offset)
		return projectSession.ProjectPreamble(), body, start, ok
	}
	var pageBitmap *ui.Bitmap
	var follow string
	var settled typeset.PageAnchor
	page, pages := max(1, p.resumePage), 0
	draw := func(heads []mdedit.Heading) error {
		img, e := session.Page(buildCtx, page)
		if e != nil {
			return e
		}
		bmp := ui.NewBitmap(img)
		pageBitmap = bmp
		n, total, placement := page, pages, settled
		publish(revision.version, false, func() {
			p.bookmarks = heads
			p.tab.outRows = nil
			p.cached = nil
			first, last := p.list.Visible()
			if (p.image == nil && p.resumePage > 0) || (placement.Page > 0 && (n-1 < first || n-1 > last)) {
				p.list.ScrollTo(n-1, ui.Start)
			}
			p.loading = 0
			if !p.cacheDark {
				p.keepPage(n, bmp)
			}
			p.inlineText = ""
			p.anchor = placement
			p.page, p.pages, p.image, p.busy, p.err, p.paragraph = n, total, bmp, false, "", nil
		})
		return nil
	}
	full := func() {
		started := time.Now()
		var n int
		var e error
		if inputs := revision.project; inputs != nil {
			e = inputs.err
			if e == nil {
				if projectSession == nil {
					projectSession, e = typeset.OpenProjectPreview(buildCtx, p.project.Root, p.project.Main, inputs.readers())
					if e == nil {
						session = projectSession
					}
				} else {
					e = projectSession.SyncProjectPreview(buildCtx, disk != inputs.disk, inputs.readers())
				}
				if e == nil {
					disk = inputs.disk
					n, e = projectSession.CompileProjectPreview(buildCtx)
				}
			}
		} else {
			if p.previewRoot != "" {
				standaloneStamp, _ = (scholar.Project{Root: p.previewRoot}).InputStamp(buildCtx)
			}
			n, e = session.Compile(buildCtx, strings.NewReader(snap.String()))
		}
		version := revision.version
		if e == nil {
			elapsed := time.Since(started)
			var warnings []typeset.Diagnostic
			if logged, ok := session.(interface{ Warnings() []typeset.Diagnostic }); ok {
				warnings = slices.Clone(logged.Warnings())
			}
			if projectSession != nil {
				for i := range warnings {
					warnings[i].File = projectSession.ProjectLog(warnings[i].File)
					warnings[i].Text = projectSession.ProjectLog(warnings[i].Text)
				}
			}
			publish(version, false, func() { p.elapsed, p.warnings = elapsed, warnings; p.fullVersion = version; p.fullSerial++ })
			var heads []mdedit.Heading
			if book, ok := session.(interface {
				Bookmarks(context.Context) []typeset.Bookmark
			}); ok {
				for _, b := range book.Bookmarks(buildCtx) {
					heads = append(heads, mdedit.Heading{Level: b.Level, Text: b.Title, Pos: b.Page})
				}
			}
			if fast, ok := session.(typeset.ParagraphSession); p.liveUpdates != nil && ok && p.provider.Fragment != nil {
				fast.PrepareAnchors(buildCtx)
				settled = typeset.PageAnchor{}
				if follow != "" {
					settled = fast.Anchor(buildCtx, follow)
					if settled.Page != 0 {
						page = settled.Page
					}
				}
			}
			base, baseVersion, baseProject = snap, version, revision.project
			pages = n
			page = min(page, pages)
			e = draw(heads)
		}
		if e != nil && buildCtx.Err() == nil {
			msg := e.Error()
			if projectSession != nil {
				msg = projectSession.ProjectLog(msg)
			}
			pages = 0
			publish(version, false, func() { p.err = msg; p.busy = false; p.paragraph = nil; p.inlineText = ""; p.warnings = nil })
		}
	}
	full()
	old := snap
	var quick, layout <-chan time.Time
	qt, lt := time.NewTimer(time.Hour), time.NewTimer(time.Hour)
	qt.Stop()
	lt.Stop()
	defer qt.Stop()
	defer lt.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case check := <-p.reuseChecks:
			reusable, ok := session.(*typeset.Session)
			if check == nil {
				if ok {
					reusable.StopParagraphPreview()
				}
				continue
			}
			current := ok && pages > 0 && buildCtx.Err() == nil
			if current && baseProject != nil {
				current = reusable.PreviewInputsCurrent(ctx, baseProject.readers())
			} else if current {
				stamp, err := (scholar.Project{Root: p.previewRoot}).InputStamp(ctx)
				current = err == nil && stamp == standaloneStamp && reusable.PreviewInputsCurrent(ctx, nil)
			}
			check(current)
		case click := <-p.clicks:
			if click.page < 1 || click.page > pages || click.version != baseVersion || buildCtx.Err() != nil {
				continue
			}
			if projectSession != nil {
				location := projectSession.ProjectSourceLocation(buildCtx, click.page, click.x, click.y)
				if location.Path != "" {
					publish(click.version, false, func() { p.jumpProject(location) })
				}
			} else if locator, ok := session.(interface {
				SourceLine(context.Context, int, float64, float64, func() string) int
			}); ok {
				line := locator.SourceLine(buildCtx, click.page, click.x, click.y, base.String)
				if line >= 0 && line < base.LineCount() {
					publish(click.version, false, func() {
						if !p.busy {
							p.jump(line)
						}
					})
				}
			}
		case section := <-p.sections:
			line := section.line
			if buildCtx.Err() != nil || pages == 0 || section.version != baseVersion {
				continue
			}
			dest := 0
			if projectSession != nil {
				dest = projectSession.PageForProjectLine(buildCtx, p.sourcePath, line, section.text)
			} else if rev, ok := session.(interface {
				PageForLine(context.Context, int) int
			}); ok {
				dest = rev.PageForLine(buildCtx, line)
			} else {
				continue
			}
			publish(baseVersion, false, func() {
				if dest > 0 && dest <= p.pages {
					p.list.ScrollTo(dest-1, ui.Start)
				} else {
					p.jump(line)
				}
			})
		case request := <-p.requests:
			n := request.number
			if n >= 1 && n <= pages {
				img, err := session.PageAtSize(buildCtx, n, request.edge)
				if err != nil {
					if buildCtx.Err() == nil {
						return err
					}
					continue
				}
				if request.dark {
					img = darkPDFPage(img)
				}
				bmp := ui.NewBitmap(img)
				publish(baseVersion, false, func() {
					if p.cacheDark == request.dark {
						p.keepPage(n, bmp)
					}
					p.loading = 0
				})
			}
		case next := <-p.liveUpdates:
			revision = next
			snap, buildCtx = next.snap, next.ctx
			if quick == nil {
				qt.Reset(12 * time.Millisecond)
				quick = qt.C
			}
			lt.Reset(300 * time.Millisecond)
			layout = lt.C
		case <-quick:
			quick = nil
			if fast, ok := session.(typeset.ParagraphSession); buildCtx.Err() == nil && ok && p.provider.Fragment != nil && (revision.project == nil || revision.project.onlySourceChanged(baseProject, sourceName)) {
				off := changedOffset(old, snap)
				if off == old.Len() && off == snap.Len() {
					old = snap
					continue
				}
				if header, text, start, ok := fragment(snap, off); ok {
					var anchor typeset.PageAnchor
					if pages > 0 {
						first := changedOffset(base, snap)
						if first >= start && first <= start+len(text) {
							_, orig, baseStart, ok := fragment(base, first)
							if ok && start == baseStart && sameSuffix(base, start+len(orig), snap, start+len(text)) {
								anchor = fast.Anchor(ctx, orig)
							}
						}
					}
					plain := ""
					if anchor.Page > 0 && anchor.FontPt >= 5 && anchor.FontPt <= 36 && anchor.LinePt > 0 && !strings.ContainsAny(text, `\{}$%&#_^~`) {
						plain = strings.Join(strings.Fields(text), " ")
						if anchor.IndentPt >= anchor.FontPt*.9 {
							plain = strings.Repeat("　", min(4, int(anchor.IndentPt/anchor.FontPt+.5))) + plain
						}
					}
					var bmp *ui.Bitmap
					if plain == "" {
						if img := fast.Paragraph(ctx, header, text); img != nil {
							bmp = ui.NewBitmap(img)
						}
					}
					if plain != "" || bmp != nil {
						if anchor.Page != 0 && anchor.Page != page {
							if img, e := session.Page(ctx, anchor.Page); e == nil {
								pageBitmap = ui.NewBitmap(img)
								page = anchor.Page
							} else {
								continue
							}
						}
						if anchor.Page != 0 {
							follow = text
						} else {
							follow = ""
						}
						target, total := pageBitmap, pages
						publish(revision.version, true, func() {
							if anchor.Page != 0 {
								first, last := p.list.Visible()
								if anchor.Page-1 < first || anchor.Page-1 > last {
									p.list.ScrollTo(anchor.Page-1, ui.Start)
								}
								p.image = target
								p.page = anchor.Page
								p.pages = total
							}
							p.anchor = anchor
							p.inlineText = plain
							p.paragraph = bmp
							p.err = ""
						})
						lt.Reset(650 * time.Millisecond)
						layout = lt.C
					}
				}
			}
			old = snap
		case <-layout:
			layout = nil
			quick = nil
			old = snap
			full()
		}
	}
}

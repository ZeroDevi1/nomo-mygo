package shell

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"nomo/desktop/provider"
	"nomo/desktop/typeset"
	"nomo/desktop/ui/mdedit"
)

const (
	typesetSource = iota
	typesetPreview
	typesetSplit
)

type pageRequest struct{ number, edge int }

type pageClick struct {
	page    int
	version uint64
	x, y    float64
}
type previewRevision struct {
	snap editor.Snapshot
	ctx  context.Context
}

// 工作协程拥有编译会话；UI 只保留有限页缓存与一张临时段落位图。
type compilePane struct {
	provider    *provider.Descriptor
	liveUpdates chan previewRevision
	lifetime    context.Context
	cancelBuild context.CancelFunc
	anchor      typeset.PageAnchor
	shown       uint64
	inlineText  string
	paragraph   *ui.Bitmap
	stopped     bool
	tab         *Tab
	path        string
	version     uint64
	cancel      context.CancelFunc
	list        ui.ListState
	cached      map[int]*ui.Bitmap
	loading     int
	bookmarks   []mdedit.Heading
	cacheSlots  int
	requests    chan pageRequest
	sections    chan int
	clicks      chan pageClick
	jump        func(int)
	page, pages int
	busy        bool
	err         string
	image       *ui.Bitmap
	split       float32
}

func (s *Shell) closeCompile() {
	if p := s.compile; p != nil {
		p.cancel()
		p.tab.outRows = nil
		s.compile = nil
	}
}

func (s *Shell) sourceDocument(c *ui.Context, t *Tab) (el ui.Element) {
	st := t.Source()
	if st.OnCompletion == nil && t.Kind() == kindMarkdown {
		st.OnCompletion = func(req editor.CompletionRequest) { s.sourceCompletion(t, req) }
	}
	if t.typesetMode != typesetSource && s.compile == nil {
		s.openTypeset(t, true)
	}
	if p := s.compile; p != nil {
		if p.liveUpdates != nil && !p.stopped && p.version != st.Buffer.Version() {
			snap := st.Buffer.Snapshot()
			select {
			case <-p.liveUpdates:
			default:
			}
			p.cancelBuild()
			buildCtx, cancel := context.WithCancel(p.lifetime)
			p.cancelBuild = cancel
			p.liveUpdates <- previewRevision{snap, buildCtx}
			p.version = snap.Version()
			p.busy = true
		}
	}
	source := func() {
		ui.Row(c).Grow(1).Fill().Justify(ui.Center).Padding(16, 0, 0, 0).Children(func() {
			el = s.sourceEditor(c, t)
		})
	}
	if p := s.compile; p != nil {
		preview := func() {
			ui.Column(c).Fill().MinWidth(0).Clip().Background(c.Theme().SurfaceHover).Padding(8).Gap(6).Children(func() {
				if p.busy && p.image == nil {
					ui.Text(c, s.tr("compiling"))
				}
				if p.err != "" {
					el := ui.Scroll(c).FillWidth().MaxHeight(150)
					el.Children(func() { ui.Text(c, p.err).Wrap() })
				}

				if p.image == nil {
					return
				}
				s.previewPages(c, p, st)
				if p.paragraph != nil && p.anchor.Page == 0 {
					ui.Column(c).Absolute().Bottom(12).Left(12).Right(12).Background(c.Theme().Surface).Padding(6).Gap(3).Children(func() {
						ui.Text(c, s.tr("liveParagraph")).FontSize(12)
						_, h := p.paragraph.Size()
						ui.Image(c, p.paragraph).FillWidth().Height(min(180, float32(h)))
					})
				}
			})
		}
		if t.typesetMode == typesetPreview {
			el = ui.Column(c).Fill()
			el.Children(preview)
		} else {
			ui.SplitPercent(c, &p.split, source, preview).Fill()
		}
	} else {
		source()
	}
	return
}

// 整页缓存的原始 RGBA 像素预算为 128 MiB；拖宽窗口时保留现有位图，待后台从 PDF 补绘。
func (s *Shell) previewPages(c *ui.Context, p *compilePane, st *editor.State) {
	first, last := p.list.Visible()
	slots := max(2, last-first+1)
	if slots != p.cacheSlots && p.loading == 0 {
		p.cached = nil
		p.cacheSlots = slots
	}
	ui.List(c, &p.list, p.pages, func(row int) {
		number := row + 1
		bmp := p.cached[number]
		if bmp == nil && number == p.page {
			bmp = p.image
		}
		w, h := p.image.Size()
		if bmp != nil {
			w, h = bmp.Size()
		}
		ui.Column(c).FillWidth().AlignItems(ui.Center).Padding(0, 0, 8, 0).Children(func() {
			frame := ui.Box(c).WidthPercent(float32(s.prefs.ContentWidthPercent)).AspectRatio(float32(w) / float32(h)).Clip()
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
				case p.requests <- pageRequest{number, edge}:
					p.loading = number
				default:
				}
			})
			frame.Children(func() {
				if bmp == nil {
					return
				}
				pageImage := ui.Image(c, bmp).Fill().Label(s.tr("compiledPage"))
				if pageImage.Clicked() && !p.busy && p.err == "" && p.shown == st.Buffer.Version() {
					x, y, over := pageImage.PointerPosition()
					box := pageImage.Bounds()
					x, y = x/box.W, y/box.H
					if over && x >= 0 && y >= 0 && x <= 1 && y <= 1 {
						select {
						case p.clicks <- pageClick{number, p.shown, float64(x), float64(y)}:
						default:
						}
					}
				}
				if a := p.anchor; (p.paragraph != nil || p.inlineText != "") && a.Page == number {
					box := frame.Bounds()
					pw, ph := box.W, box.H
					x, y := pw*float32(a.X/a.PageW), ph*float32(a.Y/a.PageH)
					patch := ui.Box(c).Absolute().Left(x).Top(y).Size(pw*float32(a.W/a.PageW), ph*float32(a.H/a.PageH)).Background(ui.Hex("#ffffff")).Clip().Label(s.tr("inlineDraft"))
					patch.Children(func() {
						if p.inlineText != "" {
							scale := ph / float32(a.PageH)
							ui.Text(c, p.inlineText).Font("serif").FontSize(float32(a.FontPt) * scale).FixedLineHeight(float32(a.LinePt) * scale).TextColor(ui.Hex("#111111")).FillWidth().Wrap()
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
	if t.afterSourceInput != nil {
		t.afterSourceInput()
	}
	return editor.View(c, st).FillHeight().WidthPercent(float32(s.prefs.ContentWidthPercent))
}

func (s *Shell) openTypeset(t *Tab, live bool) {
	pr := s.previewProvider(t)
	if pr == nil || pr.Open == nil {
		return
	}
	s.closeCompile()
	ctx, cancel := context.WithCancel(context.Background())
	initial := t.Source().Buffer.Snapshot()
	p := &compilePane{provider: pr, tab: t, path: t.Path, version: initial.Version(), cancel: cancel, cacheSlots: 2, requests: make(chan pageRequest, 1), sections: make(chan int, 1), clicks: make(chan pageClick, 1), busy: true, split: 50}
	buildCtx, cancelBuild := context.WithCancel(ctx)
	p.lifetime = ctx
	p.cancelBuild = cancelBuild
	if live {
		p.liveUpdates = make(chan previewRevision, 1)
	}
	p.jump = func(line int) {
		st := t.Source()
		off := st.Buffer.Snapshot().LineStart(line)
		st.Select(off, off)
		st.ScrollTo(line)
		if t.typesetMode == typesetPreview {
			t.typesetMode = typesetSplit
		}
		s.focusEditor = true
	}
	s.compile = p
	go func() {
		err := p.runPreview(ctx, buildCtx, initial, func(v uint64, draft bool, apply func()) {
			s.post(func() {
				if s.compile == p && (!live || t.Source().Buffer.Version() == v || draft && v >= p.shown) {
					p.shown = v
					apply()
				}
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
func (p *compilePane) runPreview(ctx, buildCtx context.Context, snap editor.Snapshot, publish func(uint64, bool, func())) error {
	session, err := p.provider.Open(p.path)
	if err != nil {
		return err
	}
	defer session.Close()
	fast, canPatch := session.(typeset.ParagraphSession)
	canPatch = canPatch && p.provider.Fragment != nil
	var base editor.Snapshot
	var pageBitmap *ui.Bitmap
	var follow string
	var settled typeset.PageAnchor
	page, pages := 1, 0
	draw := func(heads []mdedit.Heading) error {
		img, e := session.Page(buildCtx, page)
		if e != nil {
			return e
		}
		bmp := ui.NewBitmap(img)
		pageBitmap = bmp
		n, total, placement := page, pages, settled
		publish(snap.Version(), false, func() {
			p.bookmarks = heads
			p.tab.outRows = nil
			p.cached = nil
			first, last := p.list.Visible()
			if placement.Page > 0 && (n-1 < first || n-1 > last) {
				p.list.ScrollTo(n-1, ui.Start)
			}
			p.loading = 0
			p.keepPage(n, bmp)
			p.inlineText = ""
			p.anchor = placement
			p.page, p.pages, p.image, p.busy, p.err, p.paragraph = n, total, bmp, false, "", nil
		})
		return nil
	}
	full := func() {
		n, e := session.Compile(buildCtx, strings.NewReader(snap.String()))
		if e == nil {
			var heads []mdedit.Heading
			if book, ok := session.(interface {
				Bookmarks(context.Context) []typeset.Bookmark
			}); ok {
				for _, b := range book.Bookmarks(buildCtx) {
					heads = append(heads, mdedit.Heading{Level: b.Level, Text: b.Title, Pos: b.Page})
				}
			}
			if p.liveUpdates != nil && canPatch {
				fast.PrepareAnchors(buildCtx)
				settled = typeset.PageAnchor{}
				if follow != "" {
					settled = fast.Anchor(buildCtx, follow)
					if settled.Page != 0 {
						page = settled.Page
					}
				}
			}
			base = snap
			pages = n
			page = min(page, pages)
			e = draw(heads)
		}
		if e != nil && buildCtx.Err() == nil {
			msg := e.Error()
			pages = 0
			publish(snap.Version(), false, func() { p.err = msg; p.busy = false; p.paragraph = nil; p.inlineText = "" })
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
		case click := <-p.clicks:
			locator, ok := session.(interface {
				SourceLine(context.Context, int, float64, float64, func() string) int
			})
			if ok && click.page >= 1 && click.page <= pages && click.version == base.Version() && buildCtx.Err() == nil {
				line := locator.SourceLine(buildCtx, click.page, click.x, click.y, base.String)
				if line >= 0 && line < base.LineCount() {
					publish(click.version, false, func() {
						if !p.busy {
							p.jump(line)
						}
					})
				}
			}
		case line := <-p.sections:
			if rev, ok := session.(interface {
				PageForLine(context.Context, int) int
			}); ok {
				dest := rev.PageForLine(buildCtx, line)
				publish(base.Version(), false, func() {
					if dest > 0 && dest <= p.pages {
						p.list.ScrollTo(dest-1, ui.Start)
					} else {
						p.jump(line)
					}
				})
			}
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
				bmp := ui.NewBitmap(img)
				publish(base.Version(), false, func() { p.keepPage(n, bmp); p.loading = 0 })
			}
		case next := <-p.liveUpdates:
			snap, buildCtx = next.snap, next.ctx
			if quick == nil {
				qt.Reset(12 * time.Millisecond)
				quick = qt.C
			}
			lt.Reset(300 * time.Millisecond)
			layout = lt.C
		case <-quick:
			quick = nil
			if buildCtx.Err() == nil && canPatch {
				off := changedOffset(old, snap)
				if header, text, start, ok := p.provider.Fragment(snap, off); ok {
					var anchor typeset.PageAnchor
					if pages > 0 {
						first := changedOffset(base, snap)
						if first >= start && first <= start+len(text) {
							_, orig, baseStart, ok := p.provider.Fragment(base, first)
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
						publish(snap.Version(), true, func() {
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

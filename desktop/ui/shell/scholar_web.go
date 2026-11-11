package shell

import (
	"net/url"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/scholar"
)

const scholarWebStateLimit = 128 << 10

// Only visible tabs own a WebView. The bounded, memory-only session snapshot
// restores history/scroll position on backends that provide it.
type scholarWebTab struct {
	source, url string
	view        *mygo.WebView
	state       []byte
	off         []func()
	loading     bool
	err         string
}

func scholarWebURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) > 4096 {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
		return ""
	}
	return u.String()
}

func scholarEntryWebURL(e scholar.Entry) string {
	if address := scholarWebURL(e.URL); address != "" {
		return address
	}
	doi := scholar.NormalizeDOI(e.DOI)
	if strings.HasPrefix(doi, "10.") && strings.Contains(doi, "/") {
		return (&url.URL{Scheme: "https", Host: "doi.org", Path: "/" + doi}).String()
	}
	return ""
}

func (w *scholarWebTab) stop(remember bool) {
	if w.view == nil {
		if !remember {
			w.state = nil
		}
		return
	}
	view := w.view
	w.view, w.loading, w.state = nil, false, nil
	if remember && !view.IsDestroyed() {
		page := view.Page()
		if address := scholarWebURL(page.URL()); address != "" {
			w.url = address
			if state, err := page.SessionState(); err == nil && len(state) <= scholarWebStateLimit {
				w.state = state
			}
		}
	}
	for _, off := range w.off {
		off()
	}
	w.off = nil
	view.Destroy()
}

func (s *Shell) openScholarWeb(p *scholarState, e scholar.Entry, address string) {
	address = scholarWebURL(address)
	if address == "" {
		return
	}
	r := &p.references
	for _, t := range r.tabs {
		if t.web != nil && t.web.source == address {
			s.selectScholarPDF(p, t)
			return
		}
	}
	if len(r.tabs) >= scholarPDFTabLimit {
		p.err = s.tr("scholarPDFTabLimit")
		return
	}
	e.Raw, e.Record = "", nil
	title := e.Title
	if title == "" {
		title = address
	}
	t := &scholarReferenceTab{entry: e, title: title, web: &scholarWebTab{source: address, url: address}}
	r.tabs = append(r.tabs, t)
	p.literature.openNotice = ""
	s.selectScholarPDF(p, t)
	if e.RemoteID != "" {
		p.literature.rememberReading(e)
	}
}

func (s *Shell) loadScholarWeb(w *scholarWebTab) {
	if s.win == nil || w.view != nil || w.err != "" {
		return
	}
	state := w.state
	w.state = nil
	v, err := s.win.NewWebView(mygo.WebViewOptions{Page: mygo.PageOptions{
		Untrusted: true, Session: mygo.NewEphemeralSession(), SessionState: state,
	}, BackgroundColor: "light-dark(#ffffff, #1e1e1e)"})
	if err != nil {
		w.err = s.tr("scholarWebUnavailable")
		return
	}
	w.view, w.loading = v, len(state) == 0
	page := v.Page()
	w.off = []func(){
		page.OnWillNavigate(func(e *mygo.NavigateEvent) {
			if scholarWebURL(e.URL) == "" {
				e.PreventDefault()
				return
			}
			if w.view == v {
				w.loading, w.err = true, ""
				s.win.Invalidate()
			}
		}),
		page.OnDidNavigate(func(address string) {
			if w.view == v {
				if safe := scholarWebURL(address); safe != "" {
					w.url = safe
				}
				s.win.Invalidate()
			}
		}),
		page.OnDidFinishLoad(func() {
			if w.view == v {
				w.loading = false
				s.win.Invalidate()
			}
		}),
		page.OnDidFailLoad(func(e *mygo.LoadError) {
			if w.view == v && e.Code != -999 {
				w.loading, w.err = false, s.tr("scholarWebLoadFailed")
				s.win.Invalidate()
			}
		}),
		page.OnRenderProcessGone(func(string) {
			if w.view == v {
				w.loading, w.err = false, s.tr("scholarWebLoadFailed")
				s.win.Invalidate()
			}
		}),
	}
	if len(state) == 0 {
		if err := page.LoadURL(w.url); err != nil {
			w.loading, w.err = false, s.tr("scholarWebLoadFailed")
		}
	}
}

func (s *Shell) scholarWebPage(c *ui.Context, t *scholarReferenceTab) {
	w := t.web
	s.loadScholarWeb(w)
	ui.Column(c).Grow(1).Fill().MinWidth(0).MinHeight(0).Clip().Children(func() {
		ui.Row(c.Key("web-controls")).Height(sidebarHd).FillWidth().Padding(0, 6).Gap(3).AlignItems(ui.Center).Children(func() {
			var page *mygo.Page
			if w.view != nil {
				page = w.view.Page()
			}
			if s.scholarCompactIcon(c.Key("back"), "chevron-left", s.tr("scholarWebBack"), false).Disabled(page == nil || !page.CanGoBack()).Clicked() {
				page.GoBack()
			}
			if s.scholarCompactIcon(c.Key("forward"), "chevron-right", s.tr("scholarWebForward"), false).Disabled(page == nil || !page.CanGoForward()).Clicked() {
				page.GoForward()
			}
			if s.scholarCompactIcon(c.Key("reload"), "refresh-cw", s.tr("refresh"), false).Disabled(page == nil || w.err != "").Clicked() {
				w.loading = true
				page.Reload()
			}
			label := w.url
			if w.loading {
				label = s.tr("scholarWebLoading") + " · " + label
			}
			ui.Text(c, label).FontSize(11).SingleLine().Grow(1).MinWidth(0).TextColor(s.ch.muted).Tooltip(w.url)
			if s.scholarCompactIcon(c.Key("browser"), "external-link", s.tr("scholarOpenBrowser"), false).Clicked() {
				c.Services().OpenURL(w.url)
			}
		})
		ui.Box(c).Height(1).FillWidth().Background(s.ch.separator)
		if w.err != "" {
			s.scholarReaderNotice(c.Key("web-error"), w.err, nil)
			if s.scholarReaderCommand(c.Key("retry"), "refresh-cw", s.tr("refresh"), false).Clicked() {
				w.stop(false)
				w.err = ""
			}
		} else if w.view != nil {
			ui.WebView(c.Key("web"), w.view).Grow(1).FillWidth().MinHeight(0).MinWidth(0)
		}
	})
}

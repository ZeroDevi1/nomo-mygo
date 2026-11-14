package shell

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/scholar"
	"nomo/desktop/typeset"
)

const (
	scholarACPSourcesBudget       = 128 << 10
	scholarACPMarkerPrefix        = "[[nomo-ref:"
	scholarACPSourceIDBytes       = 12
	scholarACPCitationInstruction = "When using an attached source, cite its exact opening [[nomo-ref:ID]] marker."
)

type scholarPDFSource struct {
	Item       string  `json:"item,omitempty"`
	Attachment string  `json:"attachment,omitempty"`
	Library    int     `json:"library,omitempty"`
	Path       string  `json:"path,omitempty"`
	Page       int     `json:"page"`
	PageLabel  string  `json:"pageLabel,omitempty"`
	Region     ui.Rect `json:"region"`
	Version    string  `json:"version"`
}

type scholarACPSentSource struct {
	ID      string           `json:"id"`
	Session acp.SessionId    `json:"session"`
	Message acp.MessageId    `json:"message"`
	Label   string           `json:"label"`
	Locator string           `json:"locator,omitempty"`
	Image   bool             `json:"image,omitempty"`
	PDF     scholarPDFSource `json:"pdf,omitzero"`
}

func (ref scholarACPQuote) sentSource(session acp.SessionId, message acp.MessageId) scholarACPSentSource {
	return scholarACPSentSource{ref.id, session, message, ref.label, ref.locator, len(ref.png) > 0, ref.pdf}
}

func rememberScholarACPSources(pref *scholarProjectPrefs, session acp.SessionId, message acp.MessageId, quotes []scholarACPQuote) {
	pref.ACPSources = slices.DeleteFunc(slices.Clone(pref.ACPSources), func(source scholarACPSentSource) bool {
		return !slices.ContainsFunc(pref.ACPThreads, func(thread scholarACPThread) bool { return thread.ID == source.Session })
	})
	if message != "" {
		for _, quote := range quotes {
			if quote.id != "" {
				pref.ACPSources = append(pref.ACPSources, quote.sentSource(session, message))
			}
		}
	}
	// The array punctuation and all escaped strings count toward the on-disk budget.
	budget, first := scholarACPSourcesBudget-2, len(pref.ACPSources)
	for first > 0 {
		data, err := json.Marshal(pref.ACPSources[first-1])
		if err != nil || len(data)+1 > budget {
			break
		}
		budget -= len(data) + 1
		first--
	}
	pref.ACPSources = slices.Clone(pref.ACPSources[first:])
}

func (s *Shell) indexScholarACPSources(p *scholarState) (map[string]scholarACPSentSource, map[acp.MessageId][]scholarACPSentSource) {
	state := &p.agent.acp
	var byID map[string]scholarACPSentSource
	var byMessage map[acp.MessageId][]scholarACPSentSource
	for _, source := range s.prefs.ScholarProjects[p.project.Root].ACPSources {
		if source.Session == state.thread.ID && source.Message != "" {
			if byID == nil {
				byID = make(map[string]scholarACPSentSource)
				byMessage = make(map[acp.MessageId][]scholarACPSentSource)
			}
			byID[source.ID] = source
			byMessage[source.Message] = append(byMessage[source.Message], source)
		}
	}
	return byID, byMessage
}

func scholarACPCitationLinks(text string, sources map[string]scholarACPSentSource) (string, []scholarACPSentSource) {
	if len(sources) == 0 || !strings.Contains(text, scholarACPMarkerPrefix) {
		return text, nil
	}
	var body strings.Builder
	var matched []scholarACPSentSource
	for {
		before, rest, found := strings.Cut(text, scholarACPMarkerPrefix)
		body.WriteString(before)
		if !found {
			break
		}
		text = rest
		const idLen = 2 * scholarACPSourceIDBytes
		if len(rest) >= idLen+2 && rest[idLen:idLen+2] == "]]" {
			if source, ok := sources[rest[:idLen]]; ok {
				index := slices.IndexFunc(matched, func(old scholarACPSentSource) bool { return old.ID == source.ID })
				if index < 0 {
					index = len(matched)
					matched = append(matched, source)
				}
				fmt.Fprintf(&body, "[%d]", index+1)
				text = rest[idLen+2:]
				continue
			}
		}
		body.WriteString(scholarACPMarkerPrefix)
	}
	return body.String(), matched
}

func scholarACPSourceID() string {
	var id [scholarACPSourceIDBytes]byte
	rand.Read(id[:])
	return hex.EncodeToString(id[:])
}

func (source scholarACPSentSource) marker() string    { return scholarACPMarkerPrefix + source.ID + "]]" }
func (source scholarACPSentSource) endMarker() string { return "[[/nomo-ref:" + source.ID + "]]" }

func (source scholarACPSentSource) replay(text string) (before, quote, after string, found bool) {
	before, block, found := strings.Cut(text, source.marker())
	if !found {
		return text, "", "", false
	}
	block, after, found = strings.Cut(block, source.endMarker())
	if !found {
		return text, "", "", false
	}
	_, quote, _ = strings.Cut(strings.TrimPrefix(block, " "), "\n")
	// Codex ACP can replay embedded resources as a resource link followed
	// by a context element. Remove only the wrapper for this known source.
	uri := "nomo://reference/" + source.ID
	contextOpen := "<context ref=\"" + uri + "\">"
	trimmed := strings.TrimRight(before, " \t\r\n")
	contextEnd := strings.TrimLeft(after, " \t\r\n")
	if strings.HasSuffix(trimmed, contextOpen) && strings.HasPrefix(contextEnd, "</context>") {
		before = strings.TrimSuffix(trimmed, contextOpen)
		before = strings.TrimSuffix(strings.TrimRight(before, " \t\r\n"), uri)
		after = strings.TrimPrefix(contextEnd, "</context>")
	}
	return before, strings.TrimSpace(quote), after, true
}

func (s *Shell) scholarACPSourceLabel(source scholarACPSentSource) string {
	label := source.Label
	if source.PDF.Page > 0 {
		if first, _, multiple := strings.Cut(label, " and "); multiple {
			if year := strings.LastIndexByte(label, ' '); year >= 0 && len(label[year+1:]) == 4 {
				if _, err := strconv.Atoi(label[year+1:]); err == nil {
					label = first + " et al." + label[year:]
				}
			}
		}
		page := source.PDF.PageLabel
		if page == "" {
			page = strconv.Itoa(source.PDF.Page)
		}
		label += " · p." + page
	} else if source.Locator != "" {
		label += " · " + source.Locator
	}
	kind := "scholarACPSourceText"
	if source.Image {
		kind = "scholarACPSourceImage"
	}
	return label + " · " + s.tr(kind)
}

func (source scholarPDFSource) path(p *scholarState) (string, scholar.Zotero, error) {
	z := p.literature.zotero
	z.Group = source.Library
	if source.Attachment != "" && z.ItemLink(source.Attachment) != "" {
		return "", z, nil
	}
	path, err := p.project.Path(filepath.FromSlash(source.Path))
	return path, z, err
}

func (s *Shell) jumpScholarACPSource(p *scholarState, source scholarACPSentSource) {
	pdf := source.PDF
	path, z, err := pdf.path(p)
	if err != nil && pdf.Attachment == "" && pdf.Path == "" {
		p.err = s.tr("scholarACPSourceUnavailable")
		return
	}
	e := scholar.Entry{Title: source.Label, RemoteID: pdf.Item, Library: pdf.Library, File: pdf.Path}
	if err != nil {
		// Keep the same project-relative identity so Retry can resolve a restored file.
		if name := filepath.FromSlash(pdf.Path); filepath.IsLocal(name) && name != "." {
			path = filepath.Join(p.project.Root, name)
		} else {
			p.err = s.tr("scholarACPSourceUnavailable")
			return
		}
	}
	t := s.openScholarAttachmentFrom(p, e, z, pdf.Attachment, path, source.Label, 0)
	if t == nil {
		return
	}
	p.references.sourceJump = t
	p.returnConversation = true
	t.releasePage()
	t.clearSourceJump()
	t.err = ""
	if t.pages < pdf.Page {
		t.pages = 0
	}
	t.page = pdf.Page
	t.jump = &pdf
	t.scroll.X, t.scroll.Y = 0, 0
}

// A stamp binds the selection to the bytes presented by the reader. Hashes are
// computed only for user-triggered extraction, replay preview, and navigation.
func scholarPDFVersion(ctx context.Context, path string, shown os.FileInfo) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > scholarReferenceFileLimit ||
		shown != nil && !scholarSamePDFVersion(shown, info) {
		return "", fmt.Errorf("PDF changed")
	}
	hash := sha256.New()
	var buffer [64 << 10]byte
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := file.Read(buffer[:])
		hash.Write(buffer[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	if after, err := os.Stat(path); err != nil || !scholarSamePDFVersion(info, after) {
		return "", fmt.Errorf("PDF changed")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func scholarSamePDFVersion(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func scholarValidPDFRegion(r ui.Rect) bool {
	return r.X >= 0 && r.Y >= 0 && r.W > 0 && r.H > 0 && r.X+r.W <= 1.00001 && r.Y+r.H <= 1.00001
}

func (s *Shell) loadScholarACPSourcePreview(p *scholarState, source scholarACPSentSource, previewKey string) {
	state := &p.agent.acp
	path, z, err := source.PDF.path(p)
	if err != nil {
		state.sourcePreviewError = s.tr("scholarACPSourceUnavailable")
		return
	}
	state.sourcePreviewError = ""
	ctx, cancel := context.WithTimeout(p.ctx, 20*time.Second)
	state.cancelSourcePreview, state.sourcePreviewContext = cancel, ctx
	unavailable, stale := s.tr("scholarACPSourceUnavailable"), s.tr("scholarACPSourceRegionChanged")
	go func() {
		defer cancel()
		var bitmap *ui.Bitmap
		var message string
		if path == "" {
			path, err = z.AttachmentPath(ctx, source.PDF.Attachment)
		}
		if err == nil {
			info, statErr := os.Stat(path)
			if statErr == nil {
				version, hashErr := scholarPDFVersion(ctx, path, info)
				if hashErr == nil && (version != source.PDF.Version || !scholarValidPDFRegion(source.PDF.Region)) {
					message = stale
				} else if hashErr == nil {
					page, renderErr := typeset.ReadPDFPage(ctx, path, source.PDF.Page, scholarReferenceEdge)
					if after, statErr := os.Stat(path); renderErr == nil && statErr == nil && scholarSamePDFVersion(info, after) {
						if data, err := encodeScholarACPImage(page, source.PDF.Region, image.White); err == nil {
							bitmap, _ = ui.DecodeBitmap(data)
						}
					}
				}
			}
		}
		if bitmap == nil && message == "" {
			message = unavailable
		}
		s.post(func() {
			if s.scholar != p || state.sourcePreviewContext != ctx || state.sourcePreview != previewKey {
				return
			}
			state.sourcePreviewImage, state.sourcePreviewError = bitmap, message
			state.cancelSourcePreview = nil
		})
	}()
}

func (a *scholarACPState) closeSourcePreview() {
	if a.cancelSourcePreview != nil {
		a.cancelSourcePreview()
	}
	a.sourcePreview, a.sourcePreviewError, a.sourcePreviewImage = "", "", nil
	a.cancelSourcePreview, a.sourcePreviewContext = nil, nil
}

func (s *Shell) scholarACPSentSource(c *ui.Context, p *scholarState, source scholarACPSentSource, excerpt, previewKey string, ordinal int) {
	state := &p.agent.acp
	label := s.scholarACPSourceLabel(source)
	if ordinal > 0 {
		label = fmt.Sprintf("[%d] %s", ordinal, label)
	}
	ui.Row(c).FillWidth().MinWidth(0).Gap(3).AlignItems(ui.Center).Children(func() {
		glyph := "quote"
		if source.Image {
			glyph = "image"
		}
		button := s.scholarACPAction(c.Key("source"), glyph, label, false).Grow(1)
		if button.Clicked() {
			open := state.sourcePreview != previewKey
			state.closeSourcePreview()
			if open {
				state.sourcePreview = previewKey
				if source.Image && source.PDF.Page > 0 {
					s.loadScholarACPSourcePreview(p, source, previewKey)
				}
			}
		}
		open := state.sourcePreview == previewKey
		ui.Popover(c, button, &open, func() {
			ui.Column(c).Width(272).Padding(8).Gap(6).Children(func() {
				ui.Text(c.Key("location"), label).Wrap().FontSize(12).Bold()
				if excerpt == "" && !source.Image {
					for _, m := range state.history {
						if m.ID == source.Message {
							_, excerpt, _, _ = source.replay(m.Text)
							break
						}
					}
				}
				if excerpt != "" || state.sourcePreviewImage != nil {
					ui.Scroll(c.Key("preview")).FillWidth().MaxHeight(220).Children(func() {
						if bitmap := state.sourcePreviewImage; bitmap != nil {
							w, h := bitmap.Size()
							ui.Image(c, bitmap).FillWidth().AspectRatio(float32(w) / float32(h)).Label(label)
						} else {
							ui.Text(c, excerpt).Wrap().Selectable().FontSize(12)
						}
					})
				}
				if state.sourcePreviewError != "" {
					ui.Text(c, state.sourcePreviewError).Wrap().TextColor(scholarInk(c, "warning")).FontSize(11)
				} else if state.cancelSourcePreview != nil {
					ui.Text(c, s.tr("scholarLoadingPDF")).FontSize(11).TextColor(s.ch.muted)
				}
				if source.PDF.Page > 0 && ui.Button(c.Key("view-source"), s.tr("scholarACPViewSource")).Clicked() {
					s.jumpScholarACPSource(p, source)
					open = false
				}
			})
		})
		if !open && state.sourcePreview == previewKey {
			state.closeSourcePreview()
		}
		if source.PDF.Page > 0 && s.toolButton(c.Key("jump"), "external-link", s.tr("scholarACPViewSource"), false).
			Label(s.tr("scholarACPViewSource")+" · "+label).FocusRing(true).Shrink(0).Clicked() {
			s.jumpScholarACPSource(p, source)
		}
	})
}

func (s *Shell) scholarPDFSourceHighlight(c *ui.Context, p *scholarState, t *scholarReferenceTab, bounds ui.Rect) {
	if p.references.sourceJump != t || !scholarValidPDFRegion(t.highlight) {
		return
	}
	if bounds.W <= 0 || bounds.H <= 0 {
		c.After(time.Millisecond)
		return
	}
	r := t.highlight
	now := c.Now()
	if t.revealRegion {
		t.highlightUntil = now.Add(1500 * time.Millisecond)
	}
	if !now.Before(t.highlightUntil) {
		t.highlight, t.highlightUntil = ui.Rect{}, time.Time{}
		return
	}
	left, top := r.X*bounds.W, r.Y*bounds.H
	width, height := r.W*bounds.W, r.H*bounds.H
	remaining := t.highlightUntil.Sub(now)
	reduceMotion := c.Preferences().ReduceMotion
	alpha := float32(1)
	if !reduceMotion {
		alpha = min(1, float32(remaining)/float32(320*time.Millisecond))
	}
	mark := ui.Box(c.Key("source-highlight")).Absolute().Left(left).Top(top).Size(width, height).
		Border(2, s.ch.accent.Alpha(alpha)).Background(s.ch.accent.Alpha(.22 * alpha)).PassThrough()
	if t.revealRegion {
		mark.ScrollIntoView(ui.ScrollIntoViewOptions{Block: ui.ScrollCenter})
		t.revealRegion = false
	}
	if reduceMotion {
		c.After(remaining)
	} else if remaining > 320*time.Millisecond {
		c.After(remaining - 320*time.Millisecond)
	} else {
		c.After(min(remaining, 33*time.Millisecond))
	}
}

package shell

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"nomo/desktop/internal/scholar"
)

type scholarSourceLink struct {
	Key   string `json:"key"`
	Group int    `json:"group,omitempty"`
	DOI   string `json:"doi,omitempty"`
}

func (s *Shell) scholarLinkedEntry(p *scholarState, e scholar.Entry) scholar.Entry {
	if e.RemoteID == "" {
		if link, ok := s.prefs.ScholarProjects[p.project.Root].Sources[e.Key]; ok && (link.DOI == "" || link.DOI == scholar.NormalizeDOI(e.DOI)) {
			e.RemoteID, e.Library = link.Key, link.Group
		}
	}
	return e
}

func (s *Shell) openScholarReference(p *scholarState, entry scholar.Entry) {
	e := s.scholarLinkedEntry(p, entry)
	l := &p.literature
	l.openNotice, p.err = "", ""
	if e.RemoteID == "" {
		if strings.EqualFold(filepath.Ext(e.File), ".pdf") && !filepath.IsAbs(e.File) {
			if path, err := p.project.Path(e.File); err == nil {
				s.openScholarAttachment(p, e, "", path, e.Title, 0)
				return
			}
		}
		s.openScholarZoteroPDF(p, e, scholar.Attachment{})
		return
	}
	if scholarSameCatalogItem(e, l.detail.Entry) && l.attachmentPage == 0 && !l.detail.More {
		if l.detail.Entry.URL != "" {
			e.URL = l.detail.Entry.URL
		}
		s.openScholarZoteroPDF(p, e, scholarBestAttachment(l.detail.PDFs))
		return
	}
	z := l.zotero
	z.Group = e.Library
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		var attachment scholar.Attachment
		for start := 0; start < 500; start += scholar.ZoteroPageSize {
			detail, err := z.Detail(ctx, e.RemoteID, start)
			if err != nil {
				if scholarEntryWebURL(e) != "" {
					break
				}
				return nil, err
			}
			if detail.Entry.URL != "" {
				e.URL = detail.Entry.URL
			}
			if pdf := scholarBestAttachment(detail.PDFs); pdf.Key != "" && (attachment.Key == "" || !pdf.LinkOnly) {
				attachment = pdf
			}
			if !detail.More || attachment.Key != "" && !attachment.LinkOnly {
				break
			}
		}
		return func() { s.openScholarZoteroPDF(p, e, attachment) }, nil
	})
}

func scholarBestAttachment(pdfs []scholar.Attachment) (link scholar.Attachment) {
	for _, pdf := range pdfs {
		if pdf.Key != "" && !pdf.LinkOnly {
			return pdf
		}
		if link.Key == "" && scholarWebURL(pdf.URL) != "" {
			link = pdf
		}
	}
	return link
}

func (s *Shell) openScholarZoteroPDF(p *scholarState, entry scholar.Entry, pdf scholar.Attachment) {
	switch {
	case pdf.Key != "" && !pdf.LinkOnly:
		s.openScholarAttachment(p, entry, pdf.Key, "", pdf.Title, 0)
	case pdf.LinkOnly && scholarWebURL(pdf.URL) != "":
		s.openScholarWeb(p, entry, pdf.URL)
	case scholarEntryWebURL(entry) != "":
		s.openScholarWeb(p, entry, scholarEntryWebURL(entry))
	default:
		if !scholarSameCatalogItem(p.literature.selected, entry) {
			s.status = s.tr("scholarNoReadableReference")
			return
		}
		p.literature.openNotice = s.tr("scholarNoReadableReference")
		s.showScholarInspector(p, scholarReader)
	}
}

func (s *Shell) scholarReferenceOpenLabel(p *scholarState, entry scholar.Entry) (glyph, label string) {
	l := &p.literature
	if strings.EqualFold(filepath.Ext(entry.File), ".pdf") || scholarSameCatalogItem(entry, l.detail.Entry) && len(l.detail.PDFs) > 0 && !scholarBestAttachment(l.detail.PDFs).LinkOnly {
		return "file-text", s.tr("scholarOpenPDF")
	}
	if scholarEntryWebURL(entry) != "" {
		return "globe", s.tr("scholarOpenWeb")
	}
	return "book-open", s.tr("scholarReadReference")
}

func (s *Shell) showScholarReferenceAt(st *editor.State, p *scholarState, ref scholar.Citation, pin bool) {
	e, ok := p.library.Peek(ref.Key)
	if ok {
		s.showScholarReference(st, p, s.scholarLinkedEntry(p, e), ref.Start, ref.End)
		if pin {
			s.selectScholarEntry(p, e)
		}
		return
	}
	library, version := p.library, st.Buffer.Version()
	go func() {
		entry, found := library.Find(ref.Key)
		s.post(func() {
			if !found || s.scholar != p || p.library != library || st.Buffer.Version() != version {
				return
			}
			s.showScholarReference(st, p, s.scholarLinkedEntry(p, entry), ref.Start, ref.End)
			if pin {
				s.selectScholarEntry(p, entry)
			}
		})
	}()
}

func (s *Shell) quickLookScholarAttachment(p *scholarState, key string) {
	z := p.literature.zotero
	z.Group = p.literature.selected.Library
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
		path, err := z.AttachmentPath(lookup, key)
		cancel()
		if err != nil {
			return nil, err
		}
		return nil, scholarQuickLook(ctx, path)
	})
}

func (s *Shell) copyScholarBib(p *scholarState, e scholar.Entry, svc ui.Services) {
	library := p.library
	local := slices.Contains(p.bibNames, e.Source)
	z := p.literature.zotero
	z.Group = e.Library
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		if e.RemoteID != "" {
			raw, err := z.ExportFor(ctx, e, "references.bib")
			return func() {
				if err == nil {
					svc.WriteClipboard(string(raw))
				}
			}, err
		}
		value := e
		if local {
			if full, ok := library.Find(e.Key); ok {
				value = full
			}
		}
		text, err := scholar.ExportBibTeX(value)
		return func() {
			if err == nil {
				svc.WriteClipboard(text)
			}
		}, err
	})
}

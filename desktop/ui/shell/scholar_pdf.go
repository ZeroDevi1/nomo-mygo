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

func (s *Shell) openScholarPDF(p *scholarState, entry scholar.Entry, services ui.Services) {
	e := s.scholarLinkedEntry(p, entry)
	if e.RemoteID == "" {
		file := e.File
		if strings.EqualFold(filepath.Ext(file), ".pdf") && !filepath.IsAbs(file) {
			if path, err := p.project.Path(file); err == nil {
				s.openScholarAttachment(p, e, "", path, e.Title, 0)
				return
			}
		}
		if slices.Contains(p.bibNames, e.Source) {
			p.err = s.tr("scholarNoPDF")
			return
		}
		p.err = s.tr("scholarNoPDF")
		return
	}
	z := p.literature.zotero
	z.Group = e.Library
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		var attachment scholar.Attachment
		for start := 0; start < 500; start += scholar.ZoteroPageSize {
			detail, err := z.Detail(ctx, e.RemoteID, start)
			if err != nil {
				return nil, err
			}
			for _, pdf := range detail.PDFs {
				if !pdf.LinkOnly || attachment.Key == "" && pdf.URL != "" {
					attachment = pdf
				}
				if !pdf.LinkOnly {
					break
				}
			}
			if !detail.More || attachment.Key != "" && !attachment.LinkOnly {
				break
			}
		}
		return func() {
			s.openScholarZoteroPDF(p, e, attachment, services)
		}, nil
	})
}

func (s *Shell) openScholarZoteroPDF(p *scholarState, entry scholar.Entry, pdf scholar.Attachment, services ui.Services) {
	switch {
	case pdf.URL != "" && pdf.LinkOnly:
		services.OpenURL(pdf.URL)
	case pdf.Key != "" && !pdf.LinkOnly:
		s.openScholarAttachment(p, entry, pdf.Key, "", pdf.Title, 0)
	default:
		p.err = s.tr("scholarNoPDF")
	}
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

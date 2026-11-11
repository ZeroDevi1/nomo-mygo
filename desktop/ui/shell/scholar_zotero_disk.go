package shell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"

	"nomo/desktop/internal/scholar"
)

func (s *Shell) scholarZoteroDirectory() string {
	if dir := s.prefs.ZoteroDataDirectory; dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "Zotero")
	}
	return ""
}

func readScholarZoteroDirectory(ctx context.Context, dir string) (scholar.Zotero, scholar.ZoteroPage, error) {
	if dir == "" {
		return scholar.Zotero{}, scholar.ZoteroPage{}, errors.New("无法定位 Zotero 数据目录")
	}
	z, err := scholar.OpenZoteroDirectory(dir)
	if err != nil {
		return scholar.Zotero{}, scholar.ZoteroPage{}, err
	}
	page, err := z.Browse(ctx, "", "", "", scholar.ZoteroAll, 0)
	return z, page, err
}

func (l *scholarLiteratureState) setZoteroCatalog(z scholar.Zotero, page scholar.ZoteroPage) {
	l.resetZoteroSource()
	l.zotero, l.connected, l.loaded, l.pageData = z, true, true, page
}

func (s *Shell) connectScholarZotero(p *scholarState) {
	if p.busy {
		return
	}
	l := &p.literature
	prior, dir := l.zotero, s.scholarZoteroDirectory()
	prior.Group = 0
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		probe, cancel := context.WithTimeout(ctx, 5*time.Second)
		z, err := prior.Reconnect(probe)
		cancel()
		var page scholar.ZoteroPage
		if err == nil {
			page, err = z.Browse(ctx, "", "", "", scholar.ZoteroAll, 0)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if prior.ReadOnly() {
				return nil, nil
			}
			z, page, err = readScholarZoteroDirectory(ctx, dir)
			if err != nil {
				return func() {
					l.resetZoteroSource()
					l.zotero, l.connected = scholar.Zotero{}, false
				}, err
			}
		}
		return func() { l.setZoteroCatalog(z, page) }, nil
	})
	l.zoteroConnectJob = p.job
}

func (s *Shell) chooseScholarZoteroDirectory(p *scholarState) {
	options := mygo.OpenDialogOptions{Parent: s.win, Directory: true,
		Title: s.tr("scholarZoteroOpenDirectory"), DefaultPath: s.scholarZoteroDirectory()}
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		paths, err := mygo.Dialog.Open(options)
		if err != nil || len(paths) == 0 || ctx.Err() != nil {
			return nil, err
		}
		z, page, err := readScholarZoteroDirectory(ctx, paths[0])
		if err != nil {
			return nil, err
		}
		return func() {
			p.literature.setZoteroCatalog(z, page)
			s.prefs.ZoteroDataDirectory = paths[0]
			s.setPref("zoteroDataDirectory")
		}, nil
	})
}

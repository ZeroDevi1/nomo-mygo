package shell

import (
	"context"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"

	"nomo/desktop/internal/scholar"
)

func (s *Shell) chooseScholarZoteroDirectory(p *scholarState) {
	l := &p.literature
	dir := s.prefs.ZoteroDataDirectory
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, "Zotero")
		}
	}
	options := mygo.OpenDialogOptions{Parent: s.win, Directory: true,
		Title: s.tr("scholarZoteroOpenDirectory"), DefaultPath: dir}
	s.scholarJob(p, func(ctx context.Context, _ uint64) (func(), error) {
		paths, err := mygo.Dialog.Open(options)
		if err != nil || len(paths) == 0 || ctx.Err() != nil {
			return nil, err
		}
		z, err := scholar.OpenZoteroDirectory(paths[0])
		if err != nil {
			return nil, err
		}
		page, err := z.Browse(ctx, "", "", "", scholar.ZoteroAll, 0)
		if err != nil {
			return nil, err
		}
		return func() {
			l.resetZoteroSource()
			l.zotero, l.connected, l.loaded, l.pageData = z, true, true, page
			s.prefs.ZoteroDataDirectory = paths[0]
			s.setPref("zoteroDataDirectory")
		}, nil
	})
}

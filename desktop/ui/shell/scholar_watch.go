package shell

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"nomo/desktop/internal/scholar"
)

// File metadata polling is independent from the foreground task slot. Only
// changed inputs schedule a parse, and the project context owns both jobs.
func (s *Shell) observeScholarSources(c *ui.Context, p *scholarState) {
	if p.watchPending {
		return
	}
	if !p.watchAt.IsZero() && c.Now().Before(p.watchAt) {
		c.After(p.watchAt.Sub(c.Now()))
		return
	}
	p.watchAt = c.Now().Add(2 * time.Second)
	c.After(2 * time.Second)
	p.watchPending = true
	names := append([]string{p.project.Main}, p.bibNames...)
	root := p.project.Root
	priorZ := p.literature.zotero
	probe := s.win != nil && !priorZ.ReadOnly() && (p.probeAt.IsZero() || !c.Now().Before(p.probeAt))
	preview := s.compile
	if preview != nil && (preview.project.Root == "" || preview.parkedPage != 0) {
		preview = nil
	}
	project := p.project
	if probe {
		p.probeAt = c.Now().Add(30 * time.Second)
	}
	go func() {
		var signature strings.Builder
		for _, name := range names {
			if p.ctx.Err() != nil {
				return
			}
			if !filepath.IsAbs(name) {
				name = filepath.Join(root, name)
			}
			info, err := os.Stat(name)
			if err != nil {
				fmt.Fprintf(&signature, "%s:%v\n", name, err)
			} else {
				fmt.Fprintf(&signature, "%s:%d:%d\n", name, info.Size(), info.ModTime().UnixNano())
			}
		}
		previewStamp := ""
		if preview != nil {
			var err error
			previewStamp, err = project.InputStamp(p.ctx)
			if err != nil {
				previewStamp = err.Error()
			}
		}
		var z scholar.Zotero
		var err error
		version := ""
		if probe {
			ctx, cancel := context.WithTimeout(p.ctx, 900*time.Millisecond)
			z, err = priorZ.Reconnect(ctx)
			if err == nil {
				version, _ = z.LibraryVersion(ctx)
			}
			cancel()
		}
		value := signature.String()
		s.post(func() {
			if s.scholar != p {
				return
			}
			p.watchPending = false
			if preview != nil && s.compile == preview && p.project == project {
				if p.previewStamp != previewStamp {
					p.previewStamp = previewStamp
					p.previewDisk++
				}
			}
			if p.watchSignature != "" && p.watchSignature != value {
				p.libraryReady = false
			}
			p.watchSignature = value
			l := &p.literature
			if probe && l.zotero == priorZ {
				if err == nil && !z.SameSource(priorZ) && p.busy {
					p.probeAt = time.Time{}
					return
				}
				was := l.connected
				l.connected = err == nil
				if err == nil {
					if !z.SameSource(priorZ) {
						l.resetZoteroSource()
						z.Group = 0
					}
					l.zotero = z
					if version != "" && l.pageData.Version != "" && version != l.pageData.Version {
						l.refreshPending = true
					}
				}
				if !was && err == nil {
					l.refreshPending = true
				}
			}
		})
	}()
}

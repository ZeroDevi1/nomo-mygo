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
	probe := s.win != nil && (p.probeAt.IsZero() || !c.Now().Before(p.probeAt))
	priorZ := p.literature.zotero
	body := p.bodyConfirmed
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
		bodyStamp := ""
		var bodyErr error
		if body != nil || preview != nil {
			bodyStamp, bodyErr = project.InputStamp(p.ctx)
		}
		var z scholar.Zotero
		var err error
		version := ""
		if probe {
			ctx, cancel := context.WithTimeout(p.ctx, 900*time.Millisecond)
			z, err = scholar.ConnectZotero(ctx)
			z.Group = priorZ.Group
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
				stamp := bodyStamp
				if bodyErr != nil {
					stamp = bodyErr.Error()
				}
				if p.previewStamp != stamp {
					p.previewStamp = stamp
					p.previewDisk++
				}
			}
			if body != nil && p.bodyConfirmed == body && (bodyErr != nil || bodyStamp != body.disk) {
				p.bodyConfirmed = nil
			}
			if p.watchSignature != "" && p.watchSignature != value {
				p.libraryReady = false
			}
			p.watchSignature = value
			if probe {
				was := p.literature.connected
				p.literature.connected = err == nil
				if err == nil {
					z.Group = p.literature.zotero.Group
					p.literature.zotero = z
					if version != "" && p.literature.pageData.Version != "" && version != p.literature.pageData.Version {
						p.literature.refreshPending = true
					}
				}
				if !was && err == nil && p.literature.mode == 1 && !p.busy {
					s.loadZotero(p, false, false, "", "", 0)
				}
			}
		})
	}()
}

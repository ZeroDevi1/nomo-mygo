package scholar

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"nomo/desktop/internal/childproc"
	"nomo/desktop/internal/pdfium"
)

type LocalSearchResult struct {
	Entries                                 []Entry
	Next, Scanned, PDFs, Unavailable, Total int
	More                                    bool
}

func (l *Library) scan(ctx context.Context, start, limit int, visit func(Entry) error) (int, bool, error) {
	if l == nil {
		return start, false, nil
	}
	index := 0
	handle := func(e Entry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		index++
		if index <= start {
			return nil
		}
		return visit(e)
	}
	if l.disk == nil {
		end := min(len(l.Entries), start+limit)
		for index = start; index < end; {
			if err := handle(l.Entries[index]); err != nil {
				return index, true, err
			}
		}
		return index, index < len(l.Entries), nil
	}
	l.disk.life.RLock()
	defer l.disk.life.RUnlock()
	for n := 0; n < 64; n++ {
		f, err := os.Open(filepath.Join(l.disk.dir, fmt.Sprintf("%02x.jsonl", n)))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return index, true, err
		}
		dec := json.NewDecoder(bufio.NewReader(f))
		for {
			var e Entry
			err = dec.Decode(&e)
			if err != nil {
				break
			}
			if err = handle(e); err != nil {
				break
			}
			if index >= start+limit {
				break
			}
		}
		f.Close()
		if err != nil && err != io.EOF {
			return index, true, err
		}
		if index >= start+limit {
			return index, index < l.total, nil
		}
	}
	return index, false, nil
}

func (p Project) SearchLocalFulltext(ctx context.Context, l *Library, query string, start int) (LocalSearchResult, error) {
	result := LocalSearchResult{Next: start, Total: l.Count()}
	words := strings.Fields(strings.ToLower(query))
	next, more, err := l.scan(ctx, start, 25, func(e Entry) error {
		hit := e.Matches(words)
		result.Scanned++
		pdf, err := p.localAttachment(e)
		if err != nil {
			result.Unavailable++
		} else {
			text, err := localPDFText(ctx, pdf)
			if err != nil {
				result.Unavailable++
			} else {
				result.PDFs++
				content := strings.ToLower(text)
				matched := true
				for _, word := range words {
					if !strings.Contains(content, word) {
						matched = false
						break
					}
				}
				hit = hit || matched
			}
		}
		if hit {
			e.Raw = ""
			result.Entries = append(result.Entries, e)
		}
		return nil
	})
	result.Next, result.More = next, more
	return result, err
}

func (p Project) localAttachment(e Entry) (string, error) {
	for raw := range strings.SplitSeq(e.File, ";") {
		path := strings.TrimSpace(raw)
		path = strings.TrimSuffix(path, ":application/pdf")
		if at := strings.Index(path, ":"); at >= 0 && !filepath.IsAbs(path) && !strings.ContainsAny(path[:at], "/\\") {
			path = path[at+1:]
		}
		if !strings.EqualFold(filepath.Ext(path), ".pdf") {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(filepath.Dir(e.Source), path)
		} else {
			var err error
			path, err = Relative(p.Root, path)
			if err != nil {
				continue
			}
		}
		full, err := p.Path(path)
		if err == nil {
			return full, nil
		}
	}
	return "", errors.New("no project-local PDF attachment")
}

type boundedPDFText struct{ bytes.Buffer }

func (w *boundedPDFText) Write(b []byte) (int, error) {
	if w.Len()+len(b) > 4<<20 {
		return 0, errors.New("PDF text exceeds 4 MiB")
	}
	return w.Buffer.Write(b)
}
func localPDFText(ctx context.Context, path string) (string, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !stat.Mode().IsRegular() || stat.Size() > 128<<20 {
		return "", errors.New("PDF exceeds 128 MiB")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if text, err := pdfium.Text(ctx, path, 4<<20); !errors.Is(err, pdfium.ErrUnavailable) {
		return text, err
	}
	cmd := exec.CommandContext(ctx, "pdftotext", "-enc", "UTF-8", path, "-")
	out := new(boundedPDFText)
	cmd.Stdout, cmd.Stderr = out, io.Discard
	cleanup, err := childproc.Start(cmd)
	if err != nil {
		return "", err
	}
	defer cleanup()
	if err = cmd.Wait(); err != nil {
		return "", err
	}
	return out.String(), nil
}

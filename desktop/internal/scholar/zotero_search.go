package scholar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type ZoteroLibrary struct {
	ID   int
	Name string
}

func (z Zotero) Libraries(ctx context.Context) ([]ZoteroLibrary, error) {
	// Group discovery always starts from the personal account endpoint.
	personal := z
	personal.Group = 0
	_, raw, err := personal.request(ctx, "GET", "/api/users/0/groups", nil, "")
	if err != nil {
		return nil, err
	}
	var groups []struct {
		ID   int
		Data struct {
			ID   int
			Name string
		}
	}
	if err = json.Unmarshal(raw, &groups); err != nil {
		return nil, err
	}
	out := []ZoteroLibrary{{0, "Personal library"}}
	for _, g := range groups {
		id := g.ID
		if id == 0 {
			id = g.Data.ID
		}
		if id > 0 {
			out = append(out, ZoteroLibrary{id, cleanField(g.Data.Name, 256)})
		}
	}
	return out, nil
}
func (z Zotero) PDFLink(key string, page int) string {
	link := PDFLink(key, page)
	if z.Group > 0 {
		link = strings.Replace(link, "/library/items/", fmt.Sprintf("/groups/%d/items/", z.Group), 1)
	}
	return link
}

func (z Zotero) ItemLink(key string) string {
	if !zoteroKey.MatchString(key) {
		return ""
	}
	library := "library"
	if z.Group > 0 {
		library = fmt.Sprintf("groups/%d", z.Group)
	}
	return "zotero://select/" + library + "/items/" + key
}

func (z Zotero) AnnotationLink(attachment string, annotation Annotation) string {
	link := z.PDFLink(attachment, annotation.Page)
	if link == "" || !zoteroKey.MatchString(annotation.Key) {
		return link
	}
	separator := "?"
	if annotation.Page > 0 {
		separator = "&"
	}
	return link + separator + "annotation=" + annotation.Key
}

type ZoteroFulltextResult struct {
	Entries                                 []Entry
	Scanned, PDFs, Unavailable, Next, Total int
	More, Interrupted                       bool
}

// SearchFulltext scans one bounded metadata batch, including all attachment
// pages. Next is a scan cursor, independent of result pagination. It remains
// resumable when a deadline stops the batch.
func (z Zotero) SearchFulltext(ctx context.Context, query, collection string, start int, progress func(int)) (ZoteroFulltextResult, error) {
	result := ZoteroFulltextResult{Next: start}
	interrupted := func(err error) bool {
		var timeout net.Error
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
			(errors.As(err, &timeout) && timeout.Timeout()) {
			result.More, result.Interrupted = true, true
			return true
		}
		return false
	}
	page, err := z.Search(ctx, "", collection, start)
	if err != nil {
		if interrupted(err) {
			return result, nil
		}
		return result, err
	}
	result.More, result.Total = page.More, page.Total
	words := strings.Fields(strings.ToLower(query))
	for _, e := range page.Entries {
		result.Next = e.Ordinal
		if interrupted(nil) {
			return result, nil
		}
		// Commit counters and matches together only after finishing this item.
		pdfs, unavailable := 0, 0
		hit := e.Matches(words)
		attachmentStart := 0
		for {
			detail, err := z.Detail(ctx, e.RemoteID, attachmentStart)
			if interrupted(err) {
				return result, nil
			}
			if err != nil {
				unavailable++
				break
			}
			for _, pdf := range detail.PDFs {
				_, raw, err := z.request(ctx, "GET", "/api/users/0/items/"+pdf.Key+"/fulltext", nil, "")
				if interrupted(err) {
					return result, nil
				}
				if err != nil {
					unavailable++
					continue
				}
				var body struct {
					Content                                            string
					IndexedPages, TotalPages, IndexedChars, TotalChars int
				}
				if json.Unmarshal(raw, &body) != nil || body.Content == "" {
					unavailable++
					continue
				}
				pdfs++
				if body.IndexedPages < body.TotalPages || body.IndexedChars < body.TotalChars {
					unavailable++
				}
				text := strings.ToLower(body.Content)
				matched := true
				for _, word := range words {
					if !strings.Contains(text, word) {
						matched = false
						break
					}
				}
				hit = hit || matched
			}
			if !detail.More {
				break
			}
			attachmentStart += ZoteroPageSize
			if attachmentStart >= 1000 {
				unavailable++
				break
			}
		}
		if interrupted(nil) {
			return result, nil
		}
		// A metadata match survives unavailable or unindexed attachments.
		if hit {
			result.Entries = append(result.Entries, e)
		}
		result.Scanned++
		result.PDFs += pdfs
		result.Unavailable += unavailable
		result.Next = e.Ordinal + 1
		if progress != nil {
			progress(result.Scanned)
		}
	}
	result.Next = page.Next
	return result, nil
}

// AttachmentPath uses the local file-URL endpoint and accepts a regular local
// PDF only. It never follows an HTTP redirect to a filesystem resource.
func (z Zotero) AttachmentPath(ctx context.Context, key string) (string, error) {
	if !zoteroKey.MatchString(key) {
		return "", fmt.Errorf("invalid attachment key")
	}
	_, raw, err := z.request(ctx, "GET", "/api/users/0/items/"+key+"/file/view/url", nil, "")
	if err != nil {
		return "", err
	}
	u, err := url.Parse(strings.TrimSpace(string(raw)))
	if err != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") {
		return "", fmt.Errorf("attachment has no local file")
	}
	rawPath := u.Path
	if runtime.GOOS == "windows" && len(rawPath) >= 4 && rawPath[0] == '/' && rawPath[2] == ':' && rawPath[3] == '/' {
		rawPath = rawPath[1:]
	}
	path := filepath.FromSlash(rawPath)
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("attachment has no absolute local path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(path), ".pdf") {
		return "", fmt.Errorf("attachment is not a local PDF")
	}
	return path, nil
}

func (z Zotero) LibraryVersion(ctx context.Context) (string, error) {
	headers, _, err := z.request(ctx, "GET", "/api/users/0/items?format=versions&limit=1", nil, "")
	if err != nil {
		return "", err
	}
	return headers.Get("Last-Modified-Version"), nil
}

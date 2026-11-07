package scholar

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	arxivID      = regexp.MustCompile(`^(?:[a-z.-]+/[0-9]{7}|[0-9]{4}\.[0-9]{4,5})(?:v[0-9]+)?$`)
	metadataHTTP = &http.Client{Timeout: 12 * time.Second, CheckRedirect: noRedirect}
)

func Lookup(ctx context.Context, query string) ([]Entry, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 512 {
		return nil, fmt.Errorf("请输入 DOI、arXiv 编号或论文标题（最多 512 字节）")
	}
	id := strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(query), "arxiv:"), "https://arxiv.org/abs/")
	id = strings.TrimSpace(id)
	if arxivID.MatchString(id) {
		return lookupArxiv(ctx, id)
	}
	doi := NormalizeDOI(query)
	if strings.HasPrefix(doi, "10.48550/arxiv.") && arxivID.MatchString(strings.TrimPrefix(doi, "10.48550/arxiv.")) {
		return lookupArxiv(ctx, strings.TrimPrefix(doi, "10.48550/arxiv."))
	}
	return lookupCrossref(ctx, query, doi)
}

func metadata(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "NomoScholar (https://github.com/ZeroDevi1/nomo-mygo)")
	resp, err := metadataHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("元数据服务返回 HTTP %d", resp.StatusCode)
	}
	return readBounded(resp.Body, 1<<20)
}

func lookupArxiv(ctx context.Context, id string) ([]Entry, error) {
	raw, err := metadata(ctx, "https://export.arxiv.org/api/query?id_list="+url.QueryEscape(id)+"&max_results=1")
	if err != nil {
		return nil, err
	}
	var feed struct {
		Entries []struct {
			ID        string `xml:"id"`
			Title     string `xml:"title"`
			Published string `xml:"published"`
			Authors   []struct {
				Name string `xml:"name"`
			} `xml:"author"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(raw, &feed); err != nil {
		return nil, err
	}
	var out []Entry
	for _, item := range feed.Entries {
		if !strings.Contains(item.ID, "/abs/") {
			continue
		}
		var names []string
		for _, a := range item.Authors {
			names = append(names, a.Name)
		}
		base := regexp.MustCompile(`v[0-9]+$`).ReplaceAllString(id, "")
		c := &cslItem{Type: "article", Title: strings.Join(strings.Fields(item.Title), " "), DOI: "10.48550/arXiv." + base,
			URL: "https://arxiv.org/abs/" + id, Archive: "arXiv", ArchiveLocation: id}
		for _, name := range names {
			c.Author = append(c.Author, cslName{Literal: name})
		}
		if year, err := strconv.Atoi(yearOf(item.Published)); err == nil {
			c.Issued.DateParts = [][]int{{year}}
		}
		e := c.entry()
		e.Record, e.Venue, e.Source, e.Key = c, "arXiv:"+id, "arXiv", generatedKey(e)
		out = append(out, e)
	}
	return out, nil
}

func lookupCrossref(ctx context.Context, query, doi string) ([]Entry, error) {
	base := "https://api.crossref.org/works"
	exact := strings.HasPrefix(doi, "10.") && strings.Contains(doi, "/") && !strings.ContainsAny(doi, " \r\n\t")
	if exact {
		base += "/" + url.PathEscape(doi)
	} else {
		base += "?" + (url.Values{"query.title": {query}, "rows": {"15"}}).Encode()
	}
	raw, err := metadata(ctx, base)
	if err != nil {
		return nil, err
	}
	type work struct {
		Title                                     []string
		Venue                                     []string `json:"container-title"`
		DOI                                       string
		Author                                    []cslName
		Type, Volume, Issue, Page, Publisher, URL string
		ISBN, ISSN                                []string
		Issued                                    struct {
			DateParts [][]int `json:"date-parts"`
		}
	}
	var response struct{ Message json.RawMessage }
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	var list struct{ Items []work }
	if exact {
		var item work
		if err := json.Unmarshal(response.Message, &item); err != nil {
			return nil, err
		}
		list.Items = []work{item}
	} else if err := json.Unmarshal(response.Message, &list); err != nil {
		return nil, err
	}
	var out []Entry
	seen := make(map[string]bool)
	for _, item := range list.Items {
		id := NormalizeDOI(item.DOI)
		if len(item.Title) == 0 || id == "" || seen[id] {
			continue
		}
		seen[id] = true
		c := &cslItem{Type: crossrefCSLType(item.Type), Title: item.Title[0], DOI: item.DOI, Author: item.Author,
			Volume: item.Volume, Issue: item.Issue, Page: item.Page, Publisher: item.Publisher, URL: item.URL}
		if len(item.ISBN) > 0 {
			c.ISBN = item.ISBN[0]
		}
		if len(item.ISSN) > 0 {
			c.ISSN = item.ISSN[0]
		}
		c.Issued.DateParts = item.Issued.DateParts
		if len(item.Venue) > 0 {
			c.Venue = item.Venue[0]
		}
		e := c.entry()
		e.Title, e.Author, e.Venue, e.DOI = cleanField(e.Title, 1024), cleanField(e.Author, 512), cleanField(e.Venue, 256), cleanField(e.DOI, 256)
		e.Source, e.Key, e.Record = "Crossref", generatedKey(e), c
		out = append(out, e)
	}
	return out, nil
}

func crossrefCSLType(kind string) string {
	switch kind {
	case "journal-article":
		return "article-journal"
	case "proceedings-article":
		return "paper-conference"
	case "book", "monograph", "edited-book", "reference-book":
		return "book"
	case "book-chapter", "book-section", "reference-entry":
		return "chapter"
	case "dissertation":
		return "thesis"
	case "report":
		return "report"
	case "posted-content":
		return "article"
	case "dataset":
		return "dataset"
	case "standard":
		return "standard"
	default:
		return "document"
	}
}

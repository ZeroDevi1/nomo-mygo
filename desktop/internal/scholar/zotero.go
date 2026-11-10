package scholar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/net/html"
)

const zoteroBase = "http://127.0.0.1:23119"

var (
	zoteroKey = regexp.MustCompile(`^[A-Z0-9]{8}$`)
	localHTTP = &http.Client{
		Transport:     &http.Transport{DisableKeepAlives: true},
		Timeout:       90 * time.Second,
		CheckRedirect: noRedirect,
	}
)

func noRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

type Zotero struct {
	ServerID string
	Group    int
	writeKey *zoteroWriteKey
}

func (z Zotero) Reconnect(ctx context.Context) (Zotero, error) {
	h, _, err := (Zotero{}).request(ctx, "GET", "/api/", nil, "")
	if err != nil {
		return Zotero{}, err
	}
	if h.Get("Zotero-API-Version") != "3" {
		return Zotero{}, fmt.Errorf("Zotero Local API 版本不可用")
	}
	id := h.Get("Zotero-Server-ID")
	if z.ServerID == id {
		return z, nil
	}
	z.ServerID, z.writeKey = id, nil
	if z.ServerID != "" {
		z.writeKey = &zoteroWriteKey{}
	}
	return z, nil
}

func (z Zotero) request(ctx context.Context, method, path string, data []byte, key string) (http.Header, []byte, error) {
	return z.requestWithToken(ctx, method, path, data, key, "")
}

func (z Zotero) requestWithToken(ctx context.Context, method, path string, data []byte, key, token string) (http.Header, []byte, error) {
	if z.Group > 0 {
		path = strings.Replace(path, "/api/users/0", fmt.Sprintf("/api/groups/%d", z.Group), 1)
	}
	req, err := http.NewRequestWithContext(ctx, method, zoteroBase+path, bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Zotero-API-Version", "3")
	if data != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if z.ServerID != "" {
		req.Header.Set("Zotero-Server-ID", z.ServerID)
	}
	if key != "" {
		req.Header.Set("Zotero-API-Key", key)
	}
	if token != "" {
		req.Header.Set("Zotero-Write-Token", token)
	}
	resp, err := localHTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, nil, zoteroHTTPError(resp.StatusCode)
	}
	body, err := readBounded(resp.Body, 1<<20)
	return resp.Header, body, err
}

type Collection struct{ Key, Name, Parent string }
type ZoteroPage struct {
	Next        int
	Version     string
	Total       int
	Entries     []Entry
	Collections []Collection
	More        bool
}
type zoteroItem struct {
	Key  string
	Data struct {
		Key, ItemType, Title, Date, PublicationTitle, DOI, CitationKey, AbstractNote, ContentType string
		CaseName, Subject, NameOfAct                                                              string
		URL, LinkMode                                                                             string
		AnnotationText, AnnotationComment, AnnotationPageLabel, AnnotationType                    string
		AnnotationPosition                                                                        string
		Note                                                                                      string
		Tags                                                                                      []zoteroTag
		Deleted                                                                                   bool
		Collections                                                                               []string
		Relations                                                                                 map[string]json.RawMessage
		Creators                                                                                  []struct{ FirstName, LastName, Name, CreatorType string }
	}
}

const ZoteroPageSize = 25

func paged(path string, start int, query string) string {
	v := url.Values{"limit": {strconv.Itoa(ZoteroPageSize)}, "start": {strconv.Itoa(start)}}
	if query != "" {
		v.Set("q", query)
		v.Set("qmode", "titleCreatorYear")
	}
	return path + "?" + v.Encode()
}

func hasNext(h http.Header, start, count int) bool {
	if total, err := strconv.Atoi(h.Get("Total-Results")); err == nil {
		return start+count < total
	}
	return strings.Contains(h.Get("Link"), `rel="next"`) || count == ZoteroPageSize
}

func (z Zotero) Collections(ctx context.Context, start int) (ZoteroPage, error) {
	return z.CollectionsIn(ctx, "", start)
}

func (z Zotero) CollectionsIn(ctx context.Context, parent string, start int) (ZoteroPage, error) {
	var page ZoteroPage
	path := "/api/users/0/collections/top"
	if parent != "" {
		if !zoteroKey.MatchString(parent) {
			return page, fmt.Errorf("invalid collection key")
		}
		path = "/api/users/0/collections/" + parent + "/collections"
	}
	h, raw, err := z.request(ctx, "GET", paged(path, start, ""), nil, "")
	if err != nil {
		return page, err
	}
	var list []struct {
		Key  string
		Data struct {
			Name             string
			ParentCollection json.RawMessage
		}
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return page, err
	}
	for _, item := range list {
		if !zoteroKey.MatchString(item.Key) {
			continue
		}
		var parent string
		_ = json.Unmarshal(item.Data.ParentCollection, &parent)
		page.Collections = append(page.Collections, Collection{item.Key, cleanField(item.Data.Name, 512), parent})
	}
	page.Next = start + len(list)
	page.Version = h.Get("Last-Modified-Version")
	page.Total, _ = strconv.Atoi(h.Get("Total-Results"))
	page.More = hasNext(h, start, len(list))
	return page, nil
}

func (z Zotero) Search(ctx context.Context, query, collection string, start int) (ZoteroPage, error) {
	return z.search(ctx, query, collection, "", ZoteroAll, false, start)
}

func (z Zotero) Tags(ctx context.Context, query string, start int) ([]string, bool, error) {
	params := url.Values{"limit": {strconv.Itoa(ZoteroPageSize)}, "start": {strconv.Itoa(start)}, "q": {query}}
	h, raw, err := z.request(ctx, "GET", "/api/users/0/tags?"+params.Encode(), nil, "")
	if err != nil {
		return nil, false, err
	}
	var items []struct{ Tag string }
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false, err
	}
	tags := make([]string, 0, len(items))
	for _, item := range items {
		if item.Tag != "" && len(item.Tag) <= 512 {
			tags = append(tags, item.Tag)
		}
	}
	return tags, hasNext(h, start, len(items)), nil
}

type ZoteroScope int

const (
	ZoteroAll ZoteroScope = iota
	ZoteroUnfiled
	ZoteroPublications
	ZoteroTrash
)

// Browse searches the catalog by Zotero quick search, including notes and tags.
func (z Zotero) Browse(ctx context.Context, query, collection, tag string, scope ZoteroScope, start int) (ZoteroPage, error) {
	return z.search(ctx, query, collection, tag, scope, true, start)
}

func (z Zotero) search(ctx context.Context, query, collection, tag string, scope ZoteroScope, everything bool, start int) (ZoteroPage, error) {
	var page ZoteroPage
	path := "/api/users/0"
	if collection != "" {
		if !zoteroKey.MatchString(collection) {
			return page, fmt.Errorf("无效 Zotero 分类键")
		}
		path += "/collections/" + collection
	}
	switch scope {
	case ZoteroPublications:
		path += "/publications/items"
	case ZoteroTrash:
		path += "/items/trash"
	default:
		path += "/items/top"
	}
	endpoint := paged(path, start, query)
	if everything && query != "" {
		endpoint = strings.Replace(endpoint, "qmode=titleCreatorYear", "qmode=everything", 1)
	}
	if tag != "" {
		if strings.HasPrefix(tag, "-") {
			tag = "\\" + tag
		}
		endpoint += "&tag=" + url.QueryEscape(tag)
	}
	h, raw, err := z.request(ctx, "GET", endpoint, nil, "")
	if err != nil {
		return page, err
	}
	var list []zoteroItem
	if err := json.Unmarshal(raw, &list); err != nil {
		return page, err
	}
	keys := make([]string, 0, len(list))
	for _, item := range list {
		if zoteroKey.MatchString(item.Key) && item.title() != "" {
			keys = append(keys, item.Key)
		}
	}
	var citekeys map[string]string
	if len(keys) > 0 && z.Group == 0 {
		bbtCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_ = z.bbt(bbtCtx, "item.citationkey", []any{keys}, &citekeys)
		cancel()
	}
	for itemIndex, item := range list {
		entry, ok := z.entry(item)
		if !ok || entry.ZoteroNote && !everything || scope == ZoteroUnfiled && len(item.Data.Collections) != 0 {
			continue
		}
		if candidate := citekeys[item.Key]; keyPattern.MatchString(candidate) {
			entry.Key = candidate
		}
		entry.Ordinal = start + itemIndex
		page.Entries = append(page.Entries, entry)
	}
	page.Next = start + len(list)
	page.Version = h.Get("Last-Modified-Version")
	page.Total, _ = strconv.Atoi(h.Get("Total-Results"))
	page.More = hasNext(h, start, len(list))
	return page, nil
}

func (item *zoteroItem) title() string {
	d := &item.Data
	for _, title := range [...]string{d.Title, d.CaseName, d.Subject, d.NameOfAct} {
		if title != "" {
			return title
		}
	}
	return ""
}

func (z Zotero) entry(item zoteroItem) (Entry, bool) {
	d := &item.Data
	if !zoteroKey.MatchString(item.Key) {
		return Entry{}, false
	}
	title := item.title()
	if d.ItemType == "note" && title == "" {
		title, _, _ = strings.Cut(zoteroNoteText(d.Note), "\n")
	}
	if title == "" {
		return Entry{}, false
	}
	var creators []string
	for _, c := range d.Creators {
		if c.CreatorType != "author" && c.CreatorType != "editor" {
			continue
		}
		name := c.Name
		if name == "" {
			name = strings.TrimSpace(c.FirstName + " " + c.LastName)
		}
		creators = append(creators, name)
	}
	return Entry{Key: cleanField(d.CitationKey, 256), Title: cleanField(title, 1024),
		Author: cleanField(strings.Join(creators, " and "), 512), Year: yearOf(d.Date),
		Venue: cleanField(d.PublicationTitle, 256), DOI: cleanField(d.DOI, 256), Source: "Zotero", RemoteID: item.Key,
		Abstract: cleanField(d.AbstractNote, 4096), Library: z.Group, ZoteroNote: d.ItemType == "note"}, true
}

func (z Zotero) Entry(ctx context.Context, key string) (Entry, error) {
	if !zoteroKey.MatchString(key) {
		return Entry{}, fmt.Errorf("invalid Zotero item key")
	}
	_, raw, err := z.request(ctx, "GET", "/api/users/0/items/"+key, nil, "")
	if err != nil {
		return Entry{}, err
	}
	var item zoteroItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return Entry{}, err
	}
	entry, ok := z.entry(item)
	if !ok || entry.RemoteID != key {
		return Entry{}, fmt.Errorf("invalid Zotero item")
	}
	return entry, nil
}

func (z Zotero) bbt(ctx context.Context, method string, params []any, target any) error {
	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params, "id": 1})
	if err != nil {
		return err
	}
	_, raw, err := (Zotero{}).request(ctx, "POST", "/better-bibtex/json-rpc", payload, "")
	if err != nil {
		return err
	}
	var response struct{ Result json.RawMessage }
	if err := json.Unmarshal(raw, &response); err != nil {
		return err
	}
	return json.Unmarshal(response.Result, target)
}

func (z Zotero) BibTeX(ctx context.Context, key string) (string, error) {
	var output string
	err := z.bbt(ctx, "item.export", []any{[]string{key}, "Better BibTeX"}, &output)
	return output, err
}

// ExportFor imports a single full export. Better BibTeX is preferred for
// its citekey, with the official Zotero translator as a fallback.
func (z Zotero) ExportFor(ctx context.Context, entry Entry, target string) ([]byte, error) {
	if !zoteroKey.MatchString(entry.RemoteID) {
		return nil, fmt.Errorf("无效 Zotero 条目键")
	}
	format, translator := "csljson", ""
	switch strings.ToLower(filepath.Ext(target)) {
	case ".bib":
		format, translator = "bibtex", "Better BibTeX"
	case ".biblatex":
		format, translator = "biblatex", "Better BibLaTeX"
	}
	if z.Group == 0 && translator != "" && keyPattern.MatchString(entry.Key) {
		bbtCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		var raw string
		err := z.bbt(bbtCtx, "item.export", []any{[]string{entry.Key}, translator}, &raw)
		cancel()
		if err == nil && len(raw) <= 128<<10 {
			if item, parseErr := exportedBibEntry([]byte(raw)); parseErr == nil && item.Key == entry.Key &&
				(entry.DOI == "" || item.DOI == "" || NormalizeDOI(item.DOI) == NormalizeDOI(entry.DOI)) {
				return []byte(raw), nil
			}
		}
	}
	_, raw, err := z.request(ctx, "GET", "/api/users/0/items/"+entry.RemoteID+"?format="+format, nil, "")
	if err != nil {
		return nil, fmt.Errorf("Zotero 完整导出失败：%w", err)
	}
	if len(raw) == 0 || len(raw) > 128<<10 {
		return nil, fmt.Errorf("Zotero 导出为空或超过 128 KiB")
	}
	return raw, nil
}

type ZoteroDetail struct {
	Entry       Entry
	Abstract    string
	ItemType    string
	Deleted     bool
	PDFs        []Attachment
	Notes       []ZoteroNote
	Tags        []string
	Collections []string
	Related     []string
	More        bool
}

type ZoteroNote struct {
	Key, Title, Text string
	Tags, Related    []string
}

type Attachment struct {
	Key, Title, URL string
	LinkOnly        bool
}

func (item *zoteroItem) attachment() Attachment {
	pdf := Attachment{Key: item.Key, Title: cleanField(item.Data.Title, 256), LinkOnly: item.Data.LinkMode == "linked_url"}
	if pdf.LinkOnly {
		link, err := url.Parse(strings.TrimSpace(item.Data.URL))
		if err == nil && (link.Scheme == "http" || link.Scheme == "https") && link.Hostname() != "" && link.User == nil {
			pdf.URL = link.String()
		}
	}
	return pdf
}

type Annotation struct {
	Quote, Note, Label, Kind string
	Page                     int
}

func (z Zotero) Detail(ctx context.Context, key string, start int) (ZoteroDetail, error) {
	var detail ZoteroDetail
	if !zoteroKey.MatchString(key) {
		return detail, fmt.Errorf("无效 Zotero 条目键")
	}
	path := "/api/users/0/items/" + key
	_, raw, err := z.request(ctx, "GET", path, nil, "")
	if err != nil {
		return detail, err
	}
	var item zoteroItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return detail, err
	}
	detail.Abstract = cleanField(item.Data.AbstractNote, 16<<10)
	detail.Entry, _ = z.entry(item)
	detail.ItemType, detail.Deleted = item.Data.ItemType, item.Data.Deleted
	detail.Collections = item.Data.Collections
	detail.Tags, detail.Related = z.itemMetadata(item)
	if item.Data.ItemType == "note" {
		detail.Notes = []ZoteroNote{z.note(item)}
		return detail, nil
	}
	if detail.Deleted {
		return detail, nil
	}
	if item.Data.ItemType == "attachment" && item.Data.ContentType == "application/pdf" {
		detail.PDFs = []Attachment{item.attachment()}
		return detail, nil
	}
	h, raw, err := z.request(ctx, "GET", paged(path+"/children", start, ""), nil, "")
	if err != nil {
		return detail, err
	}
	var children []zoteroItem
	if err := json.Unmarshal(raw, &children); err != nil {
		return detail, err
	}
	for _, item := range children {
		if item.Data.ContentType == "application/pdf" && zoteroKey.MatchString(item.Key) {
			detail.PDFs = append(detail.PDFs, item.attachment())
		} else if item.Data.ItemType == "note" && zoteroKey.MatchString(item.Key) {
			detail.Notes = append(detail.Notes, z.note(item))
		}
	}
	detail.More = hasNext(h, start, len(children))
	return detail, nil
}

func (z Zotero) note(item zoteroItem) ZoteroNote {
	body := zoteroNoteText(item.Data.Note)
	title, _, _ := strings.Cut(body, "\n")
	tags, related := z.itemMetadata(item)
	return ZoteroNote{Key: item.Key, Title: cleanField(title, 256), Text: body, Tags: tags, Related: related}
}

func (z Zotero) itemMetadata(item zoteroItem) (tags, related []string) {
	for _, tag := range item.Data.Tags[:min(len(item.Data.Tags), 32)] {
		if v := cleanField(tag.Tag, 256); v != "" {
			tags = append(tags, v)
		}
	}
	var links []string
	raw := item.Data.Relations["dc:relation"]
	if json.Unmarshal(raw, &links) != nil {
		var single string
		if json.Unmarshal(raw, &single) == nil {
			links = []string{single}
		}
	}
	for _, link := range links[:min(len(links), 32)] {
		u, err := url.Parse(link)
		if err != nil || u.Hostname() != "zotero.org" && u.Hostname() != "www.zotero.org" {
			continue
		}
		parent, key, found := strings.Cut(u.Path, "/items/")
		own := z.Group == 0 && strings.HasPrefix(parent, "/users/") || z.Group > 0 && parent == "/groups/"+strconv.Itoa(z.Group)
		if found && own && zoteroKey.MatchString(key) {
			related = append(related, key)
		}
	}
	return tags, related
}

// zoteroNoteText reads text only. The UI never loads embedded resources or HTML.
func zoteroNoteText(source string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(source))
	var out strings.Builder
	var suppressed string
	for out.Len() < 16<<10 {
		token := tokenizer.Next()
		switch token {
		case html.ErrorToken:
			return cleanField(out.String(), 16<<10)
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			name, _ := tokenizer.TagName()
			tag := string(name)
			if suppressed != "" {
				if token == html.EndTagToken && tag == suppressed {
					suppressed = ""
				}
				continue
			}
			switch tag {
			case "script", "style":
				if token == html.StartTagToken {
					suppressed = tag
				}
			case "br", "p", "div", "li", "h1", "h2", "h3", "blockquote", "pre":
				if out.Len() > 0 && !strings.HasSuffix(out.String(), "\n") {
					out.WriteByte('\n')
				}
			}
		case html.TextToken:
			if suppressed == "" {
				out.WriteString(strings.TrimLeftFunc(string(tokenizer.Text()), func(r rune) bool { return r != ' ' && unicode.IsSpace(r) }))
			}
		}
	}
	return cleanField(out.String(), 16<<10)
}

func (z Zotero) Annotations(ctx context.Context, key string, start int) ([]Annotation, bool, error) {
	if !zoteroKey.MatchString(key) {
		return nil, false, fmt.Errorf("无效 Zotero 附件键")
	}
	h, raw, err := z.request(ctx, "GET", paged("/api/users/0/items/"+key+"/children", start, "")+"&itemType=annotation", nil, "")
	if err != nil {
		return nil, false, err
	}
	var items []zoteroItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false, err
	}
	var result []Annotation
	for _, item := range items {
		d := item.Data
		if d.ItemType != "annotation" {
			continue
		}
		var pos struct{ PageIndex *int }
		_ = json.Unmarshal([]byte(d.AnnotationPosition), &pos)
		page := 0
		if pos.PageIndex != nil && *pos.PageIndex >= 0 {
			page = *pos.PageIndex + 1
		}
		result = append(result, Annotation{
			Quote: cleanField(d.AnnotationText, 2048), Note: cleanField(d.AnnotationComment, 2048),
			Label: cleanField(d.AnnotationPageLabel, 32), Kind: cleanField(d.AnnotationType, 32), Page: page,
		})
	}
	return result, hasNext(h, start, len(items)), nil
}

func PDFLink(key string, page int) string {
	if !zoteroKey.MatchString(key) {
		return ""
	}
	base := "zotero://open-pdf/library/items/" + key
	if page > 0 {
		base += "?page=" + strconv.Itoa(page)
	}
	return base
}

func (z Zotero) Save(ctx context.Context, e Entry) error {
	if z.ServerID == "" {
		return fmt.Errorf("该 Zotero 版本没有可用的本地写接口")
	}
	doi := NormalizeDOI(e.DOI)
	if doi != "" && e.Title != "" {
		_, raw, err := z.request(ctx, "GET", paged("/api/users/0/items/top", 0, e.Title), nil, "")
		if err != nil {
			return err
		}
		var items []zoteroItem
		if err := json.Unmarshal(raw, &items); err != nil {
			return err
		}
		for _, item := range items {
			if NormalizeDOI(item.Data.DOI) == doi {
				return fmt.Errorf("Zotero 已有相同 DOI")
			}
		}
	}
	var c cslItem
	if e.Record != nil {
		c = *e.Record
	} else {
		c.Title, c.Venue = e.Title, e.Venue
		for _, name := range strings.Split(e.Author, " and ") {
			if name != "" {
				c.Author = append(c.Author, cslName{Literal: name})
			}
		}
	}
	itemType := "document"
	switch c.Type {
	case "article-journal":
		itemType = "journalArticle"
	case "paper-conference":
		itemType = "conferencePaper"
	case "chapter":
		itemType = "bookSection"
	case "article":
		itemType = "preprint"
	case "book", "report", "thesis", "dataset", "standard":
		itemType = c.Type
	}
	fields, err := z.itemFields(ctx, itemType)
	if err != nil {
		return fmt.Errorf("读取 Zotero 条目字段失败：%w", err)
	}
	allowed := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field.Field] = true
	}
	creators := make([]map[string]string, 0, len(c.Author))
	for _, name := range c.Author {
		if name.Literal != "" {
			creators = append(creators, map[string]string{"creatorType": "author", "name": name.Literal})
		} else if name.Family != "" || name.Given != "" {
			creators = append(creators, map[string]string{"creatorType": "author", "firstName": name.Given, "lastName": name.Family})
		}
	}
	item := map[string]any{"itemType": itemType, "title": c.Title, "creators": creators}
	for _, field := range [][2]string{
		{"publicationTitle", c.Venue}, {"proceedingsTitle", c.Venue}, {"bookTitle", c.Venue},
		{"date", e.Year}, {"DOI", doi}, {"url", c.URL}, {"volume", c.Volume}, {"issue", c.Issue}, {"pages", c.Page},
		{"publisher", c.Publisher}, {"institution", c.Publisher}, {"university", c.Publisher}, {"ISBN", c.ISBN}, {"ISSN", c.ISSN},
		{"repository", c.Archive}, {"archiveID", c.ArchiveLocation},
	} {
		if field[1] != "" && allowed[field[0]] {
			item[field[0]] = field[1]
		}
	}
	return z.createObject(ctx, "/api/users/0/items", item, "")
}

var yearPattern = regexp.MustCompile(`\b[12][0-9]{3}\b`)

func yearOf(date string) string { return yearPattern.FindString(date) }

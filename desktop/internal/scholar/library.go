package scholar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"nomo/desktop/internal/fsutil"
)

type Entry struct {
	Library                                      int
	Ordinal                                      int
	Key, Title, Author, Year, Venue, DOI, Source string
	Abstract, URL, File                          string
	Raw                                          string
	RemoteID                                     string
	ItemType, CreatorSummary                     string
	NumChildren                                  int
	Record                                       *cslItem
	ZoteroNote                                   bool
}

type Library struct {
	Entries []Entry
	byKey   map[string]int
	bytes   int
	disk    *libraryDisk
	total   int
}

func (l *Library) add(e Entry) error {
	// Include map slots, slice capacity and copied metadata in the retained budget.
	if len(e.Key) > 4096 || len(e.Raw) > 1<<20 {
		return fmt.Errorf("bibliography key or record exceeds its byte budget")
	}
	e.Key = strings.Clone(e.Key)
	e.Title = cleanField(e.Title, 1024)
	e.Author = cleanField(e.Author, 512)
	e.Venue = cleanField(e.Venue, 256)
	e.Year = cleanField(e.Year, 16)
	e.DOI = cleanField(e.DOI, 256)
	e.Abstract = cleanField(e.Abstract, 4096)
	e.URL = cleanField(e.URL, 2048)
	e.File = cleanField(e.File, 2048)
	size := 640 + len(e.Key) + len(e.Title) + len(e.Author) + len(e.Venue) + len(e.Year) + len(e.DOI) + len(e.Source) + len(e.Abstract) + len(e.URL) + len(e.File) + len(e.Raw) + len(e.ItemType) + len(e.CreatorSummary)
	if l.disk != nil {
		if _, duplicate := l.disk.find(e.Key); duplicate {
			return fmt.Errorf("引用键 %q 重复", e.Key)
		}
		if err := l.disk.append(e); err != nil {
			return err
		}
	}
	if l.disk == nil && l.bytes+size > (IndexBudget-(128<<10))/2 {
		dir, err := os.MkdirTemp("", "nomo-library-*")
		if err != nil {
			return err
		}
		l.disk = &libraryDisk{dir: dir}
		for _, old := range l.Entries {
			if err := l.disk.append(old); err != nil {
				l.Close()
				return err
			}
		}
		if _, duplicate := l.disk.find(e.Key); duplicate {
			return fmt.Errorf("引用键 %q 重复", e.Key)
		}
		if err := l.disk.append(e); err != nil {
			return err
		}
	}
	l.total++
	if l.disk != nil {
		return nil
	}
	if l.byKey == nil {
		l.byKey = make(map[string]int)
	}
	if prev, ok := l.byKey[e.Key]; ok {
		return fmt.Errorf("引用键 %q 重复（%s、%s）", e.Key, l.Entries[prev].Source, e.Source)
	}
	l.byKey[e.Key] = len(l.Entries)
	l.Entries = append(l.Entries, e)
	l.bytes += size
	return nil
}

func (l *Library) Find(key string) (Entry, bool) {
	if l != nil {
		if i, ok := l.byKey[key]; ok {
			return l.Entries[i], true
		}
	}
	if l != nil && l.disk != nil {
		return l.disk.find(key)
	}
	return Entry{}, false
}

func (l *Library) Search(query string, limit int) []Entry {
	if l != nil && l.disk != nil {
		entries, _ := l.Query(context.Background(), query, limit)
		return entries
	}
	if l == nil || limit <= 0 {
		return nil
	}
	words := strings.Fields(strings.ToLower(query))
	out := make([]Entry, 0, min(limit, len(l.Entries)))
	if exact, ok := l.Find(strings.TrimSpace(query)); ok {
		out = append(out, exact)
	}
	for _, e := range l.Entries {
		if len(out) == limit {
			break
		}
		if len(out) > 0 && e.Key == out[0].Key {
			continue
		}
		if e.Matches(words) {
			out = append(out, e)
		}
	}
	return out
}

func (l *Library) Complete(prefix string, limit int) []Entry {
	if l == nil || limit <= 0 {
		return nil
	}
	if l.disk != nil {
		return l.Search(prefix, limit)
	}
	out := make([]Entry, 0, min(limit, len(l.Entries)))
	for _, e := range l.Entries {
		if strings.HasPrefix(e.Key, prefix) && keyPattern.MatchString(e.Key) {
			out = append(out, e)
			if len(out) == limit {
				return out
			}
		}
	}
	if prefix == "" {
		return out
	}
	words := strings.Fields(strings.ToLower(prefix))
	for _, e := range l.Entries {
		if !strings.HasPrefix(e.Key, prefix) && keyPattern.MatchString(e.Key) && e.Matches(words) {
			out = append(out, e)
			if len(out) == limit {
				break
			}
		}
	}
	return out
}

func (e Entry) Matches(words []string) bool {
	if len(words) == 0 {
		return true
	}
	text := strings.ToLower(strings.Join([]string{e.Key, e.Title, e.Author, e.Year, e.Venue, e.DOI, e.Abstract}, "\n"))
	for _, word := range words {
		if !strings.Contains(text, word) {
			return false
		}
	}
	return true
}

type cslName struct {
	Family  string `json:"family,omitempty"`
	Given   string `json:"given,omitempty"`
	Literal string `json:"literal,omitempty"`
}
type cslItem struct {
	ID              json.RawMessage `json:"id"`
	Title           string          `json:"title"`
	Abstract        string          `json:"abstract,omitempty"`
	Type            string          `json:"type"`
	Author          []cslName       `json:"author"`
	DOI             string          `json:"DOI,omitempty"`
	Venue           string          `json:"container-title,omitempty"`
	Volume          string          `json:"volume,omitempty"`
	Issue           string          `json:"issue,omitempty"`
	Page            string          `json:"page,omitempty"`
	Publisher       string          `json:"publisher,omitempty"`
	URL             string          `json:"URL,omitempty"`
	ISBN            string          `json:"ISBN,omitempty"`
	ISSN            string          `json:"ISSN,omitempty"`
	Archive         string          `json:"archive,omitempty"`
	ArchiveLocation string          `json:"archive_location,omitempty"`
	Issued          struct {
		DateParts [][]int `json:"date-parts,omitempty"`
	} `json:"issued"`
}

func (c cslItem) entry() Entry {
	var e Entry
	if json.Unmarshal(c.ID, &e.Key) != nil {
		e.Key = string(c.ID)
	}
	e.Title, e.DOI, e.Venue, e.Abstract, e.URL = c.Title, c.DOI, c.Venue, c.Abstract, c.URL
	var names []string
	for _, a := range c.Author {
		name := a.Literal
		if name == "" {
			name = strings.TrimSpace(a.Given + " " + a.Family)
		}
		names = append(names, name)
	}
	e.Author = strings.Join(names, " and ")
	if len(c.Issued.DateParts) > 0 && len(c.Issued.DateParts[0]) > 0 {
		e.Year = fmt.Sprint(c.Issued.DateParts[0][0])
	}
	return e
}

func yamlText(n *yaml.Node) string {
	switch n.Kind {
	case yaml.ScalarNode:
		return n.Value
	case yaml.SequenceNode:
		var out []string
		for _, child := range n.Content {
			out = append(out, yamlText(child))
		}
		return strings.Join(out, " and ")
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			switch n.Content[i].Value {
			case "value", "name", "canonical":
				return yamlText(n.Content[i+1])
			}
		}
	}
	return ""
}

func parseLibrary(name string, data []byte, add func(Entry) error) error {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if !utf8.Valid(data) {
		return fmt.Errorf("文献文件需要 UTF-8")
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".bib", ".biblatex":
		return parseBib(string(data), add)
	case ".json":
		if !bytes.HasPrefix(bytes.TrimSpace(data), []byte{'['}) {
			return fmt.Errorf("CSL-JSON 需要数组")
		}
		var items []cslItem
		if err := json.Unmarshal(data, &items); err != nil {
			return err
		}
		for _, item := range items {
			e := item.entry()
			if e.Key == "" {
				return fmt.Errorf("CSL 条目缺少 id")
			}
			if err := add(e); err != nil {
				return err
			}
		}
	case ".yaml", ".yml":
		if len(bytes.TrimSpace(data)) == 0 {
			return nil
		}
		var tree yaml.Node
		dec := yaml.NewDecoder(bytes.NewReader(data))
		if err := dec.Decode(&tree); err != nil {
			return err
		}
		if err := dec.Decode(new(yaml.Node)); err == nil {
			return fmt.Errorf("Hayagriva 需要单个 YAML 文档")
		} else if err != io.EOF {
			return err
		}
		if len(tree.Content) != 1 || tree.Content[0].Kind != yaml.MappingNode {
			return fmt.Errorf("Hayagriva 需要顶层键值映射")
		}
		top := tree.Content[0]
		for i := 0; i+1 < len(top.Content); i += 2 {
			e := Entry{Key: top.Content[i].Value}
			value := top.Content[i+1]
			if e.Key == "" || value.Kind != yaml.MappingNode {
				return fmt.Errorf("Hayagriva 条目格式无效：%s", e.Key)
			}
			for j := 0; j+1 < len(value.Content); j += 2 {
				v := yamlText(value.Content[j+1])
				switch value.Content[j].Value {
				case "title":
					e.Title = v
				case "abstract":
					e.Abstract = v
				case "url":
					e.URL = v
				case "author":
					e.Author = v
				case "date":
					e.Year = v
				case "doi":
					e.DOI = v
				case "serial-number":
					field := value.Content[j+1]
					if field.Kind == yaml.MappingNode {
						for k := 0; k+1 < len(field.Content); k += 2 {
							if field.Content[k].Value == "doi" {
								e.DOI = yamlText(field.Content[k+1])
								break
							}
						}
					}
				}
			}
			if err := add(e); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("不支持的文献文件格式：%s", name)
	}
	return nil
}

// Buffers are immutable snapshots supplied by the caller for open, unsaved bibliographies.
func (p Project) Library(ctx context.Context, names []string, buffers map[string]string) (*Library, error) {
	l := new(Library)
	ok := false
	defer func() {
		if !ok {
			l.Close()
		}
	}()
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var in io.Reader
		var file *os.File
		if text, exists := buffers[name]; exists {
			in = strings.NewReader(text)
		} else {
			path, err := p.Path(name)
			if err != nil {
				return nil, err
			}
			file, err = os.Open(path)
			if err != nil {
				return nil, err
			}
			info, err := file.Stat()
			if err != nil {
				file.Close()
				return nil, err
			}
			if info.Size() > 512<<20 {
				file.Close()
				return nil, fmt.Errorf("bibliography exceeds 512 MiB: %s", name)
			}
			in = file
		}
		err := parseLibraryReader(ctx, name, in, func(e Entry) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			e.Source = name
			return l.add(e)
		})
		if file != nil {
			file.Close()
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	ok = true
	return l, nil
}

var bibliographyWrite sync.Mutex

// Append preserves original bytes and requires an existing project-declared bibliography.
func (p Project) Append(ctx context.Context, names []string, target string, entry Entry, exported []byte) (string, error) {
	bibliographyWrite.Lock()
	defer bibliographyWrite.Unlock()
	if !slices.Contains(names, target) {
		return "", fmt.Errorf("请选择主文件声明的文献文件")
	}
	l := new(Library)
	defer l.Close()
	type baseline struct {
		name, path string
		hash       [32]byte
	}
	var originals []baseline
	var original []byte
	remaining := 64 << 20
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		path, err := p.Path(name)
		if err != nil {
			return "", err
		}
		data, err := Read(path, remaining)
		if err != nil {
			return "", err
		}
		remaining -= len(data)
		originals = append(originals, baseline{name, path, sha256.Sum256(data)})
		if name == target {
			original = data
		}
		if err := parseLibrary(name, data, func(e Entry) error {
			e.Source = name
			return l.add(e)
		}); err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
	}
	ext := strings.ToLower(filepath.Ext(target))
	var rawCSL json.RawMessage
	if len(exported) > 0 {
		if len(exported) > 128<<10 || !utf8.Valid(exported) {
			return "", fmt.Errorf("导出记录超过 128 KiB 或编码无效")
		}
		switch ext {
		case ".bib", ".biblatex":
			item, err := exportedBibEntry(exported)
			if err != nil {
				return "", err
			}
			if entry.DOI != "" && item.DOI != "" && NormalizeDOI(entry.DOI) != NormalizeDOI(item.DOI) {
				return "", fmt.Errorf("Zotero 导出与所选条目的 DOI 不一致")
			}
			if item.DOI == "" {
				item.DOI = entry.DOI
			}
			entry = item
		default:
			var c cslItem
			raw, err := singleCSLItem(exported)
			if err != nil {
				return "", err
			}
			if err := json.Unmarshal(raw, &c); err != nil {
				return "", err
			}
			item := c.entry()
			if item.Title == "" || entry.DOI != "" && item.DOI != "" && NormalizeDOI(entry.DOI) != NormalizeDOI(item.DOI) {
				return "", fmt.Errorf("Zotero CSL 导出与所选条目不一致")
			}
			item.Key, item.Record = entry.Key, &c
			entry, rawCSL = item, raw
		}
	}
	doi := NormalizeDOI(entry.DOI)
	if doi != "" {
		matches, err := l.Query(ctx, doi, l.Count())
		if err != nil {
			return "", err
		}
		for _, other := range matches {
			if NormalizeDOI(other.DOI) == doi {
				return other.Key, nil
			}
		}
	}
	if !keyPattern.MatchString(entry.Key) {
		entry.Key = generatedKey(entry)
	}
	if other, exists := l.Find(entry.Key); exists {
		title, author := entry.Title, entry.Author
		if ext := strings.ToLower(filepath.Ext(other.Source)); len(exported) == 0 && (ext == ".bib" || ext == ".biblatex") {
			title, author = bibEscape.Replace(title), bibEscape.Replace(author)
		}
		if other.Title == title && other.Author == author && other.Year == entry.Year &&
			NormalizeDOI(other.DOI) == doi {
			return other.Key, nil
		}
		return "", fmt.Errorf("引用键 %q 已被占用", entry.Key)
	}
	path, err := p.Path(target)
	if err != nil {
		return "", err
	}
	stat, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !stat.Mode().IsRegular() {
		return "", fmt.Errorf("文献目标必须是普通文件")
	}
	var result []byte
	switch {
	case len(exported) > 0 && (ext == ".bib" || ext == ".biblatex"):
		result = append(original[:len(original):len(original)], '\n', '\n')
		result = append(result, bytes.TrimSpace(bytes.TrimPrefix(exported, []byte{0xef, 0xbb, 0xbf}))...)
		result = append(result, '\n')
	case len(rawCSL) > 0 && ext == ".json":
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(rawCSL, &fields); err != nil {
			return "", err
		}
		fields["id"], _ = json.Marshal(entry.Key)
		var encoded []byte
		encoded, err = json.MarshalIndent(fields, "  ", "  ")
		if err == nil {
			result, err = appendCSL(original, encoded)
		}
	default:
		result, err = appendEntry(target, original, entry)
	}
	if err != nil {
		return "", err
	}
	if len(result)-len(original) > remaining {
		return "", fmt.Errorf("追加后项目文献超过 64 MiB")
	}
	// Validate the edited representation, including duplicate keys.
	check := new(Library)
	defer check.Close()
	if err := parseLibrary(target, result, check.add); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".nomo-bib-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(stat.Mode().Perm()); err == nil {
		_, err = f.Write(result)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	for _, base := range originals {
		currentPath, err := p.Path(base.name)
		if err != nil {
			return "", err
		}
		current, err := Read(currentPath, 64<<20)
		if err != nil {
			return "", err
		}
		if currentPath != base.path || sha256.Sum256(current) != base.hash {
			return "", fmt.Errorf("文献文件已变化，请刷新后重试：%s", base.name)
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return entry.Key, fsutil.ReplaceFile(f.Name(), path)
}

var bibEscape = strings.NewReplacer("\\", `\textbackslash{}`, "{", `\{`, "}", `\}`, "%", `\%`, "&", `\&`, "#", `\#`, "_", `\_`, "$", `\$`)

func singleCSLItem(raw []byte) (json.RawMessage, error) {
	raw = bytes.TrimSpace(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf}))
	if len(raw) > 0 && raw[0] == '{' {
		return raw, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	if len(items) != 1 {
		return nil, fmt.Errorf("需要单条 CSL-JSON 记录")
	}
	return items[0], nil
}

func appendCSL(original, encoded []byte) ([]byte, error) {
	end, begin := bytes.LastIndexByte(original, ']'), bytes.IndexByte(original, '[')
	if begin < 0 || end <= begin {
		return nil, fmt.Errorf("CSL-JSON 需要数组")
	}
	var out bytes.Buffer
	out.Write(original[:end])
	if len(bytes.TrimSpace(original[begin+1:end])) != 0 {
		out.WriteByte(',')
	}
	out.WriteString("\n  ")
	out.Write(encoded)
	out.WriteByte('\n')
	out.Write(original[end:])
	return out.Bytes(), nil
}

func appendEntry(path string, original []byte, e Entry) ([]byte, error) {
	var c cslItem
	if e.Record != nil {
		c = *e.Record
	} else {
		c.Title, c.DOI, c.Venue = e.Title, e.DOI, e.Venue
		c.Type = "document"
		for _, name := range strings.Split(e.Author, " and ") {
			if name != "" {
				c.Author = append(c.Author, cslName{Literal: name})
			}
		}
		if year, err := strconv.Atoi(e.Year); err == nil {
			c.Issued.DateParts = [][]int{{year}}
		}
	}
	if c.Title == "" {
		return nil, fmt.Errorf("文献缺少标题")
	}
	c.ID, _ = json.Marshal(e.Key)
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		encoded, err := json.MarshalIndent(c, "  ", "  ")
		if err != nil {
			return nil, err
		}
		return appendCSL(original, encoded)
	case ".bib", ".biblatex":
		kind, container := "misc", "howpublished"
		switch c.Type {
		case "article-journal", "article-magazine", "article-newspaper":
			kind, container = "article", "journal"
		case "paper-conference":
			kind, container = "inproceedings", "booktitle"
		case "chapter", "entry-encyclopedia", "entry-dictionary":
			kind, container = "incollection", "booktitle"
		case "book":
			kind = "book"
		case "thesis":
			kind = "phdthesis"
		case "report":
			kind = "techreport"
		}
		latex := strings.EqualFold(filepath.Ext(path), ".biblatex")
		if latex && container == "journal" {
			container = "journaltitle"
		}
		if latex && kind == "phdthesis" {
			kind = "thesis"
		}
		var out strings.Builder
		fmt.Fprintf(&out, "\n\n@%s{%s,\n", kind, e.Key)
		var names []string
		for _, a := range c.Author {
			if a.Literal != "" {
				names = append(names, "{"+bibEscape.Replace(a.Literal)+"}")
				continue
			}
			name := bibEscape.Replace(a.Family)
			if a.Given != "" && name != "" {
				name += ", "
			}
			names = append(names, name+bibEscape.Replace(a.Given))
		}
		if len(names) > 0 {
			fmt.Fprintf(&out, "  author = {%s},\n", strings.Join(names, " and "))
		}
		publisher := "publisher"
		if kind == "phdthesis" {
			publisher = "school"
		} else if kind == "techreport" || kind == "thesis" {
			publisher = "institution"
		}
		for _, f := range [][2]string{{"title", c.Title}, {"year", e.Year}, {container, c.Venue}, {"volume", c.Volume}, {"number", c.Issue},
			{"pages", c.Page}, {publisher, c.Publisher}, {"isbn", c.ISBN}, {"issn", c.ISSN}} {
			if f[1] != "" {
				fmt.Fprintf(&out, "  %s = {%s},\n", f[0], bibEscape.Replace(f[1]))
			}
		}
		for _, f := range [][2]string{{"doi", c.DOI}, {"url", c.URL}, {"archiveprefix", c.Archive}, {"eprint", c.ArchiveLocation}} {
			if f[1] != "" {
				if strings.ContainsAny(f[1], "{}\\\r\n") {
					return nil, fmt.Errorf("文献字段 %s 含不支持的字符", f[0])
				}
				fmt.Fprintf(&out, "  %s = {%s},\n", f[0], f[1])
			}
		}
		return append(original[:len(original):len(original)], out.String()+"}\n"...), nil
	case ".yml", ".yaml":
		kind, parent := "misc", ""
		switch c.Type {
		case "article-journal", "article-magazine", "article-newspaper":
			kind, parent = "article", "periodical"
		case "paper-conference":
			kind, parent = "article", "proceedings"
		case "chapter", "entry-encyclopedia", "entry-dictionary":
			kind, parent = "chapter", "book"
		case "book", "thesis", "report":
			kind = c.Type
		case "webpage":
			kind = "web"
		case "article":
			kind = "article"
		}
		var out strings.Builder
		key, _ := json.Marshal(e.Key)
		fmt.Fprintf(&out, "\n%s:\n  type: %s\n", key, kind)
		field := func(indent, name, value string) {
			if value != "" {
				b, _ := json.Marshal(value)
				fmt.Fprintf(&out, "%s%s: %s\n", indent, name, b)
			}
		}
		field("  ", "title", c.Title)
		field("  ", "date", e.Year)
		field("  ", "url", c.URL)
		field("  ", "page-range", c.Page)
		if len(c.Author) > 0 {
			out.WriteString("  author:\n")
			for _, name := range c.Author {
				value := name.Literal
				if value == "" {
					value = strings.Trim(strings.TrimSpace(name.Family+", "+name.Given), ", ")
				}
				b, _ := json.Marshal(value)
				fmt.Fprintf(&out, "    - %s\n", b)
			}
		}
		if c.DOI != "" || c.ISBN != "" || c.ISSN != "" {
			out.WriteString("  serial-number:\n")
			field("    ", "doi", c.DOI)
			field("    ", "isbn", c.ISBN)
			field("    ", "issn", c.ISSN)
		}
		indent := "  "
		if parent != "" && c.Venue != "" {
			fmt.Fprintf(&out, "  parent:\n    type: %s\n", parent)
			indent = "    "
			field(indent, "title", c.Venue)
		}
		field(indent, "publisher", c.Publisher)
		field(indent, "volume", c.Volume)
		field(indent, "issue", c.Issue)
		return append(original[:len(original):len(original)], out.String()...), nil
	}
	return nil, fmt.Errorf("不支持的文献文件格式：%s", path)
}

// ExportBibTeX supplies a clipboard import representation for older Zotero clients.
func ExportBibTeX(e Entry) (string, error) {
	if e.Raw != "" && (strings.EqualFold(filepath.Ext(e.Source), ".bib") || strings.EqualFold(filepath.Ext(e.Source), ".biblatex")) {
		return e.Raw, nil
	}
	if !keyPattern.MatchString(e.Key) {
		e.Key = generatedKey(e)
	}
	data, err := appendEntry("export.bib", nil, e)
	return strings.TrimSpace(string(data)), err
}

// Peek reads resident metadata only. UI callers use Query for nonresident keys.
func (l *Library) Peek(key string) (Entry, bool) {
	if l == nil {
		return Entry{}, false
	}
	if i, ok := l.byKey[key]; ok {
		return l.Entries[i], true
	}
	if l.disk != nil {
		l.disk.mu.Lock()
		defer l.disk.mu.Unlock()
		if hit := l.disk.cache[key]; hit != nil {
			return hit.Value.(libraryCached).entry, true
		}
	}
	return Entry{}, false
}

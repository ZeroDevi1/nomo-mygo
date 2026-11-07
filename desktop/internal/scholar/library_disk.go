package scholar

import (
	"bufio"
	"bytes"
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

// Large libraries spill their complete metadata to project-session temporary
// buckets. The immutable first page and a byte-bounded on-demand LRU share 8 MiB.
// Query scans all buckets on a worker, so eviction never turns known keys into
// unknown citations and large collections never silently lose search results.
type libraryDisk struct {
	dir     string
	mu      sync.Mutex
	life    sync.RWMutex
	written int64
	seen    [8192]uint64
	cache   map[string]*list.Element
	order   list.List
	bytes   int
}
type libraryCached struct {
	entry Entry
	bytes int
}

func entryBytes(e Entry) int {
	return 640 + len(e.Key) + len(e.Title) + len(e.Author) + len(e.Year) + len(e.Venue) + len(e.DOI) + len(e.Source) + len(e.Abstract) + len(e.URL) + len(e.File) + len(e.Raw)
}
func bucket(key string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return fmt.Sprintf("%02x.jsonl", h.Sum32()%64)
}
func (d *libraryDisk) append(e Entry) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if d.written+int64(len(raw)) > 128<<20 {
		return errors.New("session bibliography metadata exceeds 128 MiB")
	}
	f, err := os.OpenFile(filepath.Join(d.dir, bucket(e.Key)), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(raw)
	if err = errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	d.written += int64(len(raw))
	d.markSeen(e.Key)
	return nil
}
func bloomHashes(key string) (uint64, uint64) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	v := h.Sum64()
	return v, v>>23 | v<<41
}
func (d *libraryDisk) markSeen(key string) {
	a, b := bloomHashes(key)
	for n := uint64(0); n < 3; n++ {
		bit := (a + n*b) % (8192 * 64)
		d.seen[bit/64] |= 1 << (bit % 64)
	}
}
func (d *libraryDisk) maybeSeen(key string) bool {
	a, b := bloomHashes(key)
	for n := uint64(0); n < 3; n++ {
		bit := (a + n*b) % (8192 * 64)
		if d.seen[bit/64]&(1<<(bit%64)) == 0 {
			return false
		}
	}
	return true
}

func (d *libraryDisk) find(key string) (Entry, bool) {
	d.life.RLock()
	defer d.life.RUnlock()
	if !d.maybeSeen(key) {
		return Entry{}, false
	}
	d.mu.Lock()
	if hit := d.cache[key]; hit != nil {
		d.order.MoveToFront(hit)
		entry := hit.Value.(libraryCached).entry
		d.mu.Unlock()
		return entry, true
	}
	d.mu.Unlock()
	f, err := os.Open(filepath.Join(d.dir, bucket(key)))
	if err != nil {
		return Entry{}, false
	}
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReader(f))
	for {
		var e Entry
		if dec.Decode(&e) != nil {
			break
		}
		if e.Key == key {
			d.mu.Lock()
			d.retain(e)
			d.mu.Unlock()
			return e, true
		}
	}
	return Entry{}, false
}

func (d *libraryDisk) retain(e Entry) {
	if hit := d.cache[e.Key]; hit != nil {
		d.order.MoveToFront(hit)
		return
	}
	size := entryBytes(e)
	if size > (IndexBudget-(128<<10))/2 {
		return
	}
	if d.cache == nil {
		d.cache = make(map[string]*list.Element)
	}
	for d.bytes+size > (IndexBudget-(128<<10))/2 {
		last := d.order.Back()
		old := last.Value.(libraryCached)
		delete(d.cache, old.entry.Key)
		d.bytes -= old.bytes
		d.order.Remove(last)
	}
	d.cache[e.Key] = d.order.PushFront(libraryCached{e, size})
	d.bytes += size
}
func (l *Library) Close() {
	if l != nil && l.disk != nil {
		l.disk.life.Lock()
		defer l.disk.life.Unlock()
		l.disk.mu.Lock()
		defer l.disk.mu.Unlock()
		os.RemoveAll(l.disk.dir)
		l.disk.cache = nil
		l.disk.order.Init()
		l.disk.bytes = 0
	}
}
func (l *Library) Count() int {
	if l == nil {
		return 0
	}
	return l.total
}
func (l *Library) OnDemand() bool { return l != nil && l.disk != nil }

func (l *Library) Query(ctx context.Context, query string, limit int) ([]Entry, error) {
	if l == nil || limit <= 0 {
		return nil, nil
	}
	if l.disk == nil {
		return l.Search(query, limit), nil
	}
	var out []Entry
	if keyPattern.MatchString(strings.TrimSpace(query)) {
		if exact, ok := l.Find(strings.TrimSpace(query)); ok {
			exact.Raw = ""
			out = append(out, exact)
		}
	}
	if len(out) == limit {
		return out, nil
	}
	l.disk.life.RLock()
	defer l.disk.life.RUnlock()
	words := strings.Fields(strings.ToLower(query))
	for n := 0; n < 64; n++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f, err := os.Open(filepath.Join(l.disk.dir, fmt.Sprintf("%02x.jsonl", n)))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		dec := json.NewDecoder(bufio.NewReader(f))
		for {
			var e Entry
			err = dec.Decode(&e)
			if err != nil {
				break
			}
			if len(out) > 0 && e.Key == out[0].Key {
				continue
			}
			if e.Matches(words) {
				e.Raw = ""
				out = append(out, e)
				if len(out) == limit {
					break
				}
			}
			if ctx.Err() != nil {
				err = ctx.Err()
				break
			}
		}
		f.Close()
		if err != nil && err != io.EOF {
			return nil, err
		}
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// Bibliography parsing is streamed by record. Per-record and macro budgets
// bound temporary allocations independently of the complete collection size.
func parseLibraryReader(ctx context.Context, name string, r io.Reader, add func(Entry) error) error {
	buffered := bufio.NewReader(r)
	if prefix, _ := buffered.Peek(3); bytes.Equal(prefix, []byte{0xef, 0xbb, 0xbf}) {
		_, _ = buffered.Discard(3)
	}
	r = buffered
	switch strings.ToLower(filepath.Ext(name)) {
	case ".json":
		return readJSONBibliography(ctx, name, r, add)
	case ".bib", ".biblatex":
		br := bufio.NewReader(r)
		macros := make(map[string]string)
		for i, month := range strings.Fields("jan feb mar apr may jun jul aug sep oct nov dec") {
			macros[month] = fmt.Sprintf("%02d", i+1)
		}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			c, err := br.ReadByte()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			if c == '%' {
				for {
					_, err := br.ReadSlice('\n')
					if err == io.EOF {
						return nil
					}
					if err == nil {
						break
					}
					if err != bufio.ErrBufferFull {
						return err
					}
				}
				continue
			}
			if c != '@' {
				continue
			}
			var record strings.Builder
			record.WriteByte(c)
			close := byte(0)
			depth := 0
			quoted := false
			escaped := false
			comment := false
			for {
				c, err = br.ReadByte()
				if err != nil {
					return fmt.Errorf("unclosed BibTeX record: %w", err)
				}
				record.WriteByte(c)
				if record.Len() > 1<<20 {
					return errors.New("bibliography record exceeds 1 MiB")
				}
				if close == 0 {
					if c == '{' {
						close = '}'
						depth = 1
					} else if c == '(' {
						close = ')'
						depth = 1
					}
					continue
				}
				if comment {
					if c == '\n' {
						comment = false
					}
					continue
				}
				if c == '%' && depth == 1 && !quoted {
					comment = true
					continue
				}
				if escaped {
					escaped = false
					continue
				}
				if c == '\\' {
					escaped = true
					continue
				}
				if c == '"' && depth == 1 {
					quoted = !quoted
				}
				if c == '{' {
					depth++
				} else if c == '}' {
					depth--
				} else if close == ')' && c == ')' && !quoted && depth == 1 {
					depth--
				}
				if depth == 0 {
					break
				}
			}
			if !utf8.ValidString(record.String()) {
				return errors.New("bibliography requires UTF-8")
			}
			if err := scanBibWithMacros(record.String(), add, false, macros); err != nil {
				return err
			}
			macroBytes := 0
			for k, v := range macros {
				macroBytes += len(k) + len(v)
			}
			if macroBytes > 1<<20 {
				return errors.New("BibTeX macros exceed 1 MiB")
			}
		}
	case ".yaml", ".yml":
		scan := bufio.NewScanner(r)
		scan.Buffer(make([]byte, 4096), 1<<20)
		var record strings.Builder
		inEntry := false
		flush := func() error {
			if strings.TrimSpace(record.String()) == "" {
				return nil
			}
			err := parseLibrary(name, []byte(record.String()), add)
			record.Reset()
			return err
		}
		for scan.Scan() {
			if err := ctx.Err(); err != nil {
				return err
			}
			line := scan.Text()
			if len(line) > 0 && line[0] != ' ' && line[0] != '\t' && line[0] != '#' && strings.Contains(line, ":") && inEntry {
				if err := flush(); err != nil {
					return err
				}
			}
			if len(line) > 0 && line[0] != ' ' && line[0] != '\t' && line[0] != '#' && strings.Contains(line, ":") {
				inEntry = true
			}
			record.WriteString(line)
			record.WriteByte('\n')
			if record.Len() > 1<<20 {
				return errors.New("Hayagriva record exceeds 1 MiB")
			}
		}
		if err := scan.Err(); err != nil {
			return err
		}
		if !inEntry {
			return nil
		}
		return flush()
	}
	return fmt.Errorf("unsupported bibliography %s", name)
}

// Read one CSL object at a time, enforcing the allocation budget before the
// decoder sees the value. This also rejects trailing commas and trailing data.
func readJSONBibliography(ctx context.Context, name string, r io.Reader, add func(Entry) error) error {
	br := bufio.NewReader(r)
	steps := 0
	next := func() (byte, error) {
		for {
			steps++
			if steps%4096 == 0 {
				if err := ctx.Err(); err != nil {
					return 0, err
				}
			}
			c, err := br.ReadByte()
			if err != nil {
				return 0, err
			}
			if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
				return c, nil
			}
		}
	}
	c, err := next()
	if err != nil {
		return err
	}
	if c != '[' {
		return errors.New("CSL-JSON requires an array")
	}
	c, err = next()
	if err != nil {
		return err
	}
	for c != ']' {
		if c != '{' {
			return errors.New("CSL-JSON requires object records")
		}
		var record bytes.Buffer
		record.WriteByte(c)
		depth, quoted, escaped := 1, false, false
		for depth > 0 {
			c, err = br.ReadByte()
			if err != nil {
				return err
			}
			if record.Len() >= 1<<20 {
				return errors.New("bibliography record exceeds 1 MiB")
			}
			record.WriteByte(c)
			if record.Len()%4096 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if escaped {
				escaped = false
				continue
			}
			if quoted && c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				quoted = !quoted
				continue
			}
			if !quoted {
				switch c {
				case '{', '[':
					depth++
				case '}', ']':
					depth--
				}
			}
		}
		data := append([]byte{'['}, record.Bytes()...)
		data = append(data, ']')
		if err := parseLibrary(name, data, add); err != nil {
			return err
		}
		c, err = next()
		if err != nil {
			return err
		}
		if c == ']' {
			break
		}
		if c != ',' {
			return errors.New("CSL-JSON requires a comma between records")
		}
		c, err = next()
		if err != nil {
			return err
		}
		if c == ']' {
			return errors.New("CSL-JSON has a trailing comma")
		}
	}
	if _, err := next(); err != io.EOF {
		return errors.New("unexpected trailing bibliography data")
	}
	return ctx.Err()
}

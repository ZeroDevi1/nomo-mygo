package emoji

import (
	"encoding/json/v2"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/unicode/norm"
)

// 目录构建、搜索与冒号查询，逐条对应前端 src/lib/emoji/catalog.ts，两个外壳的结果一致。

// Groups 是选择器列出的分类（emojibase 分组号，跳过 2 号肤色/发型组件），顺序与原版一致。
var Groups = []int{0, 1, 3, 4, 6, 5, 7, 8, 9}

// Entry 是一个表情：Shortcodes 首项为主短代码；Terms 为规范化后的检索词（短代码、中英文名、标签）。
type Entry struct {
	ID         string
	Unicode    string
	En, Zh     string
	Group      int
	Order      int
	Shortcodes []string
	Terms      []string
}

type compactEmoji struct {
	Hexcode string         `json:"hexcode"`
	Unicode string         `json:"unicode"`
	Label   string         `json:"label"`
	Group   *int           `json:"group"`
	Order   *int           `json:"order"`
	Tags    []string       `json:"tags"`
	Skins   []compactEmoji `json:"skins"`
}

// Normalize 规范化检索词：NFKC、去首尾空白与一个首尾冒号、小写。
func Normalize(text string) string {
	s := strings.TrimSpace(norm.NFKC.String(text))
	s = strings.TrimPrefix(s, ":")
	s = strings.TrimSuffix(s, ":")
	return strings.ToLower(s)
}

var nonWord = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// Build 由原始数据构建目录，按 emojibase 顺序排列。只保留有界的元数据，不持有原始 JSON。
func Build(p *Payload) ([]Entry, error) {
	var en, zh []compactEmoji
	if err := json.Unmarshal(p.En, &en); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(p.Zh, &zh); err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(p.Shortcodes, &raw); err != nil {
		return nil, err
	}
	flatten := func(list []compactEmoji) []compactEmoji {
		var out []compactEmoji
		for _, e := range list {
			out = append(out, e)
			out = append(out, e.Skins...)
		}
		return out
	}
	chinese := map[string]compactEmoji{}
	for _, e := range flatten(zh) {
		chinese[e.Hexcode] = e
	}
	var out []Entry
	for _, e := range flatten(en) {
		if e.Group == nil || !slices.Contains(Groups, *e.Group) {
			continue
		}
		var shortcodes []string
		switch v := raw[e.Hexcode].(type) {
		case string:
			shortcodes = []string{v}
		case []any:
			for _, s := range v {
				if s, ok := s.(string); ok {
					shortcodes = append(shortcodes, s)
				}
			}
		}
		name := strings.Trim(nonWord.ReplaceAllString(strings.ToLower(e.Label), "_"), "_")
		if name != "" && !slices.Contains(shortcodes, name) {
			shortcodes = append(shortcodes, name)
		}
		z, ok := chinese[e.Hexcode]
		zhLabel := e.Label
		if ok && z.Label != "" {
			zhLabel = z.Label
		}
		var terms []string
		seen := map[string]bool{}
		for _, t := range slices.Concat(shortcodes, []string{e.Label, z.Label}, e.Tags, z.Tags) {
			if t == "" {
				continue
			}
			if n := Normalize(t); !seen[n] {
				seen[n] = true
				terms = append(terms, n)
			}
		}
		order := int(^uint(0) >> 1)
		if e.Order != nil {
			order = *e.Order
		}
		out = append(out, Entry{ID: e.Hexcode, Unicode: e.Unicode, En: e.Label, Zh: zhLabel, Group: *e.Group, Order: order, Shortcodes: shortcodes, Terms: terms})
	}
	slices.SortStableFunc(out, func(a, b Entry) int { return a.Order - b.Order })
	return out, nil
}

// Search 返回匹配 text 的表情：每个词须出现在某个检索词中（或等于表情本身）；group 为 -1 表示不限分类；
// limit <= 0 不限条数。排序：短代码或表情完全相同 < 检索词完全相同 < 短代码前缀 < 检索词前缀 < 包含，
// 同分按 emojibase 顺序。
func Search(cat []Entry, text string, group, limit int) []Entry {
	query := Normalize(text)
	words := strings.Fields(query)
	type match struct {
		e     *Entry
		score int
	}
	var matches []match
	for i := range cat {
		e := &cat[i]
		if group >= 0 && e.Group != group {
			continue
		}
		if query == "" {
			matches = append(matches, match{e, 0})
			continue
		}
		ok := true
		for _, w := range words {
			if e.Unicode != w && !slices.ContainsFunc(e.Terms, func(t string) bool { return strings.Contains(t, w) }) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		score := 4
		switch {
		case slices.Contains(e.Shortcodes, query) || e.Unicode == query:
			score = 0
		case slices.Contains(e.Terms, query):
			score = 1
		case slices.ContainsFunc(e.Shortcodes, func(t string) bool { return strings.HasPrefix(t, query) }):
			score = 2
		case slices.ContainsFunc(e.Terms, func(t string) bool { return strings.HasPrefix(t, query) }):
			score = 3
		}
		matches = append(matches, match{e, score})
	}
	slices.SortStableFunc(matches, func(a, b match) int {
		if a.score != b.score {
			return a.score - b.score
		}
		return a.e.Order - b.e.Order
	})
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]Entry, len(matches))
	for i, m := range matches {
		out[i] = *m.e
	}
	return out
}

// Query 是光标前的冒号查询：[From, To) 为含冒号的原文字节区间，Text 为冒号后的检索文字。
type Query struct {
	From, To int
	Text     string
}

// queryRe 对应原版 /(^|[\s\p{P}])(:([\p{L}\p{N}_+\-]{1,64}):?)$/u；RE2 的 \s 只含 ASCII 空白，补上 \p{Z}。
var queryRe = regexp.MustCompile(`(^|[\s\p{Z}\p{P}])(:([\p{L}\p{N}_+\-]{1,64}):?)$`)

// FindQuery 在光标前的文字 before（只需传入末尾一小段）中识别冒号查询，end 为光标的原文偏移。
// 时间（12:30）、协议（https:）、盘符、词中冒号与“::”不识别。
func FindQuery(before string, end int) (Query, bool) {
	m := queryRe.FindStringSubmatchIndex(before)
	if m == nil {
		return Query{}, false
	}
	if lead := before[m[2]:m[3]]; lead != "" && strings.ContainsAny(lead, "\\/:`_$") {
		return Query{}, false
	}
	return Query{From: end - (m[5] - m[4]), To: end, Text: before[m[6]:m[7]]}, true
}

// catalogState 是进程内共享的已构建目录（约两千项、1 MB 量级，加载后常驻，与原版每窗口一份等价）；
// 失败后冷却一分钟，避免每次按键重复请求，显式重试除外。
var catalogState struct {
	mu          sync.Mutex
	cat         []Entry
	failedUntil time.Time
}

// ErrUnavailable 表示目录在失败冷却期内，不重新下载。
var ErrUnavailable = errors.New("emoji catalog unavailable")

// Catalog 返回共享目录，首次调用时加载（读缓存或下载，最长约 12 秒，须在后台调用）。retry 为 true 时忽略失败冷却。
func Catalog(appID string, retry bool) ([]Entry, error) {
	catalogState.mu.Lock()
	defer catalogState.mu.Unlock()
	if catalogState.cat != nil {
		return catalogState.cat, nil
	}
	if !retry && time.Now().Before(catalogState.failedUntil) {
		return nil, ErrUnavailable
	}
	p, err := LoadPayload(appID)
	var cat []Entry
	if err == nil {
		cat, err = Build(p)
	}
	if err == nil && len(cat) == 0 {
		err = errors.New("emoji catalog empty")
	}
	if err != nil {
		catalogState.failedUntil = time.Now().Add(time.Minute)
		return nil, err
	}
	catalogState.cat = cat
	return cat, nil
}

// Label 返回表情在界面语言下的名称：中文界面用中文名，其余用英文名。
func (e Entry) Label(chinese bool) string {
	if chinese {
		return e.Zh
	}
	return e.En
}

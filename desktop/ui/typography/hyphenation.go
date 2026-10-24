package typography

import (
	"strings"
	"sync"
	"unicode"
	"unicode/utf16"
)

// 英文自动断词：Liang 算法移植自 Hypher 0.2.5（BSD，见 THIRD_PARTY_NOTICES.md）的 createTrie 与 hyphenate，
// 模式数据为 hyphenation.en-us 0.2.1（见 hyphen_en_us.go）。候选单词的筛选规则同原版 hyphenation.ts。

type trieNode struct {
	next   map[rune]*trieNode
	points []int
}

var (
	enTrieOnce sync.Once
	enTrie     *trieNode

	// hyphenCache 缓存单词的断词偏移（原版同样最多 512 项，满时淘汰最早插入的一项）。
	hyphenMu    sync.Mutex
	hyphenCache = map[string][]int{}
	hyphenOrder []string
)

const hyphenCacheMax = 512

// buildTrie 按 Hypher 的格式建立模式字典树：每组字符串按组长切成定长模式，字母构成路径，数字为各间隙的权值。
func buildTrie(patterns map[int]string) *trieNode {
	root := &trieNode{}
	for size, all := range patterns {
		for start := 0; start < len(all); start += size {
			p := all[start:min(len(all), start+size)]
			var chars []rune
			points := []int{0}
			for _, r := range p {
				if r >= '0' && r <= '9' {
					points[len(points)-1] = int(r - '0')
				} else {
					chars = append(chars, r)
					points = append(points, 0)
				}
			}
			t := root
			for _, c := range chars {
				if t.next == nil {
					t.next = map[rune]*trieNode{}
				}
				n := t.next[c]
				if n == nil {
					n = &trieNode{}
					t.next[c] = n
				}
				t = n
			}
			t.points = points
		}
	}
	return root
}

// hyphenate 返回单词（小写）的断点偏移（Hypher.hyphenate：在“_word_”上匹配所有模式，取各间隙最大权值，奇数为断点，
// 并满足左右最少字母数）。
func hyphenate(word string) []int {
	enTrieOnce.Do(func() { enTrie = buildTrie(enUSPatterns) })
	chars := []rune("_" + word + "_")
	n := len(chars)
	points := make([]int, n+1)
	for i := 0; i < n; i++ {
		node := enTrie
		for j := i; j < n; j++ {
			node = node.next[chars[j]]
			if node == nil {
				break
			}
			for k, v := range node.points {
				if i+k < len(points) && v > points[i+k] {
					points[i+k] = v
				}
			}
		}
	}
	var out []int
	for i := 1; i < n-1; i++ {
		if i > enUSLeftMin && i < n-enUSRightMin && points[i]%2 == 1 {
			out = append(out, i-1)
		}
	}
	return out
}

func isWordAdjacent(u uint16) bool {
	if utf16.IsSurrogate(rune(u)) {
		return false
	}
	r := rune(u)
	return unicode.Is(unicode.Latin, r) || unicode.IsNumber(r) || unicode.IsMark(r) || r == '_' || r == '-'
}

func isTokenDelimiter(u uint16) bool {
	r := rune(u)
	return unicode.IsSpace(r) || strings.ContainsRune("，。、；：！？（）《》“”‘’", r)
}

// EnglishHyphenationPoints 返回可断词的 UTF-16 偏移（原版 englishHyphenationPoints）：eligible 与 source 等长，
// 不可断词的部分已替换为 U+FFFC。只处理 8–80 个 ASCII 字母的全小写或首字母大写单词，排除紧邻字母数字、链接、
// 邮箱、路径与带点标识符。
func EnglishHyphenationPoints(eligible, source string) map[int]bool {
	points := map[int]bool{}
	e := utf16.Encode([]rune(eligible))
	s := utf16.Encode([]rune(source))
	letter := func(u uint16) bool { return u >= 'a' && u <= 'z' || u >= 'A' && u <= 'Z' }
	for i := 0; i < len(e); {
		if !letter(e[i]) {
			i++
			continue
		}
		start := i
		for i < len(e) && letter(e[i]) {
			i++
		}
		if i-start < 8 || i-start > 80 || start >= len(s) {
			continue
		}
		word := string(utf16.Decode(e[start:i]))
		if !validWord(word) {
			continue
		}
		if start > 0 && isWordAdjacent(s[start-1]) || i < len(s) && isWordAdjacent(s[i]) {
			continue
		}
		left, right := start, i
		for left > 0 && !isTokenDelimiter(s[left-1]) {
			left--
		}
		for right < len(s) && !isTokenDelimiter(s[right]) {
			right++
		}
		if urlLike(string(utf16.Decode(s[left:right]))) {
			continue
		}
		for _, off := range cachedHyphenate(strings.ToLower(word)) {
			points[start+off] = true
		}
	}
	return points
}

func validWord(w string) bool {
	lower := func(s string) bool {
		for i := 0; i < len(s); i++ {
			if s[i] < 'a' || s[i] > 'z' {
				return false
			}
		}
		return s != ""
	}
	return lower(w) || w[0] >= 'A' && w[0] <= 'Z' && lower(w[1:])
}

// urlLike 对应原版 /[@/\\]|[A-Za-z0-9]\.[A-Za-z0-9]|(?:https?|www)[.:]/i。
func urlLike(t string) bool {
	if strings.ContainsAny(t, "@/\\") {
		return true
	}
	an := func(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' }
	for i := 1; i+1 < len(t); i++ {
		if t[i] == '.' && an(t[i-1]) && an(t[i+1]) {
			return true
		}
	}
	l := strings.ToLower(t)
	for _, p := range []string{"http.", "http:", "https.", "https:", "www.", "www:"} {
		if strings.Contains(l, p) {
			return true
		}
	}
	return false
}

func cachedHyphenate(key string) []int {
	hyphenMu.Lock()
	defer hyphenMu.Unlock()
	if v, ok := hyphenCache[key]; ok {
		return v
	}
	v := hyphenate(key)
	if len(hyphenCache) >= hyphenCacheMax {
		delete(hyphenCache, hyphenOrder[0])
		hyphenOrder = hyphenOrder[1:]
	}
	hyphenCache[key] = v
	hyphenOrder = append(hyphenOrder, key)
	return v
}

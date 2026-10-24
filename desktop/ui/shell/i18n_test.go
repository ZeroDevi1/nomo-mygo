package shell

import (
	"regexp"
	"slices"
	"testing"

	"nomo/desktop/internal/i18n"
)

// TestJapaneseMessages：日文表与英文表的键一一对应、占位符一致，tr 在日文界面下取日文。
func TestJapaneseMessages(t *testing.T) {
	ph := regexp.MustCompile(`\{[A-Za-z0-9_]+\}`)
	for k, en := range msgEnUS {
		ja, ok := msgJaJP[k]
		if !ok {
			t.Errorf("日文缺少 %q", k)
			continue
		}
		a, b := ph.FindAllString(en, -1), ph.FindAllString(ja, -1)
		slices.Sort(a)
		slices.Sort(b)
		if !slices.Equal(a, b) {
			t.Errorf("%q 占位符不一致：%v / %v", k, a, b)
		}
	}
	if len(msgJaJP) != len(msgEnUS) {
		t.Errorf("日文 %d 条，英文 %d 条", len(msgJaJP), len(msgEnUS))
	}
	s := &Shell{locale: i18n.JaJP}
	if got := s.tr("snapshotCount", "count", "3"); got != "3 件のスナップショット" {
		t.Errorf("tr = %q", got)
	}
}

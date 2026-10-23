package typography

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

// TestCrosscheckWithOriginal 对照原版依赖（linebreak 1.1.0、hypher 0.2.5 + hyphenation.en-us 0.2.1）在测试集段落上
// 的结果：断词点须完全一致；UAX #14 断行机会允许少量差异（原版为 Unicode 13 数据，go-text 为更新的版本），差异
// 超过 1% 时失败并列出样例。是否强制不比较：两库在文末的标注不同，CreateInput 只使用机会是否存在。testdata/crosscheck.json 由测试集段落生成。
func TestCrosscheckWithOriginal(t *testing.T) {
	data, err := os.ReadFile("testdata/crosscheck.json")
	if err != nil {
		t.Skip(err)
	}
	var cases []struct {
		Text   string
		Breaks [][2]any
		Words  map[string][]int
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	total, diff := 0, 0
	for _, c := range cases {
		got := lineOpportunities(c.Text)
		want := map[int]bool{}
		for _, b := range c.Breaks {
			want[int(b[0].(float64))] = b[1].(bool)
		}
		for k, v := range want {
			total++
			_ = v // 是否强制只在段末不同（两库标注方式不同），规则只使用机会是否存在
			if _, ok := got[k]; !ok {
				diff++
				if diff <= 5 {
					t.Logf("缺少或强制性不同：%q 位置 %d", c.Text, k)
				}
			}
		}
		for k := range got {
			if _, ok := want[k]; !ok {
				diff++
				total++
				if diff <= 5 {
					t.Logf("多出断点：%q 位置 %d", c.Text, k)
				}
			}
		}
		for w, offs := range c.Words {
			if h := hyphenate(w); !slices.Equal(h, offs) && !(len(h) == 0 && len(offs) == 0) {
				t.Errorf("断词 %q：%v，原版 %v", w, h, offs)
			}
		}
	}
	t.Logf("断行机会 %d 处，差异 %d 处", total, diff)
	if diff*100 > total {
		t.Fatalf("断行机会差异 %d/%d 超过 1%%", diff, total)
	}
}

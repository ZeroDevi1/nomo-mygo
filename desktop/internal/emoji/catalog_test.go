package emoji

import (
	"context"
	"encoding/json/jsontext"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fixture 与前端 src/lib/emoji/catalog.fixture.ts 相同。
var fixture = &Payload{
	Version: "17.0.0",
	En: jsontext.Value(`[
		{"hexcode":"1F604","unicode":"😄","label":"grinning face with smiling eyes","group":0,"order":3,"tags":["happy","smile"]},
		{"hexcode":"2764","unicode":"❤️","label":"red heart","group":0,"order":10,"tags":["love"]},
		{"hexcode":"1F431","unicode":"🐱","label":"cat face","group":3,"order":20,"tags":["animal","pet"]}
	]`),
	Zh: jsontext.Value(`[
		{"hexcode":"1F604","unicode":"😄","label":"大笑","tags":["开心","高兴"]},
		{"hexcode":"2764","unicode":"❤️","label":"红心","tags":["爱"]},
		{"hexcode":"1F431","unicode":"🐱","label":"猫脸","tags":["宠物"]}
	]`),
	Shortcodes: jsontext.Value(`{"1F604":"smile","2764":["heart","red_heart"],"1F431":"cat"}`),
}

// 用例与前端 catalog.test.ts 的“emoji search and trigger”一致。
func TestSearch(t *testing.T) {
	cat, err := Build(fixture)
	if err != nil {
		t.Fatal(err)
	}
	first := func(text string, group int) string {
		r := Search(cat, text, group, 0)
		if len(r) == 0 {
			return ""
		}
		return r[0].Unicode
	}
	cases := []struct {
		text  string
		group int
		want  string
	}{{"开心", -1, "😄"}, {":SMILE:", -1, "😄"}, {"red heart", -1, "❤️"}, {"宠物", 3, "🐱"}, {"cat", 0, ""}, {"not found", -1, ""}}
	for _, c := range cases {
		if got := first(c.text, c.group); got != c.want {
			t.Errorf("Search(%q, %d) = %q，期望 %q", c.text, c.group, got, c.want)
		}
	}
	if n := len(Search(cat, "", -1, 2)); n != 2 {
		t.Errorf("limit 2 返回 %d 项", n)
	}
	if cat[0].Zh != "大笑" || cat[0].Shortcodes[0] != "smile" || cat[1].Shortcodes[1] != "red_heart" {
		t.Errorf("目录项 %+v", cat[:2])
	}
	if !strings.Contains(strings.Join(cat[0].Shortcodes, ","), "grinning_face_with_smiling_eyes") {
		t.Errorf("应由英文名生成短代码：%v", cat[0].Shortcodes)
	}
}

func TestFindQuery(t *testing.T) {
	for _, v := range []string{":smi", "hello :开心", "（:cat", ":smile:"} {
		q, ok := FindQuery(v, len(v))
		if !ok || !strings.HasPrefix(v[q.From:q.To], ":") {
			t.Errorf("%q 应识别，得到 %+v %v", v, q, ok)
		}
	}
	if q, _ := FindQuery("hello :开心", len("hello :开心")); q.Text != "开心" || q.From != len("hello ") {
		t.Errorf("区间 %+v", q)
	}
	for _, v := range []string{":", "12:30", "https://host", `C:\path`, "word:smile", "::smile", "：smile"} {
		if q, ok := FindQuery(v, len(v)); ok {
			t.Errorf("%q 不应识别，得到 %+v", v, q)
		}
	}
}

// TestRealCatalog 下载真实数据（只在内存中，不写缓存）核对规模、内存与搜索；设 NOMO_NET=1 时运行。
func TestRealCatalog(t *testing.T) {
	if os.Getenv("NOMO_NET") == "" {
		t.Skip("设 NOMO_NET=1 运行（需要网络）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	files := make([]string, len(emojiFiles))
	for i := range emojiFiles {
		data, err := downloadEmojiFile(ctx, i)
		if err != nil {
			t.Fatal(err)
		}
		files[i] = data
	}
	p, err := emojiPayload(cachedEmojiCatalog{Version: emojiVersion, Files: files})
	if err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	cat, err := Build(p)
	if err != nil {
		t.Fatal(err)
	}
	p, files = nil, nil
	runtime.GC()
	runtime.ReadMemStats(&after)
	start := time.Now()
	r := Search(cat, "开心", -1, 8)
	t.Logf("entries=%d heap delta=%.1f MB search=%v", len(cat), float64(int64(after.HeapAlloc)-int64(before.HeapAlloc))/(1<<20), time.Since(start))
	for _, e := range r {
		t.Logf("%s %s %s :%s:", e.Unicode, e.Zh, e.En, e.Shortcodes[0])
	}
	if len(r) == 0 || len(Search(cat, "smile", -1, 0)) == 0 {
		t.Fatal("真实数据搜索无结果")
	}
	runtime.KeepAlive(cat)
}

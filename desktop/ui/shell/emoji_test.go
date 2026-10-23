package shell

import (
	"encoding/json/jsontext"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/config"
	"nomo/desktop/internal/emoji"
	"nomo/desktop/internal/i18n"
)

// newEmojiShell 创建注入了固定表情目录的外壳；后台结果经 posted 通道交回，由 settle 在测试 goroutine 中执行。
func newEmojiShell(t *testing.T, text string) (*Shell, *ui.Tester, func()) {
	t.Helper()
	cfg, err := config.Load(filepath.Join(t.TempDir(), config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := emoji.Build(&emoji.Payload{
		En: jsontext.Value(`[
			{"hexcode":"1F604","unicode":"😄","label":"grinning face with smiling eyes","group":0,"order":3,"tags":["happy","smile"]},
			{"hexcode":"1F642","unicode":"🙂","label":"slightly smiling face","group":0,"order":5,"tags":["smile"]},
			{"hexcode":"1F431","unicode":"🐱","label":"cat face","group":3,"order":20,"tags":["animal","pet"]}
		]`),
		Zh:         jsontext.Value(`[{"hexcode":"1F604","label":"大笑","tags":["开心"]},{"hexcode":"1F642","label":"呵呵"},{"hexcode":"1F431","label":"猫脸","tags":["宠物"]}]`),
		Shortcodes: jsontext.Value(`{"1F604":"smile","1F642":"slightly_smiling_face","1F431":"cat"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Config: cfg, Locale: i18n.ZhCN, Files: []string{writeDoc(t, text)},
		LoadEmoji: func(bool) ([]emoji.Entry, error) { return cat, nil }})
	posted := make(chan func(), 16)
	s.post = func(fn func()) { posted <- fn }
	tt := ui.NewTester(s.view, 1200, 800)
	tt.Frame()
	settle := func() {
		for {
			select {
			case fn := <-posted:
				fn()
				tt.Frame()
			case <-time.After(50 * time.Millisecond):
				tt.Frame()
				return
			}
		}
	}
	return s, tt, settle
}

func TestEmojiSuggestionsInsertAndDismiss(t *testing.T) {
	s, tt, settle := newEmojiShell(t, "段落 `code` ")
	tab := s.activeTab()
	tab.Doc.SetSelection(selection(len(tab.Doc.Source())))
	tt.Type(":smi")
	tt.Frame()
	settle()
	if !s.emo.open || len(s.emo.results) != 2 || !tt.HasText(":smile:") {
		t.Fatalf("应弹出建议：open=%v results=%d", s.emo.open, len(s.emo.results))
	}
	snapshot(t, tt, "emoji-suggest")
	// ↓ 选第二项，回车插入：冒号查询被替换，一步撤销恢复。
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if got := tab.Doc.Source(); got != "段落 `code` 🙂" || s.emo.open {
		t.Fatalf("插入后 %q open=%v", got, s.emo.open)
	}
	tab.Ed.Run("undo")
	tt.Frame()
	if got := tab.Doc.Source(); got != "段落 `code` :smi" {
		t.Fatalf("撤销后 %q", got)
	}
	// 光标移动而未输入：不弹出；Esc 关闭后同一查询不再弹出。
	tt.Frame()
	if s.emo.open {
		t.Fatal("撤销（非键入）后不应弹出")
	}
	tt.Type("l")
	tt.Frame()
	if !s.emo.open {
		t.Fatal("继续键入应弹出")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	tt.Frame()
	if s.emo.open || !strings.HasSuffix(tab.Doc.Source(), ":smil") {
		t.Fatalf("Esc 应关闭并保留文字：%q", tab.Doc.Source())
	}
}

func TestEmojiSuggestionsSkipLiterals(t *testing.T) {
	src := "`a b` [x](y)\n\n```\nx\n```\n"
	for _, at := range []int{len("`a "), len("`a b` [x"), len("`a b` [x](y)\n\n```\n")} {
		s, tt, settle := newEmojiShell(t, src)
		tab := s.activeTab()
		tab.Doc.SetSelection(selection(at))
		tt.Type(":smi")
		tt.Frame()
		settle()
		if s.emo.open {
			t.Fatalf("位置 %d（代码内）不应弹出：%q", at, tab.Doc.Source())
		}
	}
}

func TestEmojiPickerSearchAndInsert(t *testing.T) {
	s, tt, settle := newEmojiShell(t, "你好")
	tab := s.activeTab()
	tab.Doc.SetSelection(selection(len("你好")))
	s.openEmojiPicker(ui.Point{X: 600, Y: 100}) // 工具栏图标按钮只有提示文字，Tester 无法按文字定位
	settle()
	if !s.emo.picker || len(s.emo.pickerResults) != 3 || !tt.HasText("动物与自然") {
		t.Fatalf("选择器 open=%v results=%d", s.emo.picker, len(s.emo.pickerResults))
	}
	snapshot(t, tt, "emoji-picker")
	tt.Type("宠物")
	tt.Frame()
	if len(s.emo.pickerResults) != 1 || s.emo.pickerResults[0].Unicode != "🐱" {
		t.Fatalf("搜索结果 %v", s.emo.pickerResults)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if got := tab.Doc.Source(); got != "你好🐱" || s.emo.picker {
		t.Fatalf("插入后 %q picker=%v", got, s.emo.picker)
	}
	// 再次打开后 Esc 关闭，不插入。
	s.openEmojiPicker(ui.Point{X: 600, Y: 100})
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if s.emo.picker || tab.Doc.Source() != "你好🐱" {
		t.Fatalf("Esc 后 picker=%v %q", s.emo.picker, tab.Doc.Source())
	}
}

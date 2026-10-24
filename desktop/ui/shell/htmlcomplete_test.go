package shell

import "testing"

// TestHTMLCompletionItems：标签名、属性名（含标签专有属性）与 style 中的 CSS 属性补全；属性值中不补全。
func TestHTMLCompletionItems(t *testing.T) {
	has := func(line, label, insert string, start int) {
		t.Helper()
		items, at, ok := htmlCompletionItems(line)
		if !ok || at != start {
			t.Fatalf("%q：ok=%v start=%d", line, ok, at)
		}
		for _, it := range items {
			if it.Label == label && it.Insert == insert {
				return
			}
		}
		t.Fatalf("%q 缺少 %q", line, label)
	}
	has("文字 <de", "details", "details", len("文字 <"))
	has("<img sr", "src", `src=""`, len("<img "))
	has(`<details o`, "open", `open=""`, len("<details "))
	has(`<span style="color: red; font-s`, "font-size", "font-size: ", len(`<span style="color: red; `))
	if _, _, ok := htmlCompletionItems(`<a href="ht`); ok {
		t.Fatal("属性值中不应补全")
	}
	if _, _, ok := htmlCompletionItems("普通文字"); ok {
		t.Fatal("普通文字不应补全")
	}
}

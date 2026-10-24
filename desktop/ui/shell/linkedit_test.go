package shell

import "testing"

// TestLinkEditor：选中文字插入链接；光标在已有链接内时编辑地址并保留标题；移除链接只留文字；脚本协议被拒绝。
func TestLinkEditor(t *testing.T) {
	p := writeDoc(t, "see docs here\n\n[old](http://a.example \"T\") end\n")
	s, tt := newTestShell(t, p)
	tab := s.activeTab()
	tab.Ed.Select(4, 8) // "docs"
	s.run("link")
	if !s.link.open || s.link.existing || s.link.text != "docs" {
		t.Fatalf("新链接浮层状态不对：%+v", s.link)
	}
	tt.Frame()
	if !tt.HasText(s.tr("applyLink")) {
		t.Fatal("浮层未显示")
	}
	s.link.href = "javascript:alert(1)"
	s.applyLink(tab, false)
	if !s.link.open || s.link.err == "" {
		t.Fatal("脚本协议应被拒绝")
	}
	s.link.href = "https://x.example/a b"
	s.applyLink(tab, false)
	want := "see [docs](<https://x.example/a b>) here\n"
	if got := tab.text(); got[:len(want)] != want {
		t.Fatalf("插入后原文 %q", got)
	}

	src := tab.text()
	at := len(src) - len("old](http://a.example \"T\") end\n")
	tab.Ed.Select(at, at)
	tt.Frame()
	s.run("link")
	if !s.link.existing || s.link.text != "old" || s.link.href != "http://a.example" {
		t.Fatalf("已有链接未识别：%+v", s.link)
	}
	s.link.href = "http://b.example"
	s.applyLink(tab, false)
	if got := tab.text(); got[len(got)-len("[old](http://b.example \"T\") end\n"):] != "[old](http://b.example \"T\") end\n" {
		t.Fatalf("编辑后原文 %q", got)
	}
	tab.Ed.Select(at, at)
	tt.Frame()
	s.run("link")
	s.applyLink(tab, true)
	if got := tab.text(); got[len(got)-len("old end\n"):] != "old end\n" {
		t.Fatalf("移除后原文 %q", got)
	}
	tab.Ed.Run("undo")
	if got := tab.text(); got[len(got)-len("[old](http://b.example \"T\") end\n"):] != "[old](http://b.example \"T\") end\n" {
		t.Fatalf("撤销移除后原文 %q", got)
	}
}

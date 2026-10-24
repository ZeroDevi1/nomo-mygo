package shell

import (
	"testing"
	"time"
)

// TestSnapshotsRestore：列出保存前快照，恢复所选版本后原文换成快照内容、显示为未保存，且可撤销。
func TestSnapshotsRestore(t *testing.T) {
	p := writeDoc(t, "v1\n")
	s, tt := newTestShell(t, p)
	tab := s.activeTab()
	if err := s.cfg.CreateSnapshot(p, "v0\r\n", "before-save"); err != nil {
		t.Fatal(err)
	}
	posted := make(chan func(), 1)
	s.post = func(fn func()) { posted <- fn }
	s.openSnapshots()
	select {
	case fn := <-posted:
		fn()
	case <-time.After(5 * time.Second):
		t.Fatal("快照列表未返回")
	}
	tt.Frame()
	if len(s.snaps.list) != 1 || !tt.HasText(s.tr("restoreSnapshot")) {
		t.Fatalf("快照列表 %d 项", len(s.snaps.list))
	}
	if err := tt.Click(s.tr("restoreSnapshot")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tab.text() != "v0\n" || !tab.Dirty() || s.snaps.open {
		t.Fatalf("恢复后原文 %q dirty=%v", tab.text(), tab.Dirty())
	}
	if tab.eol != "\n" {
		t.Fatalf("恢复快照改变了文件换行风格：%q", tab.eol)
	}
	tab.Ed.Run("undo")
	if tab.text() != "v1\n" {
		t.Fatalf("撤销后原文 %q", tab.text())
	}
}

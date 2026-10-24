package shell

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// TestWindowsIntegrationSettings：设置“文件”页读取状态后显示绑定按钮，点击后调用注册并重新读取状态。
func TestWindowsIntegrationSettings(t *testing.T) {
	s, _ := newTestShell(t)
	assoc := false
	calls := 0
	s.opts.Windows = &WindowsIntegration{
		AssociationStatus: func() (IntegrationStatus, error) {
			return IntegrationStatus{On: assoc, Message: map[bool]string{true: "已注册", false: "未注册"}[assoc]}, nil
		},
		SetAssociation:    func(on bool) (string, error) { calls++; assoc = on; return "ok", nil },
		ContextMenuStatus: func() (IntegrationStatus, error) { return IntegrationStatus{Message: "菜单未注册"}, nil },
		SetContextMenu:    func(bool) (string, error) { return "", nil },
		OpenDefaultApps:   func() error { return nil },
	}
	posted := make(chan func(), 4)
	s.post = func(fn func()) { posted <- fn }
	drain := func() {
		select {
		case fn := <-posted:
			fn()
		case <-time.After(5 * time.Second):
			t.Fatal("后台结果未返回")
		}
	}
	sw := newSettingsWindow(s)
	sw.cat = "files"
	tt := ui.NewTester(sw.view, 780, 900)
	tt.Frame()
	drain()
	tt.Frame()
	if !tt.HasText("未注册") || !tt.HasText("菜单未注册") {
		t.Fatalf("状态未显示：%v", tt.Texts())
	}
	snapshot(t, tt, "settings-windows-integration")
	if err := tt.Click(s.tr("bindMd")); err != nil {
		t.Fatal(err)
	}
	drain() // 注册结果
	drain() // 重新读取状态
	tt.Frame()
	if calls != 1 || !tt.HasText("已注册") || !tt.HasText(s.tr("unbindMd")) {
		t.Fatalf("注册后 calls=%d texts=%v", calls, tt.Texts())
	}
}

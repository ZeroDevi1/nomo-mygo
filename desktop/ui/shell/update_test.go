package shell

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/config"
	"nomo/desktop/internal/i18n"
)

// TestUpdateFlow：启动检查到新版本时提示，下载完成后可“重启并安装”，安装前保存工作区；“不再提醒”后同版本不再提示。
func TestUpdateFlow(t *testing.T) {
	dir := t.TempDir()
	cfg, _ := config.Load(filepath.Join(dir, config.FileName))
	installed := false
	s := New(Options{Config: cfg, Locale: i18n.ZhCN, Primary: true, Version: "1.0.0",
		CheckUpdate: func() (UpdateInfo, error) {
			return UpdateInfo{Supported: true, Available: true, Installable: true, Version: "1.1.0", Notes: "- 新功能"}, nil
		},
		DownloadUpdate: func(progress func(done, total int64)) error { progress(10, 10); return nil },
		InstallUpdate:  func() error { installed = true; return nil },
	})
	posted := make(chan func(), 8)
	s.post = func(fn func()) { posted <- fn }
	drain := func() {
		t.Helper()
		select {
		case fn := <-posted:
			fn()
		case <-time.After(5 * time.Second):
			t.Fatal("后台结果未返回")
		}
	}
	tt := ui.NewTester(s.view, 1200, 800)
	s.runUpdateCheck(true)
	drain()
	tt.Frame()
	if !s.upd.notice || !tt.HasText(s.tr("softwareUpdateNoticeTitle")) || !tt.HasText("新功能") {
		t.Fatalf("未提示新版本：%v", tt.Texts())
	}
	if err := tt.Click(s.tr("softwareUpdateDownloadNow")); err != nil {
		t.Fatal(err)
	}
	drain() // 进度
	drain() // 完成
	tt.Frame()
	if !s.upd.downloaded {
		t.Fatal("下载未完成")
	}
	if err := tt.Click(s.tr("softwareUpdateRestartAndInstall")); err != nil {
		t.Fatal(err)
	}
	if !installed {
		t.Fatal("未启动安装")
	}

	s.upd = updateState{}
	s.runUpdateCheck(true)
	drain()
	tt.Frame()
	if err := tt.Click(s.tr("softwareUpdateDismissVersion")); err != nil {
		t.Fatal(err)
	}
	s.runUpdateCheck(true)
	drain()
	if s.upd.notice {
		t.Fatal("已忽略的版本仍提示")
	}
}

// TestInstalledNotes：版本升级后的首次启动显示更新日志；首次安装与同版本不显示。
func TestInstalledNotes(t *testing.T) {
	dir := t.TempDir()
	cfg, _ := config.Load(filepath.Join(dir, config.FileName))
	open := func(version string) *Shell {
		s := New(Options{Config: cfg, Locale: i18n.ZhCN, Primary: true, Version: version,
			ReleaseNotes: func(v string) (string, error) { return "notes " + v, nil }})
		posted := make(chan func(), 2)
		s.post = func(fn func()) { posted <- fn }
		s.showInstalledNotes()
		select {
		case fn := <-posted:
			fn()
		case <-time.After(200 * time.Millisecond):
		}
		return s
	}
	if s := open("1.0.0"); s.upd.installedNotes {
		t.Fatal("首次安装不应显示更新日志")
	}
	if s := open("1.0.0"); s.upd.installedNotes {
		t.Fatal("同版本不应显示")
	}
	s := open("1.2.0")
	if !s.upd.installedNotes || s.upd.notes != "notes 1.2.0" {
		t.Fatalf("升级后未显示更新日志：open=%v notes=%q", s.upd.installedNotes, s.upd.notes)
	}
	if n, ok := versionNewer("1.10.0", "1.9.9"); !ok || n {
		t.Fatal("版本比较错误")
	}
}

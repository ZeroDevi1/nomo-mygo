package shell

import (
	"encoding/json/v2"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/config"
)

// 软件更新（原版 softwareUpdate 与安装后更新日志）：
//   - 启动后（偏好 softwareUpdateAutoCheckEnabled）在后台检查一次，有新版本且未被“不再提醒”时弹出提示；
//   - 设置“关于”页可手动检查；Windows 安装版可在应用内下载（显示进度）并“重启并安装”，其他形态打开发布页；
//   - 版本升级后的首次启动显示该版本的更新日志（设置 nativeLastLaunchedVersion 记录上次启动的版本）。
// 状态由主窗口持有（Options.Primary），设置窗口读取同一份；后台结果经 post 回到 UI 线程。

const (
	updateDismissedKey    = "softwareUpdateDismissedVersion" // 与原版同键
	lastLaunchedVersionKy = "nativeLastLaunchedVersion"
	updateStartupDelay    = 5 * time.Second
	releaseNotesURL       = "https://github.com/nomo-md/nomo/releases/latest"
)

// updateState 是检查、下载与提示的状态，只在 UI 线程访问。
type updateState struct {
	checking, checked bool
	info              UpdateInfo
	err               string
	downloading       bool
	done, total       int64 // 下载进度（字节），total 为 0 表示未知
	downloaded        bool
	installing        bool
	notice            bool // 启动检查到新版本后的提示框
	installedNotes    bool // 升级后的更新日志框
	installedVersion  string
	notes             string
	md                markdown.Options
}

// startUpdateTasks 在主窗口打开后安排启动检查与更新日志（只在主窗口调用一次）。
func (s *Shell) startUpdateTasks() {
	if !s.opts.Primary || s.checkUpdate == nil {
		return
	}
	s.showInstalledNotes()
	if s.prefs.SoftwareUpdateAutoCheck {
		time.AfterFunc(updateStartupDelay, func() { s.post(func() { s.runUpdateCheck(true) }) })
	}
}

// runUpdateCheck 在后台检查更新；startup 为 true 时只在有新版本且未被忽略时提示，不报告失败。
func (s *Shell) runUpdateCheck(startup bool) {
	u := &s.upd
	if u.checking || u.downloading || u.installing {
		return
	}
	u.checking, u.err = true, ""
	go func() {
		info, err := s.checkUpdate()
		s.postToSettings(func() {
			u.checking, u.checked = false, err == nil
			if err != nil {
				applog.Warn("Update", "检查更新失败："+err.Error())
				if !startup {
					u.err = s.tr("softwareUpdateFailed") + " " + err.Error()
				}
				s.redrawSettings()
				return
			}
			if info.Version != u.info.Version {
				u.downloaded, u.done, u.total = false, 0, 0
			}
			u.info = info
			if startup && info.Available && !s.updateDismissed(info.Version) {
				u.notice = true
			}
			s.redrawSettings()
		})
	}()
}

func (s *Shell) redrawSettings() {
	if s.settings != nil && s.settings.win != nil {
		s.settings.win.Update(func() {})
	}
	if s.win != nil {
		s.win.Update(func() {})
	}
}

func (s *Shell) updateDismissed(version string) bool {
	v, ok := s.cfg.Setting(updateDismissedKey)
	if !ok {
		return false
	}
	var d string
	return json.Unmarshal([]byte(v), &d) == nil && d == version
}

// startDownload 在后台下载安装包，进度按约 10 次每秒刷新界面。
func (s *Shell) startDownload() {
	u := &s.upd
	if u.downloading || s.opts.DownloadUpdate == nil {
		return
	}
	u.downloading, u.err, u.done, u.total = true, "", 0, 0
	var last time.Time
	go func() {
		err := s.opts.DownloadUpdate(func(done, total int64) {
			if now := time.Now(); now.Sub(last) >= 100*time.Millisecond || done == total {
				last = now
				s.postToSettings(func() { u.done, u.total = done, total; s.redrawSettings() })
			}
		})
		s.postToSettings(func() {
			u.downloading = false
			if err != nil {
				u.err = s.tr("softwareUpdateFailed") + " " + err.Error()
			} else {
				u.downloaded = true
			}
			s.redrawSettings()
		})
	}()
}

// installNow 保存工作区（含未保存文档的恢复副本，重启后恢复）后启动安装器，应用随即退出。
func (s *Shell) installNow() {
	u := &s.upd
	if !u.downloaded || u.installing || s.opts.InstallUpdate == nil {
		return
	}
	if s.cfg != nil && s.persistable() {
		s.persistWorkspace(false)
	}
	u.installing = true
	if err := s.opts.InstallUpdate(); err != nil {
		u.installing = false
		u.err = s.tr("softwareUpdateInstallFailed") + " " + err.Error()
	}
}

// downloadLabel 是下载中的进度文案。
func (s *Shell) downloadLabel() string {
	u := &s.upd
	if u.total <= 0 {
		return s.tr("softwareUpdateDownloading")
	}
	return s.tr("softwareUpdateDownloadingPercent", "percent", fmt.Sprint(u.done*100/u.total))
}

// updateActions 画检查后的操作按钮（关于页与启动提示共用）：可安装时下载 / 重启并安装，否则查看发布页。
func (s *Shell) updateActions(c *ui.Context) {
	u := &s.upd
	switch {
	case !u.info.Available:
	case u.info.Installable && u.downloaded:
		if ui.PrimaryButton(c, s.tr("softwareUpdateRestartAndInstall")).Disabled(u.installing).Clicked() {
			s.installNow()
		}
	case u.info.Installable:
		label := s.tr("softwareUpdateDownloadNow")
		if u.downloading {
			label = s.tr("softwareUpdateDownloadingShort")
		}
		if ui.PrimaryButton(c, label).Disabled(u.downloading).Clicked() {
			s.startDownload()
		}
	default:
		if ui.PrimaryButton(c, s.tr("softwareUpdateViewDetails")).Clicked() {
			c.Services().OpenURL(releaseNotesURL)
		}
	}
}

// updateStatusText 是检查结果与下载进度的说明。
func (s *Shell) updateStatusText() string {
	u := &s.upd
	switch {
	case u.err != "":
		return u.err
	case u.installing:
		return s.tr("softwareUpdateInstalling")
	case u.downloading:
		return s.downloadLabel()
	case u.downloaded:
		return s.tr("softwareUpdateWaitingInstall")
	case !u.checked:
		return ""
	case !u.info.Supported:
		return s.tr("softwareUpdateUnsupported")
	case u.info.Available:
		return s.tr("softwareUpdateReleaseTitle", "version", strings.TrimPrefix(u.info.Version, "v"))
	}
	return s.tr("softwareUpdateUpToDate")
}

// updateNoticeDialog 是启动检查到新版本时的提示：版本、发布说明与操作；“不再提醒此版本”记入设置。
func (s *Shell) updateNoticeDialog(c *ui.Context) {
	u := &s.upd
	if !u.notice {
		return
	}
	ui.Modal(c, &u.notice, func() {
		ui.Column(c).Width(520).Gap(12).Children(func() {
			ui.Text(c, s.tr("softwareUpdateNoticeTitle")).FontSize(16).Bold()
			ui.Text(c, s.tr("softwareUpdateReleaseTitle", "version", strings.TrimPrefix(u.info.Version, "v"))).TextColor(s.ch.muted)
			s.notesView(c, u.info.Notes)
			if t := s.updateStatusText(); u.downloading || u.downloaded || u.err != "" {
				ui.Text(c, t).FontSize(12).Wrap().TextColor(s.ch.muted)
			}
			ui.Row(c).FillWidth().Gap(8).Children(func() {
				if ui.Button(c, s.tr("softwareUpdateDismissVersion")).Clicked() {
					v := u.info.Version
					if err := s.cfg.Update(func(cf *config.AppConfig) {
						raw, _ := json.Marshal(v)
						config.PutSetting(cf, updateDismissedKey, string(raw))
					}); err != nil {
						applog.Warn("Update", "记录忽略的版本失败："+err.Error())
					}
					u.notice = false
				}
				ui.Spacer(c)
				if ui.Button(c, s.tr("softwareUpdateRemindLater")).Clicked() {
					u.notice = false
				}
				s.updateActions(c)
			})
		})
	})
}

// notesView 显示发布说明（Markdown），过长时滚动；为空时显示通用说明。
func (s *Shell) notesView(c *ui.Context, notes string) {
	if strings.TrimSpace(notes) == "" {
		notes = s.tr("softwareUpdateReleaseFallback")
	}
	ui.Scroll(c).FillWidth().MaxHeight(320).Children(func() {
		markdown.View(c, notes, &s.upd.md)
	})
}

// showInstalledNotes 在版本升级后的首次启动读取并显示该版本的更新日志；首次安装（没有记录）只记下版本。
func (s *Shell) showInstalledNotes() {
	current := strings.TrimPrefix(s.version, "v")
	var last string
	if v, ok := s.cfg.Setting(lastLaunchedVersionKy); ok {
		_ = json.Unmarshal([]byte(v), &last)
	}
	if last != current {
		raw, _ := json.Marshal(current)
		if err := s.cfg.Update(func(cf *config.AppConfig) { config.PutSetting(cf, lastLaunchedVersionKy, string(raw)) }); err != nil {
			applog.Warn("Update", "记录启动版本失败："+err.Error())
		}
	}
	if last == "" || last == current || current == "0.0.0" || s.opts.ReleaseNotes == nil {
		return
	}
	if newer, ok := versionNewer(last, current); !ok || !newer {
		return
	}
	go func() {
		notes, err := s.opts.ReleaseNotes(current)
		if err != nil {
			applog.Warn("Update", "读取更新日志失败："+err.Error())
		}
		s.post(func() {
			s.upd.installedNotes, s.upd.installedVersion, s.upd.notes = true, current, notes
		})
	}()
}

// installedNotesDialog 是升级后的更新日志。
func (s *Shell) installedNotesDialog(c *ui.Context) {
	u := &s.upd
	if !u.installedNotes {
		return
	}
	ui.Modal(c, &u.installedNotes, func() {
		ui.Column(c).Width(520).Gap(12).Children(func() {
			ui.Text(c, s.tr("softwareUpdateInstalledTitle", "version", u.installedVersion)).FontSize(16).Bold()
			ui.Text(c, s.tr("softwareUpdateInstalledHint")).TextColor(s.ch.muted)
			s.notesView(c, u.notes)
			ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
				if ui.PrimaryButton(c, s.tr("softwareUpdateStartUsing")).Clicked() {
					u.installedNotes = false
				}
			})
		})
	})
}

// versionNewer 报告语义化版本 b 是否高于 a（只比较主、次、修订号）；无法解析时 ok 为 false。
func versionNewer(a, b string) (newer, ok bool) {
	parse := func(v string) ([3]int, bool) {
		var out [3]int
		v, _, _ = strings.Cut(strings.TrimPrefix(v, "v"), "-")
		parts := strings.Split(v, ".")
		if len(parts) != 3 {
			return out, false
		}
		for i, p := range parts {
			if _, err := fmt.Sscan(p, &out[i]); err != nil {
				return out, false
			}
		}
		return out, true
	}
	x, ok1 := parse(a)
	y, ok2 := parse(b)
	if !ok1 || !ok2 {
		return false, false
	}
	for i := range x {
		if x[i] != y[i] {
			return y[i] > x[i], true
		}
	}
	return false, true
}

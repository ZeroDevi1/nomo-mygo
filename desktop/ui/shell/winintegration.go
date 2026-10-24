package shell

import "github.com/egoist/mygo/ui"

// WindowsIntegration 是 Windows 文件关联与资源管理器右键菜单的操作（主程序提供，只在 Windows 上非空）。
// 各函数访问注册表或启动系统设置，在后台调用；返回的文案已按界面语言生成。
type WindowsIntegration struct {
	AssociationStatus func() (IntegrationStatus, error)
	SetAssociation    func(on bool) (string, error)
	ContextMenuStatus func() (IntegrationStatus, error)
	SetContextMenu    func(on bool) (string, error)
	OpenDefaultApps   func() error
	OpenInstalledApps func() error
	// LegacyInstaller 报告 Store 包是否检测到仍装着旧的安装版（只在首次运行时为 true）。
	LegacyInstaller func() bool
}

// checkLegacyInstaller 在主窗口打开后后台检测旧安装版，检测到时提示（原版 legacyInstallerPromptOpen）。
func (s *Shell) checkLegacyInstaller() {
	w := s.opts.Windows
	if w == nil || w.LegacyInstaller == nil || !s.opts.Primary {
		return
	}
	go func() {
		if w.LegacyInstaller() {
			s.post(func() { s.legacyPrompt = true })
		}
	}()
}

// legacyInstallerDialog 提示手动卸载旧安装版，可打开系统“已安装的应用”。
func (s *Shell) legacyInstallerDialog(c *ui.Context) {
	if !s.legacyPrompt {
		return
	}
	switch ui.AlertDialog(c, &s.legacyPrompt, s.tr("legacyInstallerTitle"), s.tr("legacyInstallerMessage"), s.tr("close"), s.tr("legacyInstallerOpenApps")) {
	case 1:
		if w := s.opts.Windows; w != nil && w.OpenInstalledApps != nil {
			go func() { _ = w.OpenInstalledApps() }()
		}
	}
}

// IntegrationStatus 是一项集成的当前状态：On 为已注册（右键菜单为已启用），Managed 为由 MSIX 包清单管理。
type IntegrationStatus struct {
	On, Managed bool
	Message     string
}

// winState 是设置页“Windows 集成”的状态：loaded 后显示两项状态；busy 时按钮不可用。
type winState struct {
	loaded, loading, busy bool
	assoc, menu           IntegrationStatus
	err                   string
}

// windowsIntegration 画设置页的“Windows 集成”分组；首次显示时在后台读取状态。
func (sw *settingsWindow) windowsIntegration(c *ui.Context) {
	s, w := sw.s, sw.s.opts.Windows
	if w == nil {
		return
	}
	st := &sw.win32
	if !st.loaded && !st.loading {
		sw.refreshIntegration()
	}
	run := func(fn func() (string, error)) {
		st.busy = true
		go func() {
			msg, err := fn()
			s.postToSettings(func() {
				st.busy = false
				st.err = ""
				if err != nil {
					st.err = err.Error()
				} else if msg != "" {
					s.status = msg
				}
				sw.refreshIntegration()
			})
		}()
	}
	sw.section(c, "windowsIntegration", func() {
		f := ui.Field(c.Key("mdAssociation"), s.tr("bindMdDefaultApp"), func() {
			ui.Column(c).Gap(6).Children(func() {
				ui.Row(c).Gap(8).Children(func() {
					switch {
					case st.assoc.Managed:
						if ui.Button(c, s.tr("openWindowsDefaultApps")).Disabled(st.busy).Clicked() {
							run(func() (string, error) { return "", w.OpenDefaultApps() })
						}
					case st.assoc.On:
						if ui.Button(c, s.tr("openWindowsDefaultApps")).Disabled(st.busy).Clicked() {
							run(func() (string, error) { return "", w.OpenDefaultApps() })
						}
						if ui.Button(c, s.tr("unbindMd")).Disabled(st.busy).Clicked() {
							run(func() (string, error) { return w.SetAssociation(false) })
						}
					default:
						if ui.PrimaryButton(c, s.tr("bindMd")).Disabled(st.busy || !st.loaded).Clicked() {
							run(func() (string, error) { return w.SetAssociation(true) })
						}
					}
				})
				msg := s.tr("mdAssociationCheckingDescription")
				if st.loaded {
					msg = st.assoc.Message
				}
				ui.Text(c, msg).FontSize(12).Wrap().TextColor(c.Theme().TextMuted)
			})
		})
		f.Description(s.tr("mdAssociationDefaultDescription"))
		g := ui.Field(c.Key("contextMenu"), s.tr("registerMdContextMenu"), func() {
			ui.Column(c).Gap(6).Children(func() {
				on, off := s.tr("registerContextMenu"), s.tr("unregisterContextMenu")
				if st.menu.Managed {
					on, off = s.tr("enableContextMenu"), s.tr("disableContextMenu")
				}
				ui.Row(c).Children(func() {
					if st.menu.On {
						if ui.Button(c, off).Disabled(st.busy).Clicked() {
							run(func() (string, error) { return w.SetContextMenu(false) })
						}
					} else if ui.PrimaryButton(c, on).Disabled(st.busy || !st.loaded).Clicked() {
						run(func() (string, error) { return w.SetContextMenu(true) })
					}
				})
				msg := s.tr("contextMenuCheckingDescription")
				if st.loaded {
					msg = st.menu.Message
				}
				ui.Text(c, msg).FontSize(12).Wrap().TextColor(c.Theme().TextMuted)
			})
		})
		g.Description(s.tr("contextMenuDefaultDescription"))
		if st.err != "" {
			ui.Text(c, st.err).FontSize(12).Wrap().TextColor(c.Theme().Danger)
		}
	})
}

// refreshIntegration 在后台重新读取两项状态。
func (sw *settingsWindow) refreshIntegration() {
	s, w, st := sw.s, sw.s.opts.Windows, &sw.win32
	st.loading = true
	go func() {
		assoc, err1 := w.AssociationStatus()
		menu, err2 := w.ContextMenuStatus()
		s.postToSettings(func() {
			st.loading, st.loaded = false, true
			st.assoc, st.menu = assoc, menu
			if err1 != nil {
				st.err = err1.Error()
			} else if err2 != nil {
				st.err = err2.Error()
			}
			if sw.win != nil {
				sw.win.Update(func() {})
			}
		})
	}()
}

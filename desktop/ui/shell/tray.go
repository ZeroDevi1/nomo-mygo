package shell

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 关闭到托盘（原版 closeWindowBehavior）：主程序经 Options.InstallTray 提供托盘（图标、单击恢复窗口、“打开 / 退出”
// 菜单）。点关闭按钮时：close-to-tray 隐藏窗口，进程与未保存的文档留在内存中；close-window 照常关闭（有未保存
// 修改时先确认）；ask-every-time 弹出选择框，默认勾选“以后默认这样做”，勾选时把选择写入设置。托盘不可用时一律关闭。

// handleWindowClose 处理窗口关闭请求（在 OnClose 中调用），返回是否已拦截。quitting 为 true 表示由“退出”触发。
func (s *Shell) handleWindowClose(e *mygo.CloseEvent) bool {
	if s.quitting || !s.trayOK || s.closeEmpty {
		return false
	}
	switch s.prefs.CloseWindowBehavior {
	case "close-to-tray":
		e.PreventDefault()
		s.hideToTray()
		return true
	case "ask-every-time":
		e.PreventDefault()
		s.win.Update(func() { s.closeChoiceOpen, s.closeChoiceRemember = true, true })
		return true
	}
	return false
}

// closeChoiceDialog 是“关闭 Nomo”选择框（原版 closeWindowChoice）：隐藏到托盘、关闭窗口或取消。
func (s *Shell) closeChoiceDialog(c *ui.Context) {
	if !s.closeChoiceOpen {
		return
	}
	ui.Modal(c, &s.closeChoiceOpen, func() {
		ui.Column(c).Width(380).Gap(12).Children(func() {
			ui.Text(c, s.tr("closeWindowChoiceTitle")).Bold()
			ui.Text(c, s.tr("closeWindowChoiceMessage")).TextColor(s.ch.muted)
			ui.Checkbox(c, &s.closeChoiceRemember, s.tr("rememberCloseWindowChoice"))
			ui.Row(c).Gap(8).Justify(ui.End).FillWidth().Children(func() {
				if ui.Button(c, s.tr("cancel")).Clicked() {
					s.closeChoiceOpen = false
				}
				if ui.Button(c, s.tr("closeWindowBehaviorCloseWindow")).Clicked() {
					s.resolveCloseChoice("close-window")
				}
				if ui.PrimaryButton(c, s.tr("closeWindowBehaviorCloseToTray")).Clicked() {
					s.resolveCloseChoice("close-to-tray")
				}
			})
		})
	})
}

func (s *Shell) resolveCloseChoice(behavior string) {
	s.closeChoiceOpen = false
	if s.closeChoiceRemember {
		s.prefs.CloseWindowBehavior = behavior
		s.setPref("closeWindowBehavior")
	}
	if behavior == "close-to-tray" {
		s.hideToTray()
		return
	}
	s.quit()
}

// quit 关闭窗口并退出（托盘“退出”、选择“关闭窗口”）：有未保存修改时先逐个确认。
func (s *Shell) quit() {
	s.quitting = true
	s.win.Show()
	if s.anyDirty() {
		s.requestClose(s.tabIDs(), true)
		return
	}
	s.win.Close()
}

// showFromTray 从托盘恢复并聚焦窗口。
func (s *Shell) showFromTray() {
	s.win.Show()
	s.win.Focus()
	s.hiddenToTray = false
	s.syncTrayActive()
}

// ShowWindow 显示并聚焦窗口（第二实例、打开文件事件），窗口隐藏在托盘时一并恢复托盘图标状态。
func (s *Shell) ShowWindow() { s.showFromTray() }

// hideToTray 把窗口隐藏到托盘。
func (s *Shell) hideToTray() {
	s.win.Hide()
	s.hiddenToTray = true
	s.syncTrayActive()
}

// syncTrayActive 让托盘图标反映是否还有可见窗口。
func (s *Shell) syncTrayActive() {
	if s.opts.SetTrayActive == nil {
		return
	}
	active := false
	for _, sh := range registry.shells {
		active = active || !sh.hiddenToTray
	}
	s.opts.SetTrayActive(active)
}

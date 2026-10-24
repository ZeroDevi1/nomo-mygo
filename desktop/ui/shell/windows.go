package shell

import (
	"image"
	"slices"

	"github.com/egoist/mygo"

	"nomo/desktop/internal/config"
)

// 多窗口：同一进程可以有多个外壳窗口（“移动到新窗口”创建）。注册表与共享状态只在 UI 线程访问。
//   - 偏好由所有窗口共享同一个 *Prefs（设置在任一窗口修改，其他窗口下一帧生效）。
//   - macOS 的系统菜单是全局的：只由第一个窗口安装，菜单命令发给当前聚焦的窗口（current）。
//   - 托盘只安装一次；“退出”依次关闭全部窗口（各自确认未保存的修改）。
// 标签可在窗口间拖动（view.go 的 tabDragTarget）；全部窗口随工作区恢复（workspace.go），额外窗口不恢复位置与尺寸。

var registry struct {
	shells                []*Shell
	focused               *Shell
	prefs                 map[*config.Manager]*Prefs // 同一配置的窗口共享一份偏好
	trayInstalled, trayOK bool
	quitHooked            bool
}

// quitAll 依次退出全部窗口（托盘“退出”）：每个窗口先确认未保存的修改。
func quitAll() {
	for _, s := range slices.Clone(registry.shells) {
		s.win.Update(s.quit)
	}
}

// shellForWindow 返回拥有窗口 w（主窗口或其设置窗口）的外壳；找不到时返回最近聚焦的外壳。
func shellForWindow(w any) *Shell {
	for _, s := range registry.shells {
		if any(s.win) == w || s.settings != nil && any(s.settings.win) == w {
			return s
		}
	}
	return registry.focused
}

// sharedPrefs 返回同一配置的窗口共享的偏好：第一个窗口从配置读取，之后的窗口复用同一份。
func sharedPrefs(cfg *config.Manager) *Prefs {
	if p := registry.prefs[cfg]; p != nil {
		return p
	}
	p := loadPrefs(cfg)
	if registry.prefs == nil {
		registry.prefs = map[*config.Manager]*Prefs{}
	}
	registry.prefs[cfg] = &p
	return &p
}

func register(s *Shell) {
	if len(registry.shells) == 0 && !registry.quitHooked {
		// ⌘Q、系统注销与 SIGINT/SIGTERM 都经 App.Quit：先标记正在退出，关闭窗口时不再按“关闭到托盘”拦截
		// （未保存修改的确认照常进行，可取消退出）。
		registry.quitHooked = true
		mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) {
			for _, s := range registry.shells {
				s.quitting = true
			}
		})
	}
	registry.shells = append(registry.shells, s)
	registry.focused = s
}

func unregister(s *Shell) {
	registry.shells = slices.DeleteFunc(registry.shells, func(x *Shell) bool { return x == s })
	if registry.focused == s {
		registry.focused = nil
		if n := len(registry.shells); n > 0 {
			registry.focused = registry.shells[n-1]
		}
	}
}

// Current 返回最近聚焦的外壳窗口（第二实例、打开文件事件投递到这里）；没有窗口时返回 nil。
func Current() *Shell { return registry.focused }

// moveToNewWindow 把标签（组合标签整组）移到新窗口：两边共享同一份文档对象，原窗口只是不再显示它。
func (s *Shell) moveToNewWindow(tab *Tab) { s.moveToNewWindowAt(tab, image.Point{X: -1}) }

// moveToNewWindowAt 同 moveToNewWindow，at.X >= 0 时把新窗口的左上角放在屏幕点 at（拖出标签松手的位置）。
func (s *Shell) moveToNewWindowAt(tab *Tab, at image.Point) {
	if s.opts.Config == nil {
		return
	}
	var moving []*Tab
	if left, right, ok := s.comparisonOf(tab); ok {
		moving = []*Tab{left, right}
	} else {
		moving = []*Tab{tab}
	}
	if len(moving) == len(s.tabs) {
		return // 只剩这些标签时移出没有意义（原版同样不提供）
	}
	o := s.opts
	o.Files, o.Folders = nil, nil
	n := New(o)
	for _, t := range moving {
		t.syncFromSource()
		// 源码编辑器状态绑定原窗口，交给新窗口时重建（文档与语义撤销历史随 Document 保留）。
		t.source = nil
		t.Ed.ReleaseLayouts()
		i := s.findTabIndex(t.ID)
		s.tabs = slices.Delete(s.tabs, i, i+1)
	}
	if s.active >= len(s.tabs) {
		s.active = len(s.tabs) - 1
	}
	s.focusEditor = true
	s.updateTitle()
	for _, t := range moving {
		n.nextID = max(n.nextID, t.ID)
		t.Ed.PasteData = nil
		n.addTab(t)
	}
	n.Open()
	if at.X >= 0 {
		n.win.SetPosition(at.X, at.Y)
	}
}

// adoptTabs 把来源窗口 from 中的标签 id（组合标签整组）移到本窗口，放在标签 before 之前（0 表示末尾）。来源窗口
// 只剩这些标签时关闭它（文档对象随标签移交，不涉及未保存确认）。
func (s *Shell) adoptTabs(from *Shell, id, before int) {
	if from == nil || from == s {
		return
	}
	tab := from.tabWithID(id)
	if tab == nil {
		return
	}
	var moving []*Tab
	if left, right, ok := from.comparisonOf(tab); ok {
		moving = []*Tab{left, right}
	} else {
		moving = []*Tab{tab}
	}
	for _, t := range moving {
		t.syncFromSource()
		t.source = nil // 源码编辑器状态绑定原窗口，在新窗口重建
		t.Ed.ReleaseLayouts()
		i := from.findTabIndex(t.ID)
		from.tabs = slices.Delete(from.tabs, i, i+1)
	}
	if from.active >= len(from.tabs) {
		from.active = len(from.tabs) - 1
	}
	from.updateTitle()
	// 标签 ID 只在窗口内唯一：移入时重新编号，组合关系随之更新。
	ids := map[int]int{}
	for _, t := range moving {
		s.nextID++
		ids[t.ID], t.ID = s.nextID, s.nextID
	}
	for _, t := range moving {
		if t.partner != 0 {
			t.partner = ids[t.partner]
		}
	}
	at := len(s.tabs)
	if i := s.findTabIndex(before); before != 0 && i >= 0 {
		at = i
	}
	for k, t := range moving {
		s.wirePaste(t)
		s.tabs = slices.Insert(s.tabs, at+k, t)
	}
	s.active = -1
	s.activate(at)
	if s.win != nil {
		s.win.Focus()
	}
	if len(from.tabs) == 0 && from.win != nil {
		from.closeEmpty = true // 标签都移走了：直接关闭，不按“关闭到托盘”隐藏
		from.win.Close()
	} else if from.win != nil {
		from.win.Update(func() {})
	}
}

// Window 返回外壳的主窗口（Open 之前为 nil）。
func (s *Shell) Window() *mygo.Window { return s.win }

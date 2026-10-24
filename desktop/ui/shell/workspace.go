package shell

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/config"
	"nomo/desktop/internal/docfs"
)

// 工作区恢复：所有窗口的文件夹、标签（含组合标签关系与左栏比例）与活动标签写入设置 nativeWorkspace（主窗口在前），
// 未保存的文档另写恢复副本到数据目录 drafts/<内容散列>.md。下次无启动参数地启动时恢复全部窗口，带副本的标签载入
// 副本内容（显示为未保存）。副本写入后文件又被外部修改的，逐个询问保留哪个版本（原版 startupDraftConflict）。
//
// 写入时机：任一窗口的标签、文件夹或未保存内容变化后防抖 workspaceDelay（大文档更久）在后台写；窗口关闭时同步写：
// 退出应用（或关闭最后一个窗口）时按关闭前的标签记录该窗口（关闭确认中“不保存”的新文档不记录，已有路径的照常
// 恢复）；还有其他窗口时单独关闭的窗口不再记录。额外窗口的位置与尺寸不恢复（只有主窗口记住几何）。
// 成本：每个未保存文档每次防抖到期复制并散列一次原文、写一个文件；同内容的副本不重复写，不再引用的副本随即删除。

const (
	workspaceKey   = "nativeWorkspace"
	workspaceDelay = 2 * time.Second
	// workspaceLargeDelay 用于原文超过 1 MiB 的未保存文档，减少大文档反复整篇写盘。
	workspaceLargeDelay = 8 * time.Second
	firstRunSampleKey   = "firstRunSampleDocumentOpened"
)

// workspaceState 是 nativeWorkspace 的 JSON 结构。版本 2 为多窗口（Windows，主窗口在前）；版本 1 为单窗口
// （Folder / Active / Tabs 在顶层），读取时按一个窗口处理。
type workspaceState struct {
	Version int               `json:"version"`
	Windows []workspaceWindow `json:"windows,omitzero"`
	workspaceWindow
}

type workspaceWindow struct {
	Folder string         `json:"folder,omitzero"`
	Active int            `json:"active,omitzero"` // Tabs 中的下标
	Tabs   []workspaceTab `json:"tabs,omitzero"`
}

type workspaceTab struct {
	Path     string `json:"path,omitzero"`
	Untitled int    `json:"untitled,omitzero"`
	// Draft 为恢复副本的内容标识（drafts/<Draft>.md）；空表示无未保存修改。Base 为写副本时磁盘文件的修改时间
	// （毫秒），恢复时与当前修改时间不同即为冲突。
	Draft string `json:"draft,omitzero"`
	Base  int64  `json:"base,omitzero"`
	// Pair 只在组合标签的左侧成员上：右侧成员在同一窗口 Tabs 中的下标 + 1；Pct 为左栏宽度百分比。
	Pair int     `json:"pair,omitzero"`
	Pct  float32 `json:"pct,omitzero"`
}

// workspaceWrites 串行化工作区写盘：两次写入交错时，前一次删除旧副本可能删掉后一次刚写回的同名副本。
var workspaceWrites sync.Mutex

// workspaceSaver 是同一配置的工作区写入状态（各窗口共用，见 savers），只在 UI 线程访问。
type workspaceSaver struct {
	sig     string    // 上次安排写入时的工作区签名（各窗口的标签、文件夹、未保存内容版本）
	due     time.Time // 待写入的到期时间；零值表示没有待写入
	written map[string]bool
	// closed 为退出过程中已关闭的窗口，仍计入工作区；取消退出时清空。
	closed []windowSnap
}

var savers = map[*config.Manager]*workspaceSaver{}

func (s *Shell) saver() *workspaceSaver {
	sv := savers[s.cfg]
	if sv == nil {
		sv = &workspaceSaver{written: map[string]bool{}}
		savers[s.cfg] = sv
	}
	return sv
}

// windowWS 是窗口自己的恢复状态：closing 为开始关闭（确认未保存修改之前）时的标签，nil 表示未在关闭；
// conflicts 为待询问的副本冲突；pending 为启动时待恢复的其他窗口（只在主窗口上）。
type windowWS struct {
	closing   []*Tab
	conflicts []draftConflict
	pending   []workspaceWindow
}

// windowSnap 是写入时一个窗口的内容；drafts 为 false 时不写副本（窗口已关闭，未保存的修改已逐个确认）。
type windowSnap struct {
	folder string
	active *Tab
	tabs   []*Tab
	drafts bool
}

// draftConflict 是一个副本与外部修改冲突的标签：tab 已按磁盘内容打开，draft 为副本原文。
type draftConflict struct {
	tab   *Tab
	draft string
}

func (s *Shell) draftsDir() string { return filepath.Join(s.cfg.DataDir(), config.DraftsDirName) }

func (s *Shell) snap(tabs []*Tab, drafts bool) windowSnap {
	w := windowSnap{active: s.activeTab(), tabs: tabs, drafts: drafts}
	if s.folder != nil {
		w.folder = s.folder.path
	}
	return w
}

// liveShells 返回与 s 同一配置的已打开窗口（主窗口在前）；s 尚未打开（测试中）时只有 s 自己。
func (s *Shell) liveShells() []*Shell {
	var out []*Shell
	registered := false
	for _, sh := range registry.shells {
		registered = registered || sh == s
		if sh.cfg == s.cfg && !sh.closedForWorkspace {
			out = append(out, sh)
		}
	}
	if !registered && !s.closedForWorkspace {
		out = append(out, s) // 尚未打开的窗口（测试中直接调用）
	}
	slices.SortStableFunc(out, func(a, b *Shell) int {
		switch {
		case a.opts.Primary == b.opts.Primary:
			return 0
		case a.opts.Primary:
			return -1
		}
		return 1
	})
	return out
}

// windows 返回要写入的全部窗口：退出过程中已关闭的窗口在前（按关闭顺序，主窗口先关），其后为仍打开的窗口。
func (s *Shell) windows() []windowSnap {
	out := append([]windowSnap{}, s.saver().closed...)
	for _, sh := range s.liveShells() {
		if sh.ws.closing != nil {
			out = append(out, sh.snap(sh.ws.closing, false))
		} else {
			out = append(out, sh.snap(sh.tabs, true))
		}
	}
	return out
}

// trackWorkspace 每帧检查工作区是否变化，变化后防抖写入（任一窗口的帧都可完成写入）。
func (s *Shell) trackWorkspace(c *ui.Context) {
	if s.cfg == nil || !s.persistable() {
		return
	}
	sv := s.saver()
	now := c.Now()
	if sig, large := s.workspaceSig(); sig != sv.sig {
		sv.sig = sig
		delay := workspaceDelay
		if large {
			delay = workspaceLargeDelay
		}
		sv.due = now.Add(delay)
	}
	if sv.due.IsZero() {
		return
	}
	if now.Before(sv.due) {
		c.After(sv.due.Sub(now))
		return
	}
	sv.due = time.Time{}
	s.persistWorkspace(true)
}

// persistable 报告是否记录工作区：应用有主窗口（Options.Primary，正式运行时由主程序设置）。
func (s *Shell) persistable() bool {
	for _, sh := range s.liveShells() {
		if sh.opts.Primary {
			return true
		}
	}
	return false
}

// workspaceSig 返回全部窗口的工作区签名；large 报告是否有超过 1 MiB 的未保存文档。
func (s *Shell) workspaceSig() (sig string, large bool) {
	var b []byte
	for _, w := range s.windows() {
		b = append(b, '#')
		b = append(b, w.folder...)
		for _, t := range w.tabs {
			b = append(b, '|')
			if t == w.active {
				b = append(b, '>')
			}
			b = append(b, t.Path...)
			b = appendUint(b, uint64(t.partner))
			if t.left {
				b = appendUint(b, uint64(t.comparePct))
			}
			if w.drafts && t.Dirty() {
				b = append(b, '*')
				b = appendUint(b, t.version())
				large = large || t.size() > 1<<20
			}
		}
	}
	return string(b), large
}

func appendUint(b []byte, v uint64) []byte {
	var tmp [20]byte
	i := len(tmp)
	for {
		i--
		tmp[i] = byte('0' + v%10)
		v /= 10
		if v == 0 {
			break
		}
	}
	return append(b, tmp[i:]...)
}

// persistWorkspace 写入全部窗口组成的工作区。background 为 true 时在后台写盘；否则同步写（窗口关闭时，进程可能
// 随后退出）。未保存的新文档没有副本时（空白或已放弃）不记录。
func (s *Shell) persistWorkspace(background bool) {
	sv := s.saver()
	st := workspaceState{Version: 2}
	type pending struct{ id, text string }
	var writes []pending
	keep := map[string]bool{}
	for _, w := range s.windows() {
		ww := workspaceWindow{Folder: w.folder}
		index := map[int]int{} // 标签 ID → 本窗口 Tabs 中的下标
		for _, t := range w.tabs {
			e := workspaceTab{Path: t.Path, Untitled: t.Untitled}
			if t.Path != "" {
				e.Untitled = 0
			}
			if w.drafts && t.Dirty() {
				text := t.text()
				e.Draft, e.Base = config.HashText(text), t.diskModified
				keep[e.Draft] = true
				if !sv.written[e.Draft] {
					writes = append(writes, pending{e.Draft, text})
				}
			}
			if e.Path == "" && e.Draft == "" {
				continue
			}
			if t == w.active {
				ww.Active = len(ww.Tabs)
			}
			index[t.ID] = len(ww.Tabs)
			ww.Tabs = append(ww.Tabs, e)
		}
		// 组合关系：两侧成员都被记录时才保留。
		for _, t := range w.tabs {
			l, okL := index[t.ID]
			r, okR := index[t.partner]
			if t.left && t.partner != 0 && okL && okR {
				ww.Tabs[l].Pair, ww.Tabs[l].Pct = r+1, t.comparePct
			}
		}
		if len(ww.Tabs) > 0 || ww.Folder != "" {
			st.Windows = append(st.Windows, ww)
		}
	}
	var stale []string
	for id := range sv.written {
		if !keep[id] {
			stale = append(stale, id)
		}
	}
	sv.written = keep
	raw, err := json.Marshal(st)
	if err != nil {
		return
	}
	dir, cfg := s.draftsDir(), s.cfg
	write := func() {
		workspaceWrites.Lock()
		defer workspaceWrites.Unlock()
		// 先写副本再写状态：状态引用的副本总在磁盘上；旧副本在新状态写入后才删除。
		for _, w := range writes {
			if err := config.WriteMarkdownStoreFile(dir, w.id, w.text, "写入恢复副本失败"); err != nil {
				applog.Warn("Native", err.Error())
			}
		}
		if err := cfg.Update(func(c *config.AppConfig) { config.PutSetting(c, workspaceKey, string(raw)) }); err != nil {
			applog.Warn("Native", "保存工作区失败："+err.Error())
			return
		}
		for _, id := range stale {
			if p, err := config.MarkdownStorePath(dir, id); err == nil {
				_ = os.Remove(p)
			}
		}
	}
	if background {
		go write()
	} else {
		write()
	}
}

// beginWorkspaceClose 在窗口开始关闭（确认未保存修改之前）时记录当前标签；取消关闭时由 cancelWorkspaceClose 清除。
func (s *Shell) beginWorkspaceClose() {
	if s.ws.closing == nil {
		s.ws.closing = append([]*Tab{}, s.tabs...)
	}
}

// cancelWorkspaceClose 取消关闭（或取消退出）：之前因退出而关闭的窗口已真正关闭，不再计入工作区。
func (s *Shell) cancelWorkspaceClose() {
	s.ws.closing = nil
	if s.cfg != nil {
		sv := s.saver()
		sv.closed, sv.sig = nil, ""
	}
}

// finishWorkspaceClose 在窗口关闭后（注销之前）同步写入工作区：退出应用或关闭最后一个窗口时该窗口计入工作区，
// 否则不再记录它。
func (s *Shell) finishWorkspaceClose() {
	if s.cfg == nil || !s.persistable() {
		return
	}
	tabs := s.ws.closing
	if tabs == nil {
		tabs = s.tabs
	}
	sv := s.saver()
	if s.quitting || len(s.liveShells()) <= 1 { // 退出应用，或这是最后一个窗口
		sv.closed = append(sv.closed, s.snap(tabs, false))
	}
	s.ws.closing = nil
	s.closedForWorkspace = true
	s.persistWorkspace(false)
}

// readWorkspace 读取上次的工作区（版本 1 按单窗口处理）；没有或无法解析时返回 nil。
func (s *Shell) readWorkspace() []workspaceWindow {
	raw, ok := s.cfg.Setting(workspaceKey)
	if !ok {
		return nil
	}
	var st workspaceState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return nil
	}
	switch st.Version {
	case 1:
		return []workspaceWindow{st.workspaceWindow}
	case 2:
		return st.Windows
	}
	return nil
}

// restoreWorkspace 把上次的第一个窗口恢复到 s，其余窗口留到主窗口打开后创建（openPendingWindows），返回是否恢复了标签。
func (s *Shell) restoreWorkspace() bool {
	ws := s.readWorkspace()
	if len(ws) == 0 {
		return false
	}
	s.ws.pending = ws[1:]
	return s.restoreWindow(ws[0])
}

// openPendingWindows 打开启动时待恢复的其他窗口（主窗口打开之后调用）。
func (s *Shell) openPendingWindows() {
	pending := s.ws.pending
	s.ws.pending = nil
	for _, w := range pending {
		o := s.opts
		o.Files, o.Folders, o.Primary = nil, nil, false
		n := New(o)
		if n.restoreWindow(w) || n.folder != nil {
			n.Open()
		}
	}
}

// restoreWindow 把一个窗口的文件夹、标签与组合关系恢复到 s，返回是否恢复了标签。
func (s *Shell) restoreWindow(w workspaceWindow) bool {
	if w.Folder != "" {
		if fi, err := os.Stat(w.Folder); err == nil && fi.IsDir() {
			s.openFolder(w.Folder)
		}
	}
	sv := s.saver()
	dir := s.draftsDir()
	byIndex := map[int]*Tab{}
	for i, e := range w.Tabs {
		draft, hasDraft := "", false
		if e.Draft != "" {
			if text, err := config.ReadMarkdownStoreFile(dir, e.Draft, "读取恢复副本失败"); err == nil {
				draft, hasDraft = text, true
				sv.written[e.Draft] = true // 不再引用后由下次写入删除
			} else {
				applog.Warn("Native", err.Error())
			}
		}
		var t *Tab
		if e.Path != "" {
			doc, err := docfs.ReadDocument(e.Path)
			switch {
			case err == nil:
				s.nextID++
				t = newTab(s.nextID, doc.Path, doc.Markdown, doc.Encoding)
				if hasDraft && t.text() != draft {
					if t.diskModified != e.Base {
						s.ws.conflicts = append(s.ws.conflicts, draftConflict{tab: t, draft: draft})
					} else {
						t.replaceText(draft)
					}
				}
			case hasDraft:
				// 文件已不在：副本作为新文档打开，不丢内容。
				t = s.draftTab(draft)
			default:
				continue
			}
		} else if hasDraft {
			t = s.draftTab(draft)
			if e.Untitled > 0 {
				t.Untitled = e.Untitled
				s.untitled = max(s.untitled, e.Untitled)
			}
		} else {
			continue
		}
		s.wirePaste(t)
		s.tabs = append(s.tabs, t)
		byIndex[i] = t
	}
	if len(s.tabs) == 0 {
		return false
	}
	for i, e := range w.Tabs {
		if l, r := byIndex[i], byIndex[e.Pair-1]; e.Pair > 0 && l != nil && r != nil {
			s.combine(l, r)
			if e.Pct >= 20 && e.Pct <= 80 {
				l.comparePct = e.Pct
			}
		}
	}
	s.active = -1
	idx := 0
	if t := byIndex[w.Active]; t != nil {
		idx = s.findTabIndex(t.ID)
	}
	s.activate(idx)
	return true
}

// draftTab 创建内容为 text 的未保存新文档标签（尚未加入窗口）。
func (s *Shell) draftTab(text string) *Tab {
	s.nextID++
	s.untitled++
	t := newTab(s.nextID, "", "", docfs.EncodingUTF8)
	t.Untitled = s.untitled
	t.replaceText(text)
	return t
}

// openFirstRunSample 按原版 maybeOpenFirstRunSample：首次启动、没有恢复的标签与最近记录时打开随包示例文档；
// 不满足条件时只标记为已处理。
func (s *Shell) openFirstRunSample() {
	if v, ok := s.cfg.Setting(firstRunSampleKey); ok && v == "true" {
		return
	}
	mark := func() {
		if err := s.cfg.Update(func(c *config.AppConfig) { config.PutSetting(c, firstRunSampleKey, "true") }); err != nil {
			applog.Warn("Native", "记录示例文档状态失败："+err.Error())
		}
	}
	if len(s.tabs) > 0 || s.folder != nil || len(s.recent) > 0 || s.opts.SampleDocument == nil {
		mark()
		return
	}
	path, err := s.opts.SampleDocument()
	if err == nil {
		var doc docfs.DocumentPayload
		if doc, err = docfs.ReadDocument(path); err == nil {
			s.nextID++
			s.addTab(newTab(s.nextID, doc.Path, doc.Markdown, doc.Encoding))
			mark()
			return
		}
	}
	s.status = s.tr("sampleOpenFailed", "error", err.Error())
}

// draftConflictDialog 逐个询问副本与外部修改冲突的标签：恢复副本、使用磁盘版本或把副本另开为新文档。
// 关闭对话框按“另存副本”处理，不丢内容。
func (s *Shell) draftConflictDialog(c *ui.Context) {
	if len(s.ws.conflicts) == 0 {
		return
	}
	cf := s.ws.conflicts[0]
	open := true
	msg := s.tr("startupDraftConflictMessage", "fileName", cf.tab.Name(s.untitledName))
	choice := ui.AlertDialog(c, &open, s.tr("startupDraftConflictTitle"), msg,
		s.tr("startupDraftConflictUseDisk"), s.tr("startupDraftConflictSaveDraftAs"), s.tr("startupDraftConflictRestoreDraft"))
	switch choice {
	case 0:
	case 2:
		cf.tab.replaceText(cf.draft)
	case 1, ui.AlertDismissed:
		s.addTab(s.draftTab(cf.draft))
	default:
		return
	}
	s.ws.conflicts = s.ws.conflicts[1:]
	s.focusEditor = true
}

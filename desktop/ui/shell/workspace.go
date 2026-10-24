package shell

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/config"
	"nomo/desktop/internal/docfs"
)

// 工作区恢复：主窗口（Options.Primary）把打开的文件夹、标签与活动标签写入设置 nativeWorkspace，未保存的文档
// 另写恢复副本到数据目录 drafts/<内容散列>.md。下次无启动参数地启动时恢复标签，带副本的标签载入副本内容（显示
// 为未保存）。副本写入后文件又被外部修改的，逐个询问保留哪个版本（原版 startupDraftConflict）。
//
// 写入时机：标签、文件夹或未保存内容变化后防抖 workspaceDelay（大文档更久）在后台写；关闭主窗口时按关闭前的标签
// 同步写一次（关闭确认中“不保存”的文档不留副本，已有路径的照常恢复）。其他窗口的标签不记录。
// 成本：每个未保存文档每次防抖到期复制并散列一次原文、写一个文件；同内容的副本不重复写，不再引用的副本随即删除。

const (
	workspaceKey   = "nativeWorkspace"
	workspaceDelay = 2 * time.Second
	// workspaceLargeDelay 用于原文超过 1 MiB 的未保存文档，减少大文档反复整篇写盘。
	workspaceLargeDelay = 8 * time.Second
	firstRunSampleKey   = "firstRunSampleDocumentOpened"
)

// workspaceState 是 nativeWorkspace 的 JSON 结构。
type workspaceState struct {
	Version int            `json:"version"`
	Folder  string         `json:"folder,omitzero"`
	Active  int            `json:"active"` // Tabs 中的下标
	Tabs    []workspaceTab `json:"tabs"`
}

type workspaceTab struct {
	Path     string `json:"path,omitzero"`
	Untitled int    `json:"untitled,omitzero"`
	// Draft 为恢复副本的内容标识（drafts/<Draft>.md）；空表示无未保存修改。Base 为写副本时磁盘文件的修改时间
	// （毫秒），恢复时与当前修改时间不同即为冲突。
	Draft string `json:"draft,omitzero"`
	Base  int64  `json:"base,omitzero"`
}

// workspaceWrites 串行化工作区写盘：两次写入交错时，前一次删除旧副本可能删掉后一次刚写回的同名副本。
var workspaceWrites sync.Mutex

// workspaceSaver 是主窗口的工作区写入状态，只在 UI 线程访问。
type workspaceSaver struct {
	sig     string    // 上次安排写入时的工作区签名（标签、文件夹、未保存内容版本）
	due     time.Time // 待写入的到期时间；零值表示没有待写入
	written map[string]bool
	// closing 为关闭主窗口时（确认未保存修改之前）的标签，窗口关闭后据此写入；nil 表示未在关闭。
	closing   []*Tab
	conflicts []draftConflict
}

// draftConflict 是一个副本与外部修改冲突的标签：tab 已按磁盘内容打开，draft 为副本原文。
type draftConflict struct {
	tab   *Tab
	draft string
}

func (s *Shell) draftsDir() string { return filepath.Join(s.cfg.DataDir(), config.DraftsDirName) }

// trackWorkspace 每帧检查工作区是否变化，变化后防抖写入。
func (s *Shell) trackWorkspace(c *ui.Context) {
	if !s.opts.Primary || s.ws.closing != nil {
		return
	}
	now := c.Now()
	if sig, large := s.workspaceSig(); sig != s.ws.sig {
		s.ws.sig = sig
		delay := workspaceDelay
		if large {
			delay = workspaceLargeDelay
		}
		s.ws.due = now.Add(delay)
	}
	if s.ws.due.IsZero() {
		return
	}
	if now.Before(s.ws.due) {
		c.After(s.ws.due.Sub(now))
		return
	}
	s.ws.due = time.Time{}
	s.persistWorkspace(s.tabs, true, false)
}

// workspaceSig 返回工作区签名；large 报告是否有超过 1 MiB 的未保存文档。
func (s *Shell) workspaceSig() (sig string, large bool) {
	b := make([]byte, 0, 64+len(s.tabs)*48)
	if s.folder != nil {
		b = append(b, s.folder.path...)
	}
	b = append(b, '|', byte(s.active))
	for _, t := range s.tabs {
		b = append(b, '|')
		b = append(b, t.Path...)
		if t.Dirty() {
			b = append(b, '*')
			b = appendUint(b, t.version())
			large = large || t.size() > 1<<20
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

// persistWorkspace 写入 tabs 组成的工作区。drafts 为 false 时不写副本（关闭时已逐个确认），未保存的新文档不记录。
// background 为 true 时在后台写盘；否则同步写（窗口关闭时，进程随后退出）。
func (s *Shell) persistWorkspace(tabs []*Tab, drafts, background bool) {
	st := workspaceState{Version: 1, Active: 0, Tabs: []workspaceTab{}}
	if s.folder != nil {
		st.Folder = s.folder.path
	}
	type pending struct{ id, text string }
	var writes []pending
	keep := map[string]bool{}
	active := s.activeTab()
	for _, t := range tabs {
		e := workspaceTab{Path: t.Path, Untitled: t.Untitled}
		if t.Path != "" {
			e.Untitled = 0
		}
		if drafts && t.Dirty() {
			text := t.text()
			e.Draft, e.Base = config.HashText(text), t.diskModified
			keep[e.Draft] = true
			if !s.ws.written[e.Draft] {
				writes = append(writes, pending{e.Draft, text})
			}
		}
		if e.Path == "" && e.Draft == "" {
			continue // 空白或已放弃的新文档
		}
		if t == active {
			st.Active = len(st.Tabs)
		}
		st.Tabs = append(st.Tabs, e)
	}
	var stale []string
	for id := range s.ws.written {
		if !keep[id] {
			stale = append(stale, id)
		}
	}
	s.ws.written = keep
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

// beginWorkspaceClose 在主窗口开始关闭（确认未保存修改之前）时记录当前标签；取消关闭时由 cancelWorkspaceClose 清除。
func (s *Shell) beginWorkspaceClose() {
	if s.opts.Primary && s.ws.closing == nil {
		s.ws.closing = append([]*Tab{}, s.tabs...)
	}
}

func (s *Shell) cancelWorkspaceClose() { s.ws.closing, s.ws.sig = nil, "" }

// finishWorkspaceClose 在主窗口关闭后同步写入工作区：关闭前的标签中，已有路径的（含关闭时保存的新文档）照常记录。
func (s *Shell) finishWorkspaceClose() {
	if !s.opts.Primary {
		return
	}
	tabs := s.ws.closing
	if tabs == nil {
		tabs = s.tabs
	}
	s.persistWorkspace(tabs, false, false)
}

// restoreWorkspace 恢复上次的工作区，返回是否恢复了标签。
func (s *Shell) restoreWorkspace() bool {
	raw, ok := s.cfg.Setting(workspaceKey)
	if !ok {
		return false
	}
	var st workspaceState
	if err := json.Unmarshal([]byte(raw), &st); err != nil || st.Version != 1 {
		return false
	}
	if st.Folder != "" {
		if fi, err := os.Stat(st.Folder); err == nil && fi.IsDir() {
			s.openFolder(st.Folder)
		}
	}
	s.ws.written = map[string]bool{}
	dir := s.draftsDir()
	activeID := 0
	for i, e := range st.Tabs {
		draft, hasDraft := "", false
		if e.Draft != "" {
			if text, err := config.ReadMarkdownStoreFile(dir, e.Draft, "读取恢复副本失败"); err == nil {
				draft, hasDraft = text, true
				s.ws.written[e.Draft] = true // 不再引用后由下次写入删除
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
		if i == st.Active {
			activeID = t.ID
		}
	}
	if len(s.tabs) == 0 {
		return false
	}
	s.active = -1
	idx := s.findTabIndex(activeID)
	if idx < 0 {
		idx = 0
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

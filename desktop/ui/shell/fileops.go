package shell

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 资源管理器与标签的文件操作：新建文档、重命名、复制路径、在文件夹中显示。
// 不提供删除：MyGo 没有移到废纸篓的接口，直接删除不可恢复，留待接入系统回收站后再做。

func (s *Shell) copyText(text string) {
	if s.svc != nil {
		s.svc.WriteClipboard(text)
	}
}

func revealInFolder(path string) { mygo.Shell.ShowItemInFolder(path) }

// newFileIn 在目录 dir 中新建一个不重名的“无标题.md”并打开，随后进入重命名。
func (s *Shell) newFileIn(dir string) {
	base := strings.TrimSuffix(s.tr("untitledMarkdown"), ".md")
	path := filepath.Join(dir, base+".md")
	for n := 2; ; n++ {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			break
		}
		path = filepath.Join(dir, fmt.Sprintf("%s %d.md", base, n))
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		s.status = err.Error()
		return
	}
	f.Close()
	if s.folder != nil {
		s.folder.refresh()
	}
	s.OpenFiles([]string{path})
	s.beginRename(path)
}

// beginRename 打开重命名对话框；确认后改名并同步已打开标签的路径。
func (s *Shell) beginRename(path string) {
	s.renaming, s.renameFrom, s.renameTo = true, path, filepath.Base(path)
}

func (s *Shell) renameDialog(c *ui.Context) {
	if !s.renaming {
		return
	}
	ui.Modal(c, &s.renaming, func() {
		ui.Column(c).Width(360).Gap(12).Children(func() {
			ui.Text(c, s.tr("rename")).Bold()
			submitted := ui.TextInput(c, &s.renameTo).AutoFocus().FillWidth().Submitted()
			ui.Row(c).Gap(8).Justify(ui.End).FillWidth().Children(func() {
				if ui.Button(c, s.tr("cancel")).Clicked() {
					s.renaming = false
				}
				if ui.PrimaryButton(c, s.tr("rename")).Clicked() || submitted {
					s.applyRename()
				}
			})
		})
	})
}

func (s *Shell) applyRename() {
	name := strings.TrimSpace(s.renameTo)
	s.renaming = false
	if name == "" || strings.ContainsAny(name, `/\`) || name == filepath.Base(s.renameFrom) {
		return
	}
	to := filepath.Join(filepath.Dir(s.renameFrom), name)
	if _, err := os.Stat(to); err == nil {
		// macOS 与 Linux 的 rename 会静默覆盖同名文件，先拒绝。
		s.status = (&os.PathError{Op: "rename", Path: to, Err: os.ErrExist}).Error()
		return
	}
	if err := os.Rename(s.renameFrom, to); err != nil {
		s.status = err.Error()
		return
	}
	for _, t := range s.tabs {
		if t.Path != "" && samePath(t.Path, s.renameFrom) {
			t.Path = to
		}
	}
	if s.folder != nil {
		s.folder.refresh()
	}
	s.updateTitle()
}

// nodeMenu 是资源管理器条目的右键菜单。
func (s *Shell) nodeMenu(el ui.Element, n *folderNode) {
	el.ContextMenu(func(m *ui.Menu) {
		dir := n.path
		if !n.dir {
			dir = filepath.Dir(n.path)
			if m.Item(s.tr("open")).Chosen() {
				s.OpenFiles([]string{n.path})
			}
		}
		if m.Item(s.tr("newFile")).Chosen() {
			s.newFileIn(dir)
		}
		if n.path != s.folder.path && m.Item(s.tr("rename")).Chosen() {
			s.beginRename(n.path)
		}
		m.Separator()
		if m.Item(s.tr("copyPath")).Chosen() {
			s.copyText(n.path)
		}
		if m.Item(s.tr("revealInFolder")).Chosen() {
			revealInFolder(n.path)
		}
		if n.dir && m.Item(s.tr("refresh")).Chosen() {
			n.refresh()
		}
	})
}

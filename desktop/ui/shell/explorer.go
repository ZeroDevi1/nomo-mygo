package shell

import (
	"path/filepath"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/docfs"
	"nomo/desktop/ui/icons"
)

// folderNode 是资源管理器中的一个目录或文件。目录的子项在第一次展开时才读取
// （docfs.ListFolderChildren 只读一层，并应用与原版一致的忽略规则与 .gitignore）。
type folderNode struct {
	name, path, root string
	dir              bool
	hasChildren      bool
	loaded           bool
	open             bool
	children         []*folderNode
	err              string
}

func newFolderNode(path, root string) (*folderNode, error) {
	n := &folderNode{name: filepath.Base(path), path: path, root: root, dir: true, hasChildren: true, open: true}
	if err := n.load(); err != nil {
		return nil, err
	}
	return n, nil
}

func (n *folderNode) load() error {
	entries, err := docfs.ListFolderChildren(n.path, n.root)
	if err != nil {
		n.err = err.Error()
		return err
	}
	n.children = n.children[:0]
	for _, e := range entries {
		n.children = append(n.children, &folderNode{name: e.Name, path: e.Path, root: n.root, dir: e.IsDir, hasChildren: e.HasChildren})
	}
	n.loaded, n.err = true, ""
	return nil
}

// refresh 重新读取已加载的目录，保留展开状态。
func (n *folderNode) refresh() {
	if !n.dir || !n.loaded {
		return
	}
	open := map[string]*folderNode{}
	for _, c := range n.children {
		if c.dir && c.loaded {
			open[c.path] = c
		}
	}
	_ = n.load()
	for i, c := range n.children {
		if old, ok := open[c.path]; ok {
			old.hasChildren = c.hasChildren
			n.children[i] = old
			old.refresh()
		}
	}
}

// tree 画目录树，行样式对应原版 ExplorerSidebar：按层级缩进、可旋转的展开箭头、文件夹与
// 文件类型图标（Markdown / JSON / TXT / 图片）、当前文档高亮、右键菜单。
// 每帧只构建已展开目录中的行；目录子项在第一次展开时读取。
func (s *Shell) tree(c *ui.Context, root *folderNode) {
	active := ""
	if t := s.activeTab(); t != nil {
		active = t.Path
	}
	var walk func(n *folderNode, depth int)
	walk = func(n *folderNode, depth int) {
		for _, ch := range n.children {
			s.treeRow(c, ch, depth, active)
			if ch.dir && ch.open {
				if !ch.loaded {
					_ = ch.load()
				}
				walk(ch, depth+1)
			}
		}
	}
	ui.Column(c).FillWidth().Gap(1).Children(func() { walk(root, 0) })
}

func (s *Shell) treeRow(c *ui.Context, n *folderNode, depth int, active string) {
	ck := c.Key(n.path)
	selected := !n.dir && active != "" && samePath(active, n.path)
	row := ui.ButtonBase(ck).FillWidth().Height(26).Padding(0, 8, 0, 6+float32(depth)*14).Gap(4).
		Radius(c.Theme().Radius).Justify(ui.Start).Label(n.name)
	switch {
	case selected:
		row.Background(s.ch.pressed)
	case row.Hovered():
		row.Background(s.ch.hover)
	}
	row.Children(func() {
		if n.dir {
			arrow := ui.Icon(ck, icons.Get("chevron-right")).FontSize(12).TextColor(s.ch.muted)
			if n.open {
				arrow.Rotate(90)
			}
			if !n.hasChildren {
				arrow.Opacity(0)
			}
			folder := "folder"
			if n.open {
				folder = "folder-open"
			}
			ui.Icon(ck, icons.Get(folder)).FontSize(15).TextColor(s.ch.accent)
		} else {
			ui.Box(ck).Width(12)
			ui.Icon(ck, icons.ForFile(n.name)).FontSize(15).TextColor(s.ch.muted)
		}
		ui.Text(ck, n.name).FontSize(13).SingleLine().Shrink(1)
	})
	s.nodeMenu(row, n)
	if row.Clicked() {
		if n.dir {
			n.open = !n.open && n.hasChildren
		} else {
			s.openPreview(n.path)
		}
	}
}

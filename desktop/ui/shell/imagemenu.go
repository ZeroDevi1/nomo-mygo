package shell

import (
	"net/url"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/ui/mdedit"
)

// 图片右键菜单与预览（原版图片菜单与全屏预览）：预览、复制地址、在文件夹中显示、宽度与对齐、删除。
// 宽度与对齐按插入图片的同一规则改写原文（mdedit.ImageSnippet），对齐只在图片独占所在块时可用。

// imageWidths 是右键菜单中的宽度预设；空串为原始尺寸。
var imageWidths = []string{"", "25%", "50%", "75%", "100%"}

// imagePreview 是全屏图片预览的状态；bmp 非空时显示。
type imagePreview struct {
	bmp  *ui.Bitmap
	w, h float32
}

// localImagePath 把图片地址解析为本地文件路径（相对文档目录）；网络、data 地址或无法解析时返回空串。
func localImagePath(tab *Tab, src string) string {
	if u, err := url.Parse(src); err == nil && u.Scheme != "" {
		if u.Scheme == "file" {
			return u.Path
		}
		return ""
	}
	p, err := url.PathUnescape(src)
	if err != nil {
		p = src
	}
	if !filepath.IsAbs(p) {
		if tab.Path == "" {
			return ""
		}
		p = filepath.Join(filepath.Dir(tab.Path), p)
	}
	return p
}

// imageMenu 填充右键落在图片上时的菜单项。
func (s *Shell) imageMenu(c *ui.Context, m *ui.Menu, tab *Tab, tg mdedit.ContextTarget) {
	ref, ok := tab.Ed.ImageAt(tg.Pos)
	if !ok {
		return
	}
	done := func() { s.focusEditor = true }
	if m.Item(s.tr("fullscreenImagePreview")).Chosen() {
		s.openImagePreview(tab, ref.Src)
	}
	local := localImagePath(tab, ref.Src)
	if m.Item(s.tr("copyImagePath")).Chosen() {
		if local != "" {
			c.Services().WriteClipboard(local)
		} else {
			c.Services().WriteClipboard(ref.Src)
		}
	}
	if m.Item(s.tr("openImageLocation")).Disabled(local == "").Chosen() {
		revealInFolder(local)
	}
	m.Separator()
	m.Submenu(s.tr("imageWidth"), func(sub *ui.Menu) {
		for _, w := range imageWidths {
			label := w
			if w == "" {
				label = s.tr("originalSize")
			}
			if sub.Item(label).Checked(strings.TrimSpace(ref.Width) == w).Chosen() {
				tab.Ed.SetImage(tg.Pos, w, ref.Align)
				done()
			}
		}
	})
	m.Submenu(s.tr("imageAlignment"), func(sub *ui.Menu) {
		for _, a := range [][2]string{{"", "followText"}, {"left", "alignLeft"}, {"center", "alignCenter"}, {"right", "alignRight"}} {
			if sub.Item(s.tr(a[1])).Checked(ref.Align == a[0]).Disabled(!ref.Standalone && a[0] != "").Chosen() {
				tab.Ed.SetImage(tg.Pos, ref.Width, a[0])
				done()
			}
		}
	})
	m.Separator()
	if m.Item(s.tr("deleteImage")).Chosen() {
		tab.Ed.DeleteImage(tg.Pos)
		done()
	}
}

// openImagePreview 全屏预览图片；图片尚未加载或加载失败时提示。
func (s *Shell) openImagePreview(tab *Tab, src string) {
	bmp, w, h, ok := tab.Ed.ImageBitmap(src)
	if !ok {
		s.status = s.tr("imageLoadFailed")
		return
	}
	s.preview = imagePreview{bmp: bmp, w: w, h: h}
}

// imagePreviewView 画全屏预览：深色背景上按窗口等比缩小（不放大）居中显示，点击任意处或 Esc 关闭。
func (s *Shell) imagePreviewView(c *ui.Context) {
	pv := &s.preview
	if pv.bmp == nil {
		return
	}
	w, h := c.Size()
	f := min(1, (w-80)/max(1, pv.w), (h-80)/max(1, pv.h))
	ui.Overlay(c, func() {
		bg := ui.ButtonBase(c).Absolute().Left(0).Top(0).Size(w, h).Center().Background(ui.RGBA(0, 0, 0, 0.82)).
			Label(s.tr("closeFullscreenImage")).Children(func() {
			ui.Image(c, pv.bmp).Size(pv.w*f, pv.h*f)
		})
		if bg.Clicked() || bg.Shortcut(0, ui.KeyEscape) {
			*pv = imagePreview{}
			s.focusEditor = true
		}
		bg.Focus()
	})
}

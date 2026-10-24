package shell

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/imageasset"
	"nomo/desktop/ui/mdedit"
)

// 图片插入（对应原版 src/app/services/imageInsertion.ts）：粘贴、拖放或菜单选择的图片按
// imageHandlingSettings 的策略复制到文档旁的资源目录或经 PicGo 上传，再在光标处插入
// Markdown（设置了默认宽度或对齐时插入与原版相同的 <img> / <p align> HTML）。
// 读文件、复制和上传在后台进行，结果回到 UI 线程插入；期间标签已关闭则放弃。

var imageExts = []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".bmp", ".avif", ".ico", ".tif", ".tiff"}

func isImageFile(path string) bool {
	return slices.Contains(imageExts, strings.ToLower(filepath.Ext(path)))
}

// imageInput 是一张待插入的图片：path 非空时在后台读取文件，否则使用 data（剪贴板 PNG）。
type imageInput struct {
	name, path string
	data       []byte
}

// pasteData 处理剪贴板中的图片文件或位图；没有图片时返回 false，由编辑器按文本粘贴。
func (s *Shell) pasteData(tab *Tab, d transfer.Data) bool {
	var in []imageInput
	if files, err := d.Files(); err == nil {
		for _, f := range files {
			if isImageFile(f) {
				in = append(in, imageInput{name: filepath.Base(f), path: f})
			}
		}
	}
	if len(in) == 0 {
		if png, err := d.Read(transfer.PNG); err == nil && len(png) > 0 {
			in = append(in, imageInput{name: "image.png", data: png})
		}
	}
	if len(in) == 0 {
		return false
	}
	s.insertImages(tab, in)
	return true
}

// dropFiles 处理拖到编辑区的文件：图片插入当前文档，其余交给 OpenFiles 打开。
func (s *Shell) dropFiles(tab *Tab, paths []string) {
	var images []imageInput
	var other []string
	for _, p := range paths {
		if isImageFile(p) {
			images = append(images, imageInput{name: filepath.Base(p), path: p})
		} else {
			other = append(other, p)
		}
	}
	if len(images) > 0 && tab != nil {
		s.insertImages(tab, images)
	}
	if len(other) > 0 {
		s.openDropped(other)
	}
}

// pickImages 打开文件选择框，选中的图片插入当前文档。
func (s *Shell) pickImages() {
	tab := s.activeTab()
	if tab == nil {
		return
	}
	go func() {
		exts := make([]string, len(imageExts))
		for i, e := range imageExts {
			exts[i] = e[1:]
		}
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: s.win, Multiple: true,
			Filters: []mygo.FileFilter{{Name: s.tr("image"), Extensions: exts}}})
		if err != nil || len(paths) == 0 {
			return
		}
		s.postToSettings(func() { s.dropFiles(tab, paths) })
	}()
}

// insertImages 导入或上传图片后插入 Markdown。本地策略要求文档已保存，未保存时提示并放弃。
func (s *Shell) insertImages(tab *Tab, in []imageInput) {
	set := s.prefs.ImageHandlingSettings
	if set.ImageInsertStrategy != "upload" && tab.Path == "" {
		s.status = s.tr("saveBeforeInsertLocalImage")
		return
	}
	docPath, id := tab.Path, tab.ID
	go func() {
		type item struct{ src, alt string }
		var done []item
		failed := 0
		for _, img := range in {
			src, err := importImage(docPath, set, img)
			if err != nil {
				failed++
				applog.Error("ImageInsertion", img.name+"："+err.Error())
				continue
			}
			done = append(done, item{src, img.name})
		}
		s.postToSettings(func() {
			t := s.tabWithID(id)
			if t == nil {
				return
			}
			if len(done) > 0 {
				parts := make([]string, len(done))
				for i, it := range done {
					parts[i] = mdedit.ImageSnippet(it.src, it.alt, set.DefaultImageWidth, set.DefaultImageAlign)
				}
				s.insertBlock(t, strings.Join(parts, "\n"))
			}
			n := strconv.Itoa(len(done))
			if failed > 0 {
				s.status = s.tr("imagesInsertedWithFailures", "inserted", n, "failed", strconv.Itoa(failed))
			} else {
				s.status = s.tr("imagesInserted", "inserted", n)
			}
		})
	}()
}

// importImage 在后台按策略处理一张图片，返回 Markdown 中引用的地址。
func importImage(docPath string, set ImageSettings, img imageInput) (string, error) {
	data := img.data
	if img.path != "" {
		var err error
		if data, err = os.ReadFile(img.path); err != nil {
			return "", err
		}
	}
	if set.ImageInsertStrategy == "upload" {
		if set.UploadProvider == "picgo-core" {
			return imageasset.UploadPicgoCore(img.name, data, set.PicgoCoreCommand, set.PicgoCoreConfigPath)
		}
		return imageasset.UploadPicgoServer(img.name, data, set.PicgoServerURL)
	}
	a, err := imageasset.Import(docPath, filepath.Base(docPath), set.ImageInsertStrategy, img.name, data)
	if err != nil {
		return "", err
	}
	return a.MarkdownSrc, nil
}

// insertBlock 在光标处插入独占行的内容：前后不是行边界时补换行（与原版源码模式插入一致）。
func (s *Shell) insertBlock(t *Tab, snippet string) {
	if s.mode() == modeSource {
		src := t.Source()
		text, sel := src.Buffer.String(), src.Selection()
		a, z := min(sel.Anchor, sel.Head), max(sel.Anchor, sel.Head)
		src.Insert(padBlock(text[:a], text[z:], snippet))
		return
	}
	text, sel := t.Doc.Source(), t.Doc.Sel
	a, z := min(sel.Anchor, sel.Head), max(sel.Anchor, sel.Head)
	t.Ed.InsertText(padBlock(text[:a], text[z:], snippet))
	s.focusEditor = true
}

func padBlock(before, after, snippet string) string {
	if before != "" && !strings.HasSuffix(before, "\n") {
		snippet = "\n" + snippet
	}
	if after != "" && !strings.HasPrefix(after, "\n") {
		snippet += "\n"
	}
	return snippet
}

func (s *Shell) tabWithID(id int) *Tab {
	for _, t := range s.tabs {
		if t.ID == id {
			return t
		}
	}
	return nil
}

package word

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// 图片资源：本地（相对文档目录）、data URL 与远程（8 秒超时）。PNG / JPEG 原样嵌入，GIF 取首帧、SVG 栅格化、
// WebP / BMP / TIFF 转 PNG。同一内容只嵌入一次；媒体总量与单图像素有上限（与原版一致），超出时整个导出失败。

const (
	mediaBudget    = 128 << 20  // 媒体总字节上限
	pixelBudget    = 16_000_000 // 单图像素上限
	remoteTimeout  = 8 * time.Second
	remoteMaxBytes = 32 << 20
	svgRasterScale = 2 // SVG 栅格化倍率（清晰度）
)

// errBudget 表示超出媒体预算：整个导出失败，不输出截断的文档。
var errBudget = errors.New("budget")

// picture 是一张已嵌入的图片：rid 为关系 ID，w、h 为显示尺寸（CSS 像素）。
type picture struct {
	rid  string
	w, h float64
}

// loadImage 读取 src 指向的图片数据。
func (b *builder) loadImage(src string) ([]byte, error) {
	switch {
	case strings.HasPrefix(src, "data:"):
		comma := strings.IndexByte(src, ',')
		if comma < 0 {
			return nil, errors.New("bad data url")
		}
		meta, payload := src[len("data:"):comma], src[comma+1:]
		if strings.HasSuffix(meta, ";base64") {
			return base64.StdEncoding.DecodeString(strings.Join(strings.Fields(payload), ""))
		}
		s, err := url.PathUnescape(payload)
		return []byte(s), err
	case strings.HasPrefix(src, "http://"), strings.HasPrefix(src, "https://"):
		client := b.o.HTTPClient
		if client == nil {
			client = &http.Client{Timeout: remoteTimeout}
		}
		resp, err := client.Get(src)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, remoteMaxBytes+1))
		if err == nil && len(data) > remoteMaxBytes {
			return nil, errBudget
		}
		return data, err
	}
	p := src
	if u, err := url.Parse(src); err == nil && (u.Scheme == "file" || u.Scheme == "") {
		p = u.Path
	}
	if !filepath.IsAbs(p) {
		if b.o.BaseDir == "" {
			return nil, errors.New("未保存文档的相对路径")
		}
		p = filepath.Join(b.o.BaseDir, p)
	}
	return os.ReadFile(p)
}

// embedImage 嵌入 src 指向的图片，返回图片与是否成功；失败时记录降级说明（budget 错误向上返回）。
func (b *builder) embedImage(src string) (picture, bool, error) {
	data, err := b.loadImage(src)
	if err == nil {
		var pic picture
		pic, err = b.embedBytes(data, src)
		if err == nil {
			return pic, true, nil
		}
	}
	if errors.Is(err, errBudget) {
		return picture{}, false, err
	}
	b.warnResource(b.t("wordExportImage"), src)
	return picture{}, false, nil
}

// embedBytes 把图片数据转为 PNG / JPEG 并加入媒体，返回图片。
func (b *builder) embedBytes(data []byte, src string) (picture, error) {
	head := data[:min(len(data), 512)]
	if bytes.Contains(head, []byte("<svg")) || strings.HasSuffix(strings.ToLower(strings.SplitN(src, "?", 2)[0]), ".svg") {
		return b.embedSVG(data)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return picture{}, err
	}
	if cfg.Width*cfg.Height > pixelBudget {
		return picture{}, errBudget
	}
	ext := ""
	switch format {
	case "png":
		ext = "png"
	case "jpeg":
		ext = "jpeg"
	case "gif":
		g, err := gif.Decode(bytes.NewReader(data))
		if err != nil {
			return picture{}, err
		}
		if all, err := gif.DecodeAll(bytes.NewReader(data)); err == nil && len(all.Image) > 1 {
			b.warn(b.t("wordExportGifWarning"))
		}
		return b.embedImageValue(g, float64(cfg.Width), float64(cfg.Height))
	default:
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return picture{}, err
		}
		return b.embedImageValue(img, float64(cfg.Width), float64(cfg.Height))
	}
	rid, err := b.addMedia(data, ext)
	return picture{rid: rid, w: float64(cfg.Width), h: float64(cfg.Height)}, err
}

// embedSVG 栅格化 SVG（MyGo 绘制形状，不画文字、不加载外部资源），显示尺寸取 SVG 自身的宽高。
func (b *builder) embedSVG(data []byte) (picture, error) {
	doc, err := ui.ParseSVG(data)
	if err != nil {
		return picture{}, err
	}
	w, h := doc.Size()
	if w <= 0 || h <= 0 {
		w, h = 300, 150
	}
	pw, ph := int(w*svgRasterScale), int(h*svgRasterScale)
	if pw*ph > pixelBudget {
		return picture{}, errBudget
	}
	t := ui.NewTester(func(c *ui.Context) {
		c.Root().Background(ui.Transparent)
		ui.Image(c, doc).Fill()
	}, int(w), int(h))
	t.SetScale(svgRasterScale)
	t.Frame()
	return b.embedImageValue(t.Image(), float64(w), float64(h))
}

// embedImageValue 把位图编码为 PNG 嵌入，w、h 为显示尺寸（CSS 像素）。
func (b *builder) embedImageValue(img image.Image, w, h float64) (picture, error) {
	if r := img.Bounds(); r.Dx()*r.Dy() > pixelBudget {
		return picture{}, errBudget
	}
	if _, ok := img.(*image.Paletted); ok {
		rgba := image.NewRGBA(img.Bounds())
		draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
		img = rgba
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return picture{}, err
	}
	rid, err := b.addMedia(buf.Bytes(), "png")
	return picture{rid: rid, w: w, h: h}, err
}

// addMedia 加入媒体文件（同内容只加一次），返回关系 ID；超出总预算时返回 errBudget。
func (b *builder) addMedia(data []byte, ext string) (string, error) {
	sum := sha256.Sum256(data)
	if rid, ok := b.mediaByHash[sum]; ok {
		return rid, nil
	}
	if b.mediaBytes += len(data); b.mediaBytes > mediaBudget {
		return "", errBudget
	}
	name := fmt.Sprintf("media/image%d.%s", len(b.media)+1, ext)
	b.media = append(b.media, mediaFile{name: "word/" + name, data: data})
	rid := b.addRel("image", name, false)
	b.mediaByHash[sum] = rid
	return rid, nil
}

type mediaFile struct {
	name string
	data []byte
}

// drawing 返回内联图片的 run（显示尺寸按版心宽度等比缩小；lower 为下沉的磅数，行内公式对齐基线用）。
func (b *builder) drawing(p picture, alt string, maxWidthEMU int64, lower float64) string {
	cx, cy := int64(p.w*emuPerPx), int64(p.h*emuPerPx)
	if maxWidthEMU > 0 && cx > maxWidthEMU {
		cy = cy * maxWidthEMU / cx
		cx = maxWidthEMU
	}
	b.docPrID++
	id := b.docPrID
	pos := ""
	if lower > 0 {
		pos = fmt.Sprintf(`<w:rPr><w:position w:val="%d"/></w:rPr>`, -int(lower*2+0.5))
	}
	return fmt.Sprintf(`<w:r>%s<w:drawing><wp:inline distT="0" distB="0" distL="0" distR="0"><wp:extent cx="%d" cy="%d"/>`+
		`<wp:docPr id="%d" name="Picture %d" descr="%s"/><a:graphic xmlns:a="%s"><a:graphicData uri="%s">`+
		`<pic:pic xmlns:pic="%s"><pic:nvPicPr><pic:cNvPr id="%d" name="Picture %d"/><pic:cNvPicPr/></pic:nvPicPr>`+
		`<pic:blipFill><a:blip r:embed="%s"/><a:stretch><a:fillRect/></a:stretch></pic:blipFill>`+
		`<pic:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr>`+
		`</pic:pic></a:graphicData></a:graphic></wp:inline></w:drawing></w:r>`,
		pos, cx, cy, id, id, esc(alt), nsA, nsPic, nsPic, id, id, p.rid, cx, cy)
}

package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"path/filepath"

	_ "golang.org/x/image/webp"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"

	"nomo/desktop/internal/i18n"
)

// 前端运行时（src/lib/desktop/runtime）依赖的通用命令：系统对话框、路径运算与剪贴板。

type dialogFilter struct {
	Name       string   `json:"name"`
	Extensions []string `json:"extensions"`
}

func toDialogFilters(filters []dialogFilter) []mygo.FileFilter {
	out := make([]mygo.FileFilter, 0, len(filters))
	for _, filter := range filters {
		out = append(out, mygo.FileFilter{Name: filter.Name, Extensions: filter.Extensions})
	}
	return out
}

// parentWindow 返回调用窗口的原生窗口，用于以 sheet / 模态方式弹出；非应用窗口调用时为 nil。
func (c *commandCall) parentWindow() *mygo.Window {
	if c.window == nil {
		return nil
	}
	return c.window.win
}

// askButtonLabels 返回确认对话框的默认按钮文字（确认、取消），跟随外壳语言。
func askButtonLabels(locale i18n.Locale) (string, string) {
	switch locale {
	case i18n.ZhCN:
		return "确定", "取消"
	case i18n.ZhTW:
		return "確定", "取消"
	case i18n.JaJP:
		return "OK", "キャンセル"
	default:
		return "OK", "Cancel"
	}
}

func init() {
	register("dialog_open", func(call *commandCall, args struct {
		Options struct {
			Title       string         `json:"title"`
			DefaultPath string         `json:"defaultPath"`
			Filters     []dialogFilter `json:"filters"`
			Multiple    bool           `json:"multiple"`
			Directory   bool           `json:"directory"`
		} `json:"options"`
	}) ([]string, error) {
		return mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent:            call.parentWindow(),
			Title:             args.Options.Title,
			DefaultPath:       args.Options.DefaultPath,
			Filters:           toDialogFilters(args.Options.Filters),
			Directory:         args.Options.Directory,
			Multiple:          args.Options.Multiple,
			CreateDirectories: args.Options.Directory,
		})
	})
	register("dialog_save", func(call *commandCall, args struct {
		Options struct {
			Title       string         `json:"title"`
			DefaultPath string         `json:"defaultPath"`
			Filters     []dialogFilter `json:"filters"`
		} `json:"options"`
	}) (string, error) {
		return mygo.Dialog.Save(mygo.SaveDialogOptions{
			Parent:            call.parentWindow(),
			Title:             args.Options.Title,
			DefaultPath:       args.Options.DefaultPath,
			Filters:           toDialogFilters(args.Options.Filters),
			CreateDirectories: true,
		})
	})
	register("dialog_ask", func(call *commandCall, args struct {
		Message string `json:"message"`
		Options struct {
			Title       string `json:"title"`
			Kind        string `json:"kind"`
			OKLabel     string `json:"okLabel"`
			CancelLabel string `json:"cancelLabel"`
		} `json:"options"`
	}) (bool, error) {
		okLabel, cancelLabel := askButtonLabels(effectiveLocale())
		if args.Options.OKLabel != "" {
			okLabel = args.Options.OKLabel
		}
		if args.Options.CancelLabel != "" {
			cancelLabel = args.Options.CancelLabel
		}
		messageType := mygo.MessageQuestion
		switch args.Options.Kind {
		case "info":
			messageType = mygo.MessageInfo
		case "warning":
			messageType = mygo.MessageWarning
		case "error":
			messageType = mygo.MessageError
		}
		result, err := mygo.Dialog.Message(mygo.MessageOptions{
			Parent:       call.parentWindow(),
			Type:         messageType,
			Title:        args.Options.Title,
			Message:      args.Message,
			Buttons:      []string{okLabel, cancelLabel},
			CancelButton: 1,
		})
		if err != nil {
			return false, err
		}
		return result.Button == 0, nil
	})

	register("app_version", func(_ *commandCall, _ noArgs) (string, error) {
		return appVersion(), nil
	})

	register("path_join", func(_ *commandCall, args struct {
		Paths []string `json:"paths"`
	}) (string, error) {
		return filepath.Join(args.Paths...), nil
	})
	register("path_is_absolute", func(_ *commandCall, args struct {
		Path string `json:"path"`
	}) (bool, error) {
		return filepath.IsAbs(args.Path), nil
	})
	// path_resolve 与 Node path.resolve 一致：从右向左遇到绝对路径为止拼接，结果为规范化的绝对路径。
	register("path_resolve", func(_ *commandCall, args struct {
		Paths []string `json:"paths"`
	}) (string, error) {
		resolved := ""
		for _, part := range args.Paths {
			if part == "" {
				continue
			}
			if filepath.IsAbs(part) {
				resolved = part
			} else {
				resolved = filepath.Join(resolved, part)
			}
		}
		return filepath.Abs(resolved)
	})
	register("path_dirname", func(_ *commandCall, args struct {
		Path string `json:"path"`
	}) (string, error) {
		if args.Path == "" {
			return "", errors.New("路径为空")
		}
		return filepath.Dir(args.Path), nil
	})

	// clipboard_write 写入纯文本，html 非空时同时提供 HTML 表示（富文本粘贴目标优先取 HTML）。
	register("clipboard_write", func(_ *commandCall, args struct {
		Text string `json:"text"`
		HTML string `json:"html"`
	}) (any, error) {
		representations := []transfer.Representation{transfer.Bytes(transfer.Text, []byte(args.Text))}
		if args.HTML != "" {
			representations = append(representations, transfer.Bytes(transfer.HTML, []byte(args.HTML)))
		}
		return nil, mygo.Clipboard.Write(transfer.New(transfer.NewItem(representations...)))
	})
	// clipboard_write_image 写入图片；输入可为 PNG/JPEG/GIF/WebP 字节（base64），统一转为 PNG 写入。
	register("clipboard_write_image", func(_ *commandCall, args struct {
		BytesBase64 string `json:"bytesBase64"`
	}) (any, error) {
		raw, err := base64.StdEncoding.DecodeString(args.BytesBase64)
		if err != nil {
			return nil, fmt.Errorf("图片数据无效：%w", err)
		}
		encoded, err := toPNG(raw)
		if err != nil {
			return nil, err
		}
		return nil, mygo.Clipboard.Write(transfer.New(transfer.NewItem(transfer.Bytes(transfer.PNG, encoded))))
	})
	// clipboard_read_text 读取纯文本；剪贴板没有文本时返回空字符串。
	register("clipboard_read_text", func(_ *commandCall, _ noArgs) (string, error) {
		data, err := mygo.Clipboard.ReadFormat(transfer.Text)
		if errors.Is(err, transfer.ErrFormat) {
			return "", nil
		}
		return string(data), err
	})
	// clipboard_read_image 以 base64 返回 PNG 图片；剪贴板没有图片时返回空字符串。
	register("clipboard_read_image", func(_ *commandCall, _ noArgs) (string, error) {
		data, err := mygo.Clipboard.ReadFormat(transfer.PNG)
		if errors.Is(err, transfer.ErrFormat) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(data), nil
	})
}

// toPNG 把常见位图格式转为 PNG；已是 PNG 时原样返回。
func toPNG(raw []byte) ([]byte, error) {
	if bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")) {
		return raw, nil
	}
	decoded, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("无法解码图片：%w", err)
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, decoded); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

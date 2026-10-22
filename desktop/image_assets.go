package main

import (
	"encoding/base64"
	"encoding/json/v2"
	"errors"

	"nomo/desktop/internal/imageasset"
)

// 图片资源命令：实现在 internal/imageasset（与原生外壳共用），这里只做 IPC 参数转换。

// byteArray 接受前端传来的数字数组（Array.from(Uint8Array)）或 base64 字符串。
type byteArray []byte

func (b *byteArray) UnmarshalJSON(data []byte) error {
	var numbers []int
	if err := json.Unmarshal(data, &numbers); err == nil {
		decoded := make([]byte, len(numbers))
		for i, number := range numbers {
			if number < 0 || number > 255 {
				return errors.New("图片数据格式无效")
			}
			decoded[i] = byte(number)
		}
		*b = decoded
		return nil
	}
	var encoded string
	if err := json.Unmarshal(data, &encoded); err != nil {
		return errors.New("图片数据格式无效")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return errors.New("图片数据格式无效")
	}
	*b = decoded
	return nil
}

type desktopActionPayload struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func init() {
	register("import_image_asset", func(_ *commandCall, args struct {
		Input struct {
			DocumentPath     string    `json:"document_path"`
			DocumentFileName string    `json:"document_file_name"`
			Strategy         string    `json:"strategy"`
			FileName         string    `json:"file_name"`
			Bytes            byteArray `json:"bytes"`
		} `json:"input"`
	}) (*imageasset.Asset, error) {
		in := args.Input
		return imageasset.Import(in.DocumentPath, in.DocumentFileName, in.Strategy, in.FileName, in.Bytes)
	})
	register("resolve_image_asset", func(_ *commandCall, args struct {
		Input struct {
			DocumentPath *string `json:"document_path"`
			Src          string  `json:"src"`
		} `json:"input"`
	}) (*imageasset.ResolvePayload, error) {
		return imageasset.Resolve(args.Input.DocumentPath, args.Input.Src), nil
	})
	register("delete_image_asset", func(_ *commandCall, args struct {
		Input struct {
			DocumentPath *string `json:"document_path"`
			Src          string  `json:"src"`
		} `json:"input"`
	}) (*imageasset.DeletePayload, error) {
		return imageasset.Delete(args.Input.DocumentPath, args.Input.Src), nil
	})
	register("upload_image_via_picgo_core", func(_ *commandCall, args struct {
		Input struct {
			FileName   string    `json:"file_name"`
			Bytes      byteArray `json:"bytes"`
			Command    string    `json:"command"`
			ConfigPath *string   `json:"config_path"`
		} `json:"input"`
	}) (map[string]string, error) {
		in := args.Input
		configPath := ""
		if in.ConfigPath != nil {
			configPath = *in.ConfigPath
		}
		url, err := imageasset.UploadPicgoCore(in.FileName, in.Bytes, in.Command, configPath)
		if err != nil {
			return nil, err
		}
		return map[string]string{"url": url}, nil
	})
	register("upload_image_via_picgo_server", func(_ *commandCall, args struct {
		Input struct {
			FileName  string    `json:"file_name"`
			Bytes     byteArray `json:"bytes"`
			ServerURL string    `json:"server_url"`
		} `json:"input"`
	}) (map[string]string, error) {
		url, err := imageasset.UploadPicgoServer(args.Input.FileName, args.Input.Bytes, args.Input.ServerURL)
		if err != nil {
			return nil, err
		}
		return map[string]string{"url": url}, nil
	})
	register("test_picgo_connection", func(_ *commandCall, args struct {
		Input struct {
			Provider  string  `json:"provider"`
			ServerURL *string `json:"server_url"`
			Command   *string `json:"command"`
		} `json:"input"`
	}) (*desktopActionPayload, error) {
		deref := func(p *string) string {
			if p == nil {
				return ""
			}
			return *p
		}
		message, err := imageasset.TestConnection(args.Input.Provider, deref(args.Input.ServerURL), deref(args.Input.Command))
		if err != nil {
			return nil, err
		}
		return &desktopActionPayload{OK: true, Message: message}, nil
	})
}

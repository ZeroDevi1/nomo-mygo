// Package textcodec 提供文档读写和预览共用的字符编解码。
package textcodec

import (
	"bytes"
	"errors"
	"fmt"
	"golang.org/x/text/encoding/simplifiedchinese"
	"unicode/utf16"
	"unicode/utf8"
)

// Encoding 是文档在磁盘上的字符编码，取值与前端 MarkdownEncoding 联合类型一致。
type Encoding string

const (
	EncodingUTF8       Encoding = "utf-8"
	EncodingUTF8BOM    Encoding = "utf-8-bom"
	EncodingUTF16LEBOM Encoding = "utf-16le-bom"
	EncodingUTF16BEBOM Encoding = "utf-16be-bom"
	EncodingGBK        Encoding = "gbk"
)

var (
	utf8BOM    = []byte{0xEF, 0xBB, 0xBF}
	utf16LEBOM = []byte{0xFF, 0xFE}
	utf16BEBOM = []byte{0xFE, 0xFF}
)

// Decode 按 BOM → 严格 UTF-8 → GBK 的顺序探测并解码。
func Decode(data []byte) (string, Encoding, error) {
	if body, ok := bytes.CutPrefix(data, utf8BOM); ok {
		if !utf8.Valid(body) {
			return "", "", errors.New("读取 Markdown 文件失败：UTF-8 BOM 文件内容无效")
		}
		return string(body), EncodingUTF8BOM, nil
	}
	if body, ok := bytes.CutPrefix(data, utf16LEBOM); ok {
		text, err := decodeUTF16(body, true)
		return text, EncodingUTF16LEBOM, err
	}
	if body, ok := bytes.CutPrefix(data, utf16BEBOM); ok {
		text, err := decodeUTF16(body, false)
		return text, EncodingUTF16BEBOM, err
	}
	if utf8.Valid(data) {
		return string(data), EncodingUTF8, nil
	}
	if text, ok := decodeGBKStrict(data); ok {
		return text, EncodingGBK, nil
	}
	return "", "", errors.New("读取 Markdown 文件失败：文件不是有效的 UTF-8、带 BOM 的 UTF-16 或 GBK 编码")
}

// decodeGBKStrict 用 GB18030 解码器（WHATWG 的 GBK 解码即 GB18030）解码，出现替换字符即判失败。
// 输入已确认不是合法 UTF-8，GBK 也不能表示 U+FFFD，所以替换字符只可能来自非法序列。
func decodeGBKStrict(data []byte) (string, bool) {
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(data)
	if err != nil || bytes.ContainsRune(decoded, utf8.RuneError) {
		return "", false
	}
	return string(decoded), true
}

func decodeUTF16(body []byte, littleEndian bool) (string, error) {
	if len(body)%2 != 0 {
		return "", errors.New("读取 Markdown 文件失败：UTF-16 文件字节数无效")
	}
	units := make([]uint16, len(body)/2)
	for i := range units {
		if littleEndian {
			units[i] = uint16(body[2*i]) | uint16(body[2*i+1])<<8
		} else {
			units[i] = uint16(body[2*i])<<8 | uint16(body[2*i+1])
		}
	}
	// 与 Rust String::from_utf16 一致：孤立代理项视为非法而不是替换。
	for i := 0; i < len(units); i++ {
		switch {
		case utf16.IsSurrogate(rune(units[i])):
			if units[i] >= 0xDC00 || i+1 >= len(units) || units[i+1] < 0xDC00 || units[i+1] > 0xDFFF {
				return "", errors.New("读取 Markdown 文件失败：UTF-16 文件内容无效")
			}
			i++
		}
	}
	return string(utf16.Decode(units)), nil
}

// Encode 按指定编码生成文件字节；GBK 无法表示的字符返回错误且不写盘。
func Encode(markdown string, encoding Encoding) ([]byte, error) {
	switch encoding {
	case EncodingUTF8:
		return []byte(markdown), nil
	case EncodingUTF8BOM:
		return append(bytes.Clone(utf8BOM), markdown...), nil
	case EncodingUTF16LEBOM, EncodingUTF16BEBOM:
		units := utf16.Encode([]rune(markdown))
		out := make([]byte, 2, 2+len(units)*2)
		if encoding == EncodingUTF16LEBOM {
			copy(out, utf16LEBOM)
			for _, unit := range units {
				out = append(out, byte(unit), byte(unit>>8))
			}
		} else {
			copy(out, utf16BEBOM)
			for _, unit := range units {
				out = append(out, byte(unit>>8), byte(unit))
			}
		}
		return out, nil
	case EncodingGBK:
		encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(markdown))
		if err != nil {
			return nil, errors.New("保存 Markdown 文件失败：文档包含 GBK 无法表示的字符，原文件未被修改")
		}
		return encoded, nil
	}
	return nil, fmt.Errorf("不支持的编码：%s", encoding)
}

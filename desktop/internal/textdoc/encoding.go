package textdoc

import (
	"bytes"
	"unicode/utf8"
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

type encodingProbe struct {
	encoding   TextEncoding
	bomLen     uint64
	lineEnding LineEnding
}

// probe 探测首窗编码与换行。completeFile 为 false 时块尾可能截断多字节字符，
// 只有确定的非法序列才判为 unsupported。
func probe(data []byte, completeFile bool) encodingProbe {
	body, bomLen, utf8Encoding := data, uint64(0), EncodingUTF8
	if bytes.HasPrefix(data, utf8BOM) {
		body, bomLen, utf8Encoding = data[len(utf8BOM):], 3, EncodingUTF8BOM
	}
	encoding := utf8Encoding
	if validUpTo, definite := utf8Prefix(body); validUpTo != len(body) && (completeFile || definite) {
		encoding = EncodingUnsupported
	}
	return encodingProbe{encoding: encoding, bomLen: bomLen, lineEnding: detectLineEnding(body)}
}

// utf8Prefix 返回最长合法 UTF-8 前缀长度，以及遇到的问题是否为确定的非法序列
// （false 表示只是末尾被截断的不完整字符，对应 Rust Utf8Error::error_len() == None）。
func utf8Prefix(data []byte) (validUpTo int, definiteError bool) {
	for i := 0; i < len(data); {
		if data[i] < utf8.RuneSelf {
			i++
			continue
		}
		r, size := utf8.DecodeRune(data[i:])
		if r != utf8.RuneError || size > 1 {
			i += size
			continue
		}
		// 解码失败：剩余字节若是某个合法字符的前缀，则只是截断。
		return i, !utf8IncompletePrefix(data[i:])
	}
	return len(data), false
}

// utf8IncompletePrefix 判断 tail 是否为某个合法多字节 UTF-8 字符的真前缀。
func utf8IncompletePrefix(tail []byte) bool {
	if len(tail) == 0 || len(tail) >= 4 {
		return false
	}
	need := 0
	lower, upper := byte(0x80), byte(0xBF)
	switch b := tail[0]; {
	case b >= 0xC2 && b <= 0xDF:
		need = 2
	case b == 0xE0:
		need, lower = 3, 0xA0
	case b >= 0xE1 && b <= 0xEC, b == 0xEE, b == 0xEF:
		need = 3
	case b == 0xED:
		need, upper = 3, 0x9F
	case b == 0xF0:
		need, lower = 4, 0x90
	case b >= 0xF1 && b <= 0xF3:
		need = 4
	case b == 0xF4:
		need, upper = 4, 0x8F
	default:
		return false
	}
	if len(tail) >= need {
		return false
	}
	for i := 1; i < len(tail); i++ {
		lo, hi := byte(0x80), byte(0xBF)
		if i == 1 {
			lo, hi = lower, upper
		}
		if tail[i] < lo || tail[i] > hi {
			return false
		}
	}
	return true
}

func detectLineEnding(data []byte) LineEnding {
	var lf, crlf, loneCR uint64
	for i := 0; i < len(data); {
		if data[i] == '\r' && i+1 < len(data) && data[i+1] == '\n' {
			crlf++
			i += 2
			continue
		}
		if data[i] == '\n' {
			lf++
		} else if data[i] == '\r' {
			loneCR++
		}
		i++
	}
	return classifyLineEnding(lf, crlf, loneCR)
}

func classifyLineEnding(lf, crlf, loneCR uint64) LineEnding {
	switch {
	case crlf > 0 && lf == 0 && loneCR == 0:
		return LineEndingCRLF
	case crlf == 0 && loneCR == 0:
		return LineEndingLF
	default:
		// 协议没有单独的 CR 枚举；CR-only 与混合换行都必须显式归入 mixed。
		return LineEndingMixed
	}
}

// normalizeInsertedText 在 CRLF 文档中把插入文本的 LF 规范化为 CRLF，其余情况原样返回。
func normalizeInsertedText(text string, lineEnding LineEnding) []byte {
	if lineEnding != LineEndingCRLF {
		return []byte(text)
	}
	output := make([]byte, 0, len(text))
	for i := 0; i < len(text); {
		switch {
		case text[i] == '\r' && i+1 < len(text) && text[i+1] == '\n':
			output = append(output, '\r', '\n')
			i += 2
		case text[i] == '\n':
			output = append(output, '\r', '\n')
			i++
		default:
			output = append(output, text[i])
			i++
		}
	}
	return output
}

// countLineBreaks 统计逻辑换行数：CRLF、LF 与孤立 CR 各算一次。
func countLineBreaks(data []byte) uint64 {
	previousCR := false
	breaks := advanceLineBreaks(data, &previousCR)
	if previousCR {
		breaks++
	}
	return breaks
}

// advanceLineBreaks 流式统计逻辑换行；尾部 CR 延迟到下一块，保证跨块 CRLF 只计一次。
func advanceLineBreaks(data []byte, previousCR *bool) uint64 {
	var breaks uint64
	for _, b := range data {
		if *previousCR {
			breaks++
			*previousCR = false
			if b == '\n' {
				continue
			}
		}
		switch b {
		case '\r':
			*previousCR = true
		case '\n':
			breaks++
		}
	}
	return breaks
}

// decodeLossyWithUTF16Offsets 为非 UTF-8 文档的只读显示有损解码，同时生成 UTF-16 边界到
// 原始窗口字节的映射。映射长度恒为 UTF-16 码元数 + 1，替换字符不会改写全局偏移。
func decodeLossyWithUTF16Offsets(data []byte) (string, []uint32) {
	var text []byte
	offsets := []uint32{0}
	cursor := 0
	for cursor < len(data) {
		validUpTo, definite := utf8Prefix(data[cursor:])
		appendValidWithOffsets(&text, &offsets, data[cursor:cursor+validUpTo], cursor)
		cursor += validUpTo
		if cursor >= len(data) {
			break
		}
		invalidLen := len(data) - cursor
		if definite {
			invalidLen = invalidSequenceLen(data[cursor:])
		}
		text = utf8.AppendRune(text, utf8.RuneError)
		cursor += invalidLen
		offsets = append(offsets, uint32(cursor))
	}
	return string(text), offsets
}

// invalidSequenceLen 复刻 Rust Utf8Error::error_len：最长的、可作为合法序列前缀的坏片段长度（至少 1）。
func invalidSequenceLen(tail []byte) int {
	for n := min(len(tail), 3); n > 1; n-- {
		if utf8IncompletePrefix(tail[:n]) {
			return n
		}
	}
	return 1
}

func appendValidWithOffsets(text *[]byte, offsets *[]uint32, valid []byte, base int) {
	for relative := 0; relative < len(valid); {
		r, size := utf8.DecodeRune(valid[relative:])
		start := base + relative
		end := start + size
		*text = append(*text, valid[relative:relative+size]...)
		if r >= 0x10000 {
			// 代理项中间不是合法编辑边界，映射到字符起点供前端拒绝拆分。
			*offsets = append(*offsets, uint32(start))
		}
		*offsets = append(*offsets, uint32(end))
		relative += size
	}
}

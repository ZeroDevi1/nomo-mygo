package mdedit

import "unicode/utf8"

// u16Len 返回 UTF-8 字符串的 UTF-16 码元数。按字节统计：每个非续字节计 1，
// 四字节序列的首字节再计 1（对应代理对），不解码 rune。
func u16Len(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c&0xC0 != 0x80 {
			n++
			if c >= 0xF0 {
				n++
			}
		}
	}
	return n
}

// u16ToByte 返回 s 中第 u 个 UTF-16 码元对应的字节偏移；u 落在代理对中间时
// 向后取整到完整 rune，超出时返回 len(s)。
func u16ToByte(s string, u int) int {
	n := 0
	for i := 0; i < len(s); {
		if n >= u {
			return i
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
		i += size
	}
	return len(s)
}

// runeStartBefore 返回 b 之前最近的 rune 起点（b 本身是起点时返回 b）。
func runeStartBefore(s string, b int) int {
	if b >= len(s) {
		return len(s)
	}
	for b > 0 && !utf8.RuneStart(s[b]) {
		b--
	}
	return b
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

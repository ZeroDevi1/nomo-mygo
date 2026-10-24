package shell

import (
	"bytes"
	"strings"

	"golang.org/x/net/html"
)

// 导出 HTML 的安全清理：Markdown 中的原始 HTML 原样进入导出文件，PDF 还要在隐藏 WebView 中加载它。
// 这里去掉可执行或可嵌入外部页面的内容，其余标签（含内嵌 SVG、style）原样保留，保证排版与原文一致：
//   - 元素（连同内容）：script、iframe、frame、frameset、object、embed、applet、base、meta、link；
//   - 属性：事件处理（on*）、srcdoc、formaction；
//   - URL 属性（href、src、action 等）中的 javascript:、vbscript: 地址，以及 data: 地址（只有 src 允许 data:image/）。

var (
	// unsafeElements 的值表示元素有内容与结束标签（跳到结束标签为止）；false 为空元素，只去掉标签本身。
	unsafeElements = map[string]bool{"script": true, "iframe": true, "frameset": true, "object": true, "applet": true,
		"frame": false, "embed": false, "base": false, "meta": false, "link": false}
	urlAttrs = map[string]bool{"href": true, "src": true, "action": true, "poster": true, "background": true,
		"xlink:href": true, "data": true, "cite": true, "longdesc": true}
)

// sanitizeExportHTML 返回去掉不安全内容后的 HTML 片段；未改动的标签与文字按原文输出（字节不变）。
func sanitizeExportHTML(in string) string {
	z := html.NewTokenizer(strings.NewReader(in))
	var out bytes.Buffer
	skip := "" // 正在跳过的不安全元素名（到其结束标签为止）
	depth := 0
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			// io.EOF 为正常结束；其他错误（截断的标签）时已输出部分是安全的，余下丢弃。
			return out.String()
		}
		raw := z.Raw()
		if skip != "" {
			name, _ := z.TagName()
			switch {
			case tt == html.StartTagToken && string(name) == skip:
				depth++
			case tt == html.EndTagToken && string(name) == skip:
				if depth--; depth == 0 {
					skip = ""
				}
			}
			continue
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken && tt != html.EndTagToken {
			out.Write(raw)
			continue
		}
		tok := z.Token()
		if hasContent, unsafe := unsafeElements[tok.Data]; unsafe {
			if hasContent && tt == html.StartTagToken {
				skip, depth = tok.Data, 1
			}
			continue
		}
		if tt == html.EndTagToken {
			out.Write(raw)
			continue
		}
		kept := tok.Attr[:0]
		for _, a := range tok.Attr {
			key := strings.ToLower(a.Key)
			if a.Namespace != "" {
				key = a.Namespace + ":" + key
			}
			if strings.HasPrefix(key, "on") || key == "srcdoc" || key == "formaction" ||
				urlAttrs[key] && unsafeURL(key, a.Val) {
				continue
			}
			kept = append(kept, a)
		}
		if len(kept) == len(tok.Attr) {
			out.Write(raw) // 未改动：保留原文（引号、大小写与 SVG 属性名不变）
			continue
		}
		tok.Attr = kept
		out.WriteString(tok.String())
	}
}

// unsafeURL 报告属性 key 的 URL 是否使用可执行脚本的协议，或为不允许的 data: 地址：图片上下文（src）不执行
// 脚本，允许 data:image/（含 SVG）；链接等可导航的属性一律不允许 data:。浏览器解析协议前会去掉空白与控制字符。
func unsafeURL(key, v string) bool {
	v = strings.ToLower(strings.Map(func(r rune) rune {
		if r <= ' ' {
			return -1
		}
		return r
	}, v))
	switch {
	case strings.HasPrefix(v, "javascript:"), strings.HasPrefix(v, "vbscript:"):
		return true
	case strings.HasPrefix(v, "data:"):
		return key != "src" || !strings.HasPrefix(v, "data:image/")
	}
	return false
}

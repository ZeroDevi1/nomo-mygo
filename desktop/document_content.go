package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/docfs"
)

// 大文档正文经自定义协议交给页面。MyGo 把调用结果拼成脚本 __mygo.receive([...]) 经 ExecuteScript 投递：
// macOS 的 JavaScriptCore 按 JSON 快速解析，Windows 的 WebView2（V8）要把整段脚本转成 UTF-16 并编译，几十 MB
// 的正文会让打开文档明显变慢并产生数倍于正文的临时内存。超过阈值的正文改为暂存在内存中，结果只带一次性令牌，
// 页面再用 fetch 从协议流式读取（与图片的 asset 协议同一机制）。

const (
	docContentScheme = "nomodoc"
	// docContentThreshold 是改走协议的正文字节数；更小的正文直接放在调用结果里，省一次往返。
	docContentThreshold = 1 << 20
	// docContentTTL 是暂存正文的最长保留时间：页面在拿到令牌后立即读取，过期未取的（页面已关闭）在下次暂存时丢弃。
	docContentTTL = time.Minute
)

type stashedContent struct {
	text    string
	expires time.Time
}

// docContents 保存待页面读取的正文：键为令牌，读取一次即删除。值引用已解码的正文字符串，不另做副本。
var docContents struct {
	mu sync.Mutex
	m  map[string]stashedContent
}

// stashDocumentContent 暂存正文并返回令牌（128 位随机数的十六进制）。
func stashDocumentContent(text string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	token := hex.EncodeToString(b[:])
	now := time.Now()
	docContents.mu.Lock()
	defer docContents.mu.Unlock()
	if docContents.m == nil {
		docContents.m = map[string]stashedContent{}
	}
	for k, v := range docContents.m {
		if now.After(v.expires) {
			delete(docContents.m, k)
		}
	}
	docContents.m[token] = stashedContent{text: text, expires: now.Add(docContentTTL)}
	return token
}

// takeDocumentContent 取出并删除令牌对应的正文；不存在或已过期时 ok 为 false。
func takeDocumentContent(token string) (string, bool) {
	docContents.mu.Lock()
	defer docContents.mu.Unlock()
	v, ok := docContents.m[token]
	delete(docContents.m, token)
	if !ok || time.Now().After(v.expires) {
		return "", false
	}
	return v.text, true
}

// serveDocumentContent 响应 nomodoc://localhost/<令牌>（Windows 为 http://nomodoc.localhost/<令牌>）：以 UTF-8 文本
// 返回暂存的正文，只能读取一次。
func serveDocumentContent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	text, ok := takeDocumentContent(strings.TrimPrefix(r.URL.Path, "/"))
	if !ok {
		applog.Warn("FileSystem", "文档正文令牌无效或已过期："+r.URL.Path)
		http.NotFound(w, r)
		return
	}
	applog.Debug("FileSystem", fmt.Sprintf("经协议交付文档正文：%d 字节", len(text)))
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	// 页面与协议不同源；令牌一次性且只在本进程内有效。
	h.Set("Access-Control-Allow-Origin", "*")
	_, _ = w.Write([]byte(text))
}

// readDocumentResult 是 read_markdown_file 的结果：正文超过阈值时 markdown 为空，由页面按 content_token 读取。
type readDocumentResult struct {
	docfs.DocumentPayload `json:",inline"`
	ContentToken          string `json:"content_token,omitzero"`
}

// deferLargeContent 把超过阈值的正文移出调用结果。
func deferLargeContent(p docfs.DocumentPayload) readDocumentResult {
	if len(p.Markdown) < docContentThreshold {
		return readDocumentResult{DocumentPayload: p}
	}
	token := stashDocumentContent(p.Markdown)
	p.Markdown = ""
	return readDocumentResult{DocumentPayload: p, ContentToken: token}
}

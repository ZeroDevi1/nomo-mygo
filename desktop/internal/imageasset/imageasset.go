// Package imageasset 是图片资源的后端：按文档位置导入本地图片（同内容复用）、解析与删除
// 文档相对路径，以及 PicGo Server / PicGo-Core 上传。WebView 外壳（desktop/image_assets.go 的
// 命令）与原生外壳共用，行为与原版一致。
package imageasset

import (
	"bufio"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Asset 是导入结果：MarkdownSrc 为写入文档的相对路径，Reused 表示复用了同内容的已有文件。
type Asset struct {
	MarkdownSrc  string `json:"markdown_src"`
	AbsolutePath string `json:"absolute_path"`
	Reused       bool   `json:"reused"`
}

// ResolvePayload 是解析图片地址的结果。
type ResolvePayload struct {
	Src          string  `json:"src"`
	DisplaySrc   string  `json:"display_src"`
	Exists       bool    `json:"exists"`
	AbsolutePath *string `json:"absolute_path"`
	Error        *string `json:"error"`
}

// DeletePayload 是删除图片资源的结果。
type DeletePayload struct {
	Src     string  `json:"src"`
	Removed bool    `json:"removed"`
	Skipped bool    `json:"skipped"`
	Error   *string `json:"error"`
}

// Import 把图片数据导入到文档对应的资源目录（strategy 为 copy-current-folder、copy-assets、
// copy-document-assets），同目录已有同内容图片时直接复用。文档未保存（无路径）时返回错误。
func Import(documentPath, documentFileName, strategy, fileName string, data []byte) (*Asset, error) {
	documentDir := filepath.Dir(documentPath)
	if documentPath == "" || documentDir == "" {
		return nil, errors.New("请先保存 Markdown 文件，再插入本地图片")
	}
	if len(data) == 0 {
		return nil, errors.New("图片内容为空，无法导入")
	}
	stem := documentStem(documentFileName, documentPath)
	directory, prefix, err := imageAssetDirectory(documentDir, stem, strategy)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, fmt.Errorf("创建图片资源目录失败：%w", err)
	}
	if existing := findImageWithSameContent(directory, data); existing != "" {
		return &Asset{MarkdownSrc: prefix + existing, AbsolutePath: filepath.Join(directory, existing), Reused: true}, nil
	}
	name := uniqueFileName(directory, sanitizeImageFileName(fileName))
	target := filepath.Join(directory, name)
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return nil, fmt.Errorf("写入图片资源失败：%w", err)
	}
	return &Asset{MarkdownSrc: prefix + name, AbsolutePath: target}, nil
}

// UploadPicgoCore 调用 PicGo-Core 命令上传图片，返回图片 URL；configPath 为空时用其默认配置。
func UploadPicgoCore(fileName string, data []byte, command, configPath string) (string, error) {
	if len(data) == 0 {
		return "", errors.New("图片内容为空，无法上传")
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return "", errors.New("PicGo-Core 命令不能为空")
	}
	tempPath, err := writeTempImageFile(fileName, data)
	if err != nil {
		return "", err
	}
	defer os.Remove(tempPath)
	arguments := []string{"upload", tempPath}
	if strings.TrimSpace(configPath) != "" {
		arguments = append(arguments, "--config", strings.TrimSpace(configPath))
	}
	stdout, stderr, err := runPicgoCore(command, arguments...)
	if err != nil {
		if message := strings.TrimSpace(stderr); message != "" {
			return "", fmt.Errorf("PicGo-Core 上传失败：%s", message)
		}
		return "", fmt.Errorf("PicGo-Core 上传失败：%w", err)
	}
	uploaded := extractFirstURL(stdout + "\n" + stderr)
	if uploaded == "" {
		return "", errors.New("PicGo-Core 上传成功但未返回图片 URL")
	}
	return uploaded, nil
}

// UploadPicgoServer 通过 PicGo Server（如 http://127.0.0.1:36677/upload）上传图片，返回图片 URL。
func UploadPicgoServer(fileName string, data []byte, serverURL string) (string, error) {
	if len(data) == 0 {
		return "", errors.New("图片内容为空，无法上传")
	}
	serverURL = strings.TrimSpace(serverURL)
	if serverURL == "" {
		return "", errors.New("PicGo Server 地址不能为空")
	}
	tempPath, err := writeTempImageFile(fileName, data)
	if err != nil {
		return "", err
	}
	defer os.Remove(tempPath)
	body, _ := json.Marshal(map[string][]string{"list": {tempPath}})
	response, err := postJSONToPicgoServer(serverURL, body)
	if err != nil {
		return "", err
	}
	uploaded := parsePicgoServerURL(response)
	if uploaded == "" {
		return "", errors.New("PicGo Server 未返回图片 URL")
	}
	return uploaded, nil
}

// TestConnection 测试上传方式是否可用（provider 为 picgo 或 picgo-core），成功时返回提示文案。
func TestConnection(provider, serverURL, command string) (string, error) {
	switch provider {
	case "picgo":
		if strings.TrimSpace(serverURL) == "" {
			return "", errors.New("PicGo Server 地址不能为空")
		}
		endpoint, err := parseHTTPEndpoint(strings.TrimSpace(serverURL))
		if err != nil {
			return "", err
		}
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(endpoint.host, strconv.Itoa(endpoint.port)), 5*time.Second)
		if err != nil {
			return "", fmt.Errorf("连接 PicGo Server 失败：%w", err)
		}
		conn.Close()
		return "PicGo Server 可以连接", nil
	case "picgo-core":
		if strings.TrimSpace(command) == "" {
			return "", errors.New("PicGo-Core 命令不能为空")
		}
		_, stderr, err := runPicgoCore(strings.TrimSpace(command), "--version")
		if err != nil {
			if message := strings.TrimSpace(stderr); message != "" {
				return "", fmt.Errorf("PicGo-Core 测试失败：%s", message)
			}
			return "", fmt.Errorf("PicGo-Core 测试失败：%w", err)
		}
		return "PicGo-Core 命令可以调用", nil
	}
	return "", errors.New("未知图片上传方式")
}

// imageAssetDirectory 按策略返回图片目录与 Markdown 相对前缀。
func imageAssetDirectory(documentDir, documentStem, strategy string) (string, string, error) {
	switch strategy {
	case "copy-current-folder":
		return documentDir, "./", nil
	case "copy-assets":
		return filepath.Join(documentDir, "assets"), "./assets/", nil
	case "copy-document-assets":
		folder := sanitizePathSegment(documentStem) + ".assets"
		return filepath.Join(documentDir, folder), "./" + folder + "/", nil
	}
	return "", "", fmt.Errorf("未知图片资源策略：%s", strategy)
}

func documentStem(documentFileName, documentPath string) string {
	for _, name := range []string{documentFileName, filepath.Base(documentPath)} {
		if stem := strings.TrimSuffix(name, filepath.Ext(name)); stem != "" && stem != "." {
			return stem
		}
	}
	return "document"
}

func sanitizeImageFileName(fileName string) string {
	name := strings.TrimSpace(fileName)
	if name == "" {
		name = "image.png"
	}
	extension := strings.TrimPrefix(filepath.Ext(name), ".")
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	if stem == "" {
		stem = "image"
	}
	safeStem := sanitizePathSegment(stem)
	if extension == "" {
		return safeStem
	}
	return safeStem + "." + sanitizeExtension(extension)
}

// sanitizePathSegment 把控制字符、空白与 Windows 非法字符替换为 -，合并连续 - 并去掉首尾 -。
func sanitizePathSegment(value string) string {
	var builder strings.Builder
	previousDash := false
	for _, r := range strings.TrimSpace(value) {
		if unicode.IsControl(r) || unicode.IsSpace(r) || strings.ContainsRune(`\/:*?"<>|`, r) {
			r = '-'
		}
		if r == '-' {
			if previousDash {
				continue
			}
			previousDash = true
		} else {
			previousDash = false
		}
		builder.WriteRune(r)
	}
	trimmed := strings.Trim(builder.String(), "-")
	if trimmed == "" {
		return "image"
	}
	return trimmed
}

func sanitizeExtension(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			builder.WriteRune(unicode.ToLower(r))
		}
	}
	if builder.Len() == 0 {
		return "png"
	}
	return builder.String()
}

// uniqueFileName 在目录中为文件名追加 -1、-2… 直到不冲突。
func uniqueFileName(directory, name string) string {
	extension := filepath.Ext(name)
	stem := strings.TrimSuffix(name, extension)
	candidate := name
	for suffix := 1; ; suffix++ {
		if _, err := os.Stat(filepath.Join(directory, candidate)); err != nil {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d%s", stem, suffix, extension)
	}
}

// findImageWithSameContent 返回目录中与 data 内容相同（长度与 SHA-256 均一致）的图片文件名。
func findImageWithSameContent(directory string, data []byte) string {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return ""
	}
	fingerprint := sha256.Sum256(data)
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		if !isSupportedImageFile(path) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(data)) {
			continue
		}
		existing, err := os.ReadFile(path)
		if err == nil && sha256.Sum256(existing) == fingerprint {
			return entry.Name()
		}
	}
	return ""
}

func isSupportedImageFile(path string) bool {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "png", "jpg", "jpeg", "gif", "webp", "bmp", "svg", "avif":
		return true
	}
	return false
}

func isRemoteImageSrc(src string) bool {
	value := strings.ToLower(strings.TrimSpace(src))
	for _, prefix := range []string{"http://", "https://", "data:", "blob:"} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

// decodePercentEncodedPath 还原 markdown-it 编码的非 ASCII 路径；解码结果不是 UTF-8 时保留原值。
func decodePercentEncodedPath(value string) string {
	decoded := make([]byte, 0, len(value))
	for i := 0; i < len(value); i++ {
		if value[i] == '%' && i+2 < len(value) {
			if high, ok := hexDigit(value[i+1]); ok {
				if low, ok := hexDigit(value[i+2]); ok {
					decoded = append(decoded, high<<4|low)
					i += 2
					continue
				}
			}
		}
		decoded = append(decoded, value[i])
	}
	if !utf8.ValidString(string(decoded)) {
		return value
	}
	return string(decoded)
}

func hexDigit(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	}
	return 0, false
}

// documentRelativePath 把文档相对的图片路径解析为绝对路径。
func documentRelativePath(documentDir, localSrc string) string {
	relative := strings.TrimPrefix(localSrc, "./")
	return filepath.Join(documentDir, filepath.FromSlash(relative))
}

func Resolve(documentPath *string, src string) *ResolvePayload {
	src = strings.TrimSpace(src)
	if isRemoteImageSrc(src) {
		return &ResolvePayload{Src: src, DisplaySrc: src, Exists: true}
	}
	local := decodePercentEncodedPath(src)
	absolute := local
	if !filepath.IsAbs(local) && documentPath != nil {
		if parent := filepath.Dir(*documentPath); parent != "" {
			absolute = documentRelativePath(parent, local)
		}
	}
	info, err := os.Stat(absolute)
	exists := err == nil && info.Mode().IsRegular()
	payload := &ResolvePayload{Src: src, DisplaySrc: absolute, Exists: exists, AbsolutePath: &absolute}
	if !exists {
		message := "图片文件不存在"
		payload.Error = &message
	}
	return payload
}

// deleteImageAsset 只删除文档目录内、文档相对引用的图片；远程、绝对路径与目录外路径一律跳过。
func Delete(documentPath *string, src string) *DeletePayload {
	src = strings.TrimSpace(src)
	local := decodePercentEncodedPath(src)
	skipped := func(message string) *DeletePayload {
		payload := &DeletePayload{Src: src, Skipped: true}
		if message != "" {
			payload.Error = &message
		}
		return payload
	}
	if src == "" || isRemoteImageSrc(src) || filepath.IsAbs(local) {
		return skipped("")
	}
	if documentPath == nil {
		return skipped("缺少文档路径，无法删除图片文件")
	}
	documentDir := filepath.Dir(*documentPath)
	if documentDir == "" {
		return skipped("缺少文档目录，无法删除图片文件")
	}
	absolute := documentRelativePath(documentDir, local)
	if !isSupportedImageFile(absolute) {
		return skipped("")
	}
	if _, err := os.Stat(absolute); err != nil {
		return &DeletePayload{Src: src}
	}
	if err := ensurePathInsideDir(documentDir, absolute); err != nil {
		return skipped(err.Error())
	}
	if err := os.Remove(absolute); err != nil {
		message := "删除图片文件失败：" + err.Error()
		return &DeletePayload{Src: src, Error: &message}
	}
	return &DeletePayload{Src: src, Removed: true}
}

func ensurePathInsideDir(dir, path string) error {
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("解析文档目录失败：%w", err)
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("解析图片路径失败：%w", err)
	}
	rel, err := filepath.Rel(realDir, realPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("图片路径不在当前文档目录内，已跳过删除")
	}
	return nil
}

func writeTempImageFile(fileName string, data []byte) (string, error) {
	safe := sanitizeImageFileName(fileName)
	target := filepath.Join(os.TempDir(), fmt.Sprintf("nomo-image-%d-%s", time.Now().Unix(), safe))
	for index := 1; ; index++ {
		if _, err := os.Stat(target); err != nil {
			break
		}
		target = filepath.Join(os.TempDir(), fmt.Sprintf("nomo-image-%d-%d-%s", time.Now().Unix(), index, safe))
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return "", fmt.Errorf("写入临时图片失败：%w", err)
	}
	return target, nil
}

// splitCommandLine 按空白拆分命令，双引号内的空白保留。
func splitCommandLine(command string) []string {
	var parts []string
	var current strings.Builder
	inQuotes := false
	for _, r := range command {
		switch {
		case r == '"':
			inQuotes = !inQuotes
		case unicode.IsSpace(r) && !inQuotes:
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

func runPicgoCore(command string, arguments ...string) (string, string, error) {
	parts := splitCommandLine(command)
	if len(parts) == 0 {
		parts = []string{strings.TrimSpace(command)}
	}
	cmd := exec.Command(parts[0], append(parts[1:], arguments...)...)
	hideConsoleWindow(cmd)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.String(), stderr.String(), fmt.Errorf("退出码：%d", exitErr.ExitCode())
		}
		return stdout.String(), stderr.String(), fmt.Errorf("调用 PicGo-Core 失败：%w", err)
	}
	return stdout.String(), stderr.String(), nil
}

// extractFirstURL 从命令输出或响应中取出第一个 http(s) URL。
func extractFirstURL(text string) string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(`"'[],{}`, r)
	})
	for _, field := range fields {
		if strings.HasPrefix(field, "http://") || strings.HasPrefix(field, "https://") {
			return strings.Trim(field, `"',]}`)
		}
	}
	return ""
}

type httpEndpoint struct {
	host string
	port int
	path string
}

// parseHTTPEndpoint 只接受 http:// 地址（PicGo Server 为本机服务），路径缺省为 /upload。
func parseHTTPEndpoint(raw string) (httpEndpoint, error) {
	rest, ok := strings.CutPrefix(raw, "http://")
	if !ok {
		return httpEndpoint{}, errors.New("PicGo Server 仅支持 http:// 本机地址")
	}
	hostPort, path, found := strings.Cut(rest, "/")
	if !found {
		path = "upload"
	}
	host, port := hostPort, 80
	if index := strings.LastIndexByte(hostPort, ':'); index >= 0 {
		parsed, err := strconv.Atoi(hostPort[index+1:])
		if err != nil || parsed < 0 || parsed > 65535 {
			return httpEndpoint{}, errors.New("PicGo Server 端口不可用")
		}
		host, port = hostPort[:index], parsed
	}
	if host == "" {
		return httpEndpoint{}, errors.New("PicGo Server 地址缺少主机名")
	}
	return httpEndpoint{host: host, port: port, path: "/" + strings.TrimLeft(path, "/")}, nil
}

func postJSONToPicgoServer(serverURL string, body []byte) (string, error) {
	endpoint, err := parseHTTPEndpoint(serverURL)
	if err != nil {
		return "", err
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(endpoint.host, strconv.Itoa(endpoint.port)), 10*time.Second)
	if err != nil {
		return "", fmt.Errorf("连接 PicGo Server 失败：%w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	request := fmt.Sprintf("POST %s HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", endpoint.path, endpoint.host, len(body))
	if _, err := io.WriteString(conn, request); err != nil {
		return "", fmt.Errorf("请求 PicGo Server 失败：%w", err)
	}
	if _, err := conn.Write(body); err != nil {
		return "", fmt.Errorf("请求 PicGo Server 失败：%w", err)
	}
	response, err := io.ReadAll(bufio.NewReader(conn))
	if err != nil {
		return "", fmt.Errorf("读取 PicGo Server 响应失败：%w", err)
	}
	text := string(response)
	statusLine, _, _ := strings.Cut(text, "\n")
	if !strings.Contains(statusLine, " 200 ") {
		return "", fmt.Errorf("PicGo Server 返回异常状态：%s", strings.TrimSpace(statusLine))
	}
	_, body2, _ := strings.Cut(text, "\r\n\r\n")
	return body2, nil
}

func parsePicgoServerURL(body string) string {
	var value map[string]any
	if json.Unmarshal([]byte(body), &value) == nil {
		if success, ok := value["success"].(bool); ok && !success {
			return ""
		}
		if results, ok := value["result"].([]any); ok {
			for _, result := range results {
				if text, ok := result.(string); ok {
					return text
				}
			}
		}
		if text, ok := value["url"].(string); ok {
			return text
		}
	}
	return extractFirstURL(body)
}

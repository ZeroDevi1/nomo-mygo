package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"nomo/desktop/internal/applog"
)

// 软件更新检查：从 GitHub Release 读取最新版本，直连失败（可重试错误）时经 gh-proxy 重试。Windows 安装版可在应用内
// 下载并安装（update_install.go），其他形态提示新版本并打开发布页；Microsoft Store 包由商店管理，开发构建不检查。

const (
	githubLatestReleaseAPI    = "https://api.github.com/repos/nomo-md/nomo/releases/latest"
	githubProxyPrefix         = "https://gh-proxy.com/"
	updateConnectTimeout      = 5 * time.Second
	updateSmallRequestTimeout = 8 * time.Second
	updateReadTimeout         = 20 * time.Second
)

type installationKind string

const (
	installationInstaller   installationKind = "installer"
	installationPortable    installationKind = "portable"
	installationStore       installationKind = "store"
	installationManual      installationKind = "manual" // macOS / Linux：检查并提示，手动升级
	installationUnsupported installationKind = "unsupported"
)

// softwareUpdateCheck 是一次检查的结果：Supported 为当前安装形态是否由应用检查更新（Store 包与不支持的环境为 false），
// Version 为最新发布的版本号（Supported 为 false 时为空）。
type softwareUpdateCheck struct {
	Supported        bool
	Available        bool
	CurrentVersion   string
	InstallationKind installationKind
	Version          string
	Notes            string // 新版本的 Release 说明（Markdown）
}

// appVersion 返回构建时链接的应用版本；go run 等未链接版本的开发构建返回 0.0.0。
func appVersion() string {
	if version := mygo.App.Version(); version != "" {
		return version
	}
	return "0.0.0"
}

// checkSoftwareUpdate 检查是否有新版本；网络请求可能耗时数秒，在后台调用。
func checkSoftwareUpdate() (*softwareUpdateCheck, error) {
	kind, err := currentInstallationKind()
	if err != nil {
		return nil, err
	}
	current := appVersion()
	result := &softwareUpdateCheck{CurrentVersion: current, InstallationKind: kind}
	if kind == installationStore || kind == installationUnsupported {
		applog.Info("Update", "当前安装形态不由应用检查更新："+string(kind))
		return result, nil
	}
	applog.Info("Update", "开始检查软件更新，当前版本："+current)
	release, _, err := fetchLatestGithubRelease()
	if err != nil {
		return nil, err
	}
	version := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(release.TagName), "v"))
	if version == "" {
		return nil, errors.New("GitHub Release 缺少有效版本号。")
	}
	newer, err := isReleaseNewer(current, version)
	if err != nil {
		return nil, err
	}
	result.Supported, result.Available, result.Version, result.Notes = true, newer, version, release.Body
	if newer {
		rememberUpdateCandidate(release, version, kind)
	}
	return result, nil
}

type githubRelease struct {
	TagName string               `json:"tag_name"`
	Body    string               `json:"body"`
	Assets  []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
}

type requestFailure struct {
	message   string
	retryable bool
}

func (f *requestFailure) Error() string { return f.message }

func isRetryableStatus(status int) bool {
	return status == http.StatusForbidden || status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

var updateHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSHandshakeTimeout:   updateConnectTimeout,
		ResponseHeaderTimeout: updateReadTimeout,
		IdleConnTimeout:       30 * time.Second,
	},
}

func updateRequestURL(original string, proxy bool) (string, error) {
	if !proxy {
		return original, nil
	}
	if proxied, ok := githubProxyURL(original); ok {
		return proxied, nil
	}
	return "", &requestFailure{message: "该更新地址不属于允许通过 gh-proxy 访问的 Nomo GitHub 地址。"}
}

// githubProxyURL 只允许 Nomo 仓库（含迁移前地址）的 Release API 与下载地址经代理访问。
func githubProxyURL(original string) (string, bool) {
	parsed, err := url.Parse(original)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	allowed := parsed.Host == "api.github.com" && (parsed.Path == "/repos/nomo-md/nomo/releases/latest" || parsed.Path == "/repos/LIXianSenQwQ/nomo/releases/latest" ||
		strings.HasPrefix(parsed.Path, "/repos/nomo-md/nomo/releases/tags/")) ||
		parsed.Host == "github.com" && (strings.HasPrefix(parsed.Path, "/nomo-md/nomo/releases/download/") || strings.HasPrefix(parsed.Path, "/LIXianSenQwQ/nomo/releases/download/"))
	if !allowed {
		return "", false
	}
	return githubProxyPrefix + original, true
}

func sendUpdateRequest(ctx context.Context, original string, proxy bool, context string) (*http.Response, error) {
	requestURL, err := updateRequestURL(original, proxy)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, &requestFailure{message: context + "：" + err.Error()}
	}
	request.Header.Set("User-Agent", "Nomo software updater")
	response, err := updateHTTPClient.Do(request)
	if err != nil {
		return nil, &requestFailure{message: context + "：" + err.Error(), retryable: true}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		return nil, &requestFailure{message: fmt.Sprintf("%s：HTTP %s", context, response.Status), retryable: isRetryableStatus(response.StatusCode)}
	}
	return response, nil
}

func fetchSmall(original string, proxy bool, context string) ([]byte, error) {
	ctx, cancel := contextWithTimeout(updateSmallRequestTimeout)
	defer cancel()
	response, err := sendUpdateRequest(ctx, original, proxy, context)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, &requestFailure{message: context + "：" + err.Error(), retryable: true}
	}
	return data, nil
}

func contextWithTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

// fetchLatestGithubRelease 返回最新 Release 与实际使用的来源（proxy 为 true 表示 gh-proxy）。
func fetchLatestGithubRelease() (*githubRelease, bool, error) {
	return fetchGithubRelease(githubLatestReleaseAPI)
}

// fetchGithubRelease 读取 api 指向的 Release，直连失败（可重试错误）时经 gh-proxy 重试。
func fetchGithubRelease(api string) (*githubRelease, bool, error) {
	fetch := func(proxy bool) (*githubRelease, error) {
		data, err := fetchSmall(api, proxy, "请求 GitHub Release 失败")
		if err != nil {
			return nil, err
		}
		var release githubRelease
		if err := json.Unmarshal(data, &release); err != nil {
			return nil, &requestFailure{message: "解析 GitHub Release 更新信息失败：" + err.Error()}
		}
		return &release, nil
	}
	release, err := fetch(false)
	if err == nil {
		return release, false, nil
	}
	var failure *requestFailure
	if errors.As(err, &failure) && failure.retryable {
		applog.Warn("Update", "GitHub 直连 Release 请求失败，将通过 gh-proxy 重试："+err.Error())
		release, proxyErr := fetch(true)
		if proxyErr == nil {
			return release, true, nil
		}
		message := fmt.Sprintf("检查 GitHub Release 更新失败：GitHub 直连失败（%s）；gh-proxy 失败（%s）", err, proxyErr)
		applog.Error("Update", message)
		return nil, false, errors.New(message)
	}
	message := "检查 GitHub Release 更新失败：" + err.Error()
	applog.Error("Update", message)
	return nil, false, errors.New(message)
}

func isReleaseNewer(current, release string) (bool, error) {
	currentVersion, err := parseSemver(current)
	if err != nil {
		return false, fmt.Errorf("解析当前版本号失败：%w", err)
	}
	releaseVersion, err := parseSemver(release)
	if err != nil {
		return false, fmt.Errorf("解析 Release 版本号失败：%w", err)
	}
	return compareSemver(releaseVersion, currentVersion) > 0, nil
}

type semver struct {
	core       [3]uint64
	prerelease []string
}

func parseSemver(value string) (semver, error) {
	var version semver
	main, _, _ := strings.Cut(value, "+")
	main, prerelease, hasPrerelease := strings.Cut(main, "-")
	parts := strings.Split(main, ".")
	if len(parts) != 3 {
		return version, fmt.Errorf("无效的版本号：%s", value)
	}
	for i, part := range parts {
		number, err := strconv.ParseUint(part, 10, 64)
		if err != nil || part == "" || len(part) > 1 && part[0] == '0' {
			return version, fmt.Errorf("无效的版本号：%s", value)
		}
		version.core[i] = number
	}
	if hasPrerelease {
		if prerelease == "" {
			return version, fmt.Errorf("无效的版本号：%s", value)
		}
		version.prerelease = strings.Split(prerelease, ".")
	}
	return version, nil
}

func compareSemver(a, b semver) int {
	for i := range a.core {
		if a.core[i] != b.core[i] {
			if a.core[i] > b.core[i] {
				return 1
			}
			return -1
		}
	}
	switch {
	case len(a.prerelease) == 0 && len(b.prerelease) == 0:
		return 0
	case len(a.prerelease) == 0:
		return 1
	case len(b.prerelease) == 0:
		return -1
	}
	for i := 0; i < len(a.prerelease) && i < len(b.prerelease); i++ {
		left, right := a.prerelease[i], b.prerelease[i]
		leftNumber, leftErr := strconv.ParseUint(left, 10, 64)
		rightNumber, rightErr := strconv.ParseUint(right, 10, 64)
		switch {
		case leftErr == nil && rightErr == nil:
			if leftNumber != rightNumber {
				if leftNumber > rightNumber {
					return 1
				}
				return -1
			}
		case leftErr == nil:
			return -1
		case rightErr == nil:
			return 1
		case left != right:
			return strings.Compare(left, right)
		}
	}
	return len(a.prerelease) - len(b.prerelease)
}

// ---- 安装后更新日志 ----

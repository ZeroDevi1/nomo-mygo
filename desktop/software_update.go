package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"nomo"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/config"
)

// 软件更新：仅 Windows 安装版支持应用内下载与安装；Microsoft Store 包由商店管理；其他环境只报告不支持。
// Release 来自 GitHub，直连失败（可重试错误）时经 gh-proxy 重试；安装包以 checksums.md5 校验。
// 另外维护“安装后更新日志”：版本升级后由一个文档窗口领取并展示一次。

const (
	githubLatestReleaseAPI         = "https://api.github.com/repos/nomo-md/nomo/releases/latest"
	githubProxyPrefix              = "https://gh-proxy.com/"
	updateConnectTimeout           = 5 * time.Second
	updateSmallRequestTimeout      = 8 * time.Second
	updateDownloadReadTimeout      = 20 * time.Second
	checksumsAssetName             = "checksums.md5"
	downloadProgressEvent          = "nomo://software-update-download-progress"
	updateStateEvent               = "nomo://software-update-state"
	cachedUpdateInfoFile           = "update-info.json"
	installedReleaseNotesEvent     = "nomo://installed-release-notes-available"
	installedReleaseNotesHistoryKy = "installedReleaseNotesHistory"
)

type installationKind string

const (
	installationInstaller   installationKind = "installer"
	installationPortable    installationKind = "portable"
	installationStore       installationKind = "store"
	installationUnsupported installationKind = "unsupported"
)

type updateAssetKind string

const (
	assetWindowsInstaller updateAssetKind = "windowsInstaller"
	assetWindowsPortable  updateAssetKind = "windowsPortable"
)

type softwareUpdateCandidate struct {
	Version     string          `json:"version"`
	Date        *string         `json:"date"`
	Body        *string         `json:"body"`
	AssetKind   updateAssetKind `json:"assetKind"`
	AssetName   string          `json:"assetName"`
	AssetSize   *uint64         `json:"assetSize"`
	DownloadURL string          `json:"downloadUrl"`
	MD5         string          `json:"md5"`
}

type softwareUpdateCheckPayload struct {
	Supported        bool                     `json:"supported"`
	Available        bool                     `json:"available"`
	CurrentVersion   string                   `json:"currentVersion"`
	InstallationKind installationKind         `json:"installationKind"`
	Version          *string                  `json:"version"`
	Date             *string                  `json:"date"`
	Body             *string                  `json:"body"`
	Candidate        *softwareUpdateCandidate `json:"candidate"`
	StoreProductID   *string                  `json:"storeProductId"`
}

type downloadedSoftwareUpdate struct {
	Version         string `json:"version"`
	AssetName       string `json:"assetName"`
	FilePath        string `json:"filePath"`
	MD5             string `json:"md5"`
	DownloadedBytes uint64 `json:"downloadedBytes"`
}

type downloadProgress struct {
	RequestID       string  `json:"requestId"`
	DownloadedBytes uint64  `json:"downloadedBytes"`
	TotalBytes      *uint64 `json:"totalBytes"`
	Percent         *uint8  `json:"percent"`
}

type softwareUpdateSnapshot struct {
	Status            string                    `json:"status"`
	CurrentVersion    string                    `json:"currentVersion"`
	InstallationKind  installationKind          `json:"installationKind"`
	Version           *string                   `json:"version"`
	Date              *string                   `json:"date"`
	Body              *string                   `json:"body"`
	Candidate         *softwareUpdateCandidate  `json:"candidate"`
	DownloadedUpdate  *downloadedSoftwareUpdate `json:"downloadedUpdate"`
	Progress          *downloadProgress         `json:"progress"`
	Error             *string                   `json:"error"`
	NoticeWindowLabel *string                   `json:"noticeWindowLabel"`
	StoreProductID    *string                   `json:"storeProductId"`
}

var updateState struct {
	mu              sync.Mutex
	snapshot        softwareUpdateSnapshot
	checkInProgress bool
}

// appVersion 返回构建时链接的应用版本；go run 等未链接版本的开发构建返回 0.0.0。
func appVersion() string {
	if version := mygo.App.Version(); version != "" {
		return version
	}
	return "0.0.0"
}

func init() {
	updateState.snapshot = softwareUpdateSnapshot{Status: "idle", InstallationKind: installationUnsupported}
	register("is_windows_installer_installation", func(_ *commandCall, _ noArgs) (bool, error) {
		return isCurrentWindowsInstallerInstallation()
	})
	register("get_cached_software_update", func(_ *commandCall, _ noArgs) (*downloadedSoftwareUpdate, error) {
		return getCachedSoftwareUpdate()
	})
	register("get_software_update_state", func(_ *commandCall, _ noArgs) (softwareUpdateSnapshot, error) {
		return getSoftwareUpdateState()
	})
	register("check_software_update", func(call *commandCall, args struct {
		Startup *bool `json:"startup"`
	}) (*softwareUpdateCheckPayload, error) {
		label := ""
		if call.window != nil {
			label = call.window.label
		}
		return checkSoftwareUpdate(label, args.Startup != nil && *args.Startup)
	})
	register("download_software_update", func(_ *commandCall, args struct {
		Candidate softwareUpdateCandidate `json:"candidate"`
		RequestID string                  `json:"requestId"`
	}) (*downloadedSoftwareUpdate, error) {
		return downloadSoftwareUpdate(args.Candidate, args.RequestID)
	})
	register("install_software_update", func(_ *commandCall, args struct {
		DownloadedUpdate downloadedSoftwareUpdate `json:"downloadedUpdate"`
	}) (any, error) {
		updateSharedState(func(s *softwareUpdateSnapshot) { s.Status, s.Error = "installing", nil })
		if err := installSoftwareUpdate(args.DownloadedUpdate); err != nil {
			message := err.Error()
			updateSharedState(func(s *softwareUpdateSnapshot) { s.Status, s.Error = "error", &message })
			return nil, err
		}
		return nil, nil
	})
	register("claim_installed_release_notes", func(call *commandCall, args struct {
		Release *bool `json:"release"`
	}) (*installedReleaseNotes, error) {
		if call.window == nil || !isDocumentWindowLabel(call.window.label) {
			return nil, nil
		}
		if args.Release != nil && *args.Release {
			releaseInstalledReleaseNotes(call.window.label)
			return nil, nil
		}
		return claimInstalledReleaseNotes(call.window.label)
	})
	register("confirm_installed_release_notes_shown", func(call *commandCall, args struct {
		Version string `json:"version"`
	}) (any, error) {
		label := ""
		if call.window != nil {
			label = call.window.label
		}
		return nil, confirmInstalledReleaseNotesShown(label, args.Version)
	})
}

func getCachedSoftwareUpdate() (*downloadedSoftwareUpdate, error) {
	kind, err := currentInstallationKind()
	if err != nil || kind == installationStore {
		return nil, err
	}
	dir, err := softwareUpdateCacheDir()
	if err != nil {
		return nil, err
	}
	info, err := readCachedUpdateInfo(dir)
	if err != nil || info == nil {
		return info, err
	}
	if stat, err := os.Stat(info.FilePath); err != nil || !stat.Mode().IsRegular() {
		applog.Info("Update", "缓存信息存在但安装包文件已丢失，忽略")
		return nil, nil
	}
	applog.Info("Update", "发现已下载的更新："+info.Version)
	return info, nil
}

func getSoftwareUpdateState() (softwareUpdateSnapshot, error) {
	kind, err := currentInstallationKind()
	if err != nil {
		return softwareUpdateSnapshot{}, err
	}
	current := appVersion()
	var cached *downloadedSoftwareUpdate
	if kind == installationInstaller {
		if cached, err = getCachedSoftwareUpdate(); err != nil {
			return softwareUpdateSnapshot{}, err
		}
		if cached != nil {
			if newer, err := isReleaseNewer(current, cached.Version); err != nil || !newer {
				cached = nil
			}
		}
	}
	return updateSharedState(func(s *softwareUpdateSnapshot) {
		s.InstallationKind = kind
		s.StoreProductID = nil
		switch {
		case kind == installationStore:
			s.StoreProductID = storeProductID()
			body := currentReleaseNotes()
			s.Status, s.CurrentVersion, s.Version, s.Body = "managed", current, &current, &body
			s.Candidate, s.DownloadedUpdate, s.Progress, s.Error = nil, nil, nil, nil
		case cached != nil:
			version := cached.Version
			s.Version, s.DownloadedUpdate, s.Status = &version, cached, "downloaded"
		case kind == installationUnsupported && s.Status == "idle":
			s.Status = "unsupported"
		}
	}), nil
}

func checkSoftwareUpdate(callerLabel string, startup bool) (*softwareUpdateCheckPayload, error) {
	kind, err := currentInstallationKind()
	if err != nil {
		return nil, err
	}
	updateState.mu.Lock()
	if updateState.checkInProgress {
		updateState.mu.Unlock()
		return nil, errors.New("软件更新检查正在进行中。")
	}
	updateState.checkInProgress = true
	previous := updateState.snapshot
	updateState.mu.Unlock()
	defer func() {
		updateState.mu.Lock()
		updateState.checkInProgress = false
		updateState.mu.Unlock()
	}()
	var notice *string
	if startup {
		label := lastActiveDocumentWindowLabel()
		if label == "" {
			label = callerLabel
		}
		notice = &label
	}
	updateSharedState(func(s *softwareUpdateSnapshot) {
		s.Status, s.InstallationKind, s.Error, s.Progress, s.NoticeWindowLabel = "checking", kind, nil, nil, notice
	})
	payload, err := performSoftwareUpdateCheck(kind)
	if err != nil {
		if startup {
			updateSharedState(func(s *softwareUpdateSnapshot) { *s = previous })
		} else {
			message := err.Error()
			updateSharedState(func(s *softwareUpdateSnapshot) { s.Status, s.Error = "error", &message })
		}
		return nil, err
	}
	var cached *downloadedSoftwareUpdate
	if payload.Available && payload.InstallationKind == installationInstaller {
		if candidate, err := getCachedSoftwareUpdate(); err == nil && candidate != nil && payload.Version != nil && candidate.Version == *payload.Version {
			cached = candidate
		}
	}
	updateSharedState(func(s *softwareUpdateSnapshot) {
		s.CurrentVersion, s.InstallationKind = payload.CurrentVersion, payload.InstallationKind
		s.Version, s.Date, s.Body, s.Candidate = payload.Version, payload.Date, payload.Body, payload.Candidate
		s.StoreProductID, s.DownloadedUpdate, s.Error = payload.StoreProductID, cached, nil
		switch {
		case payload.InstallationKind == installationStore:
			s.Status = "managed"
		case !payload.Supported:
			s.Status = "unsupported"
		case cached != nil:
			s.Status = "downloaded"
		case payload.Available:
			s.Status = "available"
		default:
			s.Status = "upToDate"
		}
	})
	return payload, nil
}

type githubReleaseAsset struct {
	Name               string  `json:"name"`
	Size               *uint64 `json:"size"`
	BrowserDownloadURL string  `json:"browser_download_url"`
}

type githubRelease struct {
	TagName     string               `json:"tag_name"`
	PublishedAt *string              `json:"published_at"`
	CreatedAt   *string              `json:"created_at"`
	Body        *string              `json:"body"`
	Assets      []githubReleaseAsset `json:"assets"`
}

func performSoftwareUpdateCheck(kind installationKind) (*softwareUpdateCheckPayload, error) {
	current := appVersion()
	applog.Info("Update", "开始检查软件更新，当前版本："+current)
	started := time.Now()
	// Microsoft Store 包由商店管理更新，不访问 GitHub。
	if kind == installationStore {
		body := currentReleaseNotes()
		return &softwareUpdateCheckPayload{Supported: true, CurrentVersion: current, InstallationKind: kind, Version: &current, Body: &body, StoreProductID: storeProductID()}, nil
	}
	if kind == installationUnsupported {
		applog.Info("Update", "当前环境不支持软件更新，跳过远程检查")
		return &softwareUpdateCheckPayload{CurrentVersion: current, InstallationKind: kind}, nil
	}
	release, source, err := fetchLatestGithubRelease()
	if err != nil {
		return nil, err
	}
	version := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(release.TagName), "v"))
	if version == "" {
		return nil, errors.New("GitHub Release 缺少有效版本号。")
	}
	date := release.PublishedAt
	if date == nil {
		date = release.CreatedAt
	}
	newer, err := isReleaseNewer(current, version)
	if err != nil {
		return nil, err
	}
	if !newer {
		applog.Info("Update", "当前已是最新版本，无需更新")
		return &softwareUpdateCheckPayload{Supported: true, CurrentVersion: current, InstallationKind: kind, Version: &version, Date: date, Body: release.Body}, nil
	}
	assetKind := assetWindowsInstaller
	if kind == installationPortable {
		assetKind = assetWindowsPortable
	}
	expected := expectedAssetName(assetKind, version)
	asset := findAsset(release.Assets, expected)
	if asset == nil {
		applog.Error("Update", "缺少更新资产："+expected)
		return nil, fmt.Errorf("GitHub Release 缺少 Windows 更新资产：%s", expected)
	}
	checksumsAsset := findAsset(release.Assets, checksumsAssetName)
	if checksumsAsset == nil {
		return nil, errors.New("GitHub Release 缺少 MD5 校验清单 checksums.md5")
	}
	checksums, err := fetchGithubChecksums(checksumsAsset.BrowserDownloadURL, source)
	if err != nil {
		return nil, err
	}
	expectedMD5, ok := findMD5ForFile(checksums, asset.Name)
	if !ok {
		return nil, fmt.Errorf("MD5 校验清单缺少更新资产条目：%s", asset.Name)
	}
	applog.Info("Update", fmt.Sprintf("更新检查完成，新版本 %s 可用，总耗时：%v", version, time.Since(started)))
	candidate := &softwareUpdateCandidate{
		Version: version, Date: date, Body: release.Body, AssetKind: assetKind, AssetName: asset.Name,
		AssetSize: asset.Size, DownloadURL: asset.BrowserDownloadURL, MD5: expectedMD5,
	}
	return &softwareUpdateCheckPayload{Supported: true, Available: true, CurrentVersion: current, InstallationKind: kind, Version: &version, Date: date, Body: release.Body, Candidate: candidate}, nil
}

// requestFailure 区分可重试（网络、403/408/429/5xx）与终止性错误，决定是否改走 gh-proxy。
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
		ResponseHeaderTimeout: updateDownloadReadTimeout,
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
	allowed := parsed.Host == "api.github.com" && (parsed.Path == "/repos/nomo-md/nomo/releases/latest" || parsed.Path == "/repos/LIXianSenQwQ/nomo/releases/latest") ||
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
	fetch := func(proxy bool) (*githubRelease, error) {
		data, err := fetchSmall(githubLatestReleaseAPI, proxy, "请求 GitHub Release 失败")
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

func fetchGithubChecksums(original string, proxy bool) (string, error) {
	data, err := fetchSmall(original, proxy, "下载 MD5 校验清单失败")
	if err == nil {
		return string(data), nil
	}
	var failure *requestFailure
	if !proxy && errors.As(err, &failure) && failure.retryable {
		data, proxyErr := fetchSmall(original, true, "下载 MD5 校验清单失败")
		if proxyErr == nil {
			return string(data), nil
		}
		return "", fmt.Errorf("下载 MD5 校验清单失败：GitHub 直连失败（%s）；gh-proxy 失败（%s）", err, proxyErr)
	}
	label := "GitHub 直连"
	if proxy {
		label = "gh-proxy"
	}
	return "", fmt.Errorf("通过 %s下载 MD5 校验清单失败：%s", label, err)
}

func downloadSoftwareUpdate(candidate softwareUpdateCandidate, requestID string) (*downloadedSoftwareUpdate, error) {
	version := candidate.Version
	zero := uint8(0)
	updateSharedState(func(s *softwareUpdateSnapshot) {
		s.Status, s.Version, s.Candidate, s.Error = "downloading", &version, &candidate, nil
		s.Progress = &downloadProgress{RequestID: requestID, TotalBytes: candidate.AssetSize, Percent: &zero}
	})
	downloaded, err := downloadSoftwareUpdateInner(candidate, requestID)
	if err != nil {
		message := err.Error()
		updateSharedState(func(s *softwareUpdateSnapshot) { s.Status, s.Progress, s.Error = "error", nil, &message })
		return nil, err
	}
	updateSharedState(func(s *softwareUpdateSnapshot) {
		s.Status, s.DownloadedUpdate, s.Progress, s.Error = "downloaded", downloaded, nil, nil
	})
	return downloaded, nil
}

func downloadSoftwareUpdateInner(candidate softwareUpdateCandidate, requestID string) (*downloadedSoftwareUpdate, error) {
	kind, err := currentInstallationKind()
	if err != nil {
		return nil, err
	}
	if candidate.AssetKind != assetWindowsInstaller || kind != installationInstaller {
		return nil, errors.New("当前环境不支持自动更新：仅 Windows 安装版支持应用内更新。")
	}
	if !isValidMD5(candidate.MD5) {
		return nil, errors.New("Release 记录的 MD5 格式无效。")
	}
	dir, err := softwareUpdateCacheDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建更新缓存目录失败：%w", err)
	}
	if filepath.Base(candidate.AssetName) != candidate.AssetName {
		return nil, errors.New("更新安装包文件名无效。")
	}
	target := filepath.Join(dir, candidate.AssetName)
	temp := target + ".download"
	os.Remove(temp)
	downloadedBytes, actualMD5, err := downloadUpdateFrom(candidate, requestID, temp, false)
	if err != nil {
		var failure *requestFailure
		if !errors.As(err, &failure) || !failure.retryable {
			os.Remove(temp)
			return nil, err
		}
		applog.Warn("Update", "GitHub 直连下载失败，将通过 gh-proxy 重试："+err.Error())
		os.Remove(temp)
		emitDownloadProgress(requestID, 0, candidate.AssetSize)
		var proxyErr error
		downloadedBytes, actualMD5, proxyErr = downloadUpdateFrom(candidate, requestID, temp, true)
		if proxyErr != nil {
			os.Remove(temp)
			message := fmt.Sprintf("下载更新安装包失败：GitHub 直连失败（%s）；gh-proxy 失败（%s）", err, proxyErr)
			applog.Error("Update", message)
			return nil, errors.New(message)
		}
	}
	if err := os.Rename(temp, target); err != nil {
		return nil, fmt.Errorf("保存更新安装包失败：%w", err)
	}
	downloaded := &downloadedSoftwareUpdate{Version: candidate.Version, AssetName: candidate.AssetName, FilePath: target, MD5: actualMD5, DownloadedBytes: downloadedBytes}
	// 持久化下载信息，重新打开设置页时仍能识别已下载的更新。
	data, _ := json.Marshal(downloaded)
	if err := os.WriteFile(filepath.Join(dir, cachedUpdateInfoFile), data, 0o644); err != nil {
		return nil, fmt.Errorf("写入更新缓存信息失败：%w", err)
	}
	return downloaded, nil
}

// downloadUpdateFrom 流式下载安装包并边下边算 MD5；读超时由 Transport 的响应头超时与每块读取期限约束。
func downloadUpdateFrom(candidate softwareUpdateCandidate, requestID, temp string, proxy bool) (uint64, string, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	response, err := sendUpdateRequest(ctx, candidate.DownloadURL, proxy, "发起更新安装包下载请求失败")
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	total := candidate.AssetSize
	if response.ContentLength > 0 {
		size := uint64(response.ContentLength)
		total = &size
	}
	file, err := os.Create(temp)
	if err != nil {
		return 0, "", &requestFailure{message: "创建更新安装包缓存失败：" + err.Error()}
	}
	defer file.Close()
	hasher := md5.New()
	var downloaded uint64
	emitDownloadProgress(requestID, 0, total)
	buffer := make([]byte, 256*1024)
	for {
		// 每块读取都有独立期限，网络停滞超过 20 秒即视为可重试失败。
		timer := time.AfterFunc(updateDownloadReadTimeout, cancel)
		n, readErr := response.Body.Read(buffer)
		timer.Stop()
		if n > 0 {
			if _, err := file.Write(buffer[:n]); err != nil {
				return 0, "", &requestFailure{message: "写入更新安装包缓存失败：" + err.Error()}
			}
			hasher.Write(buffer[:n])
			downloaded += uint64(n)
			emitDownloadProgress(requestID, downloaded, total)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return 0, "", &requestFailure{message: "读取更新安装包下载内容失败：" + readErr.Error(), retryable: true}
		}
	}
	if err := file.Sync(); err != nil {
		return 0, "", &requestFailure{message: "刷新更新安装包缓存失败：" + err.Error()}
	}
	actual := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(actual, candidate.MD5) {
		return 0, "", &requestFailure{message: fmt.Sprintf("更新包校验失败：Release 记录的 MD5 为 %s，实际下载文件 MD5 为 %s。", candidate.MD5, actual)}
	}
	return downloaded, actual, nil
}

func installSoftwareUpdate(update downloadedSoftwareUpdate) error {
	applog.Info("Update", "开始安装更新："+update.Version)
	installer, err := isCurrentWindowsInstallerInstallation()
	if err != nil {
		return err
	}
	if !installer {
		return errors.New("当前环境不支持自动更新：仅 Windows 安装版支持应用内更新。")
	}
	dir, err := softwareUpdateCacheDir()
	if err != nil {
		return err
	}
	path, err := validateDownloadedInstallerPath(update.FilePath, dir, update.AssetName)
	if err != nil {
		return err
	}
	actual, err := fileMD5(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, update.MD5) {
		return fmt.Errorf("更新包校验失败：Release 记录的 MD5 为 %s，实际安装文件 MD5 为 %s。", update.MD5, actual)
	}
	// 启动安装前清除缓存信息，避免用户取消安装后仍显示“已下载”。
	os.Remove(filepath.Join(dir, cachedUpdateInfoFile))
	return launchInstallerAndExit(path)
}

func validateDownloadedInstallerPath(filePath, updateDir, assetName string) (string, error) {
	canonicalFile, err := filepath.EvalSymlinks(filePath)
	if err != nil {
		return "", fmt.Errorf("读取更新安装包路径失败：%w", err)
	}
	canonicalDir, err := filepath.EvalSymlinks(updateDir)
	if err != nil {
		return "", fmt.Errorf("读取更新缓存目录失败：%w", err)
	}
	if filepath.Dir(canonicalFile) != canonicalDir || filepath.Base(canonicalFile) != assetName {
		return "", errors.New("更新安装包路径无效。")
	}
	return canonicalFile, nil
}

func fileMD5(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("读取更新安装包失败：%w", err)
	}
	defer file.Close()
	hasher := md5.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", fmt.Errorf("读取更新安装包失败：%w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func expectedAssetName(kind updateAssetKind, version string) string {
	if kind == assetWindowsPortable {
		return "Nomo_" + version + "_x64.zip"
	}
	return "Nomo_" + version + "_x64-setup.exe"
}

func findAsset(assets []githubReleaseAsset, name string) *githubReleaseAsset {
	for i := range assets {
		if assets[i].Name == name {
			return &assets[i]
		}
	}
	return nil
}

// findMD5ForFile 在 “<md5> <文件名>” 格式的校验清单中查找文件的 MD5（小写）。
func findMD5ForFile(checksums, fileName string) (string, bool) {
	for _, line := range strings.Split(checksums, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		hash, rest, ok := strings.Cut(trimmed, " ")
		if !ok {
			hash, rest, ok = strings.Cut(trimmed, "\t")
		}
		if ok && isValidMD5(strings.TrimSpace(hash)) && strings.TrimSpace(rest) == fileName {
			return strings.ToLower(strings.TrimSpace(hash)), true
		}
	}
	return "", false
}

func isValidMD5(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func softwareUpdateCacheDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("获取更新缓存目录失败：%w", err)
	}
	return filepath.Join(cache, appIdentifier, "updates"), nil
}

func readCachedUpdateInfo(dir string) (*downloadedSoftwareUpdate, error) {
	data, err := os.ReadFile(filepath.Join(dir, cachedUpdateInfoFile))
	if err != nil {
		return nil, nil
	}
	var info downloadedSoftwareUpdate
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("解析更新缓存信息失败：%w", err)
	}
	return &info, nil
}

func emitDownloadProgress(requestID string, downloaded uint64, total *uint64) {
	progress := &downloadProgress{RequestID: requestID, DownloadedBytes: downloaded, TotalBytes: total}
	if total != nil && *total > 0 {
		percent := uint8(min(downloaded*100 / *total, 100))
		progress.Percent = &percent
	}
	broadcast(downloadProgressEvent, progress)
	updateSharedState(func(s *softwareUpdateSnapshot) { s.Status, s.Progress = "downloading", progress })
}

// updateSharedState 修改共享快照并广播给所有页面，返回修改后的快照。
func updateSharedState(update func(*softwareUpdateSnapshot)) softwareUpdateSnapshot {
	updateState.mu.Lock()
	if updateState.snapshot.CurrentVersion == "" {
		updateState.snapshot.CurrentVersion = appVersion()
	}
	update(&updateState.snapshot)
	snapshot := updateState.snapshot
	updateState.mu.Unlock()
	broadcast(updateStateEvent, snapshot)
	return snapshot
}

// isReleaseNewer 按语义化版本优先级比较（预发布版本低于正式版）。
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

type installedReleaseNotes struct {
	Version string `json:"version"`
	Body    string `json:"body"`
}

// releaseNotesHistory 放在设置容器中，旧版本重写配置时也能保留升级记录。
type releaseNotesHistory struct {
	HighestVersion *string `json:"highestVersion"`
	PendingVersion *string `json:"pendingVersion"`
}

var releaseNotesRuntime struct {
	mu          sync.Mutex
	initialized bool
	owner       string
}

func readReleaseNotesHistory(c *config.AppConfig) (releaseNotesHistory, error) {
	var history releaseNotesHistory
	record, ok := c.App.Settings[installedReleaseNotesHistoryKy]
	if !ok {
		return history, nil
	}
	if err := json.Unmarshal([]byte(record.ValueJSON), &history); err != nil {
		return history, fmt.Errorf("读取更新日志版本记录失败：%w", err)
	}
	return history, nil
}

func saveReleaseNotesHistory(history releaseNotesHistory) error {
	encoded, err := json.Marshal(history)
	if err != nil {
		return fmt.Errorf("序列化更新日志版本记录失败：%w", err)
	}
	return app.config.Update(func(c *config.AppConfig) {
		c.App.Settings[installedReleaseNotesHistoryKy] = config.SettingRecord{Key: installedReleaseNotesHistoryKy, ValueJSON: string(encoded), UpdatedAt: config.NowTS()}
	})
}

// initializeInstalledReleaseNotes 必须在本次启动写入窗口、工作区等记录前调用，避免把全新安装识别为已有用户。
func initializeInstalledReleaseNotes() error {
	releaseNotesRuntime.mu.Lock()
	defer releaseNotesRuntime.mu.Unlock()
	current := appVersion()
	snapshot := app.config.Get()
	previous, err := readReleaseNotesHistory(&snapshot)
	if err != nil {
		return err
	}
	history := previous
	var shouldPrompt bool
	if history.HighestVersion != nil {
		if shouldPrompt, err = isReleaseNewer(*history.HighestVersion, current); err != nil {
			return err
		}
	} else {
		shouldPrompt = hasExistingUsage(&snapshot)
	}
	if history.HighestVersion == nil || shouldPrompt {
		history.HighestVersion = &current
		history.PendingVersion = nil
		if shouldPrompt {
			history.PendingVersion = &current
		}
	}
	if !optionalEqualString(history.HighestVersion, previous.HighestVersion) || !optionalEqualString(history.PendingVersion, previous.PendingVersion) {
		if err := saveReleaseNotesHistory(history); err != nil {
			return err
		}
	}
	releaseNotesRuntime.initialized = true
	return nil
}

func optionalEqualString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func hasExistingUsage(c *config.AppConfig) bool {
	if len(c.Recent.Entries) > 0 || len(c.Workspace.Settings) > 0 || len(c.Snapshots.Documents) > 0 || len(c.Editor.Settings) > 0 {
		return true
	}
	for key := range c.App.Settings {
		// 待打开目标与窗口几何不代表用户已使用过应用。
		if key != installedReleaseNotesHistoryKy && !strings.HasPrefix(key, "pending") && !strings.HasPrefix(key, "windowState:") {
			return true
		}
	}
	return false
}

// claimInstalledReleaseNotes 原子领取待展示日志：同一时刻只有一个窗口持有领取权。
func claimInstalledReleaseNotes(label string) (*installedReleaseNotes, error) {
	releaseNotesRuntime.mu.Lock()
	defer releaseNotesRuntime.mu.Unlock()
	if !releaseNotesRuntime.initialized {
		return nil, errors.New("更新日志版本记录初始化失败，本次启动不展示。")
	}
	if releaseNotesRuntime.owner != "" {
		return nil, nil
	}
	snapshot := app.config.Get()
	history, err := readReleaseNotesHistory(&snapshot)
	if err != nil {
		return nil, err
	}
	current := appVersion()
	// 降级时保留较高版本记录，不能展示与当前运行版本不符的日志。
	if history.PendingVersion == nil || *history.PendingVersion != current {
		return nil, nil
	}
	releaseNotesRuntime.owner = label
	return &installedReleaseNotes{Version: current, Body: currentReleaseNotes()}, nil
}

// confirmInstalledReleaseNotesShown 在所属窗口实际挂载弹窗后确认；写盘失败时保留待展示记录。
func confirmInstalledReleaseNotesShown(label, version string) error {
	releaseNotesRuntime.mu.Lock()
	defer releaseNotesRuntime.mu.Unlock()
	if version != appVersion() || releaseNotesRuntime.owner != label || label == "" {
		return errors.New("当前窗口未领取此版本更新日志。")
	}
	snapshot := app.config.Get()
	history, err := readReleaseNotesHistory(&snapshot)
	if err != nil {
		return err
	}
	if history.PendingVersion == nil || *history.PendingVersion != version {
		return errors.New("此版本更新日志已不在待展示记录中。")
	}
	history.PendingVersion = nil
	if err := saveReleaseNotesHistory(history); err != nil {
		return err
	}
	releaseNotesRuntime.owner = ""
	return nil
}

// releaseInstalledReleaseNotes 释放窗口持有的领取权（卸载或销毁时），并通知其他窗口可再领取。
func releaseInstalledReleaseNotes(label string) {
	releaseNotesRuntime.mu.Lock()
	released := releaseNotesRuntime.owner != "" && releaseNotesRuntime.owner == label
	if released {
		releaseNotesRuntime.owner = ""
	}
	releaseNotesRuntime.mu.Unlock()
	if released {
		broadcast(installedReleaseNotesEvent, nil)
	}
}

// currentReleaseNotes 返回编译时嵌入的当前版本更新日志；该版本没有日志时为空字符串。
func currentReleaseNotes() string {
	data, err := nomo.ReleaseNotes.ReadFile(".github/release-notes/v" + appVersion() + ".md")
	if err != nil {
		return ""
	}
	return string(data)
}

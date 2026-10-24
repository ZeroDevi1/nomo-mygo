package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nomo/desktop/internal/applog"
)

// 应用内下载与安装更新（只用于 Windows 安装版）：最近一次检查到新版本时记下安装包与 checksums.md5 中的 MD5；
// 下载到用户缓存目录 <cache>/com.nomo.desktop/updates，边下边算 MD5，校验通过后才保留；安装时再校验一次，启动
// 安装器并退出应用。直连失败（可重试错误）时经 gh-proxy 重试，与检查更新一致。

const (
	checksumsAssetName        = "checksums.md5"
	updateDownloadReadTimeout = 20 * time.Second
)

// updateCandidate 是可下载的安装包。
type updateCandidate struct {
	Version, AssetName, URL, MD5 string
	Size                         int64
}

var updateState struct {
	mu         sync.Mutex
	candidate  *updateCandidate // 最近一次检查到的可安装版本；nil 表示没有
	downloaded string           // 已下载并校验通过的安装包路径
}

// installerAssetName 是 Release 中 Windows x64 安装包的文件名（发布流程按此名上传，见 .github/workflows/release.yml）。
func installerAssetName(version string) string { return "Nomo_" + version + "_x64-setup.exe" }

// rememberUpdateCandidate 在 Windows 安装版检查到新版本时记下安装包与 MD5；缺少安装包或校验清单时不提供应用内安装。
func rememberUpdateCandidate(release *githubRelease, version string, kind installationKind) {
	updateState.mu.Lock()
	updateState.candidate, updateState.downloaded = nil, ""
	updateState.mu.Unlock()
	if kind != installationInstaller {
		return
	}
	var asset, sums *githubReleaseAsset
	for i := range release.Assets {
		switch release.Assets[i].Name {
		case installerAssetName(version):
			asset = &release.Assets[i]
		case checksumsAssetName:
			sums = &release.Assets[i]
		}
	}
	if asset == nil || sums == nil {
		applog.Warn("Update", "Release 缺少 Windows 安装包或 MD5 校验清单，不提供应用内安装")
		return
	}
	data, err := fetchWithProxy(sums.URL, "下载 MD5 校验清单失败")
	if err != nil {
		applog.Warn("Update", err.Error())
		return
	}
	sum, ok := findMD5ForFile(string(data), asset.Name)
	if !ok {
		applog.Warn("Update", "MD5 校验清单中没有 "+asset.Name)
		return
	}
	updateState.mu.Lock()
	updateState.candidate = &updateCandidate{Version: version, AssetName: asset.Name, URL: asset.URL, MD5: sum, Size: asset.Size}
	updateState.mu.Unlock()
}

// updateInstallable 报告最近一次检查到的新版本是否可在应用内安装。
func updateInstallable() bool {
	updateState.mu.Lock()
	defer updateState.mu.Unlock()
	return updateState.candidate != nil
}

// fetchWithProxy 下载小文件，直连可重试失败时经 gh-proxy 重试。
func fetchWithProxy(u, context string) ([]byte, error) {
	data, err := fetchSmall(u, false, context)
	var failure *requestFailure
	if err != nil && errors.As(err, &failure) && failure.retryable {
		data, err = fetchSmall(u, true, context)
	}
	return data, err
}

// downloadUpdate 下载最近一次检查到的安装包并校验 MD5；progress 在后台 goroutine 中调用（已下载、总字节，总数未知为 0）。
func downloadUpdate(progress func(done, total int64)) error {
	updateState.mu.Lock()
	c := updateState.candidate
	updateState.mu.Unlock()
	if c == nil {
		return errors.New("没有可下载的更新")
	}
	if !isValidMD5(c.MD5) || filepath.Base(c.AssetName) != c.AssetName {
		return errors.New("更新信息无效")
	}
	dir, err := updateCacheDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建更新缓存目录失败：%w", err)
	}
	target := filepath.Join(dir, c.AssetName)
	temp := target + ".download"
	_ = os.Remove(temp)
	err = downloadUpdateFrom(c, temp, false, progress)
	var failure *requestFailure
	if err != nil && errors.As(err, &failure) && failure.retryable {
		applog.Warn("Update", "GitHub 直连下载失败，将通过 gh-proxy 重试："+err.Error())
		_ = os.Remove(temp)
		err = downloadUpdateFrom(c, temp, true, progress)
	}
	if err != nil {
		_ = os.Remove(temp)
		return err
	}
	if err := os.Rename(temp, target); err != nil {
		return fmt.Errorf("保存更新安装包失败：%w", err)
	}
	updateState.mu.Lock()
	updateState.downloaded = target
	updateState.mu.Unlock()
	return nil
}

// downloadUpdateFrom 流式下载并边下边算 MD5；每块读取有独立期限，停滞超过 updateDownloadReadTimeout 视为可重试失败。
func downloadUpdateFrom(c *updateCandidate, temp string, proxy bool, progress func(done, total int64)) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	response, err := sendUpdateRequest(ctx, c.URL, proxy, "下载更新安装包失败")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	total := c.Size
	if response.ContentLength > 0 {
		total = response.ContentLength
	}
	file, err := os.Create(temp)
	if err != nil {
		return fmt.Errorf("创建更新安装包缓存失败：%w", err)
	}
	defer file.Close()
	hasher := md5.New()
	var done int64
	buf := make([]byte, 256<<10)
	for {
		timer := time.AfterFunc(updateDownloadReadTimeout, cancel)
		n, readErr := response.Body.Read(buf)
		timer.Stop()
		if n > 0 {
			if _, err := file.Write(buf[:n]); err != nil {
				return fmt.Errorf("写入更新安装包缓存失败：%w", err)
			}
			hasher.Write(buf[:n])
			done += int64(n)
			progress(done, total)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return &requestFailure{message: "读取更新安装包失败：" + readErr.Error(), retryable: true}
		}
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("写入更新安装包缓存失败：%w", err)
	}
	if actual := hex.EncodeToString(hasher.Sum(nil)); !strings.EqualFold(actual, c.MD5) {
		return fmt.Errorf("更新包校验失败：发布清单 MD5 为 %s，下载文件为 %s", c.MD5, actual)
	}
	return nil
}

// installUpdate 再次校验已下载的安装包后启动安装器并退出应用（launchInstallerAndExit，按平台实现）。
func installUpdate() error {
	updateState.mu.Lock()
	path, c := updateState.downloaded, updateState.candidate
	updateState.mu.Unlock()
	if path == "" || c == nil {
		return errors.New("更新尚未下载")
	}
	actual, err := fileMD5(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, c.MD5) {
		return fmt.Errorf("更新包校验失败：发布清单 MD5 为 %s，安装文件为 %s", c.MD5, actual)
	}
	applog.Info("Update", "启动更新安装器："+c.Version)
	return launchInstallerAndExit(path)
}

// releaseNotes 返回 version 的 Release 说明（Markdown），用于安装后的更新日志。
func releaseNotes(version string) (string, error) {
	release, _, err := fetchGithubRelease("https://api.github.com/repos/" + githubRepo + "/releases/tags/v" + strings.TrimPrefix(version, "v"))
	if err != nil {
		return "", err
	}
	return release.Body, nil
}

func updateCacheDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("获取更新缓存目录失败：%w", err)
	}
	return filepath.Join(cache, appIdentifier, "updates"), nil
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

// findMD5ForFile 在“<md5> <文件名>”格式的校验清单中查找文件的 MD5（小写）。
func findMD5ForFile(checksums, fileName string) (string, bool) {
	for _, line := range strings.Split(checksums, "\n") {
		hash, rest, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		if isValidMD5(hash) && strings.TrimSpace(rest) == fileName {
			return strings.ToLower(hash), true
		}
	}
	return "", false
}

func isValidMD5(v string) bool {
	if len(v) != 32 {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}

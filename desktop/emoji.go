package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nomo/desktop/internal/applog"
)

// 按需获取固定版本的公开 Emoji 元数据：三个镜像错峰竞速，按 SHA-256 校验后缓存到用户缓存目录。
// 缓存只含公开元数据；网络失败不影响编辑。

const (
	emojiVersion       = "17.0.0"
	emojiMaxCacheBytes = 4 * 1024 * 1024
	emojiMaxFileBytes  = 2 * 1024 * 1024
	emojiLoadBudget    = 12 * time.Second
	emojiSourceStagger = 1500 * time.Millisecond
)

var emojiSources = []string{
	"https://cdn.jsdelivr.net/npm/emojibase-data@17.0.0/",
	"https://jsd.onmicrosoft.cn/npm/emojibase-data@17.0.0/",
	"https://unpkg.com/emojibase-data@17.0.0/",
}

var emojiFiles = []struct{ path, sha256 string }{
	{"en/compact.json", "1c7cf539328563743c2b2adff7117ccff0ae6852d591366f4363f50503d5e398"},
	{"zh/compact.json", "f2eac1880c74d7c5ececc24672aac23f951df00f0966c98cf39081fe0a395515"},
	{"en/shortcodes/github.json", "d6bb7101e7bab1e52b30f4b3562da2319b484a6266352a1119f0d1ba52db23db"},
}

type cachedEmojiCatalog struct {
	Version string   `json:"version"`
	Files   []string `json:"files"`
}

type emojiCatalogPayload struct {
	Version    string         `json:"version"`
	En         jsontext.Value `json:"en"`
	Zh         jsontext.Value `json:"zh"`
	Shortcodes jsontext.Value `json:"shortcodes"`
}

// emojiLoad 串行化加载：并发窗口共享正在进行的失败结果，避免断网时排队重复下载；之后的显式重试仍会重新获取。
var emojiLoad struct {
	mu         sync.Mutex
	failedAt   time.Time
	failureErr error
}

func init() {
	register("load_emoji_catalog", func(_ *commandCall, _ noArgs) (*emojiCatalogPayload, error) {
		started := time.Now()
		locked := make(chan struct{})
		go func() {
			emojiLoad.mu.Lock()
			close(locked)
		}()
		select {
		case <-locked:
		case <-time.After(emojiLoadBudget):
			// 取得锁的 goroutine 稍后会释放它，避免锁被永久占用。
			go func() {
				<-locked
				emojiLoad.mu.Unlock()
			}()
			return nil, errors.New("Emoji load timed out")
		}
		defer emojiLoad.mu.Unlock()
		if emojiLoad.failureErr != nil && !emojiLoad.failedAt.Before(started) {
			return nil, emojiLoad.failureErr
		}
		ctx, cancel := context.WithTimeout(context.Background(), emojiLoadBudget-time.Since(started))
		defer cancel()
		payload, err := loadEmojiCatalog(ctx)
		if err != nil {
			emojiLoad.failedAt, emojiLoad.failureErr = time.Now(), err
			return nil, err
		}
		emojiLoad.failureErr = nil
		return payload, nil
	})
}

func emojiCachePath() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, appIdentifier, "emoji", "catalog-v1.json"), nil
}

func loadEmojiCatalog(ctx context.Context) (*emojiCatalogPayload, error) {
	path, err := emojiCachePath()
	if err != nil {
		return nil, err
	}
	if cache, err := readEmojiCache(path); err == nil {
		if payload, err := emojiPayload(cache); err == nil {
			return payload, nil
		}
	}
	files := make([]string, len(emojiFiles))
	errs := make([]error, len(emojiFiles))
	var wg sync.WaitGroup
	for index := range emojiFiles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			files[index], errs[index] = downloadEmojiFile(ctx, index)
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, errors.New("Emoji download timed out")
	}
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	cache := cachedEmojiCatalog{Version: emojiVersion, Files: files}
	payload, err := emojiPayload(cache)
	if err != nil {
		return nil, err
	}
	// 网络数据仍可用于本次会话；落盘失败只记录日志，下次启动重新获取。
	if err := writeEmojiCache(path, cache); err != nil {
		applog.Warn("Emoji", "Cannot cache emoji data: "+err.Error())
	}
	return payload, nil
}

func verifyEmojiData(data []byte, expected string) error {
	digest := sha256.Sum256(data)
	if len(data) > emojiMaxFileBytes || hex.EncodeToString(digest[:]) != expected {
		return errors.New("Emoji data integrity check failed")
	}
	return nil
}

func emojiPayload(cache cachedEmojiCatalog) (*emojiCatalogPayload, error) {
	if cache.Version != emojiVersion || len(cache.Files) != len(emojiFiles) {
		return nil, errors.New("Emoji cache version mismatch")
	}
	values := make([]jsontext.Value, len(emojiFiles))
	for index, file := range cache.Files {
		if err := verifyEmojiData([]byte(file), emojiFiles[index].sha256); err != nil {
			return nil, err
		}
		value := jsontext.Value(file)
		if !value.IsValid() {
			return nil, errors.New("Emoji data is not valid JSON")
		}
		values[index] = value
	}
	return &emojiCatalogPayload{Version: emojiVersion, En: values[0], Zh: values[1], Shortcodes: values[2]}, nil
}

func readEmojiCache(path string) (cachedEmojiCatalog, error) {
	var cache cachedEmojiCatalog
	info, err := os.Stat(path)
	if err != nil {
		return cache, err
	}
	if info.Size() > emojiMaxCacheBytes {
		return cache, errors.New("Emoji cache exceeds budget")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cache, err
	}
	return cache, json.Unmarshal(data, &cache)
}

func writeEmojiCache(path string, cache cachedEmojiCatalog) error {
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	if len(data) > emojiMaxCacheBytes {
		return errors.New("Emoji cache exceeds budget")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary := strings.TrimSuffix(path, filepath.Ext(path)) + ".tmp"
	if err := os.WriteFile(temporary, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		os.Remove(temporary)
		return err
	}
	return nil
}

var emojiHTTPClient = &http.Client{Timeout: 5 * time.Second}

// downloadEmojiFile 依次错峰 1.5s 向各镜像发起请求，取最先通过校验的结果并取消其余请求。
func downloadEmojiFile(ctx context.Context, index int) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		data string
		err  error
	}
	results := make(chan result, len(emojiSources))
	for sourceIndex, source := range emojiSources {
		go func() {
			select {
			case <-time.After(emojiSourceStagger * time.Duration(sourceIndex)):
			case <-ctx.Done():
				results <- result{err: ctx.Err()}
				return
			}
			data, err := fetchEmojiFile(ctx, source+emojiFiles[index].path, emojiFiles[index].sha256)
			results <- result{data, err}
		}()
	}
	var failures []string
	for range emojiSources {
		outcome := <-results
		if outcome.err == nil {
			return outcome.data, nil
		}
		failures = append(failures, outcome.err.Error())
	}
	return "", fmt.Errorf("Cannot load %s: %s", emojiFiles[index].path, strings.Join(failures, "; "))
}

func fetchEmojiFile(ctx context.Context, url, expected string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	response, err := emojiHTTPClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP status %s", response.Status)
	}
	if response.ContentLength > emojiMaxFileBytes {
		return "", errors.New("Emoji response exceeds budget")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, emojiMaxFileBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > emojiMaxFileBytes {
		return "", errors.New("Emoji response exceeds budget")
	}
	if err := verifyEmojiData(data, expected); err != nil {
		return "", err
	}
	return string(data), nil
}

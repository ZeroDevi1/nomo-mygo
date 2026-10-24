package main

import (
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestDownloadUpdateVerifiesMD5：记下安装包与校验清单中的 MD5，下载后校验通过才保留；清单不符时下载失败。
func TestDownloadUpdateVerifiesMD5(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	payload := strings.Repeat("installer", 1000)
	sum := md5.Sum([]byte(payload))
	good := hex.EncodeToString(sum[:])
	var checksums string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/setup.exe":
			_, _ = w.Write([]byte(payload))
		case "/checksums.md5":
			_, _ = w.Write([]byte(checksums))
		}
	}))
	defer srv.Close()
	release := &githubRelease{Assets: []githubReleaseAsset{
		{Name: installerAssetName("9.9.9"), Size: int64(len(payload)), URL: srv.URL + "/setup.exe"},
		{Name: checksumsAssetName, URL: srv.URL + "/checksums.md5"},
	}}

	checksums = good + "  " + installerAssetName("9.9.9") + "\n"
	rememberUpdateCandidate(release, "9.9.9", installationInstaller)
	if !updateInstallable() {
		t.Fatal("应可安装")
	}
	var last int64
	if err := downloadUpdate(func(done, total int64) { last = done }); err != nil {
		t.Fatal(err)
	}
	if last != int64(len(payload)) {
		t.Fatalf("进度 %d", last)
	}
	if _, err := os.Stat(updateState.downloaded); err != nil {
		t.Fatalf("安装包未保留：%v", err)
	}

	checksums = strings.Repeat("0", 32) + "  " + installerAssetName("9.9.9") + "\n"
	rememberUpdateCandidate(release, "9.9.9", installationInstaller)
	if err := downloadUpdate(func(int64, int64) {}); err == nil || !strings.Contains(err.Error(), "校验失败") {
		t.Fatalf("MD5 不符应失败：%v", err)
	}
	rememberUpdateCandidate(release, "9.9.9", installationManual)
	if updateInstallable() {
		t.Fatal("非安装版不应可安装")
	}
}

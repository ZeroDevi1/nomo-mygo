// Package typesetassets downloads the fixed upstream archives used by Nomo.
package typesetassets

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ulikunitz/xz"
)

type Asset struct {
	URL, SHA256 string
	Size        int64
}

// Fetch writes a verified archive to dir. The caller owns dir and its cleanup.
func (a Asset) Fetch(ctx context.Context, dir string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Nomo-Typeset")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	name := filepath.Base(req.URL.Path)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", name, resp.Status)
	}
	f, err := os.CreateTemp(dir, "archive-*")
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(resp.Body, a.Size+1))
	if err != nil {
		return "", err
	}
	if n != a.Size || hex.EncodeToString(hash.Sum(nil)) != a.SHA256 {
		return "", fmt.Errorf("%s: size or SHA-256 mismatch", name)
	}
	return f.Name(), f.Close()
}

func ReadTar(file, url string, visit func(*tar.Header, io.Reader) error) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	var r io.Reader = f
	switch {
	case strings.HasSuffix(url, ".xz"):
		// LZMA reads compressed bytes individually. Buffer them rather
		// than paying for a syscall on each byte of the TeX archive.
		r, err = (xz.ReaderConfig{DictCap: 64 << 20}).NewReader(bufio.NewReaderSize(f, 64<<10))
	case strings.HasSuffix(url, ".gz"), strings.HasSuffix(url, ".tgz"):
		var gz *gzip.Reader
		gz, err = gzip.NewReader(f)
		if err == nil {
			defer gz.Close()
			r = gz
		}
	}
	if err != nil {
		return err
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := visit(h, tr); err != nil {
			return err
		}
	}
}

func WriteFile(path string, data io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, data)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

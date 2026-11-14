// Stage the per-platform resources consumed by MyGo's build command.
package main

import (
	"archive/tar"
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"time"

	"nomo/internal/typesetassets"
)

const (
	typstBase  = "https://github.com/typst/typst/releases/download/v0.15.1/"
	pdfMacBase = "https://github.com/bblanchon/pdfium-binaries/releases/download/chromium%2F7891/" // macOS 12
	pdfBase    = "https://github.com/bblanchon/pdfium-binaries/releases/download/chromium%2F8152/"
	fontBase   = "https://raw.githubusercontent.com/notofonts/noto-cjk/f8d157532fbfaeda587e826d4cd5b21a49186f7c/Sans/"
)

// Official Typst Windows builds import the MSVC runtime. Ship the Microsoft
// redistributable DLL beside typst.exe so the ZIP and MSIX work on a clean PC.
var windowsCRT = typesetassets.Asset{
	URL:    "https://download.microsoft.com/download/4/7/c/47c6134b-d61f-4024-83bd-b9c9ea951c25/Microsoft.VCLibs.x64.14.00.Desktop.appx",
	SHA256: "b56a9101f706f9d95f815f5b7fa6efbac972e86573d378b96a07cff5540c5961", Size: 6764349,
}

type target struct {
	platform, pdfName, typstName string
	typst, pdf                   typesetassets.Asset
}

var targets = []target{
	{"darwin-arm64", "libpdfium.dylib", "typst",
		typesetassets.Asset{URL: typstBase + "typst-aarch64-apple-darwin.tar.xz", SHA256: "48f62ed034aa3a7978309579ac6ca00045e2ef0da73114e8af27cfd8e74dc05a", Size: 14438168},
		typesetassets.Asset{URL: pdfMacBase + "pdfium-mac-arm64.tgz", SHA256: "95d44263629eb8d0f6a619d5443da1ed449d4f916b26f4df1878ad4d5a64b0fc", Size: 3535601}},
	{"darwin-amd64", "libpdfium.dylib", "typst",
		typesetassets.Asset{URL: typstBase + "typst-x86_64-apple-darwin.tar.xz", SHA256: "7f9fdd9584866245de9a79e0add8f9236fae6f40a8a45e2c4771ccc14db4e0fa", Size: 15635600},
		typesetassets.Asset{URL: pdfMacBase + "pdfium-mac-x64.tgz", SHA256: "785c4fce5ca1d7bbd4c2d07fcb3f5adb1dcd1ceba37e1e1fd0b2fd70875481d6", Size: 3591935}},
	{"windows-amd64", "pdfium.dll", "typst.exe",
		typesetassets.Asset{URL: typstBase + "typst-x86_64-pc-windows-msvc.zip", SHA256: "19ce3551153c2fe7ee9fa2f95208310c8f4d3209fedb699e0333faf8913f6736", Size: 22463684},
		typesetassets.Asset{URL: pdfBase + "pdfium-win-x64.tgz", SHA256: "cee2b0e9990bc67c0721774932961b9dbf2766046c10eec925acd4cfba6fdfe3", Size: 4037307}},
	{"linux-amd64", "libpdfium.so", "typst",
		typesetassets.Asset{URL: typstBase + "typst-x86_64-unknown-linux-musl.tar.xz", SHA256: "a6d077d0a95eed5a2eba715b2dae06be954f624ccbf85758a03f389ded33118c", Size: 17462992},
		typesetassets.Asset{URL: pdfBase + "pdfium-linux-x64.tgz", SHA256: "384ab5c51882408a143a21f9c14dcd2caf1f75280ae41c554d0489b46c3dba9f", Size: 3948043}},
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, 15*time.Minute)
	defer timeout()
	if err := stage(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "typeset assets:", err)
		os.Exit(1)
	}
}

func stage(ctx context.Context) error {
	if err := os.MkdirAll("resources", 0755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("resources", ".typeset-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for _, t := range targets {
		dest := filepath.Join("resources", t.platform)
		stamp := t.typst.SHA256 + t.pdf.SHA256
		marker, _ := os.ReadFile(filepath.Join(dest, ".typeset-version"))
		if t.platform == "windows-amd64" {
			stamp += windowsCRT.SHA256
		}
		if string(marker) == stamp && ready(dest, t) {
			continue
		}
		work := filepath.Join(tmp, t.platform)
		if err := stageTools(ctx, tmp, work, t); err != nil {
			return fmt.Errorf("%s: %w", t.platform, err)
		}
		if err := os.WriteFile(filepath.Join(work, ".typeset-version"), []byte(stamp), 0644); err != nil {
			return err
		}
		if err := installTools(work, dest, t); err != nil {
			return err
		}
	}
	font := typesetassets.Asset{URL: fontBase + "SubsetOTF/SC/NotoSansSC-Regular.otf", SHA256: "faa6c9df652116dde789d351359f3d7e5d2285a2b2a1f04a2d7244df706d5ea9", Size: 8331336}
	license := typesetassets.Asset{URL: fontBase + "LICENSE", SHA256: "6a73f9541c2de74158c0e7cf6b0a58ef774f5a780bf191f2d7ec9cc53efe2bf2", Size: 4301}
	dest := filepath.Join("resources", "typeset-fonts")
	stamp := font.SHA256 + license.SHA256
	marker, _ := os.ReadFile(filepath.Join(dest, ".version"))
	if string(marker) == stamp && exists(filepath.Join(dest, "NotoSansSC-Regular.otf")) && exists(filepath.Join(dest, "OFL.txt")) {
		return nil
	}
	work := filepath.Join(tmp, "fonts")
	for _, f := range []struct {
		name  string
		asset typesetassets.Asset
	}{{"NotoSansSC-Regular.otf", font}, {"OFL.txt", license}} {
		in, err := f.asset.Fetch(ctx, tmp)
		if err != nil {
			return err
		}
		if err := copyLocal(in, filepath.Join(work, f.name)); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(work, ".version"), []byte(stamp), 0644); err != nil {
		return err
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	return os.Rename(work, dest)
}

func ready(dir string, t target) bool {
	return exists(filepath.Join(dir, t.typstName)) && exists(filepath.Join(dir, t.pdfName)) &&
		(t.platform != "windows-amd64" || exists(filepath.Join(dir, "vcruntime140.dll")))
}

func installTools(work, dest string, t target) error {
	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}
	names := []string{t.typstName, t.pdfName, "typeset-licenses", ".typeset-version"}
	if t.platform == "windows-amd64" {
		names = append([]string{"vcruntime140.dll"}, names...)
	}
	for _, name := range names {
		out := filepath.Join(dest, name)
		if err := os.RemoveAll(out); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(work, name), out); err != nil {
			return err
		}
	}
	return nil
}

func exists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

func copyLocal(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	return typesetassets.WriteFile(dst, f, 0644)
}

func stageTools(ctx context.Context, tmp, work string, t target) error {
	fmt.Println("Preparing Typst and PDFium for", t.platform)
	typst, err := t.typst.Fetch(ctx, tmp)
	if err != nil {
		return err
	}
	defer os.Remove(typst)
	pdf, err := t.pdf.Fetch(ctx, tmp)
	if err != nil {
		return err
	}
	defer os.Remove(pdf)
	writeTypst := func(name string, r io.Reader) error {
		switch name = path.Base(name); name {
		case t.typstName:
			return typesetassets.WriteFile(filepath.Join(work, name), r, 0755)
		case "LICENSE", "NOTICE":
			return typesetassets.WriteFile(filepath.Join(work, "typeset-licenses", "typst", name), r, 0644)
		}
		return nil
	}
	if strings.HasSuffix(t.typst.URL, ".zip") {
		z, err := zip.OpenReader(typst)
		if err != nil {
			return err
		}
		defer z.Close()
		for _, f := range z.File {
			if !f.Mode().IsRegular() {
				continue
			}
			r, err := f.Open()
			if err != nil {
				return err
			}
			err = writeTypst(f.Name, r)
			r.Close()
			if err != nil {
				return err
			}
		}
	} else if err := typesetassets.ReadTar(typst, t.typst.URL, func(h *tar.Header, r io.Reader) error {
		if h.Typeflag != tar.TypeReg {
			return nil
		}
		return writeTypst(h.Name, r)
	}); err != nil {
		return err
	}
	if err := typesetassets.ReadTar(pdf, t.pdf.URL, func(h *tar.Header, r io.Reader) error {
		if h.Typeflag != tar.TypeReg {
			return nil
		}
		if path.Base(h.Name) == t.pdfName {
			return typesetassets.WriteFile(filepath.Join(work, t.pdfName), r, 0755)
		}
		if h.Name == "LICENSE" || strings.HasPrefix(h.Name, "licenses/") {
			return typesetassets.WriteFile(filepath.Join(work, "typeset-licenses", "pdfium", path.Base(h.Name)), r, 0644)
		}
		return nil
	}); err != nil {
		return err
	}
	if t.platform == "windows-amd64" {
		if err := stageWindowsCRT(ctx, tmp, work); err != nil {
			return err
		}
	}
	if !ready(work, t) {
		return errors.New("archive does not contain the expected binaries")
	}
	return nil
}

func stageWindowsCRT(ctx context.Context, tmp, work string) error {
	archive, err := windowsCRT.Fetch(ctx, tmp)
	if err != nil {
		return err
	}
	defer os.Remove(archive)
	z, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer z.Close()
	for _, file := range z.File {
		if file.Name != "vcruntime140.dll" {
			continue
		}
		r, err := file.Open()
		if err != nil {
			return err
		}
		defer r.Close()
		return typesetassets.WriteFile(filepath.Join(work, file.Name), r, 0755)
	}
	return errors.New("vcruntime140.dll is missing from the redistributable")
}

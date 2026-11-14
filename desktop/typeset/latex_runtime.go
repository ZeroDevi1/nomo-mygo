package typeset

import (
	"archive/tar"
	"archive/zip"
	"context"
	"debug/macho"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"nomo/internal/typesetassets"
)

// The portable TeX Live tree is selected per OS, independently of the bundled
// Typst and PDFium versions. The supplements use a frozen TeX Live mirror.
const (
	texVersion = "2026.11"
	tinyTeXURL = "https://github.com/rstudio/tinytex-releases/releases/download/v" + texVersion + "/"
	texArchive = "https://ftp.math.utah.edu/pub/tex/historic/systems/texlive/2025/tlnet-final/archive/"
)

type latexRuntime struct {
	tree, bin     string
	main, synctex typesetassets.Asset
}

func platformLatex() (latexRuntime, bool) {
	switch {
	case runtime.GOOS == "darwin" && (runtime.GOARCH == "arm64" || runtime.GOARCH == "amd64"):
		return latexRuntime{"TinyTeX", "universal-darwin",
			typesetassets.Asset{URL: tinyTeXURL + "TinyTeX-darwin-v2026.11.tar.xz", SHA256: "a54a3a16aca1717580b1cc0e4965791487c0650a450d1502dda457dd68e41962", Size: 210514532},
			typesetassets.Asset{URL: texArchive + "synctex.universal-darwin.tar.xz", SHA256: "583cfafd249543b05bc8e7610e232151e5f35b7ab55e5d3c530e03dc29a6fc97", Size: 136120}}, true
	case runtime.GOOS == "windows" && runtime.GOARCH == "amd64":
		return latexRuntime{"TinyTeX", "windows",
			typesetassets.Asset{URL: tinyTeXURL + "TinyTeX-v2026.11.zip", SHA256: "a13d14ba987873d1bdf81773f5bdfcc34537882b825dd2134d61eb6ee21d5f69", Size: 247432160},
			typesetassets.Asset{URL: texArchive + "synctex.windows.tar.xz", SHA256: "5e68f16401d30b3d6261425f5c4eeff9b47ff2199de51dbb8fc8109bef9580ce", Size: 179572}}, true
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		return latexRuntime{".TinyTeX", "x86_64-linux",
			typesetassets.Asset{URL: tinyTeXURL + "TinyTeX-linux-x86_64-v2026.11.tar.xz", SHA256: "50e68059058db770a28bcca44d5a323cb57fc10d470017c858404aafeaac9dd7", Size: 151918900},
			typesetassets.Asset{URL: texArchive + "synctex.x86_64-linux.tar.xz", SHA256: "94fee6f19e93969db2424dda3a796c6d4b5f29c1acb57ba9a512bed98dc63602", Size: 75980}}, true
	default:
		return latexRuntime{}, false
	}
}

var texPackages = []typesetassets.Asset{
	{URL: texArchive + "ctex.tar.xz", SHA256: "d2e2d951997f1db32b01e803515987a921a6f24a6326d795c42671e6a7444ac4", Size: 130996},
	{URL: texArchive + "xecjk.tar.xz", SHA256: "399ca8e7c838a3ca781bdc549381f114bbfdfca5a50d08413553c6a9633c3413", Size: 221864},
	{URL: texArchive + "zhnumber.tar.xz", SHA256: "dbcc134ce2f0cb8fbe9ad512e4d3a7a897a0d78201fe7568850443a65e59f15b", Size: 8612},
	{URL: texArchive + "fandol.tar.xz", SHA256: "0b71eebb1c9b9fbd1eb4281f8af3f6c1a556a394ba2b6eae69ed9bb76c5fa56a", Size: 21582588},
}

func latexRoot() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "com.nomo.desktop", "typeset", "texlive-"+texVersion+"-"+runtime.GOOS+"-"+runtime.GOARCH)
}

func managedLatexTool(name string) string {
	spec, ok := platformLatex()
	root := latexRoot()
	if !ok || root == "" {
		return ""
	}
	return executableAt(filepath.Join(root, "bin", spec.bin), name)
}

// LatexNeedsDownload reports whether the next LaTeX compile needs to provision
// its runtime. It does not start a download or modify the file system.
func LatexNeedsDownload() bool {
	_, err := tool("xelatex")
	return err != nil && latexManagementSupported()
}

func latexManagementSupported() bool {
	_, supported := platformLatex()
	return supported
}

var latexInstallGate = make(chan struct{}, 1)

// LaTeXReady checks for XeLaTeX without provisioning the toolchain.
func LaTeXReady() bool {
	_, err := tool("xelatex")
	return err == nil
}

// PrepareLaTeX provisions the portable toolchain if tool cannot find XeLaTeX. Callers with a preview lifetime can prepare once before their
// first revision so edits do not restart a download.
func PrepareLaTeX(ctx context.Context) error {
	if LaTeXReady() {
		return nil
	}
	spec, ok := platformLatex()
	root := latexRoot()
	if !ok || root == "" {
		return errors.New("managed LaTeX is unavailable on this platform or the user cache is inaccessible")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	select {
	case latexInstallGate <- struct{}{}:
		defer func() { <-latexInstallGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if LaTeXReady() {
		return nil
	}
	if err := installLaTeX(ctx, root, spec); err != nil {
		return fmt.Errorf("prepare LaTeX runtime: %w", err)
	}
	return nil
}

func installLaTeX(ctx context.Context, root string, spec latexRuntime) error {
	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	work, err := os.MkdirTemp(parent, ".texlive-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	archive, err := spec.main.Fetch(ctx, work)
	if err != nil {
		return err
	}
	tree := filepath.Join(work, spec.tree)
	if err := unpackTeX(ctx, spec.main, archive, work); err != nil {
		return err
	}
	if err := os.Remove(archive); err != nil {
		return err
	}
	// Add the CJK packages to texmf-dist and update its kpathsea database.
	dist := filepath.Join(tree, "texmf-dist")
	for _, pkg := range texPackages {
		archive, err := pkg.Fetch(ctx, work)
		if err != nil {
			return err
		}
		if err := unpackTeX(ctx, pkg, archive, dist); err != nil {
			return err
		}
		if err := os.Remove(archive); err != nil {
			return err
		}
	}
	archive, err = spec.synctex.Fetch(ctx, work)
	if err != nil {
		return err
	}
	if err := unpackTeX(ctx, spec.synctex, archive, tree); err != nil {
		return err
	}
	bin := filepath.Join(tree, "bin", spec.bin)
	for _, name := range []string{"xelatex", "xdvipdfmx", "bibtex", "biber", "synctex"} {
		if executableAt(bin, name) == "" {
			return fmt.Errorf("runtime does not contain %s", name)
		}
	}
	// TeX Live's mktexlsr is a shell script on Unix. The macOS/Linux
	// distribution uses !! for texmf-dist, so rebuild its ls-R before use.
	if runtime.GOOS == "darwin" {
		if err := unpackBiber(filepath.Join(bin, "biber")); err != nil {
			return err
		}
	}
	if err := updateTeXFileIndex(ctx, dist); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tree, root); err != nil && managedLatexTool("xelatex") == "" {
		return err
	}
	return nil
}

// The universal Biber wrapper invokes Xcode's lipo on first use. Extract the
// signed architecture slice while installing so Biber works without Xcode.
func unpackBiber(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fat, err := macho.NewFatFile(f)
	if errors.Is(err, macho.ErrNotFat) {
		return nil
	}
	if err != nil {
		return err
	}
	cpu := macho.CpuArm64
	if runtime.GOARCH == "amd64" {
		cpu = macho.CpuAmd64
	}
	for _, arch := range fat.Arches {
		if arch.Cpu != cpu {
			continue
		}
		thin := path + ".thin"
		if err := typesetassets.WriteFile(thin, io.NewSectionReader(f, int64(arch.Offset), int64(arch.Size)), 0700); err != nil {
			return err
		}
		return os.Rename(thin, path)
	}
	return errors.New("Biber archive lacks the host architecture")
}

func updateTeXFileIndex(ctx context.Context, dist string) error {
	lsr := filepath.Join(dist, "ls-R")
	file, err := os.Create(lsr)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err = fmt.Fprintln(file, "% ls-R -- filename database for kpathsea; do not change this line."); err != nil {
		return err
	}
	// ReadDir gives stable ordering. TeX Live needs directory sections named
	// relative to texmf-dist, with one filename per line.
	err = filepath.WalkDir(dist, func(dir string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || !d.IsDir() {
			return walkErr
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		rel, err := filepath.Rel(dist, dir)
		if err != nil {
			return err
		}
		label := "./"
		if rel != "." {
			label += filepath.ToSlash(rel)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		var section strings.Builder
		fmt.Fprintf(&section, "\n%s:\n", label)
		for _, entry := range entries {
			if !entry.IsDir() {
				fmt.Fprintln(&section, entry.Name())
			}
		}
		_, err = file.WriteString(section.String())
		return err
	})
	if err != nil {
		return err
	}
	return file.Close()
}

type archiveLink struct {
	name, target string
	hard         bool
}

func unpackTeX(ctx context.Context, asset typesetassets.Asset, archive, root string) error {
	var links []archiveLink
	write := func(name, target string, mode os.FileMode, hard bool, r io.Reader) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		name = path.Clean(name)
		if !filepath.IsLocal(filepath.FromSlash(name)) || strings.ContainsRune(name, '\\') {
			return fmt.Errorf("invalid TeX archive path %q", name)
		}
		dest := filepath.Join(root, filepath.FromSlash(name))
		switch {
		case mode.IsDir():
			return os.MkdirAll(dest, 0700)
		case mode&os.ModeSymlink != 0 || hard:
			resolved := path.Clean(target)
			if !hard {
				if path.IsAbs(target) {
					return fmt.Errorf("absolute TeX archive link %q", target)
				}
				resolved = path.Join(path.Dir(name), target)
			}
			if !filepath.IsLocal(filepath.FromSlash(resolved)) || strings.ContainsRune(target, '\\') {
				return fmt.Errorf("invalid TeX archive link %q", target)
			}
			links = append(links, archiveLink{name, target, hard})
			return nil
		case mode.IsRegular():
			return typesetassets.WriteFile(dest, projectContextReader{ctx, r}, 0600|mode.Perm()&0100)
		default:
			return fmt.Errorf("unsupported TeX archive entry %q", name)
		}
	}
	if strings.HasSuffix(asset.URL, ".zip") {
		z, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer z.Close()
		for _, f := range z.File {
			r, err := f.Open()
			if err != nil {
				return err
			}
			err = write(f.Name, "", f.Mode()|0100, false, r)
			r.Close()
			if err != nil {
				return err
			}
		}
	} else if err := typesetassets.ReadTar(archive, asset.URL, func(h *tar.Header, r io.Reader) error {
		return write(h.Name, h.Linkname, h.FileInfo().Mode(), h.Typeflag == tar.TypeLink, r)
	}); err != nil {
		return err
	}
	for _, l := range links {
		if err := ctx.Err(); err != nil {
			return err
		}
		dest := filepath.Join(root, filepath.FromSlash(l.name))
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return err
		}
		var err error
		if l.hard {
			err = os.Link(filepath.Join(root, filepath.FromSlash(l.target)), dest)
		} else {
			err = os.Symlink(filepath.FromSlash(l.target), dest)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

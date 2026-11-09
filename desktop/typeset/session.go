// Package typeset 使用本地编译器为 Go 界面生成分页预览和完整 PDF，不依赖 UI 框架。
package typeset

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"nomo/desktop/internal/fsutil"
)

// Session 由单个调用方串行使用；Close 结束段落引擎并删除预览临时目录。
type Session struct {
	trialRoot                    string
	mirror                       *projectMirror
	liveProject, depsUnsupported bool
	sourceDeps                   []string
	previewFiles                 map[string]os.FileInfo
	previewPathBytes             int
	indexed                      bool
	anchors                      map[[32]byte]PageAnchor
	path, dir, pdf               string
	pages                        int
	warnings                     []Diagnostic
	fast                         *paragraphEngine
	fastUnavailable              bool
	lastDoc                      string
	lastParagraph                image.Image
}

// IsLaTeXPath reports whether the path uses a TeX or LaTeX source suffix.
func IsLaTeXPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tex", ".ltx":
		return true
	default:
		return false
	}
}

// Open 以主文件目录为资源根目录；主文件正文由 Compile 的 reader 提供。
func Open(path string) (*Session, error) {
	if !IsLaTeXPath(path) && !strings.EqualFold(filepath.Ext(path), ".typ") {
		return nil, fmt.Errorf("unsupported document: %s", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "nomo-typeset-*")
	if err != nil {
		return nil, err
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return &Session{path: abs, dir: dir, pdf: filepath.Join(dir, name+".pdf")}, nil
}
func (s *Session) Close() {
	s.StopParagraphPreview()
	os.RemoveAll(s.dir)
	if s.trialRoot != "" {
		os.RemoveAll(s.trialRoot)
	}
}

const MaxPageEdge = 5120

// Page 渲染最近一次成功编译的指定页（从 1 起），长边上限 1400 像素。
func (s *Session) Page(ctx context.Context, n int) (image.Image, error) {
	return s.PageAtSize(ctx, n, 1400)
}

// PageAtSize 以指定的像素长边栅格化正式 PDF。
func (s *Session) PageAtSize(ctx context.Context, n, edge int) (image.Image, error) {
	if n < 1 || n > s.pages || edge < 1 || edge > MaxPageEdge {
		return nil, errors.New("invalid page")
	}
	return renderPage(ctx, filepath.Join(s.dir, "pages", "current.pdf"), n, edge)
}
func writeSource(path string, r io.Reader) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if e != nil {
		return e
	}
	_, e = io.Copy(f, r)
	return errors.Join(e, f.Close())
}

// ExportPDF 在新目录重新编译传入的源码；只有完整成功后才原子替换目标 PDF。
func ExportPDF(ctx context.Context, path string, source io.Reader, target string) error {
	if !strings.EqualFold(filepath.Ext(target), ".pdf") {
		return errors.New("expected a .pdf output file")
	}
	s, e := Open(path)
	if e != nil {
		return e
	}
	defer s.Close()
	digest := sha256.New()
	if runtime.GOOS == "darwin" {
		source = io.TeeReader(source, digest)
	}
	if e = s.build(ctx, source, true, false); e != nil {
		return e
	}
	in, e := os.Open(s.pdf)
	if e != nil {
		return e
	}
	defer in.Close()
	out, e := os.CreateTemp(filepath.Dir(target), ".nomo-export-*.pdf")
	if e != nil {
		return e
	}
	defer os.Remove(out.Name())
	_, e = io.Copy(out, in)
	if e = errors.Join(e, out.Sync(), out.Close()); e != nil {
		return e
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	if e = fsutil.ReplaceFile(out.Name(), target); e == nil {
		storeQuickLook(ctx, s.path, s.pdf, digest.Sum(nil))
	}
	return e
}
func renderPage(ctx context.Context, pdf string, page, edge int) (image.Image, error) {
	dir := filepath.Dir(pdf)
	n := strconv.Itoa(page)
	if _, err := run(ctx, dir, "", nil, "pdftoppm", "-png", "-scale-to", strconv.Itoa(edge), "-f", n, "-l", n, "-singlefile", pdf, "page"); err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(dir, "page.png"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return img, nil
}

func (s *Session) build(ctx context.Context, source io.Reader, export, projectSource bool) error {
	s.warnings = nil
	s.previewFiles = nil
	path, pdf := s.path, s.pdf
	dir := filepath.Dir(pdf)
	project, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return err
	}
	if e := os.Remove(pdf); e != nil && !os.IsNotExist(e) {
		return e
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".typ":
		var log string
		if projectSource {
			log, err = s.compileTypstProject(ctx, project, pdf)
		} else {
			log, err = runReader(ctx, project, source, nil, "typst", "compile", "--root", s.projectRoot(project), "-", pdf)
		}
		if err == nil {
			s.collectWarnings(log, false)
		}
	case ".tex", ".ltx":
		if err = writeSource(filepath.Join(dir, "preview.tex"), source); err != nil {
			return err
		}
		job := strings.TrimSuffix(filepath.Base(pdf), ".pdf")
		sep := string(os.PathListSeparator)
		search := "." + sep + project + sep
		if root := s.projectRoot(project); root != project {
			search += root + sep
		}
		env := []string{"TEXINPUTS=" + search + os.Getenv("TEXINPUTS"), "BIBINPUTS=" + search + os.Getenv("BIBINPUTS"), "BSTINPUTS=" + search + os.Getenv("BSTINPUTS"), "openout_any=p"}
		// 编译器仅写工作目录。失败（含过期取消）后丢弃本次辅助文件，避免残缺的 aux 污染下一次输入。
		defer func() {
			if err != nil {
				entries, _ := os.ReadDir(dir)
				for _, entry := range entries {
					if !entry.IsDir() {
						os.Remove(filepath.Join(dir, entry.Name()))
					}
				}
			}
		}()
		latex := func() (string, error) {
			syncFlag := "-synctex=0"
			args := []string{"-file-line-error", "-no-shell-escape", "-no-parse-first-line", "-interaction=nonstopmode", "-halt-on-error", "-jobname=" + job}
			if !export {
				syncFlag = "-synctex=1"
			}
			if s.previewFiles != nil {
				args = append(args, "-recorder")
			}
			return run(ctx, dir, "", env, "xelatex", append(args, syncFlag, "./preview.tex")...)
		}
		if !export && (!projectSource || s.liveProject) {
			s.trackPreviewFiles("xelatex")
		}
		if e := os.Remove(filepath.Join(dir, job+".bcf")); e != nil && !os.IsNotExist(e) {
			return e
		}
		var backendLog string
		for pass := 0; pass < 3; pass++ {
			var log string
			log, err = latex()
			if err != nil {
				break
			}
			if s.previewFiles != nil && !s.recordTeXInputs(ctx, dir, job) {
				s.previewFiles = nil
			}
			if pass == 0 {
				backend := ""
				if stat, e := os.Stat(filepath.Join(dir, job+".bcf")); e == nil && stat.Size() > 0 {
					backend = "biber"
				} else if aux, e := os.ReadFile(filepath.Join(dir, job+".aux")); e == nil && bytes.Contains(aux, []byte(`\bibdata{`)) {
					backend = "bibtex"
				}
				if backend != "" {
					// The TeX recorder cannot account for files opened by the bibliography process.
					s.previewFiles = nil
					backendLog, err = run(ctx, dir, "", env, backend, job)
					if err != nil {
						break
					}
					continue
				}
			}
			rerun := strings.Contains(log, "Rerun") || strings.Contains(log, "rerun") || strings.Contains(log, "undefined references")
			if !(export && pass == 0) && !rerun || pass == 2 {
				s.collectWarnings(log, true)
				s.collectWarnings(backendLog, true)
				break
			}
		}
	default:
		return fmt.Errorf("unsupported document: %s", filepath.Ext(path))
	}
	if err != nil {
		return err
	}
	// 只保留一份预览 PDF，超出上限时由调用方立即清理临时目录。
	stat, err := os.Stat(pdf)
	if err != nil {
		return err
	}
	if !export && stat.Size() > 128<<20 {
		return fmt.Errorf("preview PDF > 128 MiB")
	}
	return nil
}

// Compile 返回总页数；失败时仍可读取上一份成功预览。
func (s *Session) Compile(ctx context.Context, source io.Reader) (int, error) {
	return s.compile(ctx, source, true, false)
}

// CompileTrial keeps the PDF inside this session and skips Quick Look publication.
func (s *Session) CompileTrial(ctx context.Context, source io.Reader) (int, error) {
	return s.compile(ctx, source, false, false)
}

func (s *Session) compile(ctx context.Context, source io.Reader, publish, projectSource bool) (int, error) {
	digest := sha256.New()
	if publish && runtime.GOOS == "darwin" {
		source = io.TeeReader(source, digest)
	}
	if err := s.build(ctx, source, false, projectSource); err != nil {
		return 0, err
	}
	dir, pdf := s.dir, s.pdf
	info, err := run(ctx, dir, "", []string{"LC_ALL=C"}, "pdfinfo", pdf)
	if err != nil {
		return 0, err
	}
	for line := range strings.Lines(info) {
		if v, ok := strings.CutPrefix(line, "Pages:"); ok {
			n, e := strconv.Atoi(strings.TrimSpace(v))
			if e == nil && n > 0 {
				store := filepath.Join(s.dir, "pages")
				if err = os.MkdirAll(store, 0700); err != nil {
					return 0, err
				}
				if err = fsutil.ReplaceFile(pdf, filepath.Join(store, "current.pdf")); err != nil {
					return 0, err
				}
				if publish {
					storeQuickLook(ctx, s.path, filepath.Join(store, "current.pdf"), digest.Sum(nil))
				}
				s.indexed = false
				s.anchors = nil
				s.pages = n
				return n, nil
			}
		}
	}
	return 0, fmt.Errorf("pdfinfo: missing page count")
}

func (s *Session) projectRoot(fallback string) string {
	if s.trialRoot != "" {
		return s.trialRoot
	}
	return fallback
}

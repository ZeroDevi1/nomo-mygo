// Package typeset 使用本地编译器为 Go 界面生成分页预览和完整 PDF，不依赖 UI 框架。
package typeset

import (
	"bufio"
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
	"nomo/desktop/internal/pdfium"
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
	sourcePage                   *pdfSourcePage
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

// BlockReferences reuses label and BibTeX metadata from the last successful preview.
func (s *Session) BlockReferences() string {
	if !IsLaTeXPath(s.path) {
		return ""
	}
	const limit = 64 << 10
	var out strings.Builder
	dir := filepath.Dir(s.pdf)
	remaining := 256 << 10
	seen := make(map[string]bool)
	var readAux func(string)
	readAux = func(name string) {
		if !filepath.IsLocal(name) || filepath.Ext(name) != ".aux" || seen[name] || len(seen) >= 32 || remaining <= 0 {
			return
		}
		seen[name] = true
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return
		}
		defer f.Close()
		budget := &io.LimitedReader{R: f, N: int64(remaining)}
		sc := bufio.NewScanner(budget)
		var children []string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "\\newlabel{"), strings.HasPrefix(line, "\\bibcite{"):
				if strings.HasSuffix(line, "}") && out.Len()+len(line)+1 <= limit {
					out.WriteString(line)
					out.WriteByte('\n')
				}
			case strings.HasPrefix(line, "\\@input{"):
				if child, ok := strings.CutSuffix(strings.TrimPrefix(line, "\\@input{"), "}"); ok && len(children) < 32 {
					children = append(children, filepath.Clean(child))
				}
			}
		}
		remaining = int(budget.N)
		if sc.Err() != nil {
			return
		}
		for _, child := range children {
			readAux(child)
		}
	}
	readAux(strings.TrimSuffix(filepath.Base(s.pdf), ".pdf") + ".aux")
	return out.String()
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
	return ReadPDFPage(ctx, filepath.Join(s.dir, "pages", "current.pdf"), n, edge)
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
	if e = replacePDF(ctx, s.pdf, target); e == nil {
		storeQuickLook(ctx, s.path, s.pdf, digest.Sum(nil))
	}
	return e
}

func replacePDF(ctx context.Context, pdf, target string) error {
	in, e := os.Open(pdf)
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
	return fsutil.ReplaceFile(out.Name(), target)
}

// ReadPDFPage renders a local attachment without writing beside the original.
func ReadPDFPage(ctx context.Context, path string, page, edge int) (image.Image, error) {
	if !filepath.IsAbs(path) || page < 1 || edge < 1 || edge > MaxPageEdge {
		return nil, errors.New("invalid PDF page request")
	}
	img, err := pdfium.Render(ctx, path, page, edge)
	if !errors.Is(err, pdfium.ErrUnavailable) {
		return img, err
	}
	// Unpackaged developer runs can still use an existing Poppler.
	dir, err := os.MkdirTemp("", "nomo-pdf-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	n := strconv.Itoa(page)
	if _, err := run(ctx, dir, "", nil, "pdftoppm", "-png", "-scale-to", strconv.Itoa(edge), "-f", n, "-l", n, "-singlefile", path, "page"); err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(dir, "page.png"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err = png.Decode(f)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return img, err
}

// PDFPages reads the page count of an existing local PDF.
func PDFPages(ctx context.Context, path string) (int, error) {
	count, err := pdfium.PageCount(ctx, path)
	if !errors.Is(err, pdfium.ErrUnavailable) {
		return count, err
	}
	info, err := run(ctx, "", "", []string{"LC_ALL=C"}, "pdfinfo", path)
	if err != nil {
		return 0, err
	}
	for line := range strings.Lines(info) {
		if v, ok := strings.CutPrefix(line, "Pages:"); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
				return n, nil
			}
		}
	}
	return 0, errors.New("pdfinfo: missing page count")
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
		if err = PrepareLaTeX(ctx); err != nil {
			return err
		}
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
	n, err := PDFPages(ctx, s.pdf)
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	store := filepath.Join(s.dir, "pages")
	if err = os.MkdirAll(store, 0700); err != nil {
		return 0, err
	}
	current := filepath.Join(store, "current.pdf")
	if err = fsutil.ReplaceFile(s.pdf, current); err != nil {
		return 0, err
	}
	if publish {
		storeQuickLook(ctx, s.path, current, digest.Sum(nil))
	}
	s.indexed = false
	s.anchors = nil
	s.sourcePage = nil
	s.pages = n
	return n, nil
}

func (s *Session) projectRoot(fallback string) string {
	if s.trialRoot != "" {
		return s.trialRoot
	}
	return fallback
}

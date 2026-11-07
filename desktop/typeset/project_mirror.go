package typeset

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ProjectBuffer identifies an immutable open buffer in a preview revision.
type ProjectBuffer struct {
	Reader  io.Reader
	TabID   int
	Version uint64
}

type mirrorEntry struct {
	size           int64
	diskInfo       fs.FileInfo
	tabID          int
	version        uint64
	overlay, valid bool
}

type projectMirror struct {
	root, dir              string
	main                   string
	sourceRoot, sourceMain string
	files                  map[string]mirrorEntry
	pending                map[string]bool
	bytes                  int64
	output                 string
	dirs                   []string
	refresh                bool
}

// OpenProjectPreview creates the same isolated input tree as a project trial.
// The worker can update this tree between compilations.
func OpenProjectPreview(ctx context.Context, root, main string, buffers map[string]ProjectBuffer) (*Session, error) {
	s, err := openProjectSession(ctx, root, main, buffers)
	if err == nil {
		s.liveProject = true
	}
	return s, err
}

func openProjectSession(ctx context.Context, root, main string, buffers map[string]ProjectBuffer) (*Session, error) {
	sourceRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	sourceMain, err := filepath.Abs(main)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(sourceRoot)
	if err != nil {
		return nil, err
	}
	main, err = filepath.EvalSymlinks(sourceMain)
	if err != nil {
		return nil, err
	}
	name, err := filepath.Rel(root, main)
	if err != nil || !filepath.IsLocal(name) || name == "." {
		return nil, errors.New("main file is outside the typeset project")
	}
	dir, err := os.MkdirTemp("", "nomo-typeset-project-*")
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	dir = canonical
	tree := &projectMirror{root: root, dir: dir, main: main, sourceRoot: sourceRoot, sourceMain: sourceMain, files: make(map[string]mirrorEntry), pending: make(map[string]bool)}
	if err := tree.sync(ctx, true, buffers); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	s, err := Open(filepath.Join(dir, name))
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	s.trialRoot, s.mirror = dir, tree
	output, err := filepath.EvalSymlinks(s.dir)
	if err != nil {
		s.Close()
		return nil, err
	}
	s.dir, s.pdf = output, filepath.Join(output, filepath.Base(s.pdf))
	tree.output = s.dir
	if err := tree.prepareOutputDirs(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// SyncProjectPreview updates changed buffers and, when needed, reconciles disk
// inputs by metadata. No compiler runs while the worker is updating its tree.
func (s *Session) SyncProjectPreview(ctx context.Context, diskChanged bool, buffers map[string]ProjectBuffer) error {
	if s.mirror == nil {
		return errors.New("missing isolated project tree")
	}
	return s.mirror.sync(ctx, diskChanged, buffers)
}

func (m *projectMirror) sync(ctx context.Context, diskChanged bool, buffers map[string]ProjectBuffer) error {
	for name := range buffers {
		if !filepath.IsLocal(name) || name == "." {
			return fmt.Errorf("invalid project buffer path: %s", name)
		}
		// An opened file absent from the last scan may have just been created.
		if _, ok := m.files[name]; !ok {
			diskChanged = true
		}
	}
	if diskChanged || m.refresh {
		return m.refreshDisk(ctx, buffers)
	}
	// Only previously overlaid or interrupted files need to be restored.
	for name := range m.pending {
		if _, overlaid := buffers[name]; overlaid {
			continue
		}
		info, err := os.Lstat(filepath.Join(m.root, name))
		if err != nil {
			if os.IsNotExist(err) {
				return m.refreshDisk(ctx, buffers)
			}
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("typeset input must be a regular file: %s", name)
		}
		if err := m.update(ctx, name, info, ProjectBuffer{}, false); err != nil {
			return err
		}
	}
	for name, buffer := range buffers {
		if err := m.update(ctx, name, nil, buffer, true); err != nil {
			return err
		}
	}
	return nil
}

func (m *projectMirror) refreshDisk(ctx context.Context, buffers map[string]ProjectBuffer) error {
	m.refresh = true
	files, dirs, err := m.scanDisk(ctx)
	if err != nil {
		return err
	}
	for name := range buffers {
		if files[name] == nil {
			return fmt.Errorf("project buffer has no regular disk input: %s", name)
		}
	}
	// Free removed and replaced inputs before writing additions, so the cap
	// applies to the reconciled tree regardless of update order.
	for name, old := range m.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		info := files[name]
		buffer, overlaid := buffers[name]
		if info != nil && old.matches(info, buffer, overlaid) {
			continue
		}
		if err := os.Remove(filepath.Join(m.dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
		m.bytes -= old.size
		delete(m.files, name)
		delete(m.pending, name)
	}
	currentDirs := make(map[string]bool, len(dirs))
	for _, name := range dirs {
		currentDirs[name] = true
	}
	for i := len(m.dirs) - 1; i >= 0; i-- {
		if name := m.dirs[i]; !currentDirs[name] {
			os.Remove(filepath.Join(m.dir, name))
		}
	}
	for _, name := range dirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(m.dir, name), 0700); err != nil {
			return err
		}
	}
	m.dirs = dirs
	for name, info := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		buffer, overlaid := buffers[name]
		if m.files[name].matches(info, buffer, overlaid) {
			continue
		}
		dest := filepath.Join(m.dir, name)
		if old, err := os.Lstat(dest); err == nil && old.IsDir() {
			if err := os.RemoveAll(dest); err != nil {
				return err
			}
		}
		if err := m.update(ctx, name, info, buffer, overlaid); err != nil {
			return err
		}
	}
	if err := m.prepareOutputDirs(ctx); err != nil {
		return err
	}
	m.refresh = false
	return nil
}

func (m *projectMirror) scanDisk(ctx context.Context) (map[string]fs.FileInfo, []string, error) {
	files := make(map[string]fs.FileInfo)
	var dirs []string
	err := filepath.WalkDir(m.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		name, err := filepath.Rel(m.root, path)
		if err != nil || name == "." {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".agents", "node_modules", ".DS_Store":
				return filepath.SkipDir
			}
			if len(dirs) >= 20000 {
				return errors.New("typeset project exceeds 20000 directories")
			}
			dirs = append(dirs, name)
			return nil
		}
		if len(files) >= 20000 {
			return errors.New("typeset project exceeds 20000 files")
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("typeset input must be a regular file: %s", name)
		}
		files[name] = info
		return nil
	})
	return files, dirs, err
}

func (old mirrorEntry) matches(info fs.FileInfo, buffer ProjectBuffer, overlaid bool) bool {
	if !old.valid || old.overlay != overlaid {
		return false
	}
	if overlaid {
		return old.tabID == buffer.TabID && old.version == buffer.Version
	}
	return os.SameFile(old.diskInfo, info) && old.diskInfo.Size() == info.Size() && old.diskInfo.ModTime().Equal(info.ModTime())
}

func (m *projectMirror) update(ctx context.Context, name string, info fs.FileInfo, buffer ProjectBuffer, overlaid bool) error {
	old := m.files[name]
	next := mirrorEntry{overlay: overlaid, tabID: buffer.TabID, version: buffer.Version}
	if old.matches(info, buffer, overlaid) {
		return nil
	}
	if !overlaid {
		next.diskInfo = info
	}
	var input io.Reader = buffer.Reader
	var disk *os.File
	if !overlaid {
		path := filepath.Join(m.root, name)
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || resolved != path {
			return fmt.Errorf("typeset input cannot follow a symlink: %s", name)
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || !os.SameFile(st, info) || st.Size() != info.Size() || !st.ModTime().Equal(info.ModTime()) {
			return fmt.Errorf("typeset input changed while opening: %s", name)
		}
		input, disk = f, f
	}
	dest := filepath.Join(m.dir, name)
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	m.bytes -= old.size
	m.files[name] = next
	m.pending[name] = true
	remaining := int64(trialProjectBytes) - m.bytes
	n, copyErr := io.Copy(out, io.LimitReader(projectContextReader{ctx, input}, remaining+1))
	if disk != nil && copyErr == nil && n <= remaining {
		after, statErr := disk.Stat()
		if statErr != nil || n != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
			copyErr = fmt.Errorf("typeset input changed while copying: %s", name)
		}
	}
	if err := errors.Join(copyErr, out.Close()); err != nil || n > remaining {
		os.Remove(dest)
		if err != nil {
			return err
		}
		return errors.New("typeset project exceeds 512 MiB")
	}
	next.valid, next.size = true, n
	m.files[name] = next
	if !overlaid {
		delete(m.pending, name)
	}
	m.bytes += n
	return nil
}

type projectContextReader struct {
	ctx context.Context
	in  io.Reader
}

func (r projectContextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.in.Read(b)
}

// ProjectLog maps private input paths back to their source project.
func (s *Session) ProjectLog(text string) string {
	if s.mirror == nil {
		return text
	}
	return strings.NewReplacer(
		filepath.Join(s.dir, "preview.tex"), s.mirror.sourceMain,
		filepath.ToSlash(filepath.Join(s.dir, "preview.tex")), filepath.ToSlash(s.mirror.sourceMain),
		s.mirror.dir, s.mirror.sourceRoot,
		filepath.ToSlash(s.mirror.dir), filepath.ToSlash(s.mirror.sourceRoot),
	).Replace(text)
}

// LaTeX includes may write per-chapter auxiliary files under the output directory.
func (m *projectMirror) prepareOutputDirs(ctx context.Context) error {
	if m.output == "" || !IsLaTeXPath(m.main) {
		return nil
	}
	for _, name := range m.dirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(m.output, name), 0700); err != nil {
			return err
		}
		relative, err := filepath.Rel(filepath.Dir(m.main), filepath.Join(m.root, name))
		if err == nil && filepath.IsLocal(relative) && relative != "." {
			if err := os.MkdirAll(filepath.Join(m.output, relative), 0700); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *projectMirror) diskStable(ctx context.Context) bool {
	if len(m.pending) > 0 || m.refresh {
		return false
	}
	files, _, err := m.scanDisk(ctx)
	if err != nil || len(files) != len(m.files) {
		return false
	}
	for name, info := range files {
		if !m.files[name].matches(info, ProjectBuffer{}, false) {
			return false
		}
	}
	return ctx.Err() == nil
}

// CompileProjectPreview publishes a Finder snapshot only for a successfully
// compiled, disk-only revision whose inputs still match the private tree.
func (s *Session) CompileProjectPreview(ctx context.Context) (int, error) {
	n, err := s.CompileProjectTrial(ctx)
	if err == nil && runtime.GOOS == "darwin" && s.liveProject && len(s.mirror.pending) == 0 && ctx.Err() == nil {
		s.publishProjectQuickLook(ctx)
	}
	return n, err
}

func (s *Session) publishProjectQuickLook(ctx context.Context) {
	f, err := os.Open(s.path)
	if err != nil {
		return
	}
	defer f.Close()
	hash := sha256.New()
	const limit = 16 << 20
	n, err := io.Copy(hash, io.LimitReader(projectContextReader{ctx, f}, limit+1))
	if err != nil || n > limit {
		return
	}
	storeQuickLookIf(ctx, s.mirror.main, filepath.Join(s.dir, "pages", "current.pdf"), hash.Sum(nil), func() bool { return s.mirror.diskStable(ctx) })
}

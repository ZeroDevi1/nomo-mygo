package scholar

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
)

// InputStamp conservatively invalidates a manually observed PDF boundary when
// any project input changes. It reads metadata only and never follows symlinks.
func (p Project) InputStamp(ctx context.Context) (string, error) {
	hash := sha256.New()
	count := 0
	err := filepath.WalkDir(p.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".agents", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		count++
		if count > 20000 {
			return fmt.Errorf("project input metadata exceeds 20000 files")
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("manual PDF boundary cannot track symlink input %s", path)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		name, _ := filepath.Rel(p.Root, path)
		fmt.Fprintf(hash, "%s\x00%d\x00%d\n", name, info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

//go:build !windows

package fsutil

import "os"

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}

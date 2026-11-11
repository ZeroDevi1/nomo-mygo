//go:build !windows

package shell

import "time"

func scholarDoubleClickInterval() time.Duration {
	return 500 * time.Millisecond
}

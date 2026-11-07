//go:build !darwin

package shell

import (
	"context"
	"errors"
)

func scholarQuickLook(context.Context, string) error {
	return errors.New("Quick Look is available on macOS")
}

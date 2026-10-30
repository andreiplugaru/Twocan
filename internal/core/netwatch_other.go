//go:build !linux || android

package core

import (
	"context"
	"errors"
)

// watchNetlink is Linux only; elsewhere network changes are not noticed
// (on Android the app reports them).
func watchNetlink(context.Context, func()) error {
	return errors.New("not supported on this system")
}

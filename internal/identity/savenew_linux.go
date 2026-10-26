package identity

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// saveNew moves tmp to path, failing if path exists. renameat2's
// RENAME_NOREPLACE does that atomically, and unlike link(2) it is allowed
// on Android, which forbids apps to create hard links. Kernels or
// filesystems without RENAME_NOREPLACE fall back to linking.
func saveNew(tmp, path string) error {
	err := unix.Renameat2(unix.AT_FDCWD, tmp, unix.AT_FDCWD, path, unix.RENAME_NOREPLACE)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.ENOSYS), errors.Is(err, unix.EINVAL):
		return hardLink(tmp, path)
	}
	return &os.LinkError{Op: "rename", Old: tmp, New: path, Err: err}
}

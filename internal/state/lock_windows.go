//go:build windows

package state

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryLockFile takes a non-blocking exclusive LockFileEx region. The OS drops
// the lock when the handle is closed or the owning process dies.
func tryLockFile(file *os.File) error {
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, new(windows.Overlapped))
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errLockHeld
	}
	return err
}

func unlockFile(file *os.File) error {
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, new(windows.Overlapped))
}

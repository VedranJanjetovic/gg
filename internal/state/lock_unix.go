//go:build unix

package state

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLockFile takes a non-blocking exclusive flock. The kernel drops the lock
// when the descriptor is closed or the owning process dies.
func tryLockFile(file *os.File) error {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return errLockHeld
	}
	return err
}

func unlockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}

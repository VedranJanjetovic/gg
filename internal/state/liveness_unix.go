//go:build unix

package state

import (
	"errors"

	"golang.org/x/sys/unix"
)

// processAlive reports whether pid refers to a live process. Signal 0 probes
// existence without affecting the target; EPERM still proves it exists.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := unix.Kill(pid, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}

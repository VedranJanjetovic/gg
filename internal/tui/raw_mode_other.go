//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !aix

package tui

import "io"

// prepareRawMode is intentionally a no-op on platforms where the Unix
// terminal API is unavailable. Bubble Tea retains ownership of platform
// terminal setup there.
func prepareRawMode(io.Reader) (func() error, error) {
	return func() error { return nil }, nil
}

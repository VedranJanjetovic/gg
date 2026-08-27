//go:build unix

package cli

import (
	"os"
	"testing"
)

// assertMode fails when path does not exist or its permission bits differ from
// want. Configuration files carry a 0600 hardening guarantee, so it is asserted
// in full wherever POSIX permission bits exist.
func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %o, want %o", path, got, want)
	}
}

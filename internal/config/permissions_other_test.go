//go:build !unix

package config

import (
	"os"
	"testing"
)

// assertMode only checks existence where POSIX permission bits do not exist.
// Windows has no such bits and Go synthesizes 0666 for files and 0777 for
// directories, so the store's chmod calls are correct but unobservable there;
// the permission contract stays fully asserted on unix.
func assertMode(t *testing.T, path string, _ os.FileMode) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
}

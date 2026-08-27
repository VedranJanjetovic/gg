//go:build windows

package robustio_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/robustio"
	"golang.org/x/sys/windows"
)

// openWithoutShareDelete reproduces how a scanner holds a file: a read handle
// that withholds FILE_SHARE_DELETE, which makes replacing the file fail.
func openWithoutShareDelete(t *testing.T, path string) windows.Handle {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

func writeFiles(t *testing.T) (source, destination string) {
	t.Helper()
	dir := t.TempDir()
	source, destination = filepath.Join(dir, "source"), filepath.Join(dir, "destination")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	return source, destination
}

func TestRenameOutlastsATransientHandleOnTheDestination(t *testing.T) {
	source, destination := writeFiles(t)
	handle := openWithoutShareDelete(t, destination)
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(50 * time.Millisecond)
		_ = windows.CloseHandle(handle)
	}()
	t.Cleanup(func() { <-released })

	if err := robustio.Rename(source, destination); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new" {
		t.Fatalf("destination = %q, want %q", content, "new")
	}
}

func TestRenameGivesUpOnAHandleThatIsNeverReleased(t *testing.T) {
	source, destination := writeFiles(t)
	handle := openWithoutShareDelete(t, destination)
	defer windows.CloseHandle(handle)

	if err := robustio.Rename(source, destination); err == nil {
		t.Fatal("Rename() over a permanently held destination unexpectedly succeeded")
	}
}

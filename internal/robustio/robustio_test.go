package robustio_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/robustio"
)

func TestRenameReplacesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	destination := filepath.Join(dir, "destination")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

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
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source still present: err = %v", err)
	}
}

func TestRenameReportsMissingSource(t *testing.T) {
	dir := t.TempDir()
	if err := robustio.Rename(filepath.Join(dir, "absent"), filepath.Join(dir, "destination")); err == nil {
		t.Fatal("Rename() of a missing source unexpectedly succeeded")
	}
}

package git

import (
	"os"
	"path/filepath"
	"testing"
)

// NativeAbs builds a volume-qualified native absolute path out of path
// segments: "/repo/worktree" on Unix, `D:\repo\worktree` on Windows.
//
// The volume prefix is the whole point of this helper. On Windows a rooted
// path with no volume, such as "/repo", is drive-relative rather than
// absolute: filepath.IsAbs("/repo") is false, so the production guards in
// client.go, worktree.go, and remote.go reject it outright, and
// filepath.Abs("/repo") silently attaches the process's current drive. Every
// path argument this package accepts is normalized through filepath.Abs and
// filepath.Clean before it reaches a Command.Dir or a returned value, so a
// test literal must already carry the exact volume filepath.Abs would attach
// or the comparison can never match.
//
// Deriving the volume from the working directory is precisely what
// filepath.Abs does for a rooted, volume-less path, which gives the property
// TestNativeAbsIsAbsoluteAndAbsStable pins: NativeAbs(t, s...) is absolute,
// already clean, and a fixed point of filepath.Abs.
//
// It is exported so the external git_test package can use it; declaring it in
// an in-package _test.go file keeps it out of the shipped API.
func NativeAbs(t *testing.T, segments ...string) string {
	t.Helper()
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	// VolumeName is "" on Unix and e.g. "D:" on Windows, so root is "/" or `D:\`.
	root := filepath.VolumeName(workingDir) + string(filepath.Separator)
	return filepath.Join(append([]string{root}, segments...)...)
}

func TestNativeAbsIsAbsoluteAndAbsStable(t *testing.T) {
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, segments := range [][]string{{"repo"}, {"repo", "worktree"}, {"projects", ".gg-worktrees", "my-project"}} {
		got := NativeAbs(t, segments...)
		if !filepath.IsAbs(got) {
			t.Fatalf("NativeAbs(%q) = %q, want an absolute path", segments, got)
		}
		if filepath.VolumeName(got) != filepath.VolumeName(workingDir) {
			t.Fatalf("NativeAbs(%q) = %q, want volume %q", segments, got, filepath.VolumeName(workingDir))
		}
		if clean := filepath.Clean(got); clean != got {
			t.Fatalf("NativeAbs(%q) = %q, want the cleaned form %q", segments, got, clean)
		}
		// Production normalizes with filepath.Abs; the helper must be a fixed
		// point of it or no assertion against production output can match.
		abs, err := filepath.Abs(got)
		if err != nil {
			t.Fatal(err)
		}
		if abs != got {
			t.Fatalf("filepath.Abs(%q) = %q, want the input unchanged", got, abs)
		}
	}
}

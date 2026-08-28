//go:build unix

package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallShPlainCopiesSkills(t *testing.T) {
	rawRoot, err := os.MkdirTemp("", "gg-install-sh-")
	if err != nil {
		t.Fatalf("create test root: %v", err)
	}
	defer os.RemoveAll(rawRoot)
	root, err := filepath.EvalSymlinks(rawRoot)
	if err != nil {
		t.Fatalf("canonicalize test root: %v", err)
	}
	home := filepath.Join(root, "home")
	prefix := filepath.Join(root, "prefix")
	tmpDir := filepath.Join(root, "tmp")
	fakeBin := filepath.Join(root, "fake-bin")
	for _, path := range []string{home, prefix, tmpDir, fakeBin} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("create test directory %q: %v", path, err)
		}
	}

	archivePath := filepath.Join(root, "gg.tar.gz")
	binary := []byte("test gg binary\n")
	writeInstallArchive(t, archivePath, binary)

	const expectedURL = "https://example.invalid/gg-test.tar.gz"
	curlLog := filepath.Join(root, "curl.log")
	writeFakeCurl(t, filepath.Join(fakeBin, "curl"))

	legacy := map[string][]byte{
		filepath.Join(home, ".claude", "commands", "review.md"):        []byte("legacy claude command\n"),
		filepath.Join(home, ".claude", "skills", "review", "SKILL.md"): []byte("legacy claude skill\n"),
		filepath.Join(home, ".codex", "skills", "review", "SKILL.md"):  []byte("legacy codex skill\n"),
	}
	for path, want := range legacy {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create legacy parent for %q: %v", path, err)
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatalf("seed legacy file %q: %v", path, err)
		}
	}

	for _, path := range []string{home, prefix, tmpDir, fakeBin, archivePath, curlLog} {
		assertWithin(t, root, path)
	}
	pathValue := fakeBin + string(os.PathListSeparator) + os.Getenv("PATH")
	env := []string{
		"HOME=" + home,
		"TMPDIR=" + tmpDir,
		"GG_INSTALL_PREFIX=" + prefix,
		"GG_TEST_ARCHIVE=" + archivePath,
		"GG_TEST_CURL_LOG=" + curlLog,
		"GG_TEST_EXPECTED_URL=" + expectedURL,
		"PATH=" + pathValue,
	}
	result := RunWithTimeout(t, root, env, "bash", filepath.Join(moduleRoot(t), "install.sh"), "--url", expectedURL)
	if result.Err != nil {
		t.Fatalf("install.sh: %v\nstdout:\n%s\nstderr:\n%s", result.Err, result.Stdout, result.Stderr)
	}

	if got, err := os.ReadFile(filepath.Join(prefix, "gg")); err != nil {
		t.Fatalf("read installed binary: %v", err)
	} else if !bytes.Equal(got, binary) {
		t.Fatalf("installed binary = %q, want %q", got, binary)
	}

	module := moduleRoot(t)
	canonicalRoot := filepath.Join(module, "skills", "canonical")
	canonicalEntries, err := os.ReadDir(canonicalRoot)
	if err != nil {
		t.Fatalf("read canonical skill inventory: %v", err)
	}
	if len(canonicalEntries) != 20 {
		t.Fatalf("canonical skill count = %d, want 20", len(canonicalEntries))
	}
	var claudeAdapted, claudeFallback, codexAdapted, codexFallback bool
	for _, entry := range canonicalEntries {
		if !entry.IsDir() {
			t.Fatalf("canonical inventory entry %q is not a directory", entry.Name())
		}
		identity := entry.Name()
		canonical := filepath.Join(canonicalRoot, identity, identity+".md")
		claudeSource := filepath.Join(module, "skills", "claude", "commands", identity+".md")
		if _, err := os.Stat(claudeSource); os.IsNotExist(err) {
			claudeSource = canonical
			claudeFallback = true
		} else if err != nil {
			t.Fatalf("inspect Claude source for %q: %v", identity, err)
		} else {
			claudeAdapted = true
		}
		codexSource := filepath.Join(module, "skills", "codex", "skills", identity, "SKILL.md")
		if _, err := os.Stat(codexSource); os.IsNotExist(err) {
			codexSource = canonical
			codexFallback = true
		} else if err != nil {
			t.Fatalf("inspect Codex source for %q: %v", identity, err)
		} else {
			codexAdapted = true
		}

		claudeCommand := filepath.Join(home, ".claude", "commands", identity+".md")
		claudeSkill := filepath.Join(home, ".claude", "skills", identity, "SKILL.md")
		codexSkill := filepath.Join(home, ".codex", "skills", identity, "SKILL.md")
		for _, destination := range []string{claudeCommand, claudeSkill, codexSkill} {
			assertWithin(t, root, destination)
		}
		assertFileEqual(t, claudeSource, claudeCommand)
		assertFileEqual(t, claudeSource, claudeSkill)
		assertFileEqual(t, codexSource, codexSkill)
	}
	if !claudeAdapted || !claudeFallback || !codexAdapted || !codexFallback {
		t.Fatalf("skill inventory did not exercise adapted and canonical fallback sources: claude adapted=%t fallback=%t, codex adapted=%t fallback=%t", claudeAdapted, claudeFallback, codexAdapted, codexFallback)
	}

	patterns := filepath.Join(module, "skills", "core", "gg-coding-patterns.md")
	for _, destination := range []string{
		filepath.Join(home, ".claude", "commands", "gg-coding-patterns.md"),
		filepath.Join(home, ".claude", "skills", "gg-coding-patterns", "SKILL.md"),
		filepath.Join(home, ".codex", "skills", "gg-coding-patterns", "SKILL.md"),
		filepath.Join(home, ".gg", "gg-coding-patterns.md"),
	} {
		assertWithin(t, root, destination)
		assertFileEqual(t, patterns, destination)
	}

	// The installer must not leave a copy of itself behind. gg update fetches
	// the installer pinned to the release tag it installs; a persisted copy
	// could never be improved, because on update it was both the running script
	// and the source it copied from.
	if _, err := os.Stat(filepath.Join(home, ".gg", "install.sh")); !os.IsNotExist(err) {
		t.Fatalf("installer persisted a copy of itself at ~/.gg/install.sh: err=%v", err)
	}

	for path, want := range legacy {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read legacy file %q: %v", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("legacy file %q changed to %q", path, got)
		}
	}

	log, err := os.ReadFile(curlLog)
	if err != nil {
		t.Fatalf("read fake curl log: %v", err)
	}
	if got := strings.TrimSpace(string(log)); got != expectedURL {
		t.Fatalf("curl calls = %q, want one call for %q", got, expectedURL)
	}

	err = filepath.WalkDir(home, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if strings.Contains(entry.Name(), "gg-gg-") {
			t.Errorf("doubly prefixed destination exists: %q", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk installed home: %v", err)
	}
}

func TestInstallShRejectsUnexpectedDownloadURL(t *testing.T) {
	rawRoot, err := os.MkdirTemp("", "gg-install-sh-failure-")
	if err != nil {
		t.Fatalf("create test root: %v", err)
	}
	defer os.RemoveAll(rawRoot)
	root, err := filepath.EvalSymlinks(rawRoot)
	if err != nil {
		t.Fatalf("canonicalize test root: %v", err)
	}
	home := filepath.Join(root, "home")
	prefix := filepath.Join(root, "prefix")
	tmpDir := filepath.Join(root, "tmp")
	fakeBin := filepath.Join(root, "fake-bin")
	for _, path := range []string{home, prefix, tmpDir, fakeBin} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("create test directory %q: %v", path, err)
		}
	}

	archivePath := filepath.Join(root, "gg.tar.gz")
	writeInstallArchive(t, archivePath, []byte("unused\n"))
	const requestedURL = "https://example.invalid/unexpected.tar.gz"
	const expectedURL = "https://example.invalid/expected.tar.gz"
	curlLog := filepath.Join(root, "curl.log")
	writeFakeCurl(t, filepath.Join(fakeBin, "curl"))
	result := RunWithTimeout(t, root, []string{
		"HOME=" + home,
		"TMPDIR=" + tmpDir,
		"GG_INSTALL_PREFIX=" + prefix,
		"GG_TEST_ARCHIVE=" + archivePath,
		"GG_TEST_CURL_LOG=" + curlLog,
		"GG_TEST_EXPECTED_URL=" + expectedURL,
		"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
	}, "bash", filepath.Join(moduleRoot(t), "install.sh"), "--url", requestedURL)
	if result.Err == nil {
		t.Fatalf("install.sh succeeded for rejected URL; stdout:\n%s\nstderr:\n%s", result.Stdout, result.Stderr)
	}
	if _, err := os.Stat(filepath.Join(prefix, "gg")); !os.IsNotExist(err) {
		t.Fatalf("installed binary after rejected URL, stat error: %v", err)
	}
	if entries, err := os.ReadDir(home); err != nil {
		t.Fatalf("read home after rejected URL: %v", err)
	} else if len(entries) != 0 {
		t.Fatalf("skill files written after rejected URL: %v", entries)
	}
	log, err := os.ReadFile(curlLog)
	if err != nil {
		t.Fatalf("read fake curl log: %v", err)
	}
	if got := strings.TrimSpace(string(log)); got != requestedURL {
		t.Fatalf("curl calls = %q, want rejected URL %q", got, requestedURL)
	}
}

func TestInstallShRejectsRelativeHomeBeforeWrites(t *testing.T) {
	root := t.TempDir()
	prefix := filepath.Join(root, "prefix")
	tmpDir := filepath.Join(root, "tmp")
	fakeBin := filepath.Join(root, "fake-bin")
	archivePath := filepath.Join(root, "gg.tar.gz")
	curlLog := filepath.Join(root, "curl.log")
	for _, path := range []string{prefix, tmpDir, fakeBin} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("create test directory %q: %v", path, err)
		}
	}
	writeInstallArchive(t, archivePath, []byte("unused\n"))
	writeFakeCurl(t, filepath.Join(fakeBin, "curl"))

	const expectedURL = "https://example.invalid/gg-test.tar.gz"
	result := RunWithTimeout(t, root, []string{
		"HOME=relative-home",
		"TMPDIR=" + tmpDir,
		"GG_INSTALL_PREFIX=" + prefix,
		"GG_TEST_ARCHIVE=" + archivePath,
		"GG_TEST_CURL_LOG=" + curlLog,
		"GG_TEST_EXPECTED_URL=" + expectedURL,
		"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
	}, "bash", filepath.Join(moduleRoot(t), "install.sh"), "--url", expectedURL)
	if result.Err == nil {
		t.Fatalf("install.sh accepted relative HOME; stdout:\n%s\nstderr:\n%s", result.Stdout, result.Stderr)
	}
	if !strings.Contains(result.Stderr, "HOME must be an absolute path without newlines") {
		t.Fatalf("unexpected relative HOME error: %s", result.Stderr)
	}
	for _, path := range []string{
		filepath.Join(root, "relative-home"),
		filepath.Join(prefix, "gg"),
		curlLog,
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("installer wrote %q before rejecting HOME; stat error: %v", path, err)
		}
	}
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("read temporary directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("installer wrote temporary files before rejecting HOME: %v", entries)
	}
}

func TestInstallShDoesNotWriteThroughSkillDestinationSymlink(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.md")
	destinationDir := filepath.Join(root, "home", ".claude", "commands")
	destination := filepath.Join(destinationDir, "gg-review.md")
	target := filepath.Join(root, "outside.md")
	if err := os.WriteFile(source, []byte("new skill bytes\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	const original = "keep outside bytes\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	if err := os.MkdirAll(destinationDir, 0o755); err != nil {
		t.Fatalf("create destination directory: %v", err)
	}
	if err := os.Symlink(target, destination); err != nil {
		t.Fatalf("create destination symlink: %v", err)
	}

	result := RunWithTimeout(t, root, nil, "bash", "-c",
		`source "$1"; install_skill_file "$2" "$3"`, "install-skill-file",
		filepath.Join(moduleRoot(t), "install.sh"), source, destination)
	if result.Err == nil {
		t.Fatalf("install_skill_file followed destination symlink; stdout:\n%s\nstderr:\n%s", result.Stdout, result.Stderr)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read symlink target: %v", err)
	}
	if string(got) != original {
		t.Fatalf("symlink target changed to %q, want %q", got, original)
	}
	info, err := os.Lstat(destination)
	if err != nil {
		t.Fatalf("lstat destination: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("destination is no longer a symlink: mode %v", info.Mode())
	}
}

func writeInstallArchive(t *testing.T, path string, binary []byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "gg", Mode: 0o755, Size: int64(len(binary))}); err != nil {
		t.Fatalf("write archive header: %v", err)
	}
	if _, err := tarWriter.Write(binary); err != nil {
		t.Fatalf("write archive binary: %v", err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar archive: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip archive: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
}

func writeFakeCurl(t *testing.T, path string) {
	t.Helper()
	const script = `#!/bin/sh
set -eu
output=
url=
while [ "$#" -gt 0 ]; do
    case "$1" in
        --output) output=$2; shift 2 ;;
        --proto) shift 2 ;;
        -*) shift ;;
        *) url=$1; shift ;;
    esac
done
printf '%s\n' "$url" >> "$GG_TEST_CURL_LOG"
[ "$url" = "$GG_TEST_EXPECTED_URL" ] || exit 42
cp -- "$GG_TEST_ARCHIVE" "$output"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake curl: %v", err)
	}
}

func assertFileEqual(t *testing.T, source, destination string) {
	t.Helper()
	want, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source %q: %v", source, err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read destination %q: %v", destination, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("destination %q differs from source %q", destination, source)
	}
}

func assertWithin(t *testing.T, root, path string) {
	t.Helper()
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		t.Fatalf("path %q is outside test root %q", path, root)
	}
}

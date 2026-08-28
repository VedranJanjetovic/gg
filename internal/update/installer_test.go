package update

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// installedBinary creates a stand-in for the running gg executable and returns
// its path. The destination prefix is derived from this file, so every test that
// asserts on --prefix needs a real regular file on disk.
func installedBinary(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// resolvedDir is the destination prefix a test must expect for dir. The
// installer resolves symlinks, and on macOS t.TempDir() hands back a path under
// /var that is really a symlink to /private/var.
func resolvedDir(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// recordedRun captures what the installer would have executed. Nothing in this
// package's tests ever starts a real process.
type recordedRun struct {
	name  string
	args  []string
	calls int
}

func (r *recordedRun) record(_ context.Context, name string, args []string, _ io.Writer) error {
	r.calls++
	r.name, r.args = name, append([]string(nil), args...)
	return nil
}

// scriptInstallerForTest wires an installer whose executable, fetch, and run
// seams are all injected, so a test exercises the real Install sequence without
// touching the network or the host's installed binary.
func scriptInstallerForTest(t *testing.T, platform, executable string, run func(context.Context, string, []string, io.Writer) error) (*ScriptInstaller, *[]string) {
	t.Helper()
	var fetched []string
	return &ScriptInstaller{
		platform:   platform,
		env:        func(string) string { return "" },
		executable: func() (string, error) { return executable, nil },
		fetch: func(_ context.Context, url string) ([]byte, error) {
			fetched = append(fetched, url)
			return []byte("#!/usr/bin/env bash\nexit 0\n"), nil
		},
		run:     run,
		tempDir: t.TempDir(),
	}, &fetched
}

// This is the regression test for the staleness bug: the installer must be
// fetched from the tag being installed, so the installer and the binary it
// installs always come from the same commit. A moving branch tip or a copy
// persisted at first-install time both fail this assertion.
func TestInstallFetchesTheInstallerPinnedToTheReleaseTag(t *testing.T) {
	dir := t.TempDir()
	installer, fetched := scriptInstallerForTest(t, "linux", installedBinary(t, dir, "gg"), func(context.Context, string, []string, io.Writer) error { return nil })

	if err := installer.Install(context.Background(), "1.3.0"); err != nil {
		t.Fatal(err)
	}
	want := []string{installerSourceBase + "/gg-v1.3.0/install.sh"}
	if !reflect.DeepEqual(*fetched, want) {
		t.Fatalf("fetched=%v, want %v", *fetched, want)
	}
}

func TestInstallFetchesTheWindowsInstallerPinnedToTheReleaseTag(t *testing.T) {
	dir := t.TempDir()
	installer, fetched := scriptInstallerForTest(t, "windows", installedBinary(t, dir, "gg.exe"), func(context.Context, string, []string, io.Writer) error { return nil })

	if err := installer.Install(context.Background(), "1.3.0"); err != nil {
		t.Fatal(err)
	}
	want := []string{installerSourceBase + "/gg-v1.3.0/install.ps1"}
	if !reflect.DeepEqual(*fetched, want) {
		t.Fatalf("fetched=%v, want %v", *fetched, want)
	}
}

// The installer source override redirects the base URL but must never drop the
// tag path element, or an override would silently downgrade the fetch to a
// moving branch tip.
func TestInstallerSourceOverrideKeepsTheReleaseTagInThePath(t *testing.T) {
	dir := t.TempDir()
	installer, fetched := scriptInstallerForTest(t, "linux", installedBinary(t, dir, "gg"), func(context.Context, string, []string, io.Writer) error { return nil })
	installer.env = func(key string) string {
		if key == InstallerSourceEnv {
			return "http://127.0.0.1:8080/fixtures/"
		}
		return ""
	}

	if err := installer.Install(context.Background(), "1.3.0"); err != nil {
		t.Fatal(err)
	}
	want := []string{"http://127.0.0.1:8080/fixtures/gg-v1.3.0/install.sh"}
	if !reflect.DeepEqual(*fetched, want) {
		t.Fatalf("fetched=%v, want %v", *fetched, want)
	}
}

func TestInstallerSourceOverrideMustBeAnHTTPURL(t *testing.T) {
	dir := t.TempDir()
	installer, fetched := scriptInstallerForTest(t, "linux", installedBinary(t, dir, "gg"), func(context.Context, string, []string, io.Writer) error {
		t.Fatal("installer process invoked")
		return nil
	})
	installer.env = func(key string) string {
		if key == InstallerSourceEnv {
			return "/local/checkout"
		}
		return ""
	}

	err := installer.Install(context.Background(), "1.3.0")
	if err == nil || !strings.Contains(err.Error(), InstallerSourceEnv) {
		t.Fatalf("err=%v", err)
	}
	if len(*fetched) != 0 {
		t.Fatalf("fetched=%v, want no fetch", *fetched)
	}
}

// The argv shape is a contract with install.sh: --version pins the release and
// --prefix pins the destination. Passing --prefix is what stops an update from
// landing in the installer's default prefix while the gg on PATH stays stale.
func TestInstallRunsTheStagedScriptWithExplicitVersionAndPrefix(t *testing.T) {
	dir := t.TempDir()
	run := &recordedRun{}
	installer, _ := scriptInstallerForTest(t, "linux", installedBinary(t, dir, "gg"), run.record)

	if err := installer.Install(context.Background(), "1.3.0"); err != nil {
		t.Fatal(err)
	}
	if run.name != "bash" {
		t.Fatalf("command=%q, want bash", run.name)
	}
	if len(run.args) != 5 {
		t.Fatalf("args=%v", run.args)
	}
	if got := run.args[1:]; !reflect.DeepEqual(got, []string{"--version", "1.3.0", "--prefix", resolvedDir(t, dir)}) {
		t.Fatalf("args=%v", run.args)
	}
	if staged := run.args[0]; filepath.Dir(staged) != installer.tempDir || !strings.HasSuffix(staged, ".sh") {
		t.Fatalf("staged script=%q", staged)
	}
}

func TestInstallRunsPowerShellWithExplicitVersionAndPrefixOnWindows(t *testing.T) {
	dir := t.TempDir()
	run := &recordedRun{}
	installer, _ := scriptInstallerForTest(t, "windows", installedBinary(t, dir, "gg.exe"), run.record)

	if err := installer.Install(context.Background(), "1.3.0"); err != nil {
		t.Fatal(err)
	}
	if run.name != "powershell.exe" {
		t.Fatalf("command=%q, want powershell.exe", run.name)
	}
	if len(run.args) != 10 {
		t.Fatalf("args=%v", run.args)
	}
	if got := run.args[:5]; !reflect.DeepEqual(got, []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File"}) {
		t.Fatalf("args=%v", run.args)
	}
	if got := run.args[6:]; !reflect.DeepEqual(got, []string{"-Version", "1.3.0", "-Prefix", resolvedDir(t, dir)}) {
		t.Fatalf("args=%v", run.args)
	}
	// PowerShell -File rejects a script whose name does not end in .ps1, so the
	// staging pattern's extension is part of the contract.
	if staged := run.args[5]; !strings.HasSuffix(staged, ".ps1") {
		t.Fatalf("staged script=%q, want a .ps1 name", staged)
	}
}

// os.Executable is symlink-resolved on Linux but not on darwin, so the
// destination must be resolved explicitly or the two platforms would target
// different directories from the same installation.
func TestInstallPrefixIsTheDirectoryOfTheResolvedExecutable(t *testing.T) {
	real := t.TempDir()
	link := t.TempDir()
	binary := installedBinary(t, real, "gg")
	linked := filepath.Join(link, "gg")
	if err := os.Symlink(binary, linked); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	run := &recordedRun{}
	installer, _ := scriptInstallerForTest(t, "linux", linked, run.record)
	if err := installer.Install(context.Background(), "1.3.0"); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	if got := run.args[len(run.args)-1]; got != resolved {
		t.Fatalf("prefix=%q, want %q (the symlink target's directory)", got, resolved)
	}
}

func TestInstallRemovesTheStagedScriptOnSuccess(t *testing.T) {
	dir := t.TempDir()
	var staged string
	installer, _ := scriptInstallerForTest(t, "linux", installedBinary(t, dir, "gg"), func(_ context.Context, _ string, args []string, _ io.Writer) error {
		staged = args[0]
		if _, err := os.Stat(staged); err != nil {
			t.Fatalf("staged script missing while the installer runs: %v", err)
		}
		return nil
	})

	if err := installer.Install(context.Background(), "1.3.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged script %q still exists: %v", staged, err)
	}
}

func TestInstallRemovesTheStagedScriptWhenTheInstallerFails(t *testing.T) {
	dir := t.TempDir()
	var staged string
	installer, _ := scriptInstallerForTest(t, "linux", installedBinary(t, dir, "gg"), func(_ context.Context, _ string, args []string, output io.Writer) error {
		staged = args[0]
		_, _ = output.Write([]byte("download failed\n"))
		return errors.New("exit status 1")
	})

	err := installer.Install(context.Background(), "1.3.0")
	if err == nil || !strings.Contains(err.Error(), "download failed") {
		t.Fatalf("err=%v, want the installer output surfaced", err)
	}
	if _, statErr := os.Stat(staged); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("staged script %q still exists: %v", staged, statErr)
	}
}

// Every destination failure is reported. A silent fallback to a guessed
// directory is the defect class this whole change exists to remove.
func TestInstallReportsDestinationFailuresWithoutRunningTheInstaller(t *testing.T) {
	tests := []struct {
		name       string
		executable func(t *testing.T) (string, error)
		want       string
	}{
		{
			name:       "executable cannot be located",
			executable: func(*testing.T) (string, error) { return "", errors.New("no executable") },
			want:       "locate the running gg executable",
		},
		{
			name: "executable no longer exists",
			executable: func(t *testing.T) (string, error) {
				return filepath.Join(t.TempDir(), "gg"), nil
			},
			want: "resolve the running gg executable",
		},
		{
			name: "executable resolves to a directory",
			executable: func(t *testing.T) (string, error) {
				dir := filepath.Join(t.TempDir(), "gg")
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				return dir, nil
			},
			want: "is not a regular file",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			executable, execErr := tc.executable(t)
			installer := &ScriptInstaller{
				platform:   "linux",
				env:        func(string) string { return "" },
				executable: func() (string, error) { return executable, execErr },
				fetch: func(context.Context, string) ([]byte, error) {
					t.Fatal("installer fetched despite an unresolved destination")
					return nil, nil
				},
				run: func(context.Context, string, []string, io.Writer) error {
					t.Fatal("installer process invoked")
					return nil
				},
				tempDir: t.TempDir(),
			}
			err := installer.Install(context.Background(), "1.3.0")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// An unwritable destination is the realistic failure for a gg installed into a
// root-owned directory. It must be reported before the download, and the message
// must name the directory and the remedy.
func TestInstallReportsAnUnwritableDestinationBeforeFetching(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions do not gate writes on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	binary := installedBinary(t, dir, "gg")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	installer := &ScriptInstaller{
		platform:   "linux",
		env:        func(string) string { return "" },
		executable: func() (string, error) { return binary, nil },
		fetch: func(context.Context, string) ([]byte, error) {
			t.Fatal("installer fetched despite an unwritable destination")
			return nil, nil
		},
		run: func(context.Context, string, []string, io.Writer) error {
			t.Fatal("installer process invoked")
			return nil
		},
		tempDir: t.TempDir(),
	}
	err := installer.Install(context.Background(), "1.3.0")
	if err == nil {
		t.Fatal("expected an error for an unwritable destination")
	}
	for _, want := range []string{resolvedDir(t, dir), "GG_INSTALL_PREFIX"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

// A body that is not a script must never reach bash. The realistic case is a
// GitHub 404 HTML page served for a tag that does not exist.
func TestInstallRefusesAFetchedBodyThatIsNotAScript(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		err  error
		want string
	}{
		{name: "fetch failure", err: errors.New("HTTP 404"), want: "fetch installer"},
		{name: "empty body", body: []byte("   \n"), want: "is empty"},
		{name: "html body", body: []byte("\n<!DOCTYPE html>\n<html>404</html>\n"), want: "returned a document rather than a script"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			installer := &ScriptInstaller{
				platform:   "linux",
				env:        func(string) string { return "" },
				executable: func() (string, error) { return installedBinary(t, dir, "gg"), nil },
				fetch:      func(context.Context, string) ([]byte, error) { return tc.body, tc.err },
				run: func(context.Context, string, []string, io.Writer) error {
					t.Fatal("installer process invoked with a non-script body")
					return nil
				},
				tempDir: t.TempDir(),
			}
			err := installer.Install(context.Background(), "1.3.0")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestInstallRejectsAnUnsupportedPlatformBeforeFetching(t *testing.T) {
	installer := &ScriptInstaller{
		platform:   "plan9",
		env:        func(string) string { return "" },
		executable: func() (string, error) { t.Fatal("destination resolved for an unsupported platform"); return "", nil },
		fetch:      func(context.Context, string) ([]byte, error) { t.Fatal("installer fetched"); return nil, nil },
		run:        func(context.Context, string, []string, io.Writer) error { t.Fatal("process invoked"); return nil },
	}
	err := installer.Install(context.Background(), "1.3.0")
	if err == nil || !strings.Contains(err.Error(), `unsupported installer platform "plan9"`) {
		t.Fatalf("err=%v", err)
	}
}

func TestInstallRejectsAnEmptyVersion(t *testing.T) {
	installer := &ScriptInstaller{
		platform:   "linux",
		env:        func(string) string { return "" },
		executable: func() (string, error) { t.Fatal("destination resolved for an empty version"); return "", nil },
		fetch:      func(context.Context, string) ([]byte, error) { t.Fatal("installer fetched"); return nil, nil },
		run:        func(context.Context, string, []string, io.Writer) error { t.Fatal("process invoked"); return nil },
	}
	if err := installer.Install(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "empty release version") {
		t.Fatalf("err=%v", err)
	}
}

func TestInstallCancellationPreventsFetchAndProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	installer := &ScriptInstaller{
		platform:   "linux",
		env:        func(string) string { return "" },
		executable: func() (string, error) { t.Fatal("destination resolved after cancellation"); return "", nil },
		fetch: func(context.Context, string) ([]byte, error) {
			t.Fatal("installer fetched after cancellation")
			return nil, nil
		},
		run: func(context.Context, string, []string, io.Writer) error {
			t.Fatal("installer process invoked after cancellation")
			return nil
		},
	}
	if err := installer.Install(ctx, "1.3.0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestNewScriptInstallerWiresProductionSeams(t *testing.T) {
	installer := NewScriptInstaller()
	if installer.platform == "" || installer.env == nil || installer.executable == nil || installer.fetch == nil || installer.run == nil {
		t.Fatalf("installer=%#v", installer)
	}
}

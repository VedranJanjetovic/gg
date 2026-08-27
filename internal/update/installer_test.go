package update

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// trustedInstallerPath returns the configured installer path in the host's own
// absolute form. installer.go validates with filepath.IsAbs, which is the host
// implementation, while the unix argv shape under test comes from the injected
// platform field and stays assertable from any host.
func trustedInstallerPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs("/trusted/gg-tool/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPlatformInstallerUsesExplicitUnixArgvAndConfiguredPath(t *testing.T) {
	var gotName string
	var gotArgs []string
	installerPath := trustedInstallerPath(t)
	runner := &PlatformInstaller{
		platform: "linux",
		env: func(key string) string {
			if key == InstallerPathEnv {
				return installerPath
			}
			return ""
		},
		run: func(_ context.Context, name string, args []string, _ io.Writer) error {
			gotName, gotArgs = name, append([]string(nil), args...)
			return nil
		},
	}
	if err := runner.Install(context.Background(), "1.3.0", []string{"--version", "1.3.0"}); err != nil {
		t.Fatal(err)
	}
	if gotName != "bash" || !reflect.DeepEqual(gotArgs, []string{installerPath, "--version", "1.3.0"}) {
		t.Fatalf("command=%q args=%v", gotName, gotArgs)
	}
}

// persistedInstallerHome builds a home directory holding the trusted installer
// copy that the platform installers write at install time.
func persistedInstallerHome(t *testing.T, installerName string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".gg")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, installerName), []byte("#!/usr/bin/env bash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestPlatformInstallerUsesPersistedCopyWhenOverrideUnset(t *testing.T) {
	var gotName string
	var gotArgs []string
	home := persistedInstallerHome(t, "install.sh")
	runner := &PlatformInstaller{
		platform: "linux",
		env:      func(string) string { return "" },
		home:     func() (string, error) { return home, nil },
		run: func(_ context.Context, name string, args []string, _ io.Writer) error {
			gotName, gotArgs = name, append([]string(nil), args...)
			return nil
		},
	}
	if err := runner.Install(context.Background(), "1.3.0", []string{"--version", "1.3.0"}); err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(home, ".gg", "install.sh"), "--version", "1.3.0"}
	if gotName != "bash" || !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("command=%q args=%v", gotName, gotArgs)
	}
}

func TestPlatformInstallerUsesPersistedWindowsInstaller(t *testing.T) {
	var gotName string
	var gotArgs []string
	home := persistedInstallerHome(t, "install.ps1")
	runner := &PlatformInstaller{
		platform: "windows",
		env:      func(string) string { return "" },
		home:     func() (string, error) { return home, nil },
		run: func(_ context.Context, name string, args []string, _ io.Writer) error {
			gotName, gotArgs = name, append([]string(nil), args...)
			return nil
		},
	}
	if err := runner.Install(context.Background(), "1.3.0", []string{"--version", "1.3.0"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".gg", "install.ps1")
	want := []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", path, "-Version", "1.3.0"}
	if gotName != "powershell.exe" || !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("command=%q args=%v", gotName, gotArgs)
	}
}

// A missing trusted copy must name both the expected path and the override, so
// the message states the action instead of only the unmet precondition.
func TestPlatformInstallerReportsMissingPersistedInstaller(t *testing.T) {
	home := t.TempDir()
	runner := &PlatformInstaller{
		platform: "linux",
		env:      func(string) string { return "" },
		home:     func() (string, error) { return home, nil },
		run:      func(context.Context, string, []string, io.Writer) error { t.Fatal("process invoked"); return nil },
	}
	err := runner.Install(context.Background(), "1.0.0", []string{"--version", "1.0.0"})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{filepath.Join(home, ".gg", "install.sh"), InstallerPathEnv, "re-run the gg installer"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestPlatformInstallerRejectsRelativeOverride(t *testing.T) {
	runner := &PlatformInstaller{
		platform: "linux",
		env:      func(string) string { return "install.sh" },
		home:     func() (string, error) { t.Fatal("home consulted despite override"); return "", nil },
		run:      func(context.Context, string, []string, io.Writer) error { t.Fatal("process invoked"); return nil },
	}
	err := runner.Install(context.Background(), "1.0.0", []string{"--version", "1.0.0"})
	if err == nil || !strings.Contains(err.Error(), InstallerPathEnv) {
		t.Fatalf("err=%v", err)
	}
}

func TestPlatformInstallerCancellationPreventsProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	installerPath := trustedInstallerPath(t)
	runner := &PlatformInstaller{platform: "linux", env: func(string) string { return installerPath }, run: func(context.Context, string, []string, io.Writer) error {
		called = true
		return errors.New("must not run")
	}}
	if err := runner.Install(ctx, "1.0.0", []string{"--version", "1.0.0"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if called {
		t.Fatal("installer process invoked after cancellation")
	}
}

package update

import (
	"context"
	"errors"
	"io"
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

func TestPlatformInstallerRequiresExplicitAbsolutePath(t *testing.T) {
	runner := &PlatformInstaller{platform: "linux", env: func(string) string { return "" }, run: func(context.Context, string, []string, io.Writer) error { t.Fatal("process invoked"); return nil }}
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

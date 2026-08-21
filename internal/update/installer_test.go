package update

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestPlatformInstallerUsesExplicitUnixArgvAndConfiguredPath(t *testing.T) {
	var gotName string
	var gotArgs []string
	runner := &PlatformInstaller{
		platform: "linux",
		env: func(key string) string {
			if key == InstallerPathEnv {
				return "/trusted/gg-tool/install.sh"
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
	if gotName != "bash" || !reflect.DeepEqual(gotArgs, []string{"/trusted/gg-tool/install.sh", "--version", "1.3.0"}) {
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
	runner := &PlatformInstaller{platform: "linux", env: func(string) string { return "/trusted/install.sh" }, run: func(context.Context, string, []string, io.Writer) error {
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

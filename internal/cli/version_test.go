package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/version"
)

func TestVersionCommandDoesNotNeedConfiguration(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := New(WithVersion(version.Metadata{Version: "v1.2.3", Commit: "abc123", Date: "2026-08-04T12:00:00Z"}))
	if code := app.Run(context.Background(), []string{"version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, stderr.String())
	}
	want := "gg version v1.2.3 (commit abc123, build date 2026-08-04T12:00:00Z)\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestVersionFlagMatchesVersionCommand(t *testing.T) {
	metadata := version.Metadata{Version: "v2.0.0", Commit: "fedcba9", Date: "2026-08-04T13:14:15Z"}
	for _, args := range [][]string{{"--version"}, {"version"}} {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := New(WithVersion(metadata)).Run(context.Background(), args, &stdout, &stderr); code != 0 {
				t.Fatalf("exit code = %d, stderr=%q", code, stderr.String())
			}
			for _, want := range []string{"v2.0.0", "fedcba9", "2026-08-04T13:14:15Z"} {
				if !strings.Contains(stdout.String(), want) {
					t.Fatalf("stdout = %q, missing %q", stdout.String(), want)
				}
			}
		})
	}
}

func TestVersionRejectsArguments(t *testing.T) {
	for _, args := range [][]string{{"version", "extra"}, {"--version", "extra"}, {"--version", "--help"}} {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := New().Run(context.Background(), args, &stdout, &stderr); code == 0 {
				t.Fatalf("exit code = 0, stdout=%q", stdout.String())
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "version") {
				t.Fatalf("stdout=%q stderr=%q, want version argument error", stdout.String(), stderr.String())
			}
		})
	}
}

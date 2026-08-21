//go:build unix

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunCapturesBothStreamsAndExitCode(t *testing.T) {
	result := RunWithTimeout(t, t.TempDir(), nil, "sh", "-c", "printf out; printf err >&2; exit 7")
	if result.Err == nil || result.ExitCode != 7 {
		t.Fatalf("result = %+v, want exit 7", result)
	}
	if result.Stdout != "out" || result.Stderr != "err" {
		t.Fatalf("captured output = %q/%q", result.Stdout, result.Stderr)
	}
}

func TestRunHonorsCancellation(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "descendant.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	result := Run(ctx, dir, nil, "sh", "-c", "sleep 10 & child=$!; printf \"%s\\n\" \"$child\" > \"$1\"; wait", "sh", pidFile)
	elapsed := time.Since(started)
	if result.Err == nil || result.ExitCode == 0 {
		t.Fatalf("result = %+v, want cancellation failure", result)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("cancellation elapsed %s, want bounded process-tree shutdown", elapsed)
	}
	pidData, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read descendant pid: %v", err)
	}
	pid := strings.TrimSpace(string(pidData))
	if pid == "" || processExists(pid) {
		t.Fatalf("descendant process %q still exists after cancellation", pid)
	}
}

//go:build !unix

package e2e

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestRunCapturesBothStreamsAndExitCode(t *testing.T) {
	result := RunWithTimeout(t, t.TempDir(), []string{"GG_E2E_HELPER=streams"}, os.Args[0])
	if result.Err == nil || result.ExitCode != 7 {
		t.Fatalf("result = %+v, want exit 7", result)
	}
	if result.Stdout != "out" || result.Stderr != "err" {
		t.Fatalf("captured output = %q/%q", result.Stdout, result.Stderr)
	}
}

func TestRunHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	result := Run(ctx, t.TempDir(), []string{"GG_E2E_HELPER=cancel"}, os.Args[0])
	if result.Err == nil || result.ExitCode == 0 {
		t.Fatalf("result = %+v, want cancellation failure", result)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("cancellation elapsed %s, want bounded leader shutdown", elapsed)
	}
}

func TestRunHonorsDescendantCancellation(t *testing.T) {
	t.Skip("non-Unix fallback terminates only the leader and cannot prove descendant cleanup")
}

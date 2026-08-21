//go:build !linux

package e2e

import (
	"errors"
	"io"
	"os/exec"
	"testing"
)

func networkDeniedSupported() bool { return false }

func networkDeniedCommand(string, []string, string, ...string) (*exec.Cmd, error) {
	return nil, errors.New("network-denied E2E helper is Linux-only")
}

func RunWithInputNetworkDeniedTimeout(t *testing.T, _ string, _ []string, _ io.Reader, _ string, _ ...string) CommandResult {
	t.Helper()
	t.Skip("network-denied E2E helper is Linux-only")
	return CommandResult{ExitCode: -1}
}

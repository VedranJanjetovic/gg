//go:build !linux

package e2e

import (
	"io"
	"os"
	"os/exec"
	"testing"
)

func networkDeniedSupported() bool { return false }

func networkDeniedCommand(dir string, env []string, name string, args ...string) (*exec.Cmd, error) {
	merged := mergeEnv(os.Environ(), env)
	cmd := exec.Command(resolveCommand(name, merged), args...)
	cmd.Dir = dir
	cmd.Env = merged
	configureCommand(cmd)
	return cmd, nil
}

func RunWithInputNetworkDeniedTimeout(t *testing.T, dir string, env []string, input io.Reader, name string, args ...string) CommandResult {
	t.Helper()
	return runWithTimeout(t, dir, env, input, name, args...)
}

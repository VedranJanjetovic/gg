//go:build !unix

package e2e

import "os/exec"

// Non-Unix has no portable process-group primitive here; this only stops the leader.
func configureCommand(_ *exec.Cmd) {}
func terminateCommand(cmd *exec.Cmd) error {
	if cmd != nil && cmd.Process != nil {
		return cmd.Process.Kill()
	}
	return nil
}

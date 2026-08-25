//go:build windows

package e2e

import (
	"fmt"
	"os/exec"
	"strconv"
)

func configureCommand(_ *exec.Cmd) {}

func terminateCommand(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	result := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
	if output, err := result.CombinedOutput(); err != nil {
		return fmt.Errorf("taskkill process tree: %w: %s", err, output)
	}
	return nil
}

//go:build unix

package agent

import (
	"os/exec"
	"syscall"
	"time"
)

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

const processTerminationGracePeriod = 250 * time.Millisecond

func terminateProcessGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		if err == syscall.ESRCH {
			return nil
		}
		return err
	}

	// Give cooperative processes a short, bounded opportunity to exit. The
	// process group is used rather than only the leader so descendants cannot
	// keep output pipes open indefinitely.
	time.Sleep(processTerminationGracePeriod)
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

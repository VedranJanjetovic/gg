//go:build unix

package agent

import (
	"os/exec"
	"syscall"
	"time"
)

// processGroup owns the lifetime of a child and its descendants. On unix it is
// a POSIX process group whose leader is the child: signalling the negated PID
// reaches every descendant that has not created its own group.
type processGroup struct {
	pid int
}

func newProcessGroup() *processGroup { return &processGroup{} }

// configure runs before the child is started and makes it a group leader.
func (g *processGroup) configure(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// attach runs after a successful start, once the leader PID exists.
func (g *processGroup) attach(cmd *exec.Cmd) error {
	g.pid = cmd.Process.Pid
	return nil
}

const processTerminationGracePeriod = 250 * time.Millisecond

func (g *processGroup) terminate() error {
	if err := syscall.Kill(-g.pid, syscall.SIGTERM); err != nil {
		if err == syscall.ESRCH {
			return nil
		}
		return err
	}

	// Give cooperative processes a short, bounded opportunity to exit. The
	// process group is used rather than only the leader so descendants cannot
	// keep output pipes open indefinitely.
	time.Sleep(processTerminationGracePeriod)
	if err := syscall.Kill(-g.pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

// release is a no-op: a unix process group holds no operating system resource
// once its members are gone.
func (g *processGroup) release() {}

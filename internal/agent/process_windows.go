//go:build windows

package agent

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processGroup owns a job object holding the child and every process the child
// spawns. Windows has no process-group signal, so a job is the only way to stop
// a tree: TerminateJobObject kills all members atomically and cannot be ignored
// the way a signal can, and JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE guarantees the
// tree dies with gg even if gg is killed without unwinding.
type processGroup struct {
	mu       sync.Mutex
	job      windows.Handle
	released bool
}

func newProcessGroup() *processGroup { return &processGroup{} }

// configure is a no-op on Windows: the job can only be joined once the child
// exists, which happens in attach.
func (g *processGroup) configure(cmd *exec.Cmd) {}

// attach creates the job and puts the freshly started child in it. Processes
// the child spawns from that point on inherit the job. A grandchild spawned in
// the window between start and assignment would escape it; closing that window
// requires starting suspended, which os/exec does not expose.
func (g *processGroup) attach(cmd *exec.Cmd) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("configure job object: %w", err)
	}
	// os/exec still holds a handle to the child until Wait, so the PID is
	// reserved and cannot have been recycled for another process here.
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("open child process: %w", err)
	}
	defer func() { _ = windows.CloseHandle(process) }()
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("assign process to job object: %w", err)
	}
	g.job = job
	return nil
}

func (g *processGroup) terminate() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.job == 0 || g.released {
		return nil
	}
	if err := windows.TerminateJobObject(g.job, 1); err != nil && !isJobAlreadyGone(err) {
		return err
	}
	return nil
}

// release drops the job handle once the tree is done. It must not run while the
// child is alive: the kill-on-close limit would take the tree down with it.
func (g *processGroup) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.job == 0 || g.released {
		return
	}
	g.released = true
	_ = windows.CloseHandle(g.job)
}

// isJobAlreadyGone reports errors that mean the job has already been torn down.
// Terminating a job whose processes have all exited succeeds, but a job racing
// its own teardown can report these instead, and neither is a failure to stop.
func isJobAlreadyGone(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_INVALID_HANDLE)
}

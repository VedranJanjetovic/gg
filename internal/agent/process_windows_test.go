//go:build windows

package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// stillActiveExitCode is STILL_ACTIVE: the exit code Windows reports while a
// process is running.
const stillActiveExitCode = 259

func TestExecProcessFactoryCancellationKillsDescendantTree(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &capturedLogs{}
	process, err := NewExecProcessFactory(nil, logs).Start(ctx, ProcessSpec{
		Command:          os.Args[0],
		Args:             []string{"-test.run=TestFakeAgentProcess", "--", "descendant"},
		WorkingDirectory: t.TempDir(),
		Env:              []string{"GO_WANT_FAKE_AGENT_PROCESS=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !logs.contains("stdout", "child-started") {
		if time.Now().After(deadline) {
			t.Fatal("descendant did not start")
		}
		time.Sleep(time.Millisecond)
	}
	leaderPID := processPID(logs.value("stdout"), "leader-ready")
	descendantPID := processPID(logs.value("stdout"), "child-started")
	if leaderPID == 0 || descendantPID == 0 {
		t.Fatalf("readiness output did not contain both PIDs: %q", logs.value("stdout"))
	}

	waitDone := make(chan error, 1)
	go func() {
		_, waitErr := process.Wait()
		waitDone <- waitErr
	}()
	cancel()
	select {
	case waitErr := <-waitDone:
		if waitErr == nil {
			t.Fatal("canceled process unexpectedly reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("descendant prevented process completion")
	}
	waitForProcessExit(t, leaderPID)
	waitForProcessExit(t, descendantPID)
}

func runPlatformFakeAgent(t *testing.T, mode string) {
	t.Helper()
	if mode != "descendant" {
		return
	}
	_, _ = os.Stdout.WriteString(fmt.Sprintf("leader-ready pid=%d\n", os.Getpid()))
	child := exec.Command(os.Args[0], "-test.run=TestFakeAgentProcess", "--", "block")
	child.Env = append(os.Environ(), "GO_WANT_FAKE_AGENT_PROCESS=1")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	_, _ = os.Stdout.WriteString(fmt.Sprintf("child-started pid=%d\n", child.Process.Pid))
	for {
		time.Sleep(time.Second)
	}
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processRunning(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d remained alive after cancellation", pid)
}

// processRunning reports whether the process still exists and has not exited.
// A handle held by a parent keeps an exited process openable, so the exit code
// rather than the open itself decides.
func processRunning(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	return code == stillActiveExitCode
}

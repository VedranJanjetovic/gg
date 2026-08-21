//go:build unix

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecProcessFactoryCancellationKillsSIGTERMIgnoringDescendant(t *testing.T) {
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
	t.Cleanup(func() {
		_ = syscall.Kill(leaderPID, syscall.SIGKILL)
		_ = syscall.Kill(descendantPID, syscall.SIGKILL)
	})

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
		t.Fatal("SIGTERM-ignoring descendant prevented process completion")
	}
	waitForProcessExit(t, leaderPID)
	waitForProcessExit(t, descendantPID)
}

func runPlatformFakeAgent(t *testing.T, mode string) {
	t.Helper()
	switch mode {
	case "descendant":
		_, _ = os.Stdout.WriteString(fmt.Sprintf("leader-ready pid=%d\n", os.Getpid()))
		child := exec.Command(os.Args[0], "-test.run=TestFakeAgentProcess", "--", "ignore-term")
		child.Env = append(os.Environ(), "GO_WANT_FAKE_AGENT_PROCESS=1")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		_, _ = os.Stdout.WriteString(fmt.Sprintf("child-started pid=%d\n", child.Process.Pid))
		for {
			time.Sleep(time.Second)
		}
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
		_, _ = os.Stdout.WriteString(fmt.Sprintf("descendant-ready pid=%d\n", os.Getpid()))
		for {
			time.Sleep(time.Second)
		}
	}
}

func processPID(output, marker string) int {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == marker && strings.HasPrefix(fields[1], "pid=") {
			var pid int
			_, _ = fmt.Sscanf(fields[1], "pid=%d", &pid)
			return pid
		}
	}
	return 0
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d remained alive after cancellation", pid)
}

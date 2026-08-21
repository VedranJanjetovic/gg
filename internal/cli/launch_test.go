package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/state"
)

type launchCall struct {
	executable string
	args       []string
	cwd        string
}

type captureLauncher struct {
	calls []launchCall
	err   error
}

func (l *captureLauncher) Launch(_ context.Context, executable string, args []string, cwd string) error {
	l.calls = append(l.calls, launchCall{executable: executable, args: append([]string(nil), args...), cwd: cwd})
	return l.err
}

func TestLaunchActionsUseProjectWorktreeAndExactArgv(t *testing.T) {
	launcher := &captureLauncher{}
	actions := NewLaunchActions(launcher, "code", "kitty", []string{"--wait", "--", "literal value"})
	project := state.ProjectState{WorktreePath: "/tmp/project worktree"}

	if err := actions.OpenCode(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if err := actions.OpenTerminal(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	want := []launchCall{
		{executable: "code", args: []string{"/tmp/project worktree"}, cwd: "/tmp/project worktree"},
		{executable: "kitty", args: []string{"--wait", "--", "literal value"}, cwd: "/tmp/project worktree"},
	}
	if !reflect.DeepEqual(launcher.calls, want) {
		t.Fatalf("launch calls = %#v, want %#v", launcher.calls, want)
	}
}

func TestLaunchActionsReturnActionableConfigurationAndLaunchErrors(t *testing.T) {
	project := state.ProjectState{WorktreePath: "/tmp/project"}
	if err := NewLaunchActions(&captureLauncher{}, "code", "", nil).OpenTerminal(context.Background(), project); err == nil || err.Error() != "launch configured terminal: set TERMINAL to the terminal executable" {
		t.Fatalf("terminal configuration error = %v", err)
	}
	launchErr := errors.New("permission denied")
	if err := NewLaunchActions(&captureLauncher{err: launchErr}, "code", "kitty", nil).OpenCode(context.Background(), project); !errors.Is(err, launchErr) || err.Error() != `launch Visual Studio Code in "/tmp/project": permission denied` {
		t.Fatalf("launch error = %v", err)
	}
	if err := NewLaunchActions(&captureLauncher{}, "code", "kitty", nil).OpenCode(context.Background(), state.ProjectState{}); err == nil || err.Error() != "launch Visual Studio Code: project worktree path is empty" {
		t.Fatalf("empty worktree error = %v", err)
	}
}

func TestExecCommandLauncherReapsStartedProcess(t *testing.T) {
	previousCommandContext := commandContext
	var started *exec.Cmd
	commandContext = func(ctx context.Context, executable string, args ...string) *exec.Cmd {
		started = exec.CommandContext(ctx, executable, args...)
		return started
	}
	t.Cleanup(func() { commandContext = previousCommandContext })

	if err := (ExecCommandLauncher{}).Launch(context.Background(), "/bin/sh", []string{"-c", "exit 0"}, t.TempDir()); err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if started == nil || started.Process == nil {
		t.Fatal("Launch() did not expose a started process")
	}

	procPath := fmt.Sprintf("/proc/%d", started.Process.Pid)
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := os.Stat(procPath)
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			t.Fatalf("stat child process: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("child process %d was not reaped", started.Process.Pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOpenTerminalExpandsWorktreePlaceholder(t *testing.T) {
	launcher := &captureLauncher{}
	actions := NewLaunchActions(launcher, "code", "open", []string{"-a", "Terminal", WorktreePlaceholder})
	project := state.ProjectState{WorktreePath: "/tmp/worktree"}
	if err := actions.OpenTerminal(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 1 {
		t.Fatalf("calls = %#v, want one launch", launcher.calls)
	}
	call := launcher.calls[0]
	if want := []string{"-a", "Terminal", "/tmp/worktree"}; !reflect.DeepEqual(call.args, want) {
		t.Fatalf("terminal args = %#v, want %#v", call.args, want)
	}
	if call.cwd != "/tmp/worktree" {
		t.Fatalf("cwd = %q, want the worktree", call.cwd)
	}
}

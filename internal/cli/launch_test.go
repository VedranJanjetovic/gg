package cli

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
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

// launchChildEnv marks the re-executed test binary as the child process spawned
// by TestExecCommandLauncherReapsStartedProcess.
const launchChildEnv = "GG_TEST_LAUNCH_CHILD"

// TestLaunchedChildProcess is not a test: it is the portable child process for
// TestExecCommandLauncherReapsStartedProcess. It exits non-zero so the
// launcher's Wait goroutine has something to report once it reaps the child.
func TestLaunchedChildProcess(t *testing.T) {
	if os.Getenv(launchChildEnv) != "1" {
		t.Skip("helper process for TestExecCommandLauncherReapsStartedProcess")
	}
	os.Exit(7)
}

// lockedBuffer collects log output written by the launcher goroutine while the
// test polls it.
type lockedBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

func TestExecCommandLauncherReapsStartedProcess(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	previousCommandContext := commandContext
	var started *exec.Cmd
	commandContext = func(ctx context.Context, executable string, args ...string) *exec.Cmd {
		started = exec.CommandContext(ctx, executable, args...)
		started.Env = append(os.Environ(), launchChildEnv+"=1")
		return started
	}
	t.Cleanup(func() { commandContext = previousCommandContext })

	// The launcher reaps the child in a goroutine whose only observable signal
	// on every platform is the log line it writes once Wait returns. Windows
	// has no zombies and macOS has no /proc, so process-table probing cannot
	// express this property portably.
	logs := &lockedBuffer{}
	previousWriter, previousFlags := log.Writer(), log.Flags()
	log.SetOutput(logs)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(previousWriter); log.SetFlags(previousFlags) })

	if err := (ExecCommandLauncher{}).Launch(context.Background(), self, []string{"-test.run=TestLaunchedChildProcess"}, t.TempDir()); err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if started == nil || started.Process == nil {
		t.Fatal("Launch() did not expose a started process")
	}

	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(logs.String(), "wait for launched") {
		if time.Now().After(deadline) {
			t.Fatalf("child process %d was not reaped; log = %q", started.Process.Pid, logs.String())
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

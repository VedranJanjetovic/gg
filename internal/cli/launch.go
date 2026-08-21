package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"

	"github.com/VedranJanjetovic/gg/internal/state"
)

// CommandLauncher is the process boundary for external project tools. Arguments
// and working directory are passed separately; no shell is involved.
type CommandLauncher interface {
	Launch(context.Context, string, []string, string) error
}

// ExecCommandLauncher starts an external process using os/exec.
type ExecCommandLauncher struct{}

var commandContext = exec.CommandContext

func (ExecCommandLauncher) Launch(ctx context.Context, executable string, args []string, cwd string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(executable) == "" {
		return errors.New("launch executable is not configured")
	}
	command := commandContext(ctx, executable, args...)
	command.Dir = cwd
	if err := command.Start(); err != nil {
		return fmt.Errorf("start %q in %q: %w", executable, cwd, err)
	}
	go func() {
		if err := command.Wait(); err != nil {
			log.Printf("wait for launched %q: %v", executable, err)
		}
	}()
	return nil
}

// WorktreePlaceholder in configured terminal arguments is replaced with the
// project's worktree path at launch time. Needed for launchers like macOS
// `open -a Terminal`, which ignore the spawned process working directory and
// require the target directory as an argument.
const WorktreePlaceholder = "{worktree}"

// LaunchActions holds the configured external tools for one project session.
type LaunchActions struct {
	launcher           CommandLauncher
	codeExecutable     string
	terminalExecutable string
	terminalArgs       []string
}

// NewLaunchActions constructs project launch actions. The production root supplies
// terminalExecutable from the conventional TERMINAL environment setting; this
// package does not invent terminal-specific flags or shell parsing.
func NewLaunchActions(launcher CommandLauncher, codeExecutable, terminalExecutable string, terminalArgs []string) *LaunchActions {
	return &LaunchActions{launcher: launcher, codeExecutable: codeExecutable, terminalExecutable: terminalExecutable, terminalArgs: append([]string(nil), terminalArgs...)}
}

func (a *LaunchActions) OpenCode(ctx context.Context, project state.ProjectState) error {
	return a.launch(ctx, "Visual Studio Code", a.codeExecutable, []string{project.WorktreePath}, project)
}

func (a *LaunchActions) OpenTerminal(ctx context.Context, project state.ProjectState) error {
	return a.launch(ctx, "configured terminal", a.terminalExecutable, a.terminalArgs, project)
}

func (a *LaunchActions) launch(ctx context.Context, label, executable string, args []string, project state.ProjectState) error {
	if a == nil || a.launcher == nil {
		return fmt.Errorf("launch %s: launcher is not configured", label)
	}
	if strings.TrimSpace(executable) == "" {
		if label == "configured terminal" {
			return errors.New("launch configured terminal: set TERMINAL to the terminal executable")
		}
		return fmt.Errorf("launch %s: executable is not configured", label)
	}
	if strings.TrimSpace(project.WorktreePath) == "" {
		return fmt.Errorf("launch %s: project worktree path is empty", label)
	}
	resolved := make([]string, len(args))
	for i, arg := range args {
		resolved[i] = strings.ReplaceAll(arg, WorktreePlaceholder, project.WorktreePath)
	}
	if err := a.launcher.Launch(ctx, executable, resolved, project.WorktreePath); err != nil {
		return fmt.Errorf("launch %s in %q: %w", label, project.WorktreePath, err)
	}
	return nil
}

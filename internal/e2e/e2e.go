// Package e2e contains small, disposable fixtures for tests that exercise the
// real gg executable without depending on a developer's machine.
package e2e

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const defaultCommandTimeout = 30 * time.Second

// Environment is an isolated process environment for a gg invocation.
type Environment struct {
	Root       string
	Home       string
	ConfigHome string
	Bin        string
	path       string
}

// NewEnvironment creates disposable HOME, XDG_CONFIG_HOME, and fake-bin roots.
// Project state remains at the CLI's supported configured repository root.
func NewEnvironment(t *testing.T) *Environment {
	t.Helper()
	root := t.TempDir()
	env := &Environment{
		Root: root, Home: filepath.Join(root, "home"),
		ConfigHome: filepath.Join(root, "config"), Bin: fakeBinDir(t),
	}
	for _, dir := range []string{env.Home, env.ConfigHome} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create isolated environment directory %q: %v", dir, err)
		}
	}
	env.path = env.Bin + string(os.PathListSeparator) + os.Getenv("PATH")
	return env
}

// Env returns deterministic overrides suitable for exec.Cmd.Env.
func (e *Environment) Env() []string {
	if e == nil {
		return nil
	}
	return []string{"HOME=" + e.Home, "XDG_CONFIG_HOME=" + e.ConfigHome,
		"PATH=" + e.path, "NO_COLOR=1"}
}

// CommandResult captures a completed subprocess, including both output streams.
type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
}

// Run executes a bounded subprocess with inherited environment plus overrides.
func Run(ctx context.Context, dir string, env []string, name string, args ...string) CommandResult {
	return run(ctx, dir, env, nil, name, args...)
}

// RunWithInput executes a subprocess with deterministic stdin and isolated environment.
func RunWithInput(ctx context.Context, dir string, env []string, input io.Reader, name string, args ...string) CommandResult {
	return run(ctx, dir, env, input, name, args...)
}

func run(ctx context.Context, dir string, env []string, input io.Reader, name string, args ...string) CommandResult {
	if ctx == nil {
		ctx = context.Background()
	}
	merged := mergeEnv(os.Environ(), env)
	commandPath := resolveCommand(name, merged)
	if err := ctx.Err(); err != nil {
		return CommandResult{Err: err, ExitCode: -1}
	}
	cmd := exec.Command(commandPath, args...)
	configureCommand(cmd)
	cmd.Dir = dir
	cmd.Env = merged
	cmd.Stdin = input
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return CommandResult{Stdout: stdout.String(), Stderr: stderr.String(), Err: err, ExitCode: -1}
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	var err error
	select {
	case err = <-waitCh:
	case <-ctx.Done():
		_ = terminateCommand(cmd)
		err = <-waitCh
		err = errors.Join(err, ctx.Err())
	}
	result := CommandResult{Stdout: stdout.String(), Stderr: stderr.String(), Err: err}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	} else if err != nil {
		result.ExitCode = -1
	}
	return result
}

// RunWithTimeout executes a subprocess with the fixture's default bound.
func RunWithTimeout(t *testing.T, dir string, env []string, name string, args ...string) CommandResult {
	t.Helper()
	return runWithTimeout(t, dir, env, nil, name, args...)
}

// RunWithInput executes a bounded subprocess with deterministic stdin.
func RunWithInputTimeout(t *testing.T, dir string, env []string, input io.Reader, name string, args ...string) CommandResult {
	t.Helper()
	return runWithTimeout(t, dir, env, input, name, args...)
}

func runWithTimeout(t *testing.T, dir string, env []string, input io.Reader, name string, args ...string) CommandResult {
	ctx, cancel := context.WithTimeout(context.Background(), defaultCommandTimeout)
	defer cancel()
	return run(ctx, dir, env, input, name, args...)
}

// BuildBinary builds the real cmd/gg executable outside the source tree.
func BuildBinary(t *testing.T) string {
	t.Helper()
	output := filepath.Join(t.TempDir(), "gg")
	result := RunWithTimeout(t, moduleRoot(t), nil, "go", "build", "-o", output, "./cmd/gg")
	if result.Err != nil {
		t.Fatalf("build gg: %v\nstdout:\n%s\nstderr:\n%s", result.Err, result.Stdout, result.Stderr)
	}
	return output
}

// Run invokes the real gg binary with isolated HOME, XDG_CONFIG_HOME, and PATH.
// The CLI persists project state under its configured repository root.
func (e *Environment) Run(t *testing.T, binary string, args ...string) CommandResult {
	t.Helper()
	return RunWithTimeout(t, e.Root, e.Env(), binary, args...)
}

// RunWithInput invokes the real gg binary with deterministic stdin.
func (e *Environment) RunWithInput(t *testing.T, dir, input, binary string, args ...string) CommandResult {
	t.Helper()
	return RunWithInputTimeout(t, dir, e.Env(), strings.NewReader(input), binary, args...)
}

// GitRepository is a disposable repository with a committed initial branch.
type GitRepository struct{ Root string }

// NewGitRepository initializes a local repository with a deterministic commit.
func NewGitRepository(t *testing.T) *GitRepository {
	t.Helper()
	repo := &GitRepository{Root: t.TempDir()}
	repo.git(t, "init", "--initial-branch=main")
	repo.git(t, "config", "user.name", "gg e2e")
	repo.git(t, "config", "user.email", "gg-e2e@example.invalid")
	if err := os.WriteFile(filepath.Join(repo.Root, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatalf("write initial repository file: %v", err)
	}
	repo.git(t, "add", "README.md")
	repo.git(t, "commit", "-m", "initial fixture")
	return repo
}

// Worktree creates an attached worktree on a new local branch.
func (r *GitRepository) Worktree(t *testing.T, branch string) string {
	t.Helper()
	if r == nil || r.Root == "" {
		t.Fatal("git repository is required")
	}
	if strings.TrimSpace(branch) == "" {
		t.Fatal("git worktree branch is required")
	}
	path := filepath.Join(t.TempDir(), "worktree")
	r.git(t, "worktree", "add", "-b", branch, path, "main")
	return path
}

func (r *GitRepository) git(t *testing.T, args ...string) string {
	t.Helper()
	result := RunWithTimeout(t, r.Root, nil, "git", args...)
	if result.Err != nil {
		t.Fatalf("git %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), result.Err, result.Stdout, result.Stderr)
	}
	return strings.TrimSpace(result.Stdout)
}

func fakeBinDir(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate e2e fixture source")
	}
	return filepath.Join(filepath.Dir(source), "..", "..", "testdata", "fake-bin")
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate e2e fixture source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}

func resolveCommand(name string, env []string) string {
	if strings.ContainsRune(name, os.PathSeparator) {
		return name
	}
	pathValue := ""
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key == "PATH" {
			pathValue = value
			break
		}
	}
	for _, dir := range filepath.SplitList(pathValue) {
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate
		}
	}
	return name
}

func mergeEnv(base, overrides []string) []string {
	values := make(map[string]string, len(base)+len(overrides))
	order := make([]string, 0, len(base)+len(overrides))
	for _, entry := range append(append([]string(nil), base...), overrides...) {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = value
	}
	result := make([]string, 0, len(order))
	for _, key := range order {
		result = append(result, key+"="+values[key])
	}
	return result
}

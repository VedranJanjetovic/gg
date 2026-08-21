//go:build linux || darwin

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvironmentUsesDisposableRootsAndDeterministicFakeAgent(t *testing.T) {
	env := NewEnvironment(t)
	logPath := filepath.Join(env.Root, "agent.log")
	result := RunWithTimeout(t, env.Root, append(env.Env(), "GG_FAKE_AGENT_LOG="+logPath), "claude", "--print", "hello")
	if result.Err != nil {
		t.Fatalf("fake claude failed: %v\nstderr: %s", result.Err, result.Stderr)
	}
	if got, want := result.Stdout, "fake-claude: deterministic response\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake-agent log: %v", err)
	}
	if got := string(log); !strings.Contains(got, "agent=claude\n") || !strings.Contains(got, "arg=--print\n") || !strings.Contains(got, "arg=hello\n") {
		t.Fatalf("unexpected fake-agent log: %q", got)
	}
	for _, dir := range []string{env.Home, env.ConfigHome} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("isolated root %q is not a directory: %v", dir, err)
		}
	}
}

func TestGitRepositoryCreatesCommittedRepositoryAndWorktree(t *testing.T) {
	repo := NewGitRepository(t)
	if _, err := os.Stat(filepath.Join(repo.Root, ".git")); err != nil {
		t.Fatalf("repository metadata missing: %v", err)
	}
	worktree := repo.Worktree(t, "fixture/branch")
	if _, err := os.Stat(filepath.Join(worktree, "README.md")); err != nil {
		t.Fatalf("worktree file missing: %v", err)
	}
	result := RunWithTimeout(t, worktree, nil, "git", "branch", "--show-current")
	if result.Err != nil || strings.TrimSpace(result.Stdout) != "fixture/branch" {
		t.Fatalf("branch result = %+v", result)
	}
}

func TestBuildBinaryAndRunRealCLI(t *testing.T) {
	binary := BuildBinary(t)
	env := NewEnvironment(t)
	result := env.Run(t, binary, "--help")
	if result.Err != nil {
		t.Fatalf("real gg --help failed: %v\nstderr: %s", result.Err, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "Usage") {
		t.Fatalf("help output = %q", result.Stdout)
	}
}

//go:build linux || darwin

package e2e

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/state"
)

func TestRealCLILifecycleCommandsExposeAndPruneDurableProjectEvidence(t *testing.T) {
	env := NewEnvironment(t)
	repo := NewGitRepository(t)
	binary := BuildBinary(t)
	processEnv := append(env.Env(), "GG_FAKE_AGENT_LOG="+filepath.Join(env.Root, "agent.log"))

	configure := RunWithInputTimeout(t, repo.Root, processEnv, strings.NewReader("codex\ngpt-5\nhigh\n"), binary, "configure")
	if configure.Err != nil || configure.ExitCode != 0 {
		t.Fatalf("configure result = %+v\nstdout:\n%s\nstderr:\n%s", configure, configure.Stdout, configure.Stderr)
	}
	createProject(t, repo.Root, processEnv, binary, "keep_me")
	createProject(t, repo.Root, processEnv, binary, "remove_me")

	store, err := state.NewFileStore(repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	finished, err := store.Load(context.Background(), "remove-me")
	if err != nil {
		t.Fatalf("load project to finish: %v", err)
	}
	finished.Status = state.StatusFinished
	finished.StatusChangedAt = finished.UpdatedAt
	if err := store.Save(context.Background(), finished); err != nil {
		t.Fatalf("persist finished fixture: %v", err)
	}
	proofPath := filepath.Join(repo.Root, ".gg", "projects", finished.Slug, "artifacts", "PROOF.md")
	if err := os.MkdirAll(filepath.Dir(proofPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proofPath, []byte("# durable fixture proof\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(repo.Root, ".gg", "projects", finished.Slug, "state.json")
	eventsPath := filepath.Join(repo.Root, ".gg", "projects", finished.Slug, "events.jsonl")
	for _, path := range []string{statePath, eventsPath, proofPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("durable evidence %q is missing before prune: %v", path, err)
		}
	}

	listed := RunWithTimeout(t, repo.Root, processEnv, binary, "list")
	if listed.Err != nil || listed.ExitCode != 0 {
		t.Fatalf("list result = %+v", listed)
	}
	if !strings.Contains(listed.Stdout, "keep_me") || strings.Contains(listed.Stdout, "remove_me") {
		t.Fatalf("default list output = %q", listed.Stdout)
	}
	all := RunWithTimeout(t, repo.Root, processEnv, binary, "list", "--all")
	if all.Err != nil || all.ExitCode != 0 {
		t.Fatalf("list --all result = %+v", all)
	}
	if !strings.Contains(all.Stdout, "keep_me") || !strings.Contains(all.Stdout, "remove_me") {
		t.Fatalf("list --all output = %q", all.Stdout)
	}

	status := RunWithTimeout(t, repo.Root, processEnv, binary, "status")
	if status.Err != nil || status.ExitCode != 0 {
		t.Fatalf("status result = %+v", status)
	}
	for _, field := range []string{"NAME", "STATUS", "CURRENT PHASE", "BRANCH", "WORKTREE", "UPDATED", "keep_me", "remove_me"} {
		if !strings.Contains(status.Stdout, field) {
			t.Fatalf("status output missing %q: %q", field, status.Stdout)
		}
	}
	detail := RunWithTimeout(t, repo.Root, processEnv, binary, "status", "keep_me")
	if detail.Err != nil || detail.ExitCode != 0 {
		t.Fatalf("status detail result = %+v", detail)
	}
	for _, field := range []string{"Name: keep_me", "Status: failed", "Current phase: acceptance_criteria", "Branch: gg/keep-me", "Worktree:", "Updated:"} {
		if !strings.Contains(detail.Stdout, field) {
			t.Fatalf("status detail missing %q: %q", field, detail.Stdout)
		}
	}

	declined := RunWithInputTimeout(t, repo.Root, processEnv, strings.NewReader("n\n"), binary, "prune")
	if declined.Err != nil || declined.ExitCode != 0 || !strings.Contains(declined.Stdout, "Prune cancelled.") {
		t.Fatalf("declined prune result = %+v", declined)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("declined prune removed durable state: %v", err)
	}

	pruned := RunWithTimeout(t, repo.Root, processEnv, binary, "prune", "--yes")
	if pruned.Err != nil || pruned.ExitCode != 0 {
		t.Fatalf("prune result = %+v", pruned)
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("finished project state still exists: %v", err)
	}
	for _, path := range []string{eventsPath, proofPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("finished project durable evidence was removed with state: %q: %v", path, err)
		}
	}
	if _, err := os.Stat(finished.WorktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("finished project worktree still exists: %v", err)
	}
	kept, err := store.Load(context.Background(), "keep-me")
	if err != nil {
		t.Fatalf("unrelated active project was removed: %v", err)
	}
	if _, err := os.Stat(kept.WorktreePath); err != nil {
		t.Fatalf("unrelated active worktree was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.Root, ".gg", "projects", "keep-me", "events.jsonl")); err != nil {
		t.Fatalf("unrelated event journal was removed: %v", err)
	}
}

func createProject(t *testing.T, root string, env []string, binary, goal string) {
	t.Helper()
	result := RunWithInputTimeout(t, root, env, strings.NewReader(goal+"\nRecord durable evidence.\n\n"), binary, "run")
	if result.ExitCode == 0 {
		t.Fatalf("run %q unexpectedly completed: %+v", goal, result)
	}
	if !strings.Contains(result.Stdout, "Describe the project") {
		t.Fatalf("run %q output = %q", goal, result.Stdout)
	}
}

package git_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/git"
)

type fakeExecutor struct {
	commands []git.Command
	output   string
	err      error
}

func (f *fakeExecutor) Execute(ctx context.Context, command git.Command) (string, error) {
	f.commands = append(f.commands, command)
	if f.err != nil {
		return "", f.err
	}
	return f.output, nil
}

func TestRepositoryRootValidatesAndReturnsGitRoot(t *testing.T) {
	executor := &fakeExecutor{output: "/repo\n"}
	client := git.NewClient("/repo/project", executor)

	root, err := client.RepositoryRoot(context.Background())
	if err != nil {
		t.Fatalf("RepositoryRoot() error = %v", err)
	}
	if root != "/repo" {
		t.Fatalf("RepositoryRoot() = %q, want /repo", root)
	}
	want := git.Command{Dir: "/repo/project", Name: "git", Args: []string{"rev-parse", "--show-toplevel"}}
	if !reflect.DeepEqual(executor.commands, []git.Command{want}) {
		t.Fatalf("commands = %#v, want %#v", executor.commands, []git.Command{want})
	}
}

func TestRepositoryRootRejectsNonGitDirectory(t *testing.T) {
	executor := &fakeExecutor{err: errors.New("not a git repository")}
	client := git.NewClient("/tmp/not-a-repository", executor)

	_, err := client.RepositoryRoot(context.Background())
	if err == nil {
		t.Fatal("RepositoryRoot() error = nil, want rejection")
	}
	if !strings.Contains(err.Error(), "validate git repository") || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("error = %q, want validation context and cause", err)
	}
}

func TestRepositoryRootPropagatesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	executor := &fakeExecutor{output: "/repo"}
	client := git.NewClient("/repo", executor)

	_, err := client.RepositoryRoot(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(executor.commands) != 0 {
		t.Fatalf("commands = %#v, want no command after cancellation", executor.commands)
	}
}

func TestDryRunConstructsCommandsWithoutExecuting(t *testing.T) {
	executor := &fakeExecutor{output: "unexpected"}
	client := git.NewClient("/repo", executor, git.WithDryRun())

	if _, err := client.Status(context.Background()); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	commands := client.Commands()
	want := []git.Command{
		{Dir: "/repo", Name: "git", Args: []string{"rev-parse", "--show-toplevel"}},
		{Dir: "/repo", Name: "git", Args: []string{"status", "--short", "--branch"}},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
	if len(executor.commands) != 0 {
		t.Fatalf("executor commands = %#v, want none in dry-run", executor.commands)
	}
}

func TestStatusPropagatesCommandFailure(t *testing.T) {
	executor := &fakeExecutor{output: "/repo\n", err: errors.New("git failed")}
	client := git.NewClient("/repo", executor)

	_, err := client.Status(context.Background())
	if err == nil || !strings.Contains(err.Error(), "validate git repository") || !errors.Is(err, executor.err) {
		t.Fatalf("error = %v, want validation failure context", err)
	}
}

func TestIsUncommittedNewFileUsesSafeGitStatusPathspec(t *testing.T) {
	executor := &fakeExecutor{output: "?? PROOF.md\x00"}
	client := git.NewClient("/repo", executor)

	got, err := client.IsUncommittedNewFile(context.Background(), "/repo/worktree", "PROOF.md")
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("IsUncommittedNewFile() = false, want true")
	}
	want := git.Command{Dir: "/repo/worktree", Name: "git", Args: []string{"status", "--porcelain=v1", "--untracked-files=all", "--ignored=matching", "-z", "--", "PROOF.md"}}
	if !reflect.DeepEqual(executor.commands, []git.Command{want}) {
		t.Fatalf("commands = %#v, want %#v", executor.commands, []git.Command{want})
	}
}

func TestIsUncommittedNewFileRejectsUnsafePath(t *testing.T) {
	for _, path := range []string{"../PROOF.md", "/tmp/PROOF.md", "./PROOF.md"} {
		t.Run(path, func(t *testing.T) {
			executor := &fakeExecutor{output: "?? PROOF.md\x00"}
			client := git.NewClient("/repo", executor)
			if _, err := client.IsUncommittedNewFile(context.Background(), "/repo/worktree", path); err == nil {
				t.Fatalf("path %q was accepted", path)
			}
			if len(executor.commands) != 0 {
				t.Fatalf("commands = %#v, want none", executor.commands)
			}
		})
	}
}

func TestIsUncommittedNewFilePropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	executor := &fakeExecutor{output: "?? PROOF.md\x00"}
	client := git.NewClient("/repo", executor)
	if _, err := client.IsUncommittedNewFile(ctx, "/repo/worktree", "PROOF.md"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(executor.commands) != 0 {
		t.Fatalf("commands = %#v, want none", executor.commands)
	}
}

func TestRepositoryRootRejectsNilClient(t *testing.T) {
	var client *git.Client

	_, err := client.RepositoryRoot(context.Background())
	if err == nil || err.Error() != "git client is nil" {
		t.Fatalf("RepositoryRoot() error = %v, want nil-client error", err)
	}
}

func TestRepositoryRootRejectsActualNonGitDirectory(t *testing.T) {
	client := git.NewClient(t.TempDir(), nil)

	_, err := client.RepositoryRoot(context.Background())
	if err == nil {
		t.Fatal("RepositoryRoot() error = nil, want non-git directory rejection")
	}
	if !strings.Contains(err.Error(), "validate git repository") {
		t.Fatalf("error = %q, want validation context", err)
	}
}

func TestHasUnresolvedConflictsUsesGitConflictIndex(t *testing.T) {
	executor := &fakeExecutor{output: "conflicted.txt\n"}
	client := git.NewClient("/repo", executor)
	got, err := client.HasUnresolvedConflicts(context.Background(), "/repo/worktree")
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("HasUnresolvedConflicts() = false, want true")
	}
	want := git.Command{Dir: "/repo/worktree", Name: "git", Args: []string{"diff", "--name-only", "--diff-filter=U", "--"}}
	if !reflect.DeepEqual(executor.commands, []git.Command{want}) {
		t.Fatalf("commands = %#v, want %#v", executor.commands, []git.Command{want})
	}
}

func TestHasUnresolvedConflictsReturnsFalseForCleanIndex(t *testing.T) {
	client := git.NewClient("/repo", &fakeExecutor{})
	got, err := client.HasUnresolvedConflicts(context.Background(), "/repo/worktree")
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("HasUnresolvedConflicts() = true, want false")
	}
}

type queuedExecutor struct {
	outputs []string
	errs    []error
	calls   []git.Command
}

func (e *queuedExecutor) Execute(_ context.Context, command git.Command) (string, error) {
	e.calls = append(e.calls, command)
	i := len(e.calls) - 1
	if i < len(e.errs) && e.errs[i] != nil {
		return "", e.errs[i]
	}
	if i < len(e.outputs) {
		return e.outputs[i], nil
	}
	return "", nil
}

func TestVerifyUnsignedDevelopmentCommitAcceptsAdvancedUnsignedHead(t *testing.T) {
	executor := &queuedExecutor{outputs: []string{"old-head\n", "", "newer-head\x00N\nnew-head\x00N\n"}}
	client := git.NewClient("/repo", executor)
	previous, err := client.HeadCommit(context.Background(), "/repo/worktree")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.VerifyUnsignedDevelopmentCommit(context.Background(), "/repo/worktree", previous); err != nil {
		t.Fatal(err)
	}
	want := []git.Command{
		{Dir: "/repo/worktree", Name: "git", Args: []string{"rev-parse", "HEAD"}},
		{Dir: "/repo/worktree", Name: "git", Args: []string{"merge-base", "--is-ancestor", "old-head", "HEAD"}},
		{Dir: "/repo/worktree", Name: "git", Args: []string{"log", "--format=%H%x00%G?", "old-head..HEAD"}},
	}
	if !reflect.DeepEqual(executor.calls, want) {
		t.Fatalf("commands = %#v, want %#v", executor.calls, want)
	}
}

func TestVerifyUnsignedDevelopmentCommitRejectsSignedHead(t *testing.T) {
	client := git.NewClient("/repo", &queuedExecutor{outputs: []string{"", "signed-head\x00G\n"}})
	if err := client.VerifyUnsignedDevelopmentCommit(context.Background(), "/repo/worktree", "old-head"); err == nil || !strings.Contains(err.Error(), "signed") {
		t.Fatalf("error = %v, want signed-commit rejection", err)
	}
}

func TestVerifyUnsignedDevelopmentCommitRejectsUnchangedHead(t *testing.T) {
	client := git.NewClient("/repo", &queuedExecutor{outputs: []string{"", ""}})
	if err := client.VerifyUnsignedDevelopmentCommit(context.Background(), "/repo/worktree", "old-head"); err == nil || !strings.Contains(err.Error(), "did not create a commit") {
		t.Fatalf("error = %v, want no-commit rejection", err)
	}
}

func TestVerifyUnsignedDevelopmentCommitsAllowsNoCommitForFailedProcess(t *testing.T) {
	client := git.NewClient("/repo", &queuedExecutor{outputs: []string{"", ""}})
	if err := client.VerifyUnsignedDevelopmentCommits(context.Background(), "/repo/worktree", "old-head", false); err != nil {
		t.Fatalf("VerifyUnsignedDevelopmentCommits() error = %v, want unchanged failed process accepted", err)
	}
}

func TestVerifyUnsignedDevelopmentCommitRejectsUnrelatedHead(t *testing.T) {
	ancestorErr := errors.New("not ancestor")
	executor := &queuedExecutor{errs: []error{ancestorErr}}
	client := git.NewClient("/repo", executor)
	err := client.VerifyUnsignedDevelopmentCommit(context.Background(), "/repo/worktree", "old-head")
	if err == nil || !errors.Is(err, ancestorErr) || !strings.Contains(err.Error(), "not an ancestor") {
		t.Fatalf("error = %v, want unrelated-head rejection", err)
	}
	if len(executor.calls) != 1 || executor.calls[0].Args[0] != "merge-base" {
		t.Fatalf("commands = %#v, want ancestry check only", executor.calls)
	}
}

func TestVerifyUnsignedDevelopmentCommitRejectsSignedIntermediateCommit(t *testing.T) {
	client := git.NewClient("/repo", &queuedExecutor{outputs: []string{"", "head\x00N\nintermediate\x00G\n"}})
	err := client.VerifyUnsignedDevelopmentCommit(context.Background(), "/repo/worktree", "old-head")
	if err == nil || !strings.Contains(err.Error(), "intermediate") || !strings.Contains(err.Error(), "signed") {
		t.Fatalf("error = %v, want signed intermediate rejection", err)
	}
}

func TestVerifyUnsignedDevelopmentCommitInTemporaryRepository(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "gg@example.test")
	runGit(t, repo, "config", "user.name", "gg test")
	if err := os.WriteFile(filepath.Join(repo, "initial.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "initial.txt")
	runGit(t, repo, "-c", "commit.gpgsign=false", "commit", "-qm", "initial")
	client := git.NewClient(repo, nil)
	previous, err := client.HeadCommit(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"one.txt", "two.txt"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo, "add", name)
		runGit(t, repo, "-c", "commit.gpgsign=false", "commit", "-qm", fmt.Sprintf("change %d", i+1))
	}
	if err := client.VerifyUnsignedDevelopmentCommit(context.Background(), repo, previous); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func TestVerifyUnsignedDevelopmentCommitPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	executor := &queuedExecutor{outputs: []string{"new-head\x00N\n"}}
	client := git.NewClient("/repo", executor)
	if err := client.VerifyUnsignedDevelopmentCommit(ctx, "/repo/worktree", "old-head"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("commands = %#v, want none after cancellation", executor.calls)
	}
}

func TestPushBranchUsesExactArgv(t *testing.T) {
	executor := &fakeExecutor{}
	client := git.NewClient("/repo", executor)
	if err := client.PushBranchToRemote(context.Background(), "/repo/worktree", "origin", "feature/name"); err != nil {
		t.Fatal(err)
	}
	want := git.Command{Dir: "/repo/worktree", Name: "git", Args: []string{"push", "origin", "feature/name"}}
	if !reflect.DeepEqual(executor.commands, []git.Command{want}) {
		t.Fatalf("commands = %#v, want %#v", executor.commands, []git.Command{want})
	}
}

func TestAutoCommitUncommittedChangesCommitsDirtyWorktreeAndSkipsClean(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "gg@example.test")
	runGit(t, repo, "config", "user.name", "gg test")
	if err := os.WriteFile(filepath.Join(repo, "initial.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "initial.txt")
	runGit(t, repo, "-c", "commit.gpgsign=false", "commit", "-qm", "initial")
	client := git.NewClient(repo, nil)
	previous, err := client.HeadCommit(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}

	// Agent forgot to commit: one modified tracked file, one new untracked file.
	if err := os.WriteFile(filepath.Join(repo, "initial.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "game.js"), []byte("code\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := client.AutoCommitUncommittedChanges(context.Background(), repo, "gg: development/implementation"); err != nil {
		t.Fatal(err)
	}
	// The safety-net commit satisfies the development subphase contract.
	if err := client.VerifyUnsignedDevelopmentCommit(context.Background(), repo, previous); err != nil {
		t.Fatalf("auto-commit did not satisfy the commit contract: %v", err)
	}

	// A clean worktree is a no-op: HEAD must not advance again.
	head, err := client.HeadCommit(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.AutoCommitUncommittedChanges(context.Background(), repo, "gg: development/implementation"); err != nil {
		t.Fatal(err)
	}
	if after, _ := client.HeadCommit(context.Background(), repo); after != head {
		t.Fatalf("clean worktree auto-commit moved HEAD %s -> %s", head, after)
	}
}

func TestIsUncommittedNewFileAcceptsTrackedModifications(t *testing.T) {
	// A proof accidentally committed by an earlier phase but modified by the
	// current attempt still counts as uncommitted work.
	executor := &fakeExecutor{output: " M PROOF.md\x00"}
	client := git.NewClient("/repo", executor)
	got, err := client.IsUncommittedNewFile(context.Background(), "/repo/worktree", "PROOF.md")
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("tracked file with uncommitted modifications = false, want true")
	}
	clean := &fakeExecutor{output: ""}
	client = git.NewClient("/repo", clean)
	got, err = client.IsUncommittedNewFile(context.Background(), "/repo/worktree", "PROOF.md")
	if err != nil || got {
		t.Fatalf("clean file = %v %v, want false", got, err)
	}
}

func TestAutoCommitExcludesProofArtifact(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "gg@example.test")
	runGit(t, repo, "config", "user.name", "gg test")
	if err := os.WriteFile(filepath.Join(repo, "initial.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "initial.txt")
	runGit(t, repo, "-c", "commit.gpgsign=false", "commit", "-qm", "initial")
	client := git.NewClient(repo, nil)

	// Code changes are committed; PROOF.md must stay uncommitted.
	if err := os.WriteFile(filepath.Join(repo, "game.js"), []byte("code\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "PROOF.md"), []byte("proof\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := client.AutoCommitUncommittedChanges(context.Background(), repo, "gg: development/implementation"); err != nil {
		t.Fatal(err)
	}
	uncommitted, err := client.IsUncommittedNewFile(context.Background(), repo, "PROOF.md")
	if err != nil {
		t.Fatal(err)
	}
	if !uncommitted {
		t.Fatal("auto-commit swept PROOF.md into the commit")
	}
	tracked, err := client.IsUncommittedNewFile(context.Background(), repo, "game.js")
	if err != nil {
		t.Fatal(err)
	}
	if tracked {
		t.Fatal("auto-commit did not commit the code change")
	}

	// Only PROOF.md dirty: no commit is created at all.
	head, err := client.HeadCommit(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "PROOF.md"), []byte("updated proof\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := client.AutoCommitUncommittedChanges(context.Background(), repo, "gg: development/testing"); err != nil {
		t.Fatal(err)
	}
	if after, _ := client.HeadCommit(context.Background(), repo); after != head {
		t.Fatalf("proof-only dirt created a commit %s -> %s", head, after)
	}
}

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
	repoRoot := git.NativeAbs(t, "repo")
	project := git.NativeAbs(t, "repo", "project")
	// git prints forward-slash paths on every platform ("D:/repo" on Windows);
	// RepositoryRoot is what converts that into the native spelling.
	executor := &fakeExecutor{output: filepath.ToSlash(repoRoot) + "\n"}
	client := git.NewClient(project, executor)

	root, err := client.RepositoryRoot(context.Background())
	if err != nil {
		t.Fatalf("RepositoryRoot() error = %v", err)
	}
	if root != repoRoot {
		t.Fatalf("RepositoryRoot() = %q, want %q", root, repoRoot)
	}
	want := git.Command{Dir: project, Name: "git", Args: []string{"rev-parse", "--show-toplevel"}}
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
	repoRoot := git.NativeAbs(t, "repo")
	executor := &fakeExecutor{output: filepath.ToSlash(repoRoot)}
	client := git.NewClient(repoRoot, executor)

	_, err := client.RepositoryRoot(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(executor.commands) != 0 {
		t.Fatalf("commands = %#v, want no command after cancellation", executor.commands)
	}
}

func TestDryRunConstructsCommandsWithoutExecuting(t *testing.T) {
	repoRoot := git.NativeAbs(t, "repo")
	executor := &fakeExecutor{output: "unexpected"}
	client := git.NewClient(repoRoot, executor, git.WithDryRun())

	if _, err := client.Status(context.Background()); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	commands := client.Commands()
	want := []git.Command{
		{Dir: repoRoot, Name: "git", Args: []string{"rev-parse", "--show-toplevel"}},
		{Dir: repoRoot, Name: "git", Args: []string{"status", "--short", "--branch"}},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
	if len(executor.commands) != 0 {
		t.Fatalf("executor commands = %#v, want none in dry-run", executor.commands)
	}
}

func TestStatusPropagatesCommandFailure(t *testing.T) {
	repoRoot := git.NativeAbs(t, "repo")
	executor := &fakeExecutor{output: filepath.ToSlash(repoRoot) + "\n", err: errors.New("git failed")}
	client := git.NewClient(repoRoot, executor)

	_, err := client.Status(context.Background())
	if err == nil || !strings.Contains(err.Error(), "validate git repository") || !errors.Is(err, executor.err) {
		t.Fatalf("error = %v, want validation failure context", err)
	}
}

func TestIsUncommittedNewFileUsesSafeGitStatusPathspec(t *testing.T) {
	worktree := git.NativeAbs(t, "repo", "worktree")
	executor := &fakeExecutor{output: "?? PROOF.md\x00"}
	client := git.NewClient(git.NativeAbs(t, "repo"), executor)

	// The pathspec stays forward-slash: it is a git argument, not an OS path.
	got, err := client.IsUncommittedNewFile(context.Background(), worktree, "PROOF.md")
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("IsUncommittedNewFile() = false, want true")
	}
	want := git.Command{Dir: worktree, Name: "git", Args: []string{"status", "--porcelain=v1", "--untracked-files=all", "--ignored=matching", "-z", "--", "PROOF.md"}}
	if !reflect.DeepEqual(executor.commands, []git.Command{want}) {
		t.Fatalf("commands = %#v, want %#v", executor.commands, []git.Command{want})
	}
}

func TestIsUncommittedNewFileRejectsUnsafePath(t *testing.T) {
	// The backslash and drive-letter cases must be rejected on every platform,
	// not just Windows: the pathspec rules git enforces do not vary by host.
	for _, path := range []string{
		"../PROOF.md", "/tmp/PROOF.md", "./PROOF.md", "..",
		`..\PROOF.md`, `.gg\PROOF.md`, "C:/PROOF.md", `C:\PROOF.md`,
	} {
		t.Run(path, func(t *testing.T) {
			// The worktree must be a genuinely absolute OS path, otherwise the
			// worktree guard rejects the call first and the pathspec rule under
			// test is never reached.
			executor := &fakeExecutor{output: "?? PROOF.md\x00"}
			client := git.NewClient(git.NativeAbs(t, "repo"), executor)
			if _, err := client.IsUncommittedNewFile(context.Background(), git.NativeAbs(t, "repo", "worktree"), path); err == nil {
				t.Fatalf("path %q was accepted", path)
			}
			if len(executor.commands) != 0 {
				t.Fatalf("commands = %#v, want none", executor.commands)
			}
		})
	}
}

// TestIsUncommittedNewFileAcceptsNestedSlashPathspec pins the pathspec contract
// that broke QA proof validation and PR creation on Windows: proof.ArtifactName
// is the forward-slash constant ".gg/PROOF.md", and validating it with filepath
// rewrote it to ".gg\PROOF.md" on Windows and rejected it. The path must be
// accepted and forwarded to git byte-for-byte, because git's porcelain output is
// forward-slash on every platform and is compared against this exact string.
func TestIsUncommittedNewFileAcceptsNestedSlashPathspec(t *testing.T) {
	const artifact = ".gg/PROOF.md"
	executor := &fakeExecutor{output: "?? " + artifact + "\x00"}
	client := git.NewClient(git.NativeAbs(t, "repo"), executor)

	got, err := client.IsUncommittedNewFile(context.Background(), git.NativeAbs(t, "repo", "worktree"), artifact)
	if err != nil {
		t.Fatalf("IsUncommittedNewFile(%q) error = %v, want accepted", artifact, err)
	}
	if !got {
		t.Fatalf("IsUncommittedNewFile(%q) = false, want true", artifact)
	}
	if len(executor.commands) != 1 {
		t.Fatalf("commands = %#v, want exactly one", executor.commands)
	}
	if gotArgs := executor.commands[0].Args; gotArgs[len(gotArgs)-1] != artifact {
		t.Fatalf("pathspec = %q, want %q unmodified", gotArgs[len(gotArgs)-1], artifact)
	}
}

func TestIsUncommittedNewFilePropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	executor := &fakeExecutor{output: "?? PROOF.md\x00"}
	client := git.NewClient(git.NativeAbs(t, "repo"), executor)
	if _, err := client.IsUncommittedNewFile(ctx, git.NativeAbs(t, "repo", "worktree"), "PROOF.md"); !errors.Is(err, context.Canceled) {
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
	worktree := git.NativeAbs(t, "repo", "worktree")
	executor := &fakeExecutor{output: "conflicted.txt\n"}
	client := git.NewClient(git.NativeAbs(t, "repo"), executor)
	got, err := client.HasUnresolvedConflicts(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("HasUnresolvedConflicts() = false, want true")
	}
	want := git.Command{Dir: worktree, Name: "git", Args: []string{"diff", "--name-only", "--diff-filter=U", "--"}}
	if !reflect.DeepEqual(executor.commands, []git.Command{want}) {
		t.Fatalf("commands = %#v, want %#v", executor.commands, []git.Command{want})
	}
}

func TestHasUnresolvedConflictsReturnsFalseForCleanIndex(t *testing.T) {
	client := git.NewClient(git.NativeAbs(t, "repo"), &fakeExecutor{})
	got, err := client.HasUnresolvedConflicts(context.Background(), git.NativeAbs(t, "repo", "worktree"))
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
	worktree := git.NativeAbs(t, "repo", "worktree")
	executor := &queuedExecutor{outputs: []string{"old-head\n", "", "newer-head\x00N\nnew-head\x00N\n"}}
	client := git.NewClient(git.NativeAbs(t, "repo"), executor)
	previous, err := client.HeadCommit(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.VerifyUnsignedDevelopmentCommit(context.Background(), worktree, previous); err != nil {
		t.Fatal(err)
	}
	want := []git.Command{
		{Dir: worktree, Name: "git", Args: []string{"rev-parse", "HEAD"}},
		{Dir: worktree, Name: "git", Args: []string{"merge-base", "--is-ancestor", "old-head", "HEAD"}},
		{Dir: worktree, Name: "git", Args: []string{"log", "--format=%H%x00%G?", "old-head..HEAD"}},
	}
	if !reflect.DeepEqual(executor.calls, want) {
		t.Fatalf("commands = %#v, want %#v", executor.calls, want)
	}
}

func TestVerifyUnsignedDevelopmentCommitRejectsSignedHead(t *testing.T) {
	client := git.NewClient(git.NativeAbs(t, "repo"), &queuedExecutor{outputs: []string{"", "signed-head\x00G\n"}})
	if err := client.VerifyUnsignedDevelopmentCommit(context.Background(), git.NativeAbs(t, "repo", "worktree"), "old-head"); err == nil || !strings.Contains(err.Error(), "signed") {
		t.Fatalf("error = %v, want signed-commit rejection", err)
	}
}

func TestVerifyUnsignedDevelopmentCommitRejectsUnchangedHead(t *testing.T) {
	client := git.NewClient(git.NativeAbs(t, "repo"), &queuedExecutor{outputs: []string{"", ""}})
	if err := client.VerifyUnsignedDevelopmentCommit(context.Background(), git.NativeAbs(t, "repo", "worktree"), "old-head"); err == nil || !strings.Contains(err.Error(), "did not create a commit") {
		t.Fatalf("error = %v, want no-commit rejection", err)
	}
}

func TestVerifyUnsignedDevelopmentCommitsAllowsNoCommitForFailedProcess(t *testing.T) {
	client := git.NewClient(git.NativeAbs(t, "repo"), &queuedExecutor{outputs: []string{"", ""}})
	if err := client.VerifyUnsignedDevelopmentCommits(context.Background(), git.NativeAbs(t, "repo", "worktree"), "old-head", false); err != nil {
		t.Fatalf("VerifyUnsignedDevelopmentCommits() error = %v, want unchanged failed process accepted", err)
	}
}

func TestVerifyUnsignedDevelopmentCommitRejectsUnrelatedHead(t *testing.T) {
	ancestorErr := errors.New("not ancestor")
	executor := &queuedExecutor{errs: []error{ancestorErr}}
	client := git.NewClient(git.NativeAbs(t, "repo"), executor)
	err := client.VerifyUnsignedDevelopmentCommit(context.Background(), git.NativeAbs(t, "repo", "worktree"), "old-head")
	if err == nil || !errors.Is(err, ancestorErr) || !strings.Contains(err.Error(), "not an ancestor") {
		t.Fatalf("error = %v, want unrelated-head rejection", err)
	}
	if len(executor.calls) != 1 || executor.calls[0].Args[0] != "merge-base" {
		t.Fatalf("commands = %#v, want ancestry check only", executor.calls)
	}
}

func TestVerifyUnsignedDevelopmentCommitRejectsSignedIntermediateCommit(t *testing.T) {
	client := git.NewClient(git.NativeAbs(t, "repo"), &queuedExecutor{outputs: []string{"", "head\x00N\nintermediate\x00G\n"}})
	err := client.VerifyUnsignedDevelopmentCommit(context.Background(), git.NativeAbs(t, "repo", "worktree"), "old-head")
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

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func TestVerifyUnsignedDevelopmentCommitPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	executor := &queuedExecutor{outputs: []string{"new-head\x00N\n"}}
	client := git.NewClient(git.NativeAbs(t, "repo"), executor)
	if err := client.VerifyUnsignedDevelopmentCommit(ctx, git.NativeAbs(t, "repo", "worktree"), "old-head"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("commands = %#v, want none after cancellation", executor.calls)
	}
}

func TestInspectDevelopmentWorktreeReturnsMeaningfulChangesAndIgnoresArtifacts(t *testing.T) {
	executor := &fakeExecutor{output: " M tracked.go\x00A  added.go\x00 D deleted.go\x00R  renamed.go\x00old.go\x00?? new.go\x00 M .gg/development.md\x00?? .gg/PROOF.md\x00"}
	worktree := git.NativeAbs(t, "repo", "worktree")
	client := git.NewClient(worktree, executor)
	changes, err := client.InspectDevelopmentWorktree(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	want := []git.DevelopmentWorktreeChange{
		{Status: "A ", Path: "added.go"},
		{Status: " D", Path: "deleted.go"},
		{Status: "??", Path: "new.go"},
		{Status: "R ", Path: "renamed.go", OriginalPath: "old.go"},
		{Status: " M", Path: "tracked.go"},
	}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("changes = %#v, want %#v", changes, want)
	}
	wantCommand := git.Command{Dir: worktree, Name: "git", Args: []string{"status", "--porcelain=v1", "--untracked-files=all", "-z", "--"}}
	if !reflect.DeepEqual(executor.commands, []git.Command{wantCommand}) {
		t.Fatalf("commands = %#v, want %#v", executor.commands, []git.Command{wantCommand})
	}
}

func TestInspectDevelopmentWorktreeCleanWhenOnlyArtifactWorkspaceIsDirty(t *testing.T) {
	worktree := git.NativeAbs(t, "repo", "worktree")
	client := git.NewClient(worktree, &fakeExecutor{output: " M .gg/development.md\x00?? .gg/verification-logs/run.log\x00"})
	changes, err := client.InspectDevelopmentWorktree(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("changes = %#v, want clean ownership state", changes)
	}
}

func TestInspectDevelopmentWorktreeTemporaryRepositoryReportsAllSourceChanges(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "gg@example.test")
	runGit(t, repo, "config", "user.name", "gg test")
	for name, content := range map[string]string{
		"modified.go":   "package before\n",
		"deleted.go":    "package deleted\n",
		"rename-old.go": "package renamed\n",
		"unchanged.go":  "package unchanged\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "-c", "commit.gpgsign=false", "commit", "-qm", "initial")

	if err := os.WriteFile(filepath.Join(repo, "modified.go"), []byte("package after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "deleted.go")); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "mv", "rename-old.go", "rename-new.go")
	if err := os.WriteFile(filepath.Join(repo, "added.go"), []byte("package added\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	changes, err := git.NewClient(repo, nil).InspectDevelopmentWorktree(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	byPath := make(map[string]git.DevelopmentWorktreeChange, len(changes))
	for _, change := range changes {
		byPath[change.Path] = change
	}
	for _, path := range []string{"added.go", "deleted.go", "modified.go", "rename-new.go"} {
		if _, ok := byPath[path]; !ok {
			t.Fatalf("changes = %#v, want path %q", changes, path)
		}
	}
	if byPath["rename-new.go"].OriginalPath != "rename-old.go" {
		t.Fatalf("rename = %#v, want old path rename-old.go", byPath["rename-new.go"])
	}
	if _, ok := byPath["unchanged.go"]; ok {
		t.Fatalf("unchanged path reported in changes: %#v", changes)
	}
}

func TestPushBranchUsesExactArgv(t *testing.T) {
	executor := &fakeExecutor{}
	worktree := git.NativeAbs(t, "repo", "worktree")
	client := git.NewClient(git.NativeAbs(t, "repo"), executor)
	if err := client.PushBranchToRemote(context.Background(), worktree, "origin", "feature/name"); err != nil {
		t.Fatal(err)
	}
	want := git.Command{Dir: worktree, Name: "git", Args: []string{"push", "origin", "feature/name"}}
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
	client := git.NewClient(git.NativeAbs(t, "repo"), executor)
	got, err := client.IsUncommittedNewFile(context.Background(), git.NativeAbs(t, "repo", "worktree"), "PROOF.md")
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("tracked file with uncommitted modifications = false, want true")
	}
	clean := &fakeExecutor{output: ""}
	client = git.NewClient(git.NativeAbs(t, "repo"), clean)
	got, err = client.IsUncommittedNewFile(context.Background(), git.NativeAbs(t, "repo", "worktree"), "PROOF.md")
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

func TestAutoCommitExcludesTrackedArtifactWorkspace(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "gg@example.test")
	runGit(t, repo, "config", "user.name", "gg test")
	if err := os.Mkdir(filepath.Join(repo, ".gg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gg", "PROOF.md"), []byte("proof\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-f", ".gg/PROOF.md")
	runGit(t, repo, "-c", "commit.gpgsign=false", "commit", "-qm", "initial")

	if err := os.WriteFile(filepath.Join(repo, ".gg", "PROOF.md"), []byte("updated proof\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "source.go"), []byte("package source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := git.NewClient(repo, nil).AutoCommitUncommittedChanges(context.Background(), repo, "gg: development/implementation"); err != nil {
		t.Fatal(err)
	}

	status := runGit(t, repo, "status", "--porcelain")
	if !strings.Contains(status, "M  .gg/PROOF.md") && !strings.Contains(status, " M .gg/PROOF.md") {
		t.Fatalf("artifact workspace change was not preserved: %q", status)
	}
	if strings.Contains(status, "source.go") {
		t.Fatalf("source change was not auto-committed: %q", status)
	}
}

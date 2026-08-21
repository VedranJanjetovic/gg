package git_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/git"
)

type worktreeExecutor struct {
	commands []git.Command
	outputs  []string
	errs     []error
}

func (f *worktreeExecutor) Execute(_ context.Context, c git.Command) (string, error) {
	f.commands = append(f.commands, c)
	i := len(f.commands) - 1
	if i < len(f.errs) && f.errs[i] != nil {
		return "", f.errs[i]
	}
	if i < len(f.outputs) {
		return f.outputs[i], nil
	}
	return "", nil
}

func TestParseAndEnsureWorktreeCommands(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "repo")
	path := filepath.Join(string(filepath.Separator), "tmp", "owned")
	listing := "worktree /repo\nHEAD abc\nbranch refs/heads/main\n\n"
	createdListing := listing + "worktree " + path + "\nHEAD def\nbranch refs/heads/gg/owned\n\n"
	f := &worktreeExecutor{outputs: []string{"/repo\n", listing, "", "/repo\n", createdListing}}
	c := git.NewClient(root, f)
	got, created, err := c.EnsureWorktree(context.Background(), git.WorktreeRequest{Path: path, Branch: "gg/owned", BaseRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("created = false, want true")
	}
	if got.Path != path || got.Branch != "gg/owned" {
		t.Fatalf("got %#v", got)
	}
	want := git.Command{Dir: root, Name: "git", Args: []string{"worktree", "add", "-b", "gg/owned", path, "main"}}
	if !reflect.DeepEqual(f.commands[2], want) {
		t.Fatalf("create command %#v, want %#v", f.commands[2], want)
	}
}

func TestEnsureRejectsPathAndBranchCollisions(t *testing.T) {
	listing := "worktree /repo\nHEAD abc\nbranch refs/heads/main\n\nworktree /other\nHEAD def\nbranch refs/heads/gg/other\n\n"
	for name, tc := range map[string]struct {
		request git.WorktreeRequest
		want    error
	}{
		"path":   {request: git.WorktreeRequest{Path: "/repo", Branch: "gg/new", BaseRef: "main"}, want: git.ErrWorktreePathInUse},
		"branch": {request: git.WorktreeRequest{Path: "/new", Branch: "gg/other", BaseRef: "main"}, want: git.ErrWorktreeBranchInUse},
	} {
		t.Run(name, func(t *testing.T) {
			f := &worktreeExecutor{outputs: []string{"/repo\n", listing}}
			_, _, err := git.NewClient("/repo", f).EnsureWorktree(context.Background(), tc.request)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error %v, want %v", err, tc.want)
			}
		})
	}
}

func TestEnsurePropagatesWorktreeCreationFailure(t *testing.T) {
	cause := errors.New("branch creation failed")
	listing := "worktree /repo\nHEAD abc\nbranch refs/heads/main\n\n"
	f := &worktreeExecutor{
		outputs: []string{"/repo\n", listing},
		errs:    []error{nil, nil, cause},
	}
	_, _, err := git.NewClient("/repo", f).EnsureWorktree(context.Background(), git.WorktreeRequest{
		Path: "/tmp/new", Branch: "gg/new", BaseRef: "main",
	})
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want original creation failure", err)
	}
	if len(f.commands) != 3 || !reflect.DeepEqual(f.commands[2].Args, []string{"worktree", "add", "-b", "gg/new", "/tmp/new", "main"}) {
		t.Fatalf("commands = %#v, want repository lookup, listing, and add", f.commands)
	}
}

func TestDryRunConstructsEnsureAndRemoveCommands(t *testing.T) {
	c := git.NewClient("/repo", &worktreeExecutor{}, git.WithDryRun())
	path := "/tmp/owned"
	if _, _, err := c.EnsureWorktree(context.Background(), git.WorktreeRequest{Path: path, Branch: "gg/owned", BaseRef: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveWorktree(context.Background(), path, "gg/owned"); err != nil {
		t.Fatal(err)
	}
	want := []git.Command{
		{Dir: "/repo", Name: "git", Args: []string{"rev-parse", "--show-toplevel"}},
		{Dir: "/repo", Name: "git", Args: []string{"worktree", "list", "--porcelain"}},
		{Dir: "/repo", Name: "git", Args: []string{"worktree", "add", "-b", "gg/owned", path, "main"}},
		{Dir: "/repo", Name: "git", Args: []string{"rev-parse", "--show-toplevel"}},
		{Dir: "/repo", Name: "git", Args: []string{"worktree", "list", "--porcelain"}},
		{Dir: path, Name: "git", Args: []string{"status", "--porcelain", "--untracked-files=all"}},
		{Dir: "/repo", Name: "git", Args: []string{"worktree", "remove", "--", path}},
		{Dir: "/repo", Name: "git", Args: []string{"branch", "-D", "--", "gg/owned"}},
	}
	if !reflect.DeepEqual(c.Commands(), want) {
		t.Fatalf("commands %#v, want %#v", c.Commands(), want)
	}
}

func TestRemoveRefusesPartialFailureAndDirtyOutput(t *testing.T) {
	cause := errors.New("status unavailable")
	f := &worktreeExecutor{outputs: []string{"/repo\n", "worktree /tmp/owned\nHEAD abc\nbranch refs/heads/gg/owned\n\n"}, errs: []error{nil, nil, cause}}
	err := git.NewClient("/repo", f).RemoveWorktree(context.Background(), "/tmp/owned", "gg/owned")
	if !errors.Is(err, cause) || len(f.commands) != 3 {
		t.Fatalf("error %v commands %#v", err, f.commands)
	}
	f = &worktreeExecutor{outputs: []string{"/repo\n", "worktree /tmp/owned\nHEAD abc\nbranch refs/heads/gg/owned\n\n", " M file\n"}}
	err = git.NewClient("/repo", f).RemoveWorktree(context.Background(), "/tmp/owned", "gg/owned")
	if !errors.Is(err, git.ErrUnsafeWorktree) || len(f.commands) != 3 {
		t.Fatalf("dirty error %v commands %#v", err, f.commands)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
func TestWorktreeIntegrationCreateReuseLookupAndRemove(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	path := filepath.Join(parent, "owned")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "init", "-q")
	gitRun(t, repo, "config", "user.email", "test@example.com")
	gitRun(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-qm", "initial")
	c := git.NewClient(repo, nil)
	created, wasCreated, err := c.EnsureWorktree(context.Background(), git.WorktreeRequest{Path: path, Branch: "gg/owned", BaseRef: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	if !wasCreated || created.Branch != "gg/owned" {
		t.Fatalf("created %#v", created)
	}
	looked, err := c.LookupWorktree(context.Background(), path, "gg/owned")
	if err != nil || looked.Path != path {
		t.Fatalf("lookup %#v %v", looked, err)
	}
	reused, wasCreated, err := c.EnsureWorktree(context.Background(), git.WorktreeRequest{Path: path, Branch: "gg/owned", BaseRef: "HEAD"})
	if err != nil || wasCreated || reused.Path != path {
		t.Fatalf("reuse %#v %v", reused, err)
	}
	if err := os.WriteFile(filepath.Join(path, "dirty"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveWorktree(context.Background(), path, "gg/owned"); !errors.Is(err, git.ErrUnsafeWorktree) {
		t.Fatalf("dirty removal error %v", err)
	}
	if err := os.Remove(filepath.Join(path, "dirty")); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveWorktree(context.Background(), path, "gg/owned"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("path still exists: %v", err)
	}
	if strings.TrimSpace(looked.Branch) == "" {
		t.Fatal("lookup branch empty")
	}
}

func TestListWorktreesAcceptsKnownMetadataVariants(t *testing.T) {
	listing := "worktree /repo\nHEAD abc\nbranch refs/heads/main\nworktreeConfig\n\n" +
		"worktree /gone\nHEAD def\nbranch refs/heads/gg/gone\nprunable stale\nlocked\nreason maintenance\n\n"
	f := &worktreeExecutor{outputs: []string{"/repo\n", listing}}
	worktrees, err := git.NewClient("/repo", f).ListWorktrees(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(worktrees) != 2 || worktrees[1].Branch != "gg/gone" {
		t.Fatalf("worktrees = %#v", worktrees)
	}
}

func TestRemoveWorktreeRejectsOccupiedUnregisteredPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(path, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &worktreeExecutor{outputs: []string{"/repo\n", "worktree /repo\nHEAD abc\nbranch refs/heads/main\n\n"}}
	err := git.NewClient("/repo", f).RemoveWorktree(context.Background(), path, "gg/occupied")
	if !errors.Is(err, git.ErrWorktreePathInUse) {
		t.Fatalf("error = %v, want occupied path error", err)
	}
}

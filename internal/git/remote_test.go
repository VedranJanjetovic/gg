package git_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/git"
)

func TestFetchParentUsesConfiguredBranch(t *testing.T) {
	f := &remoteExecutor{outputs: []string{"fetched\n"}}
	got, err := git.NewClient(git.NativeAbs(t, "repo"), f).FetchParent(context.Background(), "develop")
	if err != nil || got.Output != "fetched\n" {
		t.Fatalf("result=%#v err=%v", got, err)
	}
	want := git.Command{Dir: git.NativeAbs(t, "repo"), Name: "git", Args: []string{"fetch", "origin", "develop"}}
	if !reflect.DeepEqual(f.calls, []git.Command{want}) {
		t.Fatalf("calls=%#v want=%#v", f.calls, []git.Command{want})
	}
}

func TestRebaseSuccess(t *testing.T) {
	f := &remoteExecutor{outputs: []string{"rebased\n"}}
	got, err := git.NewClient(git.NativeAbs(t, "repo"), f).RebaseProject(context.Background(), git.RebaseRequest{WorktreePath: git.NativeAbs(t, "repo", "project"), Branch: "gg/project", ParentBranch: "develop", BaseRef: "stale/base"})
	if err != nil || got.Conflict != nil || got.Output != "rebased\n" {
		t.Fatalf("result=%#v err=%v", got, err)
	}
	want := git.Command{Dir: git.NativeAbs(t, "repo", "project"), Name: "git", Args: []string{"rebase", "origin/develop"}}
	if !reflect.DeepEqual(f.calls, []git.Command{want}) {
		t.Fatalf("calls=%#v want=%#v", f.calls, []git.Command{want})
	}
}

func TestRebaseRejectsUnsafeBranchBeforeInvokingGit(t *testing.T) {
	f := &remoteExecutor{outputs: []string{"unexpected"}}
	_, err := git.NewClient(git.NativeAbs(t, "repo"), f).RebaseProject(context.Background(), git.RebaseRequest{
		WorktreePath: git.NativeAbs(t, "repo", "project"),
		Branch:       "feature..bad",
		ParentBranch: "main",
	})
	if err == nil || !strings.Contains(err.Error(), "branch is invalid") {
		t.Fatalf("error = %v, want unsafe branch rejection", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("git calls = %#v, want none", f.calls)
	}
}

func TestRebaseRequiresConfiguredParentEvenWhenBaseRefIsPresent(t *testing.T) {
	f := &remoteExecutor{outputs: []string{"unexpected"}}
	_, err := git.NewClient(git.NativeAbs(t, "repo"), f).RebaseProject(context.Background(), git.RebaseRequest{
		WorktreePath: git.NativeAbs(t, "repo", "project"),
		Branch:       "gg/project",
		BaseRef:      "origin/main",
	})
	if err == nil || !strings.Contains(err.Error(), "parent branch is required") {
		t.Fatalf("error = %v, want configured-parent rejection", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("git calls = %#v, want none", f.calls)
	}
}

func TestRebaseCheckpointCommandsRestoreCleanBranchState(t *testing.T) {
	f := &remoteExecutor{
		outputs: []string{"gg/project\n", "0123456789abcdef\n", "", "", "", "", "", "", "gg/project\n", "0123456789abcdef\n", ""},
		errs:    []error{nil, nil, nil, errors.New("no rebase"), nil, nil, nil, errors.New("no rebase"), nil, nil, nil},
	}
	project := git.NativeAbs(t, "repo", "project")
	c := git.NewClient(git.NativeAbs(t, "repo"), f)
	checkpoint, err := c.CaptureRebaseCheckpoint(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Branch != "gg/project" || checkpoint.Head != "0123456789abcdef" {
		t.Fatalf("checkpoint = %#v", checkpoint)
	}
	if err := c.RestoreRebaseCheckpoint(context.Background(), checkpoint); err != nil {
		t.Fatal(err)
	}
	want := []git.Command{
		{Dir: project, Name: "git", Args: []string{"branch", "--show-current"}},
		{Dir: project, Name: "git", Args: []string{"rev-parse", "HEAD"}},
		{Dir: project, Name: "git", Args: []string{"status", "--porcelain=v1", "--untracked-files=all", "--"}},
		{Dir: project, Name: "git", Args: []string{"rev-parse", "--verify", "REBASE_HEAD"}},
		{Dir: project, Name: "git", Args: []string{"checkout", "--force", "gg/project"}},
		{Dir: project, Name: "git", Args: []string{"reset", "--hard", "0123456789abcdef"}},
		{Dir: project, Name: "git", Args: []string{"clean", "-fd", "--"}},
		{Dir: project, Name: "git", Args: []string{"rev-parse", "--verify", "REBASE_HEAD"}},
		{Dir: project, Name: "git", Args: []string{"branch", "--show-current"}},
		{Dir: project, Name: "git", Args: []string{"rev-parse", "HEAD"}},
		{Dir: project, Name: "git", Args: []string{"status", "--porcelain=v1", "--untracked-files=all", "--"}},
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls = %#v, want %#v", f.calls, want)
	}
}

func TestRestoreRebaseCheckpointAbortsActiveRebase(t *testing.T) {
	f := &remoteExecutor{
		outputs: []string{"rebase-head\n", "", "", "", "", "", "gg/project\n", "0123456789abcdef\n", ""},
		errs:    []error{nil, nil, nil, nil, nil, errors.New("no rebase"), nil, nil, nil},
	}
	checkpoint := git.RebaseCheckpoint{WorktreePath: git.NativeAbs(t, "repo", "project"), Branch: "gg/project", Head: "0123456789abcdef"}
	if err := git.NewClient(git.NativeAbs(t, "repo"), f).RestoreRebaseCheckpoint(context.Background(), checkpoint); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 9 || !reflect.DeepEqual(f.calls[1].Args, []string{"rebase", "--abort"}) {
		t.Fatalf("calls = %#v, want active rebase abort before restore", f.calls)
	}
}

func TestCaptureRebaseCheckpointRejectsDirtyWorktree(t *testing.T) {
	f := &remoteExecutor{outputs: []string{"gg/project\n", "0123456789abcdef\n", " M README.md\n"}}
	_, err := git.NewClient(git.NativeAbs(t, "repo"), f).CaptureRebaseCheckpoint(context.Background(), git.NativeAbs(t, "repo", "project"))
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("error = %v, want dirty-worktree rejection", err)
	}
}

func TestRebaseConflictPreservesOutputAndPaths(t *testing.T) {
	cause := errors.New("rebase failed")
	f := &remoteExecutor{outputs: []string{"CONFLICT (content): Merge conflict in app.go\n", "app.go\nREADME.md\napp.go\n"}, errs: []error{cause}}
	got, err := git.NewClient(git.NativeAbs(t, "repo"), f).RebaseProject(context.Background(), git.RebaseRequest{WorktreePath: git.NativeAbs(t, "repo", "project"), Branch: "gg/project", ParentBranch: "main", BaseRef: "stale/base"})
	if !errors.Is(err, git.ErrRebaseConflict) || got.Conflict == nil {
		t.Fatalf("result=%#v err=%v", got, err)
	}
	if got.Output == "" || !strings.Contains(got.Conflict.Output, "CONFLICT") {
		t.Fatalf("evidence=%#v", got.Conflict)
	}
	if !reflect.DeepEqual(got.Conflict.Paths, []string{"app.go", "README.md"}) {
		t.Fatalf("paths=%#v", got.Conflict.Paths)
	}
	if len(f.calls) != 2 || f.calls[1].Args[0] != "diff" {
		t.Fatalf("calls=%#v", f.calls)
	}
}

func TestRebaseMalformedConflictOutputRemainsOrdinaryError(t *testing.T) {
	cause := errors.New("bad rebase")
	inspect := errors.New("status unavailable")
	f := &remoteExecutor{outputs: []string{"rebase output\n"}, errs: []error{cause, inspect}}
	got, err := git.NewClient(git.NativeAbs(t, "repo"), f).RebaseProject(context.Background(), git.RebaseRequest{WorktreePath: git.NativeAbs(t, "repo", "project"), Branch: "gg/project", ParentBranch: "main", BaseRef: "stale/base"})
	if errors.Is(err, git.ErrRebaseConflict) || err == nil || !strings.Contains(err.Error(), "inspect conflicts") {
		t.Fatalf("result=%#v err=%v", got, err)
	}
	if got.Output != "rebase output\n" {
		t.Fatalf("output=%q", got.Output)
	}
}

func TestPushAndInspectBranch(t *testing.T) {
	f := &remoteExecutor{outputs: []string{"pushed\n", "gg/project\n", "abc123\n"}}
	project := git.NativeAbs(t, "repo", "project")
	c := git.NewClient(git.NativeAbs(t, "repo"), f)
	pushed, err := c.PushBranch(context.Background(), project, "gg/project")
	if err != nil || pushed.Output != "pushed\n" {
		t.Fatalf("push=%#v err=%v", pushed, err)
	}
	inspected, err := c.InspectBranch(context.Background(), project, "origin/develop")
	if err != nil || inspected.Branch != "gg/project" || inspected.BaseHead != "abc123" {
		t.Fatalf("inspection=%#v err=%v", inspected, err)
	}
	want := []git.Command{{Dir: project, Name: "git", Args: []string{"push", "origin", "gg/project"}}, {Dir: project, Name: "git", Args: []string{"branch", "--show-current"}}, {Dir: project, Name: "git", Args: []string{"rev-parse", "--verify", "origin/develop"}}}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls=%#v want=%#v", f.calls, want)
	}
}

func TestRemoteAdaptersPropagateCancellationAndErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &remoteExecutor{outputs: []string{"unused"}}
	_, err := git.NewClient(git.NativeAbs(t, "repo"), f).FetchParent(ctx, "main")
	if !errors.Is(err, context.Canceled) || len(f.calls) != 0 {
		t.Fatalf("err=%v calls=%#v", err, f.calls)
	}
	cause := errors.New("push failed")
	f = &remoteExecutor{errs: []error{cause}}
	result, err := git.NewClient(git.NativeAbs(t, "repo"), f).PushBranch(context.Background(), git.NativeAbs(t, "repo", "project"), "gg/project")
	if !errors.Is(err, cause) || result.Output != "" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

type remoteExecutor struct {
	calls   []git.Command
	outputs []string
	errs    []error
}

func (f *remoteExecutor) Execute(_ context.Context, command git.Command) (string, error) {
	f.calls = append(f.calls, command)
	i := len(f.calls) - 1
	if i < len(f.errs) && f.errs[i] != nil {
		if i < len(f.outputs) {
			return f.outputs[i], f.errs[i]
		}
		return "", f.errs[i]
	}
	if i < len(f.outputs) {
		return f.outputs[i], nil
	}
	return "", nil
}

func TestFetchParentFailureIncludesGitOutput(t *testing.T) {
	cause := errors.New("exit status 128")
	f := &remoteExecutor{outputs: []string{"fatal: couldn't find remote ref main\n"}, errs: []error{cause}}
	_, err := git.NewClient(git.NativeAbs(t, "repo"), f).FetchParent(context.Background(), "main")
	if err == nil || !strings.Contains(err.Error(), "couldn't find remote ref main") {
		t.Fatalf("error = %v, want git's own message included", err)
	}
}

func TestDefaultBranchDetectionFallbackChain(t *testing.T) {
	t.Run("local origin HEAD ref", func(t *testing.T) {
		f := &remoteExecutor{outputs: []string{"origin/master\n"}}
		if got := git.NewClient(git.NativeAbs(t, "repo"), f).DefaultBranch(context.Background()); got != "master" {
			t.Fatalf("branch = %q, want master", got)
		}
	})
	t.Run("remote advertised HEAD", func(t *testing.T) {
		f := &remoteExecutor{
			outputs: []string{"", "ref: refs/heads/trunk\tHEAD\nabc123\tHEAD\n"},
			errs:    []error{errors.New("not a symbolic ref")},
		}
		if got := git.NewClient(git.NativeAbs(t, "repo"), f).DefaultBranch(context.Background()); got != "trunk" {
			t.Fatalf("branch = %q, want trunk", got)
		}
	})
	t.Run("current branch fallback", func(t *testing.T) {
		f := &remoteExecutor{
			outputs: []string{"", "", "master\n"},
			errs:    []error{errors.New("no symbolic ref"), errors.New("no remote")},
		}
		if got := git.NewClient(git.NativeAbs(t, "repo"), f).DefaultBranch(context.Background()); got != "master" {
			t.Fatalf("branch = %q, want master", got)
		}
	})
	t.Run("nothing detectable", func(t *testing.T) {
		f := &remoteExecutor{errs: []error{errors.New("a"), errors.New("b"), errors.New("c")}}
		if got := git.NewClient(git.NativeAbs(t, "repo"), f).DefaultBranch(context.Background()); got != "" {
			t.Fatalf("branch = %q, want empty", got)
		}
	})
}

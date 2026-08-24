package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/git"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

func TestProjectAwareCommandsReachConfiguredFolderGate(t *testing.T) {
	root := t.TempDir()
	store := &memoryConfigureStore{projectErr: config.ErrProjectNotConfigured}
	app := New(
		WithConfigStore(store),
		WithWorkingDirectory(func() (string, error) { return root, nil }),
	)
	var stdout, stderr bytes.Buffer

	if code := app.Run(context.Background(), []string{"list"}, &stdout, &stderr); code == 0 {
		t.Fatalf("list unexpectedly succeeded in an unconfigured folder: stdout=%q", stdout.String())
	}
	if want := "current folder is not configured; run \"gg configure\" in "; !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr=%q, want configured-folder guidance containing %q", stderr.String(), want)
	}
}

func TestTopLevelHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer

	exitCode := New().Run(context.Background(), []string{"--help"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
	}

	output := stdout.String()
	for _, want := range []string{
		"Usage:",
		"gg [project]",
		"configure",
		"list",
		"status",
		"run",
		"stop",
		"prune",
		"update",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("help output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "chat") {
		t.Fatalf("help output unexpectedly contains chat:\n%s", output)
	}
}

func TestConfigureFlagMatchesConfigureCommand(t *testing.T) {
	flagOutput := runCLI(t, "--configure")
	commandOutput := runCLI(t, "configure")
	want := "Configuration workflow is not implemented yet.\n"

	if flagOutput != want {
		t.Fatalf("--configure output = %q, want %q", flagOutput, want)
	}
	if commandOutput != flagOutput {
		t.Fatalf("configure output = %q, want --configure output %q", commandOutput, flagOutput)
	}
}

func TestCommandSkeletons(t *testing.T) {
	tests := map[string]string{
		"configure": "Configuration workflow is not implemented yet.",
		"list":      "No gg agents found.",
		"status":    "gg status: no active runs",
		"run":       "Run workflow is not implemented yet.",
		"stop":      "Stop workflow is not implemented yet.",
		"prune":     "Pruned finished gg projects.",
	}

	for command, want := range tests {
		t.Run(command, func(t *testing.T) {
			output := runCLI(t, command)
			if !strings.Contains(output, want) {
				t.Fatalf("output = %q, want to contain %q", output, want)
			}
		})
	}
}

func TestUnknownProjectShorthandFails(t *testing.T) {
	stdout, stderr, exitCode := runCLIWithExitCode(t, "missing")

	if exitCode == 0 {
		t.Fatal("exit code = 0, want failure")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "project \"missing\" does not exist") {
		t.Fatalf("stderr = %q, want unknown project", stderr)
	}
}

func TestUnknownCommandHelpFails(t *testing.T) {
	stdout, stderr, exitCode := runCLIWithExitCode(t, "missing", "--help")

	if exitCode == 0 {
		t.Fatal("exit code = 0, want failure")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "unknown command \"missing\"") {
		t.Fatalf("stderr = %q, want unknown command", stderr)
	}
}

func TestNoArgumentCommandsRejectUnexpectedArgs(t *testing.T) {
	commands := []string{"configure", "prune", "update"}

	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			stdout, stderr, exitCode := runCLIWithExitCode(t, command, "unexpected")

			if exitCode == 0 {
				t.Fatal("exit code = 0, want failure")
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
			if !strings.Contains(stderr, command+"\" does not accept arguments") {
				t.Fatalf("stderr = %q, want unsupported arguments error for %q", stderr, command)
			}
		})
	}
}

func TestRunAndStopAcceptArguments(t *testing.T) {
	tests := map[string]string{
		"run":  "Run workflow is not implemented yet.",
		"stop": "Stop workflow is not implemented yet.",
	}

	for command, want := range tests {
		t.Run(command, func(t *testing.T) {
			root := t.TempDir()
			initTestRepository(t, root)
			app := New(WithRootResolver(fixedRoot{root: root}))
			if command == "stop" {
				if _, stderr, code := runApp(t, app, "run", "agent-name"); code != 0 {
					t.Fatalf("setup run exit=%d stderr=%q", code, stderr)
				}
			}
			stdout, stderr, exitCode := runApp(t, app, command, "agent-name", "--force")

			if exitCode != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr)
			}
			if !strings.Contains(stdout, want) {
				t.Fatalf("stdout = %q, want to contain %q", stdout, want)
			}
		})
	}
}

func runCLI(t *testing.T, args ...string) string {
	t.Helper()

	stdout, stderr, exitCode := runCLIWithExitCode(t, args...)
	if exitCode != 0 {
		t.Fatalf("gg %v exit code = %d, stderr=%q", args, exitCode, stderr)
	}

	return stdout
}

func runCLIWithExitCode(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	app := New(WithRootResolver(fixedRoot{root: t.TempDir()}))
	exitCode := app.Run(context.Background(), args, &stdout, &stderr)
	return stdout.String(), stderr.String(), exitCode
}

type fixedRoot struct {
	root string
	err  error
}

func (r fixedRoot) ConfiguredRoot(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return r.root, r.err
}

func TestLifecycleCommandsPersistAcrossAppRestart(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	app := New(WithRootResolver(fixedRoot{root: root}))

	if stdout, stderr, code := runApp(t, app, "run", "demo-project"); code != 0 {
		t.Fatalf("run exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.Load(context.Background(), "demo-project")
	if err != nil {
		t.Fatal(err)
	}
	if project.Status != state.StatusRunning || project.CurrentPhase != "pipeline" {
		t.Fatalf("run project = %#v", project)
	}
	if project.WorktreePath == root || project.BranchName != "gg/demo-project" {
		t.Fatalf("project metadata = %#v, want dedicated worktree and branch", project)
	}
	if _, err := os.Stat(project.WorktreePath); err != nil {
		t.Fatalf("created worktree %q: %v", project.WorktreePath, err)
	}

	if _, _, code := runApp(t, app, "stop", "demo-project"); code != 0 {
		t.Fatalf("stop exit=%d", code)
	}
	stopped, err := store.Load(context.Background(), "demo-project")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status != state.StatusStopped || stopped.StatusChangedAt.IsZero() {
		t.Fatalf("stopped project = %#v", stopped)
	}

	// A new application instance must discover the same durable state and resume it.
	restarted := New(WithRootResolver(fixedRoot{root: root}))
	if _, _, code := runApp(t, restarted, "resume", "demo-project"); code != 0 {
		t.Fatalf("resume exit=%d", code)
	}
	resumed, err := store.Load(context.Background(), "demo-project")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != state.StatusRunning || len(resumed.PhaseHistory) < 2 {
		t.Fatalf("resumed project = %#v", resumed)
	}

	list, err := restarted.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Slug != "demo-project" || !list[0].Status.IsActive() {
		t.Fatalf("Projects() = %#v", list)
	}
}

func TestPruneRequiresConfirmationAndPreservesStateWhenDeclined(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	seed := New(WithRootResolver(fixedRoot{root: root}))
	if _, stderr, code := runApp(t, seed, "run", "confirmation-project"); code != 0 {
		t.Fatalf("run exit=%d stderr=%q", code, stderr)
	}
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	if _, err := service.Transition(context.Background(), "confirmation-project", state.StatusFinished, "pipeline", "finished", nil); err != nil {
		t.Fatal(err)
	}
	app := New(WithRootResolver(fixedRoot{root: root}), WithInput(bytes.NewBufferString("n\n")))
	var stdout, stderr bytes.Buffer
	if code := app.Run(context.Background(), []string{"prune"}, &stdout, &stderr); code != 0 {
		t.Fatalf("declined prune exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Prune cancelled.") {
		t.Fatalf("confirmation output=%q", stdout.String())
	}
	if _, err := store.Load(context.Background(), "confirmation-project"); err != nil {
		t.Fatalf("declined prune removed state: %v", err)
	}
}

func TestPruneRemovesOnlyFinishedCleanOwnedProjectsAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	app := New(WithRootResolver(fixedRoot{root: root}))
	if _, stderr, code := runApp(t, app, "run", "finished-project"); code != 0 {
		t.Fatalf("run exit=%d stderr=%q", code, stderr)
	}
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	if _, err := service.Transition(context.Background(), "finished-project", state.StatusFinished, "pipeline", "finished", nil); err != nil {
		t.Fatal(err)
	}
	project, err := store.Load(context.Background(), "finished-project")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, code := runApp(t, app, "prune", "--yes"); code != 0 {
		t.Fatalf("prune exit=%d", code)
	}
	if _, err := store.Load(context.Background(), project.Slug); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state load error=%v, want removed", err)
	}
	if _, err := os.Stat(project.WorktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree error=%v, want removed", err)
	}
	branchCheck := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/gg/finished-project")
	branchCheck.Dir = root
	if err := branchCheck.Run(); err == nil {
		t.Fatal("owned branch survived prune; leftover branches block recreating same-name projects")
	}
	if _, _, code := runApp(t, New(WithRootResolver(fixedRoot{root: root})), "prune", "--yes"); code != 0 {
		t.Fatalf("second prune exit=%d", code)
	}
}

func TestPrunePreservesActiveProject(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	app := New(WithRootResolver(fixedRoot{root: root}))
	if _, stderr, code := runApp(t, app, "run", "active-project"); code != 0 {
		t.Fatalf("run exit=%d stderr=%q", code, stderr)
	}
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.Load(context.Background(), "active-project")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, code := runApp(t, app, "prune", "--yes"); code != 0 {
		t.Fatalf("prune exit=%d", code)
	}
	if _, err := store.Load(context.Background(), project.Slug); err != nil {
		t.Fatalf("active state was removed: %v", err)
	}
	if _, err := os.Stat(project.WorktreePath); err != nil {
		t.Fatalf("active worktree was removed: %v", err)
	}
}

func TestPrunePreservesStoppedAndRemovesTerminatedProjects(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	app := New(WithRootResolver(fixedRoot{root: root}))
	for _, name := range []string{"stopped-project", "terminated-project"} {
		if _, stderr, code := runApp(t, app, "run", name); code != 0 {
			t.Fatalf("run %s exit=%d stderr=%q", name, code, stderr)
		}
	}
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	if _, err := service.Transition(context.Background(), "stopped-project", state.StatusStopped, "pipeline", "stopped", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Transition(context.Background(), "terminated-project", state.StatusTerminated, "pipeline", "terminated", nil); err != nil {
		t.Fatal(err)
	}
	stopped, err := store.Load(context.Background(), "stopped-project")
	if err != nil {
		t.Fatal(err)
	}
	terminated, err := store.Load(context.Background(), "terminated-project")
	if err != nil {
		t.Fatal(err)
	}

	if _, stderr, code := runApp(t, app, "prune", "--yes"); code != 0 {
		t.Fatalf("prune exit=%d stderr=%q", code, stderr)
	}
	if got, err := store.Load(context.Background(), stopped.Slug); err != nil || got.Status != state.StatusStopped {
		t.Fatalf("stopped state = %#v, error=%v; prune must preserve stopped projects", got, err)
	}
	if _, err := os.Stat(stopped.WorktreePath); err != nil {
		t.Fatalf("stopped worktree was removed: %v", err)
	}
	if _, err := store.Load(context.Background(), terminated.Slug); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminated state load error=%v, want removed", err)
	}
	if _, err := os.Stat(terminated.WorktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminated worktree error=%v, want removed", err)
	}
}

func TestPruneForcefullyRemovesDirtyTerminalWorktree(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	app := New(WithRootResolver(fixedRoot{root: root}))
	if _, stderr, code := runApp(t, app, "run", "dirty-project"); code != 0 {
		t.Fatalf("run exit=%d stderr=%q", code, stderr)
	}
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	if _, err := service.Transition(context.Background(), "dirty-project", state.StatusFinished, "pipeline", "finished", nil); err != nil {
		t.Fatal(err)
	}
	project, err := store.Load(context.Background(), "dirty-project")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project.WorktreePath, "untracked.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runApp(t, app, "prune", "--yes")
	if code != 0 {
		t.Fatalf("prune of dirty terminal project failed: code=%d stderr=%q", code, stderr)
	}
	// Pruning a terminal project discards uncommitted leftovers by design.
	if _, err := store.Load(context.Background(), project.Slug); err == nil {
		t.Fatal("project state survived prune")
	}
	if _, err := os.Stat(project.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("dirty worktree survived prune: %v", err)
	}
}

func TestPruneRefusesMismatchedPersistedMetadata(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	app := New(WithRootResolver(fixedRoot{root: root}))
	if _, stderr, code := runApp(t, app, "run", "mismatch-project"); code != 0 {
		t.Fatalf("run exit=%d stderr=%q", code, stderr)
	}
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project, err := store.Load(context.Background(), "mismatch-project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Transition(context.Background(), project.Slug, state.StatusFinished, "pipeline", "finished", nil); err != nil {
		t.Fatal(err)
	}
	project, err = store.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	project.BranchName = "gg/unrelated"
	if err := store.Save(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runApp(t, app, "prune", "--yes")
	if code == 0 || !strings.Contains(stderr, "does not match owned path") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if _, err := store.Load(context.Background(), project.Slug); err != nil {
		t.Fatalf("mismatched state was lost: %v", err)
	}
}

func TestLifecycleCommandContextAndRootErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	app := New(WithRootResolver(fixedRoot{root: t.TempDir()}))
	_, stderr, code := runAppContext(t, ctx, app, "run", "cancelled")
	if code == 0 || !strings.Contains(stderr, "context canceled") {
		t.Fatalf("canceled run code=%d stderr=%q", code, stderr)
	}

	app = New(WithRootResolver(fixedRoot{err: errors.New("config unavailable")}))
	_, stderr, code = runApp(t, app, "list")
	if code == 0 || !strings.Contains(stderr, "resolve configured root") {
		t.Fatalf("root error code=%d stderr=%q", code, stderr)
	}
}

func runApp(t *testing.T, app *App, args ...string) (string, string, int) {
	t.Helper()
	return runAppContext(t, context.Background(), app, args...)
}

func runAppContext(t *testing.T, ctx context.Context, app *App, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := app.Run(ctx, args, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

func initTestRepository(t *testing.T, root string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README")
	run("commit", "-qm", "initial")
}

func TestRunInNonRepositoryFolderCreatesGitDisabledProjectInPlace(t *testing.T) {
	// A configured folder that is not a git repository is used directly: no
	// worktree, no branch, and the project is marked GitDisabled so commit
	// enforcement and the rebase/PR/CI phases are skipped.
	root := t.TempDir()
	app := New(WithRootResolver(fixedRoot{root: root}))
	runApp(t, app, "run", "not-a-repository")
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.Load(context.Background(), "not-a-repository")
	if err != nil {
		t.Fatalf("project was not persisted: %v", err)
	}
	if !project.GitDisabled || project.BranchName != "" {
		t.Fatalf("project = gitDisabled %v branch %q, want git-disabled and branchless", project.GitDisabled, project.BranchName)
	}
	if filepath.Clean(project.WorktreePath) != filepath.Clean(root) {
		t.Fatalf("worktree = %q, want the configured folder %q", project.WorktreePath, root)
	}
}

func TestConcurrentRunsOnlyRollbackWorktreeCreatedByInvocation(t *testing.T) {
	repo := t.TempDir()
	initTestRepository(t, repo)
	stateRoot := t.TempDir()
	persistenceErr := errors.New("state persistence failed")
	lifecycle := &failingLifecycle{createErr: persistenceErr}
	worktrees := &concurrentWorktreeService{
		repo:  repo,
		ready: make(chan struct{}),
	}
	app := New(
		WithWorkingDirectory(func() (string, error) { return repo, nil }),
		WithRootResolver(fixedRoot{root: stateRoot}),
		WithGitClient(worktrees),
		WithLifecycleService(lifecycle),
	)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var stdout, stderr bytes.Buffer
			errs[i] = app.run(context.Background(), []string{"run", "concurrent-project"}, &stdout)
			_ = stderr
		}(i)
	}
	wg.Wait()

	for _, err := range errs {
		if !errors.Is(err, persistenceErr) {
			t.Fatalf("run error = %v, want persistence failure", err)
		}
	}
	if got := worktrees.removeCount(); got != 1 {
		t.Fatalf("worktree removals = %d, want exactly one owner rollback", got)
	}
}

type concurrentWorktreeService struct {
	repo    string
	ready   chan struct{}
	mu      sync.Mutex
	calls   int
	removes int
}

func (s *concurrentWorktreeService) RepositoryRoot(context.Context) (string, error) {
	return s.repo, nil
}
func (s *concurrentWorktreeService) LookupWorktree(context.Context, string, string) (git.Worktree, error) {
	return git.Worktree{}, git.ErrWorktreeNotFound
}
func (s *concurrentWorktreeService) EnsureWorktree(_ context.Context, request git.WorktreeRequest) (git.Worktree, bool, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	if call == 2 {
		close(s.ready)
	}
	s.mu.Unlock()
	<-s.ready
	return git.Worktree{Path: request.Path, Branch: request.Branch}, call == 1, nil
}
func (s *concurrentWorktreeService) RemoveWorktree(context.Context, string, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removes++
	return nil
}
func (s *concurrentWorktreeService) removeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removes
}

func TestRunRollsBackNewWorktreeWhenStatePersistenceFails(t *testing.T) {
	repo := t.TempDir()
	initTestRepository(t, repo)
	stateRoot := t.TempDir()
	persistenceErr := errors.New("state persistence failed")
	lifecycle := &failingLifecycle{createErr: persistenceErr}
	app := New(WithRootResolver(fixedRoot{root: stateRoot}), WithGitClient(git.NewClient(repo, nil)), WithLifecycleService(lifecycle))
	_, stderr, code := runApp(t, app, "run", "rollback-project")
	if code == 0 || !strings.Contains(stderr, persistenceErr.Error()) {
		t.Fatalf("code=%d stderr=%q, want persistence failure", code, stderr)
	}
	naming, err := git.ProjectNamingFor(repo, "rollback-project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(naming.WorktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree path error = %v, want removed worktree", err)
	}
}

type failingLifecycle struct{ createErr error }

func (f *failingLifecycle) Create(context.Context, state.ProjectState) error { return f.createErr }
func (f *failingLifecycle) Load(context.Context, string) (state.ProjectState, error) {
	return state.ProjectState{}, os.ErrNotExist
}
func (f *failingLifecycle) List(context.Context) ([]state.ProjectState, error) { return nil, nil }
func (f *failingLifecycle) Delete(context.Context, string) error               { return nil }
func (f *failingLifecycle) Transition(context.Context, string, state.LifecycleStatus, string, string, []string) (state.ProjectState, error) {
	return state.ProjectState{}, nil
}

func TestRunNormalizesSelectorAndValidatesExistingProjectBeforePipeline(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	app := New(WithRootResolver(fixedRoot{root: root}))
	if _, stderr, code := runApp(t, app, "run", "Fancy Project"); code != 0 {
		t.Fatalf("initial run exit=%d stderr=%q", code, stderr)
	}
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.Load(context.Background(), "fancy-project")
	if err != nil {
		t.Fatal(err)
	}
	if project.Name != "Fancy Project" || project.Slug != "fancy-project" || project.BranchName != "gg/fancy-project" {
		t.Fatalf("normalized project = %#v", project)
	}
	project.WorktreePath = filepath.Join(root, "not-the-owned-worktree")
	if err := store.Save(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	capture := &capturePipeline{}
	app = New(WithRootResolver(fixedRoot{root: root}), WithPipelineService(capture))
	_, stderr, code := runApp(t, app, "run", "FANCY PROJECT")
	if code == 0 || !strings.Contains(stderr, "persisted worktree metadata") {
		t.Fatalf("code=%d stderr=%q, want existing metadata validation failure", code, stderr)
	}
	if len(capture.runRequests) != 0 {
		t.Fatal("pipeline ran with corrupt existing project metadata")
	}
}

type failingPipeline struct{ err error }

func (p *failingPipeline) Run(context.Context, pipeline.RunRequest) error { return p.err }
func (*failingPipeline) Stop(context.Context, pipeline.StopRequest) error { return nil }
func (*failingPipeline) Prune(context.Context) error                      { return nil }

func TestRunRollsBackActiveReservationWhenDispatchFails(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	dispatchErr := errors.New("dispatch setup failed")
	app := New(WithRootResolver(fixedRoot{root: root}), WithPipelineService(&failingPipeline{err: dispatchErr}))
	_, stderr, code := runApp(t, app, "run", "dispatch-failure")
	if code == 0 || !strings.Contains(stderr, dispatchErr.Error()) {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.Load(context.Background(), "dispatch-failure")
	if err != nil {
		t.Fatal(err)
	}
	if project.Status != state.StatusPending {
		t.Fatalf("project status = %s, want rollback to pending", project.Status)
	}
	if _, err := os.Stat(project.WorktreePath); err != nil {
		t.Fatalf("worktree was not preserved: %v", err)
	}
}

type projectEventRecorder struct{ events []orchestrator.Event }

func (r *projectEventRecorder) Publish(_ context.Context, event orchestrator.Event) error {
	r.events = append(r.events, event)
	return nil
}

type projectPromptStub struct{ input orchestrator.ProjectInput }

func (p projectPromptStub) Prompt(context.Context, io.Writer) (orchestrator.ProjectInput, error) {
	return p.input, nil
}

func TestRunPromptsAndPersistsInferredProject(t *testing.T) {
	repo := t.TempDir()
	initTestRepository(t, repo)
	stateRoot := t.TempDir()
	events := &projectEventRecorder{}
	app := New(
		WithRootResolver(fixedRoot{root: stateRoot}),
		WithGitClient(git.NewClient(repo, nil)),
		WithProjectPrompter(projectPromptStub{input: orchestrator.ProjectInput{
			Goal:               "Build a release dashboard for operators.",
			AcceptanceCriteria: []string{"Persist the project goal", "Publish project creation"},
		}}),
		WithProjectEventSink(events),
	)
	if _, stderr, code := runApp(t, app, "run"); code != 0 {
		t.Fatalf("run exit=%d stderr=%q", code, stderr)
	}
	store, err := state.NewFileStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.Load(context.Background(), "release-dashboard-operators")
	if err != nil {
		t.Fatal(err)
	}
	if project.Name != "release_dashboard_operators" || project.OriginalGoal != "Build a release dashboard for operators." {
		t.Fatalf("project input = %#v", project)
	}
	if len(project.AcceptanceCriteria) != 2 || project.Status != state.StatusRunning {
		t.Fatalf("project lifecycle = %#v", project)
	}
	if len(events.events) != 1 || events.events[0].Type != orchestrator.EventProjectCreated || events.events[0].ProjectSlug != project.Slug {
		t.Fatalf("events = %#v", events.events)
	}
}

func TestInteractiveRunReservesCreatedProjectBeforeControllerDispatch(t *testing.T) {
	repo := t.TempDir()
	initTestRepository(t, repo)
	root := t.TempDir()
	store := mustStateStore(t, root)
	service := state.NewLifecycleService(store, nil, store.Locker())
	controller := &captureController{}
	app := New(
		WithRootResolver(fixedRoot{root: root}),
		WithConfigStore(configuredMemoryStore()),
		WithGitClient(git.NewClient(repo, nil)),
		WithLifecycleService(service),
		WithProjectPrompter(projectPromptStub{input: orchestrator.ProjectInput{
			Goal:               "Build an interactive dashboard.",
			AcceptanceCriteria: []string{"Persist the created run"},
		}}),
		WithOrchestratorController(controller),
	)
	if _, stderr, code := runApp(t, app, "run"); code != 0 {
		t.Fatalf("run exit=%d stderr=%q", code, stderr)
	}
	project, err := service.Load(context.Background(), "interactive-dashboard")
	if err != nil {
		t.Fatal(err)
	}
	if project.Status != state.StatusRunning {
		t.Fatalf("created project status = %s, want running before Execute", project.Status)
	}
	if len(controller.executes) != 1 || controller.executes[0].Project.Status != state.StatusRunning {
		t.Fatalf("controller received projects=%#v, want reserved running project", controller.executes)
	}
}

func TestRunRejectsProjectInputWithoutAcceptanceCriteria(t *testing.T) {
	repo := t.TempDir()
	initTestRepository(t, repo)
	stateRoot := t.TempDir()
	app := New(
		WithRootResolver(fixedRoot{root: stateRoot}),
		WithGitClient(git.NewClient(repo, nil)),
		WithProjectPrompter(projectPromptStub{input: orchestrator.ProjectInput{Goal: "Build a dashboard"}}),
	)
	_, stderr, code := runApp(t, app, "run")
	if code == 0 || !strings.Contains(stderr, "at least one acceptance criterion is required") {
		t.Fatalf("code=%d stderr=%q, want missing-criteria validation failure", code, stderr)
	}
	store, err := state.NewFileStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), "build-a-dashboard"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state load error = %v, want missing state", err)
	}
}

type failOnceRunner struct {
	calls    int
	failOnce bool
}

func (r *failOnceRunner) Run(_ context.Context, request agent.RunRequest) (agent.RunResult, error) {
	r.calls++
	if r.failOnce && r.calls == 1 {
		return agent.RunResult{ProjectSlug: request.Project.Slug, Phase: request.Phase, Subphase: request.Subphase, Status: state.StatusFailed}, errors.New("phase execution failed")
	}
	if request.Phase == pipeline.PhasePlanning {
		if err := writeMinimalValidPlanningArtifact(request.WorkingDirectory); err != nil {
			return agent.RunResult{ProjectSlug: request.Project.Slug, Phase: request.Phase, Subphase: request.Subphase, Status: state.StatusFailed}, err
		}
	}
	return agent.RunResult{ProjectSlug: request.Project.Slug, Phase: request.Phase, Subphase: request.Subphase, Status: state.StatusFinished}, nil
}

func writeMinimalValidPlanningArtifact(worktree string) error {
	if err := os.MkdirAll(filepath.Join(worktree, ".gg"), 0o755); err != nil {
		return err
	}
	const artifact = `---
gg_run_id: "test-run"
gg_disposition: passed
gg_plan_complexity: "Trivial"
gg_plan_complexity_evidence: ["The test scope is one cohesive outcome."]
gg_plan_phases: ["Phase 1: test scope"]
gg_plan_phase_boundaries: [{"phase":"Phase 1: test scope","justification":"The test scope has no dependency ordering."}]
---
# Implementation Plan

## Complexity assessment

- Complexity category: **Trivial**
- Selected phase count: **1**

Supporting evidence:

1. The test scope is one cohesive outcome.

## Phase 1: test scope

Boundary justification: The test scope has no dependency ordering.
`
	return os.WriteFile(filepath.Join(worktree, ".gg", "plan.md"), []byte(artifact), 0o644)
}

func TestFailedControllerRunPersistsFailedStateAndResumeAcrossAppRestart(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	app := New(WithRootResolver(fixedRoot{root: root}))
	if _, stderr, code := runApp(t, app, "run", "durable-failure"); code != 0 {
		t.Fatalf("setup run exit=%d stderr=%q", code, stderr)
	}

	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := state.NewLifecycleService(store, nil, store.Locker())
	if _, err := lifecycle.Transition(context.Background(), "durable-failure", state.StatusStopped, "pipeline", "run", nil); err != nil {
		t.Fatal(err)
	}
	events := &projectEventRecorder{}
	runner := &failOnceRunner{failOnce: true}
	controller := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(lifecycle),
		orchestrator.WithEventSink(events),
		orchestrator.WithPromptBuilder(agent.StandalonePromptBuilder{}),
	)
	app = New(
		WithRootResolver(fixedRoot{root: root}),
		WithConfigStore(configuredMemoryStore()),
		WithGitClient(git.NewClient(root, nil)),
		WithLifecycleService(lifecycle),
		WithOrchestratorController(controller),
	)
	if _, stderr, code := runApp(t, app, "run", "durable-failure"); code == 0 || !strings.Contains(stderr, "phase execution failed") {
		t.Fatalf("failed run exit=%d stderr=%q", code, stderr)
	}
	failed, err := store.Load(context.Background(), "durable-failure")
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != state.StatusFailed {
		t.Fatalf("failed run status = %s, want failed", failed.Status)
	}
	if failed.RunReservationToken != "" {
		t.Fatalf("failed run retained reservation token %q", failed.RunReservationToken)
	}
	if len(failed.PhaseHistory) == 0 || failed.PhaseHistory[len(failed.PhaseHistory)-1].Status != state.StatusFailed {
		t.Fatalf("failed run history = %#v", failed.PhaseHistory)
	}
	if len(events.events) < 2 || events.events[len(events.events)-1].Type != orchestrator.EventPhaseFailed {
		t.Fatalf("failed run events = %#v", events.events)
	}

	// A fresh application instance must be able to resume the durable failed
	// state; this also proves rollback did not leave it stuck in running.
	storeAfterRestart, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	lifecycleAfterRestart := state.NewLifecycleService(storeAfterRestart, nil, storeAfterRestart.Locker())
	resumeEvents := &projectEventRecorder{}
	resumeController := orchestrator.NewController(
		orchestrator.WithRunner(&failOnceRunner{}),
		orchestrator.WithPhaseState(lifecycleAfterRestart),
		orchestrator.WithEventSink(resumeEvents),
		orchestrator.WithPromptBuilder(agent.StandalonePromptBuilder{}),
	)
	app = New(
		WithRootResolver(fixedRoot{root: root}),
		WithConfigStore(configuredMemoryStore()),
		WithGitClient(git.NewClient(root, nil)),
		WithLifecycleService(lifecycleAfterRestart),
		WithOrchestratorController(resumeController),
	)
	if stdout, stderr, code := runApp(t, app, "resume", "durable-failure"); code != 0 {
		t.Fatalf("resume exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	finished, err := storeAfterRestart.Load(context.Background(), "durable-failure")
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != state.StatusFinished {
		t.Fatalf("resumed status = %s, want finished", finished.Status)
	}
	if len(resumeEvents.events) == 0 || resumeEvents.events[len(resumeEvents.events)-1].Type != orchestrator.EventProjectFinished {
		t.Fatalf("resume events = %#v", resumeEvents.events)
	}
}

func TestNewUsesProviderAwareCatalogSource(t *testing.T) {
	app := New()
	catalog, err := app.catalogSource.AgentCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := catalog.Lookup(config.AgentClaude)
	if !ok || entry.Provider != "anthropic" || entry.Harness != "claude-code" || entry.CLI != "claude" {
		t.Fatalf("production catalog = %#v, ok=%v", entry, ok)
	}
}

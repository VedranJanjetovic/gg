package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
	gggit "github.com/VedranJanjetovic/gg/internal/git"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

func TestProductionControllerRejectsSuccessfulRebaseWithUnmergedGitIndex(t *testing.T) {
	worktree := makeUnmergedGitWorktree(t)
	runner := &finiteRunner{}
	store := &fakeState{}
	controller := orchestrator.NewProductionController(
		runner,
		store,
		gggit.NewClient(worktree, nil),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	req := request(t, resolvedPipeline(t))
	req.Project.WorktreePath = worktree
	req.AllowDevelopmentSubphaseWithoutCommit = true

	outcomes, err := controller.Execute(context.Background(), req)

	if err == nil {
		t.Fatal("Execute() accepted exit-zero Rebase while Git still had unmerged paths")
	}
	if len(outcomes) == 0 {
		t.Fatal("Execute() returned no Rebase outcome")
	}
	last := outcomes[len(outcomes)-1]
	if last.Result.Phase != pipeline.PhaseRebase || last.Result.Status != state.StatusFailed || !last.ConflictResolutionNeeded {
		t.Fatalf("last outcome = %#v, want failed Rebase requiring conflict resolution", last)
	}
}

func TestStopDuringPostConflictQASurvivesRestartWithoutReplayingRebase(t *testing.T) {
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project := durableExecutionProject(t, service)
	plan := resolvedPipeline(t, config.PhaseQA)
	reader := &mutableConflictReader{unresolved: true}

	conflictRunner := &feedbackRunner{
		statuses: []state.LifecycleStatus{
			state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished,
			state.StatusFinished, state.StatusFailed,
		},
		artifacts: []string{"rebase-report.md"},
	}
	controller := orchestrator.NewProductionController(
		conflictRunner,
		service,
		reader,
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	req := resumeRequest(t, project, plan)
	req.MaxIterations = 3
	if _, err := controller.Execute(context.Background(), req); err == nil {
		t.Fatal("initial Rebase conflict unexpectedly succeeded")
	}
	conflicted, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}

	reader.unresolved = false
	postConflictRunner := &feedbackRunner{
		statuses:  []state.LifecycleStatus{state.StatusFailed, state.StatusStopped},
		artifacts: []string{"qa-report.md"},
	}
	postConflictController := orchestrator.NewProductionController(
		postConflictRunner,
		service,
		reader,
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	resumeReq := resumeRequest(t, conflicted, plan)
	resumeReq.RunID = "run-2"
	_, err = postConflictController.Resume(context.Background(), orchestrator.ResumeRequest{
		ProjectSlug: project.Slug,
		RunID:       "run-2",
		Execution:   resumeReq,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("post-conflict Resume() error = %v, want stopped QA fix", err)
	}
	stopped, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status != state.StatusStopped || stopped.QALoopStage != "fix" {
		t.Fatalf("stopped post-conflict cursor = %#v", stopped)
	}

	restartedStore, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	restartedService := state.NewLifecycleService(restartedStore, nil, restartedStore.Locker())
	finishRunner := &finiteRunner{}
	restartedController := orchestrator.NewProductionController(
		finishRunner,
		restartedService,
		reader,
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	finishReq := resumeRequest(t, stopped, plan)
	finishReq.RunID = "run-3"
	if _, err := restartedController.Resume(context.Background(), orchestrator.ResumeRequest{
		ProjectSlug: project.Slug,
		RunID:       "run-3",
		Execution:   finishReq,
	}); err != nil {
		t.Fatalf("second Resume() error = %v", err)
	}

	want := []string{
		"development/implementation", "development/testing", "development/review",
		"qa/", "test_document/",
	}
	if !reflect.DeepEqual(finishRunner.calls, want) {
		t.Fatalf("post-conflict resume dispatches = %v, want %v (Rebase must remain skipped)", finishRunner.calls, want)
	}
	for i, artifacts := range finishRunner.artifacts {
		if !containsRegressionString(artifacts, "rebase-report.md") {
			t.Fatalf("post-conflict dispatch %d lost conflict evidence: %v", i, artifacts)
		}
	}
}

type cancelAfterFirstQAFixSuccess struct {
	cancel context.CancelFunc
}

func (s cancelAfterFirstQAFixSuccess) Publish(_ context.Context, event orchestrator.Event) error {
	if event.Type == orchestrator.EventPhaseSucceeded &&
		event.Phase == pipeline.PhaseDevelopment &&
		event.Subphase == "implementation" &&
		event.Outcome != nil &&
		event.Outcome.Iteration == 1 {
		s.cancel()
	}
	return nil
}

func TestResumeAfterSuccessfulQAFixSubphaseStartsAtNextSubphase(t *testing.T) {
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project := durableExecutionProject(t, service)
	plan := resolvedPipeline(t, config.PhaseQA)
	runner := &feedbackRunner{
		statuses: []state.LifecycleStatus{
			state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished,
			state.StatusFailed, state.StatusFinished,
		},
		artifacts: []string{"qa-report.md"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	controller := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(service),
		orchestrator.WithEventSink(cancelAfterFirstQAFixSuccess{cancel: cancel}),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	req := resumeRequest(t, project, plan)
	req.MaxIterations = 3
	if _, err := controller.Execute(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v, want cancellation after durable fix result", err)
	}
	stopped, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status != state.StatusStopped || stopped.QALoopStage != "fix" ||
		stopped.CurrentPhase != string(pipeline.PhaseDevelopment) ||
		stopped.CurrentSubphase != "implementation" {
		t.Fatalf("stopped fix cursor = %#v", stopped)
	}

	resumeRunner := &finiteRunner{}
	resumeController := orchestrator.NewController(
		orchestrator.WithRunner(resumeRunner),
		orchestrator.WithPhaseState(service),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	resumeReq := resumeRequest(t, stopped, plan)
	resumeReq.RunID = "run-2"
	if _, err := resumeController.Resume(context.Background(), orchestrator.ResumeRequest{
		ProjectSlug: project.Slug,
		RunID:       "run-2",
		Execution:   resumeReq,
	}); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if len(resumeRunner.calls) == 0 || resumeRunner.calls[0] != "development/testing" {
		t.Fatalf("resume dispatches = %v, want next fix subphase development/testing", resumeRunner.calls)
	}
	if containsRegressionString(resumeRunner.calls, "development/implementation") {
		t.Fatalf("resume replayed already successful fix subphase: %v", resumeRunner.calls)
	}
}

type failFinalizationOnceState struct {
	*state.LifecycleService
	failed bool
}

func (s *failFinalizationOnceState) Transition(ctx context.Context, slug string, target state.LifecycleStatus, phase, subphase string, artifacts []string) (state.ProjectState, error) {
	if target == state.StatusFinished && phase == "" && subphase == "" && !s.failed {
		s.failed = true
		return state.ProjectState{}, errors.New("injected finalization failure")
	}
	return s.LifecycleService.Transition(ctx, slug, target, phase, subphase, artifacts)
}

func TestResumeAfterFinalPhaseResultOnlyRetriesProjectFinalization(t *testing.T) {
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project := durableExecutionProject(t, service)
	wrapped := &failFinalizationOnceState{LifecycleService: service}
	plan := resolvedPipeline(t)
	firstRunner := &finiteRunner{}
	controller := orchestrator.NewController(
		orchestrator.WithRunner(firstRunner),
		orchestrator.WithPhaseState(wrapped),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	req := resumeRequest(t, project, plan)
	if _, err := controller.Execute(context.Background(), req); err == nil || !strings.Contains(err.Error(), "injected finalization failure") {
		t.Fatalf("Execute() error = %v, want finalization failure", err)
	}
	failed, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != state.StatusFailed || failed.CurrentPhase != string(pipeline.PhaseTestDocument) {
		t.Fatalf("state after final phase = %#v", failed)
	}
	last := failed.PhaseHistory[len(failed.PhaseHistory)-1]
	if last.Phase != string(pipeline.PhaseTestDocument) || last.Status != state.StatusFinished {
		t.Fatalf("last durable phase = %#v, want finished final phase", last)
	}

	resumeRunner := &finiteRunner{}
	resumeController := orchestrator.NewController(
		orchestrator.WithRunner(resumeRunner),
		orchestrator.WithPhaseState(wrapped),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	resumeReq := resumeRequest(t, failed, plan)
	resumeReq.RunID = "run-2"
	if _, err := resumeController.Resume(context.Background(), orchestrator.ResumeRequest{
		ProjectSlug: project.Slug,
		RunID:       "run-2",
		Execution:   resumeReq,
	}); err != nil {
		t.Fatalf("Resume() finalization error = %v", err)
	}
	if len(resumeRunner.calls) != 0 {
		t.Fatalf("resume replayed phases after durable final result: %v", resumeRunner.calls)
	}
	finished, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != state.StatusFinished {
		t.Fatalf("resumed final status = %s, want finished", finished.Status)
	}
}

func makeUnmergedGitWorktree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runRegressionGit(t, dir, "init", "-q")
	runRegressionGit(t, dir, "config", "user.email", "gg@example.test")
	runRegressionGit(t, dir, "config", "user.name", "gg test")
	if err := os.WriteFile(filepath.Join(dir, "conflict.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runRegressionGit(t, dir, "add", "conflict.txt")
	runRegressionGit(t, dir, "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	runRegressionGit(t, dir, "checkout", "-qb", "side")
	if err := os.WriteFile(filepath.Join(dir, "conflict.txt"), []byte("side\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runRegressionGit(t, dir, "commit", "-qam", "side")
	runRegressionGit(t, dir, "checkout", "-q", "-")
	if err := os.WriteFile(filepath.Join(dir, "conflict.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runRegressionGit(t, dir, "commit", "-qam", "main")

	command := exec.Command("git", "merge", "side")
	command.Dir = dir
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("git merge unexpectedly avoided conflict:\n%s", output)
	}
	return dir
}

func runRegressionGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(t.TempDir(), "empty-gitconfig"),
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func containsRegressionString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

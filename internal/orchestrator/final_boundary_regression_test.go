package orchestrator_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/git"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

type failPhaseFailedEventSink struct {
	cause error
	types []orchestrator.EventType
}

func (s *failPhaseFailedEventSink) Publish(_ context.Context, event orchestrator.Event) error {
	s.types = append(s.types, event.Type)
	if event.Type == orchestrator.EventPhaseFailed {
		return s.cause
	}
	return nil
}

func TestQAFailureEventPersistenceFailureDoesNotDispatchFix(t *testing.T) {
	eventErr := errors.New("persist phase_failed event")
	runner := &feedbackRunner{
		statuses: []state.LifecycleStatus{
			state.StatusFinished,
			state.StatusFinished,
			state.StatusFinished,
			state.StatusFinished,
			state.StatusFailed,
		},
		artifacts: []string{"qa-report.md"},
	}
	events := &failPhaseFailedEventSink{cause: eventErr}
	request := request(t, pipelineWithQA(t))
	request.MaxIterations = 3

	outcomes, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(&feedbackState{}),
		orchestrator.WithEventSink(events),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	).Execute(context.Background(), request)

	if !errors.Is(err, eventErr) {
		t.Fatalf("Execute() error = %v, want phase_failed event persistence error", err)
	}
	if len(outcomes) != 5 || runner.calls != 5 {
		t.Fatalf("outcomes = %d, dispatches = %d, want terminal initial QA boundary", len(outcomes), runner.calls)
	}
	for _, request := range runner.requests {
		if request.Phase == pipeline.PhaseDevelopment && request.ArtifactPaths != nil {
			t.Fatalf("QA fix dispatched after phase_failed event failure: %#v", runner.requests)
		}
	}
}

type cancelingRebaseRunner struct {
	cancel context.CancelFunc
	calls  []string
}

func (r *cancelingRebaseRunner) Run(_ context.Context, request agent.RunRequest) (agent.RunResult, error) {
	r.calls = append(r.calls, string(request.Phase)+"/"+request.Subphase)
	result := agent.RunResult{
		ProjectSlug: request.Project.Slug,
		Phase:       request.Phase,
		Subphase:    request.Subphase,
		Status:      state.StatusFinished,
	}
	if request.Phase == pipeline.PhaseRebase {
		r.cancel()
		result.Status = state.StatusStopped
		result.ArtifactPaths = []string{"rebase-report.md"}
		return result, context.Canceled
	}
	return result, nil
}

type cancellationSensitiveConflictReader struct {
	unresolved  bool
	sawCanceled bool
}

func (r *cancellationSensitiveConflictReader) HasUnresolvedConflicts(ctx context.Context, _ string) (bool, error) {
	if err := ctx.Err(); err != nil {
		r.sawCanceled = true
		return false, err
	}
	return r.unresolved, nil
}

type cancellationSensitiveConflictEvents struct {
	sawConflict bool
	sawCanceled bool
}

func (s *cancellationSensitiveConflictEvents) Publish(ctx context.Context, event orchestrator.Event) error {
	if event.Type != orchestrator.EventConflictDetected {
		return nil
	}
	s.sawConflict = true
	if ctx.Err() != nil {
		s.sawCanceled = true
		return ctx.Err()
	}
	return nil
}

func TestCanceledRebaseWithUnmergedPathsPersistsConflictAndResumeSkipsRebase(t *testing.T) {
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project := durableExecutionProject(t, service)
	plan := resolvedPipeline(t, config.PhaseQA)
	reader := &cancellationSensitiveConflictReader{unresolved: true}
	events := &cancellationSensitiveConflictEvents{}
	ctx, cancel := context.WithCancel(context.Background())
	runner := &cancelingRebaseRunner{cancel: cancel}
	request := resumeRequest(t, project, plan)

	outcomes, err := orchestrator.NewProductionController(
		runner,
		service,
		reader,
		orchestrator.WithEventSink(events),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	).Execute(ctx, request)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v, want Rebase cancellation", err)
	}
	if len(outcomes) == 0 || outcomes[len(outcomes)-1].Result.Phase != pipeline.PhaseRebase {
		t.Fatalf("outcomes = %#v, want terminal Rebase outcome", outcomes)
	}
	persisted, loadErr := service.Load(context.Background(), project.Slug)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if !persisted.PendingRebaseConflict ||
		!reflect.DeepEqual(persisted.RebaseConflictArtifactPaths, []string{"rebase-report.md"}) {
		t.Fatalf("persisted Rebase conflict = %#v", persisted)
	}
	if reader.sawCanceled || events.sawCanceled || !events.sawConflict {
		t.Fatalf("conflict bookkeeping used canceled context: reader=%v events=%#v", reader.sawCanceled, events)
	}

	unresolvedRunner := &finiteRunner{}
	unresolvedController := orchestrator.NewProductionController(
		unresolvedRunner,
		service,
		reader,
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	resume := resumeRequest(t, persisted, plan)
	resume.RunID = "run-2"
	if _, resumeErr := unresolvedController.Resume(context.Background(), orchestrator.ResumeRequest{
		ProjectSlug: project.Slug,
		RunID:       resume.RunID,
		Execution:   resume,
	}); resumeErr == nil {
		t.Fatal("Resume() accepted unresolved Rebase conflict")
	}
	if len(unresolvedRunner.calls) != 0 {
		t.Fatalf("unresolved Resume() dispatched phases: %v", unresolvedRunner.calls)
	}

	reader.unresolved = false
	resolvedRunner := &finiteRunner{}
	resolvedController := orchestrator.NewProductionController(
		resolvedRunner,
		service,
		reader,
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	resume.RunID = "run-3"
	if _, resumeErr := resolvedController.Resume(context.Background(), orchestrator.ResumeRequest{
		ProjectSlug: project.Slug,
		RunID:       resume.RunID,
		Execution:   resume,
	}); resumeErr != nil {
		t.Fatalf("resolved Resume() error = %v", resumeErr)
	}
	if want := []string{"qa/", "test_document/"}; !reflect.DeepEqual(resolvedRunner.calls, want) {
		t.Fatalf("resolved Resume() dispatches = %v, want %v without Rebase replay", resolvedRunner.calls, want)
	}
}

func seedFinishedQAFixCursor(
	t *testing.T,
	service *state.LifecycleService,
	project state.ProjectState,
	subphase string,
) state.ProjectState {
	t.Helper()
	if err := service.ConfigureOrchestration(context.Background(), project.Slug, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateQALoopWithFixCursor(
		context.Background(),
		project.Slug,
		1,
		"fix",
		subphase,
		[]string{"qa-report.md"},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordPhase(
		context.Background(),
		project.Slug,
		string(pipeline.PhaseDevelopment),
		subphase,
		state.StatusRunning,
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordPhase(
		context.Background(),
		project.Slug,
		string(pipeline.PhaseDevelopment),
		subphase,
		state.StatusFinished,
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := service.CloseRun(context.Background(), project.Slug, state.StatusFailed); err != nil {
		t.Fatal(err)
	}
	persisted, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	return persisted
}

func TestResumeReconcilesFinishedQAFixCursorAfterReservation(t *testing.T) {
	tests := []struct {
		name          string
		finished      string
		firstDispatch string
	}{
		{name: "middle fix advances", finished: "implementation", firstDispatch: "development/testing"},
		{name: "final fix returns to QA", finished: "review", firstDispatch: "qa/"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := state.NewFileStore(root)
			if err != nil {
				t.Fatal(err)
			}
			service := state.NewLifecycleService(store, nil, store.Locker())
			project := durableExecutionProject(t, service)
			project = seedFinishedQAFixCursor(t, service, project, test.finished)
			plan := resolvedPipeline(t, config.PhaseQA)
			runner := &finiteRunner{}
			controller := orchestrator.NewController(
				orchestrator.WithRunner(runner),
				orchestrator.WithPhaseState(service),
				orchestrator.WithPromptBuilder(fakePrompt{}),
			)
			execution := resumeRequest(t, project, plan)
			execution.RunID = "resume-fix-cursor"

			if _, err := controller.Resume(context.Background(), orchestrator.ResumeRequest{
				ProjectSlug: project.Slug,
				RunID:       execution.RunID,
				Execution:   execution,
			}); err != nil {
				t.Fatalf("Resume() error = %v", err)
			}
			if len(runner.calls) == 0 || runner.calls[0] != test.firstDispatch {
				t.Fatalf("Resume() dispatches = %v, want first %q", runner.calls, test.firstDispatch)
			}
			if containsRegressionString(runner.calls, "development/"+test.finished) {
				t.Fatalf("Resume() replayed already finished fix %q: %v", test.finished, runner.calls)
			}
		})
	}
}

func TestFailedDevelopmentStillVerifiesIntroducedCommitSignatures(t *testing.T) {
	processErr := errors.New("development process failed")
	signatureErr := errors.New("introduced development commit is signed")
	runner := &fakeSeqRunner{err: processErr, failAt: 2}
	commits := &fakeDevelopmentCommits{head: "base", verifyErr: signatureErr}
	store := &fakeState{}

	_, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(store),
		orchestrator.WithPromptBuilder(fakePrompt{}),
		orchestrator.WithDevelopmentCommitVerifier(commits),
	).Execute(context.Background(), request(t, resolvedPipeline(t, config.PhaseQA)))

	if !errors.Is(err, processErr) || !errors.Is(err, signatureErr) {
		t.Fatalf("Execute() error = %v, want process and signature verification evidence", err)
	}
	if len(commits.verifyBase) != 1 || commits.verifyBase[0] != "base" {
		t.Fatalf("commit verification baselines = %v, want failed Development baseline", commits.verifyBase)
	}
	if len(runner.phases) != 2 {
		t.Fatalf("dispatches = %v, want no phase after rejected failed Development", runner.phases)
	}
	if last := store.calls[len(store.calls)-1]; last.phase != string(pipeline.PhaseDevelopment) ||
		last.status != state.StatusFailed {
		t.Fatalf("last persisted phase = %#v, want failed Development verification evidence", last)
	}
}

type retainedSignedCommitVerifier struct {
	signatureErr error
	verifyBases  []string
	required     []bool
}

func (v *retainedSignedCommitVerifier) HeadCommit(context.Context, string) (string, error) {
	return "development-base", nil
}

func (v *retainedSignedCommitVerifier) InspectDevelopmentWorktree(context.Context, string) ([]git.DevelopmentWorktreeChange, error) {
	return nil, nil
}

func (v *retainedSignedCommitVerifier) AutoCommitUncommittedChanges(context.Context, string, string) error {
	return nil
}

func (v *retainedSignedCommitVerifier) VerifyUnsignedDevelopmentCommits(
	_ context.Context,
	_ string,
	previousHead string,
	requireCommit bool,
) error {
	v.verifyBases = append(v.verifyBases, previousHead)
	v.required = append(v.required, requireCommit)
	return v.signatureErr
}

func TestResumeReverifiesFailedDevelopmentFromPersistedBase(t *testing.T) {
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project := durableExecutionProject(t, service)
	plan := resolvedPipeline(t, config.PhaseQA)
	processErr := errors.New("development process failed")
	signatureErr := errors.New("retained commit is signed")
	verifier := &retainedSignedCommitVerifier{signatureErr: signatureErr}
	firstRunner := &fakeSeqRunner{err: processErr, failAt: 2}
	request := resumeRequest(t, project, plan)
	controller := orchestrator.NewController(
		orchestrator.WithRunner(firstRunner),
		orchestrator.WithPhaseState(service),
		orchestrator.WithPromptBuilder(fakePrompt{}),
		orchestrator.WithDevelopmentCommitVerifier(verifier),
	)
	if _, err := controller.Execute(context.Background(), request); !errors.Is(err, signatureErr) {
		t.Fatalf("Execute() error = %v, want signed commit rejection", err)
	}
	failed, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	last := failed.PhaseHistory[len(failed.PhaseHistory)-1]
	if last.Phase != string(pipeline.PhaseDevelopment) ||
		last.Status != state.StatusFailed ||
		last.Outcome == nil ||
		last.Outcome.DevelopmentBaseCommit != "development-base" {
		t.Fatalf("failed Development evidence = %#v", last)
	}

	resumeRunner := &finiteRunner{}
	resumeController := orchestrator.NewController(
		orchestrator.WithRunner(resumeRunner),
		orchestrator.WithPhaseState(service),
		orchestrator.WithPromptBuilder(fakePrompt{}),
		orchestrator.WithDevelopmentCommitVerifier(verifier),
	)
	resume := resumeRequest(t, failed, plan)
	resume.RunID = "run-2"
	if _, err := resumeController.Resume(context.Background(), orchestrator.ResumeRequest{
		ProjectSlug: project.Slug,
		RunID:       resume.RunID,
		Execution:   resume,
	}); !errors.Is(err, signatureErr) {
		t.Fatalf("Resume() error = %v, want retained signed commit rejection", err)
	}
	if len(resumeRunner.calls) != 0 {
		t.Fatalf("Resume() dispatched after retained signed commit: %v", resumeRunner.calls)
	}
	if len(verifier.verifyBases) != 2 ||
		verifier.verifyBases[1] != "development-base" ||
		verifier.required[1] {
		t.Fatalf("resume verification = bases %v required %v", verifier.verifyBases, verifier.required)
	}
	persisted, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != state.StatusFailed || persisted.RunReservationToken != "" {
		t.Fatalf("Resume() mutated ownership before verification: %#v", persisted)
	}
}

func TestResumeReverifiesInterruptedRunningDevelopmentFromPersistedBase(t *testing.T) {
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project := durableExecutionProject(t, service)
	if _, err := service.RecordPhase(context.Background(), project.Slug, string(pipeline.PhaseDevelopment), "testing", state.StatusRunning, &state.ExecutionOutcome{
		DevelopmentBaseCommit: "development-base",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, recovered, err := service.RecoverStaleRun(context.Background(), project.Slug); err != nil || !recovered {
		t.Fatalf("RecoverStaleRun() = recovered=%v err=%v, want stale running phase preserved", recovered, err)
	}
	stopped, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	verifier := &retainedSignedCommitVerifier{signatureErr: errors.New("retained commit is signed")}
	runner := &finiteRunner{}
	request := resumeRequest(t, stopped, resolvedPipeline(t, config.PhaseQA))
	request.RunID = "run-after-crash"
	controller := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(service),
		orchestrator.WithPromptBuilder(fakePrompt{}),
		orchestrator.WithDevelopmentCommitVerifier(verifier),
	)
	if _, err := controller.Resume(context.Background(), orchestrator.ResumeRequest{
		ProjectSlug: project.Slug,
		RunID:       request.RunID,
		Execution:   request,
	}); !errors.Is(err, verifier.signatureErr) {
		t.Fatalf("Resume() error = %v, want retained signed commit rejection", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("Resume() dispatched after interrupted-ownership verification failed: %v", runner.calls)
	}
	if len(verifier.verifyBases) != 1 || verifier.verifyBases[0] != "development-base" {
		t.Fatalf("resume verification bases = %v, want persisted running checkpoint", verifier.verifyBases)
	}
}

func TestStopRecoversPreBeginReservationAndPublishesStopped(t *testing.T) {
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project := durableExecutionProject(t, service)
	if err := service.CloseRun(context.Background(), project.Slug, state.StatusStopped); err != nil {
		t.Fatal(err)
	}
	reserved, reservation, err := service.ReserveRun(context.Background(), project.Slug, nil)
	if err != nil {
		t.Fatal(err)
	}
	events := &fakeEvents{}
	controller := orchestrator.NewController(
		orchestrator.WithRunner(&finiteRunner{}),
		orchestrator.WithPhaseState(service),
		orchestrator.WithEventSink(events),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)

	if err := controller.Stop(context.Background(), orchestrator.StopRequest{ProjectSlug: project.Slug}); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	recovered, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != state.StatusStopped ||
		recovered.RunReservationToken != "" ||
		recovered.ActiveRunID != "" {
		t.Fatalf("recovered reservation state = %#v", recovered)
	}
	if want := []orchestrator.EventType{orchestrator.EventProjectStopped}; !reflect.DeepEqual(events.types, want) {
		t.Fatalf("events = %v, want %v", events.types, want)
	}
	if err := service.BeginRun(context.Background(), project.Slug, "stale-owner", reserved.RunReservationToken); err == nil {
		t.Fatal("stale reservation owner claimed project after Stop()")
	}
	if _, next, err := service.ReserveRun(context.Background(), project.Slug, nil); err != nil {
		t.Fatalf("recovered project is not reservable: %v", err)
	} else if err := next.Rollback(context.Background()); err != nil {
		t.Fatalf("rollback recovery probe: %v", err)
	}
	if err := reservation.Rollback(context.Background()); err != nil {
		t.Fatalf("stale reservation rollback: %v", err)
	}
}

func TestResumeRejectsSnapshotAndLifecycleQABudgetMismatchBeforeReservation(t *testing.T) {
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project := durableExecutionProject(t, service)
	if err := service.ConfigureOrchestration(context.Background(), project.Slug, 5); err != nil {
		t.Fatal(err)
	}
	if err := service.CloseRun(context.Background(), project.Slug, state.StatusFailed); err != nil {
		t.Fatal(err)
	}
	project, err = service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	runner := &finiteRunner{}
	execution := resumeRequest(t, project, resolvedPipeline(t, config.PhaseQA))
	execution.MaxIterations = 3

	_, err = orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(service),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	).Resume(context.Background(), orchestrator.ResumeRequest{
		ProjectSlug: project.Slug,
		RunID:       "mismatched-budget",
		Execution:   execution,
	})

	if err == nil || !strings.Contains(err.Error(), "snapshot QA maximum 3 does not match lifecycle state 5") {
		t.Fatalf("Resume() error = %v, want actionable QA maximum mismatch", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("Resume() dispatched with mismatched QA budget: %v", runner.calls)
	}
	persisted, loadErr := service.Load(context.Background(), project.Slug)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if persisted.Status != state.StatusFailed || persisted.RunReservationToken != "" {
		t.Fatalf("budget mismatch mutated run ownership: %#v", persisted)
	}
}

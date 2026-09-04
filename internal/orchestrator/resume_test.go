package orchestrator_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

type persistedResumeState struct {
	mu           sync.Mutex
	project      state.ProjectState
	calls        []string
	resetActions []string
}

func (s *persistedResumeState) Load(context.Context, string) (state.ProjectState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.project, nil
}
func (s *persistedResumeState) Transition(_ context.Context, _ string, status state.LifecycleStatus, phase, subphase string, _ []string) (state.ProjectState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.project.Status, s.project.CurrentPhase, s.project.CurrentSubphase = status, phase, subphase
	s.project.PhaseHistory = append(s.project.PhaseHistory, state.PhaseRecord{Phase: phase, Subphase: subphase, Status: status})
	return s.project, nil
}
func (s *persistedResumeState) RecordPhase(_ context.Context, _ string, phase, subphase string, status state.LifecycleStatus, _ *state.ExecutionOutcome, _ []string) (state.ProjectState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, phase+"/"+subphase+":"+string(status))
	s.project.CurrentPhase, s.project.CurrentSubphase = phase, subphase
	s.project.Status = status
	s.project.PhaseHistory = append(s.project.PhaseHistory, state.PhaseRecord{Phase: phase, Subphase: subphase, Status: status})
	return s.project, nil
}

func (s *persistedResumeState) set(project state.ProjectState) {
	s.mu.Lock()
	s.project = project
	s.calls = nil
	s.mu.Unlock()
}
func (s *persistedResumeState) snapshot() state.ProjectState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.project
}

func (s *persistedResumeState) ResetVerificationRemediation(_ context.Context, _ string, nextAction string) (state.ProjectState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetActions = append(s.resetActions, nextAction)
	if s.project.Verification != nil {
		s.project.Verification.RemediationAttempts = 0
		s.project.Verification.NextAction = nextAction
	}
	return s.project, nil
}

// The verification report methods let this fake satisfy the optional
// verificationReportState extension, which the boundary gate requires whenever
// a project carries a verification contract.
func (s *persistedResumeState) RecordVerificationBaselineReport(_ context.Context, _ string, results []state.VerificationCommandResult, baseline []state.VerificationFinding) (state.ProjectState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.project.Verification != nil {
		s.project.Verification.ParentBaselineCaptured = true
		s.project.Verification.ParentResults = results
		s.project.Verification.ParentBaseline = baseline
	}
	return s.project, nil
}

func (s *persistedResumeState) RecordVerificationResultReport(_ context.Context, _ string, results []state.VerificationCommandResult, findings, warnings []state.VerificationFinding, boundary string, attempts int, nextAction string) (state.ProjectState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.project.Verification != nil {
		s.project.Verification.CurrentResults = results
		s.project.Verification.CurrentFindings = findings
		s.project.Verification.Warnings = warnings
		s.project.Verification.BoundaryCursor = boundary
		s.project.Verification.RemediationAttempts = attempts
		s.project.Verification.NextAction = nextAction
	}
	return s.project, nil
}

func (s *persistedResumeState) PromoteVerificationIdentity(_ context.Context, _ string, identity string) (state.ProjectState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.project.Verification != nil {
		s.project.Verification.PromotedRequiredGreen = append(s.project.Verification.PromotedRequiredGreen, identity)
	}
	return s.project, nil
}

type stopAwareRunner struct {
	started chan struct{}
	entered chan struct{}
	calls   []string
}

func (r *stopAwareRunner) Run(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
	r.calls = append(r.calls, string(req.Phase)+"/"+req.Subphase)
	close(r.entered)
	select {
	case <-r.started:
	case <-ctx.Done():
		return agent.RunResult{Phase: req.Phase, Subphase: req.Subphase, Status: state.StatusStopped}, ctx.Err()
	}
	return agent.RunResult{Phase: req.Phase, Subphase: req.Subphase, Status: state.StatusFinished}, nil
}

type finiteRunner struct {
	calls     []string
	artifacts [][]string
}

func (r *finiteRunner) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	r.calls = append(r.calls, string(req.Phase)+"/"+req.Subphase)
	r.artifacts = append(r.artifacts, append([]string(nil), req.ArtifactPaths...))
	return agent.RunResult{Phase: req.Phase, Subphase: req.Subphase, Status: state.StatusFinished}, nil
}

type ownershipBlockingRunner struct {
	entered chan struct{}
	once    sync.Once
}

func (r *ownershipBlockingRunner) Run(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
	r.once.Do(func() { close(r.entered) })
	<-ctx.Done()
	return agent.RunResult{Phase: req.Phase, Subphase: req.Subphase, Status: state.StatusStopped}, ctx.Err()
}

func resumeRequest(t *testing.T, project state.ProjectState, plan pipeline.ExecutablePipeline) orchestrator.Request {
	t.Helper()
	return orchestrator.Request{Project: project, Pipeline: plan, PhaseContracts: map[pipeline.PhaseID]string{}, RunID: "run-1"}
}

func TestStopCancelsActivePhaseAndPersistsStopped(t *testing.T) {
	plan := resolvedPipeline(t, config.PhaseQA)
	project := state.ProjectState{Slug: "demo", OriginalGoal: "goal", AcceptanceCriteria: []string{"criterion"}, WorktreePath: t.TempDir()}
	store := &persistedResumeState{project: project}
	runner := &stopAwareRunner{started: make(chan struct{}), entered: make(chan struct{})}
	controller := orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(store), orchestrator.WithPromptBuilder(fakePrompt{}))
	done := make(chan error, 1)
	go func() {
		_, err := controller.Execute(context.Background(), resumeRequest(t, project, plan))
		done <- err
	}()
	<-runner.entered
	if err := controller.Stop(context.Background(), orchestrator.StopRequest{ProjectSlug: project.Slug, RunID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v, want cancellation", err)
	}
	got := store.snapshot()
	if got.Status != state.StatusStopped {
		t.Fatalf("persisted status = %s, want stopped", got.Status)
	}
	if len(runner.calls) != 1 || len(store.calls) < 2 || store.calls[len(store.calls)-1] != "acceptance_criteria/:stopped" {
		t.Fatalf("calls=%v state calls=%v", runner.calls, store.calls)
	}
}

func TestStopDuringDevelopmentSubphasePreservesResumeCursor(t *testing.T) {
	plan := resolvedPipeline(t, config.PhaseQA)
	project := state.ProjectState{Slug: "demo", OriginalGoal: "goal", AcceptanceCriteria: []string{"criterion"}, WorktreePath: t.TempDir(), Status: state.StatusStopped, CurrentPhase: string(pipeline.PhaseDevelopment), CurrentSubphase: "implement", PhaseHistory: []state.PhaseRecord{{Phase: string(pipeline.PhaseDevelopment), Subphase: "implement", Status: state.StatusStopped}}}
	store := &persistedResumeState{project: project}
	runner := &stopAwareRunner{started: make(chan struct{}), entered: make(chan struct{})}
	req := resumeRequest(t, project, plan)
	req.Subphases = pipeline.DevelopmentSubphaseGeneration{Mode: pipeline.DevelopmentSubphasesOverride, Subphases: []pipeline.DevelopmentSubphaseDefinition{{ID: "design", DisplayName: "Design"}, {ID: "implement", DisplayName: "Implement"}}}
	controller := orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(store), orchestrator.WithPromptBuilder(fakePrompt{}))
	done := make(chan error, 1)
	go func() {
		_, err := controller.Resume(context.Background(), orchestrator.ResumeRequest{ProjectSlug: project.Slug, RunID: req.RunID, Execution: req})
		done <- err
	}()
	<-runner.entered
	if err := controller.Stop(context.Background(), orchestrator.StopRequest{ProjectSlug: project.Slug, RunID: req.RunID}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Resume() error = %v, want cancellation", err)
	}
	if !reflect.DeepEqual(runner.calls, []string{"development/implement"}) {
		t.Fatalf("development dispatches=%v", runner.calls)
	}
	got := store.snapshot()
	if got.Status != state.StatusStopped || got.CurrentPhase != string(pipeline.PhaseDevelopment) || got.CurrentSubphase != "implement" {
		t.Fatalf("stopped cursor=%#v", got)
	}
}

func TestResumeUsesCachedRequestAfterStoppedRun(t *testing.T) {
	plan := resolvedPipeline(t, config.PhaseQA)
	project := state.ProjectState{Slug: "demo", OriginalGoal: "goal", AcceptanceCriteria: []string{"criterion"}, WorktreePath: t.TempDir()}
	store := &persistedResumeState{project: project}
	runner := &fakeSeqRunner{cancel: true}
	req := resumeRequest(t, project, plan)
	controller := orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(store), orchestrator.WithPromptBuilder(fakePrompt{}))
	if _, err := controller.Execute(context.Background(), req); !errors.Is(err, context.Canceled) {
		t.Fatalf("initial Execute() error = %v, want cancellation", err)
	}
	runner.cancel = false
	if _, err := controller.Resume(context.Background(), orchestrator.ResumeRequest{ProjectSlug: project.Slug, RunID: req.RunID}); err != nil {
		t.Fatalf("Resume() using cached request error = %v", err)
	}
	if len(runner.phases) < 2 {
		t.Fatalf("resume dispatched no phases: %v", runner.phases)
	}
}

func TestResumeContinuesStoppedSubphaseWithoutReplay(t *testing.T) {
	plan := resolvedPipeline(t, config.PhaseQA)
	project := state.ProjectState{Slug: "demo", OriginalGoal: "goal", AcceptanceCriteria: []string{"criterion"}, WorktreePath: t.TempDir(), Status: state.StatusStopped, CurrentPhase: string(pipeline.PhaseDevelopment), CurrentSubphase: "implement", PhaseHistory: []state.PhaseRecord{
		{Phase: string(pipeline.PhaseAcceptanceCriteria), Status: state.StatusFinished},
		{Phase: string(pipeline.PhaseDevelopment), Subphase: "design", Status: state.StatusFinished},
		{Phase: string(pipeline.PhaseDevelopment), Subphase: "implement", Status: state.StatusStopped},
	}}
	store := &persistedResumeState{project: project}
	runner := &finiteRunner{}
	req := resumeRequest(t, project, plan)
	req.Subphases = pipeline.DevelopmentSubphaseGeneration{Mode: pipeline.DevelopmentSubphasesOverride, Subphases: []pipeline.DevelopmentSubphaseDefinition{{ID: "design", DisplayName: "Design"}, {ID: "implement", DisplayName: "Implement"}, {ID: "review", DisplayName: "Review"}}}
	_, err := orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(store), orchestrator.WithPromptBuilder(fakePrompt{})).Resume(context.Background(), orchestrator.ResumeRequest{ProjectSlug: project.Slug, RunID: req.RunID, Execution: req})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"development/implement", "development/review", "rebase/", "qa/", "test_document/"}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("resume dispatches=%v, want=%v", runner.calls, want)
	}
}

func TestResumeGrantsFreshVerificationBudgetAfterExhaustion(t *testing.T) {
	plan := resolvedPipeline(t, config.PhaseQA)
	project := state.ProjectState{
		Slug: "verification-resume", OriginalGoal: "goal", AcceptanceCriteria: []string{"criterion"}, WorktreePath: t.TempDir(),
		Status: state.StatusStopped, CurrentPhase: string(pipeline.PhaseDevelopment), CurrentSubphase: "implementation",
		PhaseHistory: []state.PhaseRecord{{Phase: string(pipeline.PhaseAcceptanceCriteria), Status: state.StatusFinished}, {Phase: string(pipeline.PhaseDevelopment), Subphase: "implementation", Status: state.StatusStopped}},
		Verification: &state.VerificationState{
			PlannedSteps:           []state.VerificationStep{{Name: "tests", Command: "go", Adapter: state.VerificationAdapterGoTest}},
			ParentBaselineCaptured: true,
			RemediationAttempts:    state.MaxVerificationRemediationAttempts, BoundaryCursor: "final",
		},
	}
	store := &persistedResumeState{project: project}
	runner := &finiteRunner{}
	request := resumeRequest(t, project, plan)
	request.Subphases = pipeline.DevelopmentSubphaseGeneration{Mode: pipeline.DevelopmentSubphasesOverride, Subphases: []pipeline.DevelopmentSubphaseDefinition{{ID: "implementation", DisplayName: "Implement"}}}
	if _, err := orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(store), orchestrator.WithPromptBuilder(fakePrompt{}), orchestrator.WithVerificationService(&boundaryVerification{})).Resume(context.Background(), orchestrator.ResumeRequest{ProjectSlug: project.Slug, RunID: request.RunID, Execution: request}); err != nil {
		t.Fatal(err)
	}
	got := store.snapshot()
	// The budget must be reset before the boundary re-runs, and the persisted
	// cursor must survive the reset. NextAction is not asserted here because
	// the boundary itself legitimately rewrites it once it passes.
	if !reflect.DeepEqual(store.resetActions, []string{"resume at the persisted verification boundary with three fresh remediation attempts"}) {
		t.Fatalf("reset actions=%v, want exactly one fresh-budget reset", store.resetActions)
	}
	if got.Verification.RemediationAttempts != 0 || got.Verification.BoundaryCursor != "final" {
		t.Fatalf("verification resume state=%#v, want a fresh budget retaining the boundary", got.Verification)
	}
}

func TestResumeAfterRestartLoadsPersistedStateAndRejectsTerminal(t *testing.T) {
	plan := resolvedPipeline(t, config.PhaseQA)
	project := state.ProjectState{Slug: "demo", OriginalGoal: "goal", AcceptanceCriteria: []string{"criterion"}, WorktreePath: t.TempDir(), Status: state.StatusFinished, CurrentPhase: string(pipeline.PhaseQA)}
	store := &persistedResumeState{project: project}
	req := resumeRequest(t, project, plan)
	controller := orchestrator.NewController(orchestrator.WithRunner(&finiteRunner{}), orchestrator.WithPhaseState(store), orchestrator.WithPromptBuilder(fakePrompt{}))
	if _, err := controller.Resume(context.Background(), orchestrator.ResumeRequest{ProjectSlug: project.Slug, RunID: req.RunID, Execution: req}); err == nil {
		t.Fatal("terminal project resume unexpectedly succeeded")
	}
}

func TestStopFromSeparateControllerProcessReachesActiveRunAndPersistsStopped(t *testing.T) {
	root := t.TempDir()
	store1, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	clockProject := state.ProjectState{
		Name: "Demo", Slug: "demo", OriginalGoal: "goal", AcceptanceCriteria: []string{"criterion"},
		PipelineConfig: state.PipelineConfigSnapshot{SchemaVersion: 1, Data: json.RawMessage(`{}`)},
		CurrentPhase:   "pipeline", WorktreePath: t.TempDir(), BranchName: "agent/demo",
	}
	lifecycle1 := state.NewLifecycleService(store1, nil, store1.Locker())
	if err := lifecycle1.Create(context.Background(), clockProject); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle1.Transition(context.Background(), clockProject.Slug, state.StatusRunning, "pipeline", "run", nil); err != nil {
		t.Fatal(err)
	}
	store2, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle2 := state.NewLifecycleService(store2, nil, store2.Locker())
	runner := &stopAwareRunner{started: make(chan struct{}), entered: make(chan struct{})}
	plan := resolvedPipeline(t, config.PhaseQA)
	req := resumeRequest(t, clockProject, plan)
	req.RunID = "run-1"
	controller1 := orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(lifecycle1), orchestrator.WithPromptBuilder(fakePrompt{}))
	done := make(chan error, 1)
	go func() { _, runErr := controller1.Execute(context.Background(), req); done <- runErr }()
	select {
	case <-runner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("active run did not start")
	}
	controller2 := orchestrator.NewController(orchestrator.WithPhaseState(lifecycle2))
	if err := controller2.Stop(context.Background(), orchestrator.StopRequest{ProjectSlug: "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v, want cancellation", err)
	}
	fresh, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := fresh.Load(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != state.StatusStopped {
		t.Fatalf("persisted status = %s, want stopped", persisted.Status)
	}
	if persisted.StopRequested || persisted.ActiveRunID != "" {
		t.Fatalf("stop markers not cleaned: %#v", persisted)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("dispatches = %v, want one", runner.calls)
	}
}

type dispatchClaimState struct{ service *state.LifecycleService }

func (s dispatchClaimState) RecordPhase(ctx context.Context, slug, phase, subphase string, status state.LifecycleStatus, outcome *state.ExecutionOutcome, artifacts []string) (state.ProjectState, error) {
	return s.service.RecordPhase(ctx, slug, phase, subphase, status, outcome, artifacts)
}
func (s dispatchClaimState) Transition(ctx context.Context, slug string, status state.LifecycleStatus, phase, subphase string, artifacts []string) (state.ProjectState, error) {
	return s.service.Transition(ctx, slug, status, phase, subphase, artifacts)
}
func (s dispatchClaimState) ClaimDispatch(ctx context.Context, slug, runID string) error {
	return s.service.ClaimDispatch(ctx, slug, runID)
}

func TestDurableStopClaimPreventsPostStopDispatch(t *testing.T) {
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	project := state.ProjectState{
		Name: "Demo", Slug: "demo", OriginalGoal: "goal", AcceptanceCriteria: []string{"criterion"},
		PipelineConfig: state.PipelineConfigSnapshot{SchemaVersion: 1, Data: json.RawMessage("{}")},
		CurrentPhase:   "pipeline", WorktreePath: t.TempDir(), BranchName: "agent/demo",
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	if err := service.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Transition(context.Background(), project.Slug, state.StatusRunning, "pipeline", "run", nil); err != nil {
		t.Fatal(err)
	}
	if err := service.BeginRun(context.Background(), project.Slug, "run-1", ""); err != nil {
		t.Fatal(err)
	}
	if err := service.RequestStop(context.Background(), project.Slug, ""); err != nil {
		t.Fatal(err)
	}

	runner := &finiteRunner{}
	req := resumeRequest(t, project, resolvedPipeline(t, config.PhaseQA))
	req.RunID = "run-1"
	controller := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(dispatchClaimState{service: service}),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	_, err = controller.Execute(context.Background(), req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v, want cancellation", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("dispatches = %v, want none after durable stop", runner.calls)
	}
	persisted, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != state.StatusStopped {
		t.Fatalf("persisted status = %s, want stopped", persisted.Status)
	}
}

type launchBarrierState struct {
	service *state.LifecycleService
	claimed chan struct{}
	release chan struct{}
}

func (s *launchBarrierState) RecordPhase(ctx context.Context, slug, phase, subphase string, status state.LifecycleStatus, outcome *state.ExecutionOutcome, artifacts []string) (state.ProjectState, error) {
	return s.service.RecordPhase(ctx, slug, phase, subphase, status, outcome, artifacts)
}
func (s *launchBarrierState) Transition(ctx context.Context, slug string, status state.LifecycleStatus, phase, subphase string, artifacts []string) (state.ProjectState, error) {
	return s.service.Transition(ctx, slug, status, phase, subphase, artifacts)
}
func (s *launchBarrierState) BeginRun(ctx context.Context, slug, runID, reservationToken string) error {
	return s.service.BeginRun(ctx, slug, runID, reservationToken)
}
func (s *launchBarrierState) RequestStop(ctx context.Context, slug, runID string) error {
	return s.service.RequestStop(ctx, slug, runID)
}
func (s *launchBarrierState) StopRequested(ctx context.Context, slug, runID string) (bool, error) {
	return s.service.StopRequested(ctx, slug, runID)
}
func (s *launchBarrierState) ClaimDispatch(ctx context.Context, slug, runID string) error {
	if err := s.service.ClaimDispatch(ctx, slug, runID); err != nil {
		return err
	}
	close(s.claimed)
	<-s.release
	return nil
}
func (s *launchBarrierState) Load(ctx context.Context, slug string) (state.ProjectState, error) {
	return s.service.Load(ctx, slug)
}

func TestDurableStopAfterClaimIsOrderedAfterLaunch(t *testing.T) {
	root := t.TempDir()
	store1, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service1 := state.NewLifecycleService(store1, nil, store1.Locker())
	project := state.ProjectState{
		Name: "Demo", Slug: "demo", OriginalGoal: "goal", AcceptanceCriteria: []string{"criterion"},
		PipelineConfig: state.PipelineConfigSnapshot{SchemaVersion: 1, Data: json.RawMessage("{}")},
		CurrentPhase:   "pipeline", WorktreePath: t.TempDir(), BranchName: "agent/demo",
	}
	if err := service1.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, err := service1.Transition(context.Background(), project.Slug, state.StatusRunning, "pipeline", "run", nil); err != nil {
		t.Fatal(err)
	}
	store2, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service2 := state.NewLifecycleService(store2, nil, store2.Locker())
	barrier := &launchBarrierState{service: service1, claimed: make(chan struct{}), release: make(chan struct{})}
	runner := &stopAwareRunner{started: make(chan struct{}), entered: make(chan struct{})}
	req := resumeRequest(t, project, resolvedPipeline(t, config.PhaseQA))
	req.RunID = "run-1"
	controller := orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(barrier), orchestrator.WithPromptBuilder(fakePrompt{}))
	done := make(chan error, 1)
	go func() { _, runErr := controller.Execute(context.Background(), req); done <- runErr }()
	<-barrier.claimed
	stopDone := make(chan error, 1)
	go func() { stopDone <- service2.RequestStop(context.Background(), project.Slug, "") }()
	if err := <-stopDone; err != nil {
		t.Fatal(err)
	}
	close(barrier.release)
	<-runner.entered
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %v, want launch after claimed stop", runner.calls)
	}
	close(runner.started)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v, want cancellation", err)
	}
}

type cancelAfterFirstRunner struct {
	cancel context.CancelFunc
	calls  []string
}

func (r *cancelAfterFirstRunner) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	r.calls = append(r.calls, string(req.Phase)+"/"+req.Subphase)
	if len(r.calls) == 1 {
		r.cancel()
	}
	return agent.RunResult{Phase: req.Phase, Subphase: req.Subphase, Status: state.StatusFinished}, nil
}

type failFirstStartedEventSink struct {
	types []orchestrator.EventType
	cause error
}

func (s *failFirstStartedEventSink) Publish(_ context.Context, event orchestrator.Event) error {
	s.types = append(s.types, event.Type)
	if event.Type == orchestrator.EventPhaseStarted && s.cause != nil {
		cause := s.cause
		s.cause = nil
		return cause
	}
	return nil
}

type failSuccessEventSink struct {
	types []orchestrator.EventType
	cause error
}

func (s *failSuccessEventSink) Publish(_ context.Context, event orchestrator.Event) error {
	s.types = append(s.types, event.Type)
	if event.Type == orchestrator.EventPhaseSucceeded {
		return s.cause
	}
	return nil
}

type cancelOnSuccessEventSink struct {
	cancel context.CancelFunc
	types  []orchestrator.EventType
}

func (s *cancelOnSuccessEventSink) Publish(_ context.Context, event orchestrator.Event) error {
	s.types = append(s.types, event.Type)
	if event.Type == orchestrator.EventPhaseSucceeded {
		s.cancel()
	}
	return nil
}

func durableExecutionProject(t *testing.T, service *state.LifecycleService) state.ProjectState {
	t.Helper()
	project := state.ProjectState{
		Name: "Demo", Slug: "demo", OriginalGoal: "goal", AcceptanceCriteria: []string{"criterion"},
		PipelineConfig: state.PipelineConfigSnapshot{SchemaVersion: 1, Data: json.RawMessage(`{}`)},
		CurrentPhase:   "pipeline", WorktreePath: t.TempDir(), BranchName: "agent/demo",
	}
	if err := service.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Transition(context.Background(), project.Slug, state.StatusRunning, "pipeline", "", nil); err != nil {
		t.Fatal(err)
	}
	project, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func TestRejectedDuplicateExecutePreservesActiveRunOwnership(t *testing.T) {
	tests := []struct {
		name    string
		second  func(orchestrator.Controller, *state.LifecycleService, orchestrator.Request) orchestrator.Controller
		wantErr string
	}{
		{
			name: "local registration rejection",
			second: func(first orchestrator.Controller, _ *state.LifecycleService, _ orchestrator.Request) orchestrator.Controller {
				return first
			},
			wantErr: "already has an active local run",
		},
		{
			name: "durable BeginRun rejection",
			second: func(_ orchestrator.Controller, service *state.LifecycleService, _ orchestrator.Request) orchestrator.Controller {
				return orchestrator.NewController(
					orchestrator.WithRunner(&finiteRunner{}),
					orchestrator.WithPhaseState(service),
					orchestrator.WithPromptBuilder(fakePrompt{}),
				)
			},
			wantErr: "already has active run",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := state.NewFileStore(root)
			if err != nil {
				t.Fatal(err)
			}
			service := state.NewLifecycleService(store, nil, store.Locker())
			project := durableExecutionProject(t, service)
			plan := resolvedPipeline(t)
			blocking := &ownershipBlockingRunner{entered: make(chan struct{})}
			first := orchestrator.NewController(
				orchestrator.WithRunner(blocking),
				orchestrator.WithPhaseState(service),
				orchestrator.WithPromptBuilder(fakePrompt{}),
			)
			firstReq := resumeRequest(t, project, plan)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				_, executeErr := first.Execute(ctx, firstReq)
				done <- executeErr
			}()
			<-blocking.entered

			secondReq := firstReq
			secondReq.RunID = "run-2"
			second := tt.second(first, service, secondReq)
			if _, err := second.Execute(context.Background(), secondReq); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				cancel()
				<-done
				t.Fatalf("duplicate Execute() error = %v, want %q", err, tt.wantErr)
			}
			afterRejection, loadErr := service.Load(context.Background(), project.Slug)
			cancel()
			if firstErr := <-done; !errors.Is(firstErr, context.Canceled) {
				t.Fatalf("first Execute() error = %v, want cancellation cleanup", firstErr)
			}
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if afterRejection.Status != state.StatusRunning ||
				afterRejection.ActiveRunID != "run-1" ||
				afterRejection.DispatchClaimRunID != "run-1" {
				t.Fatalf("duplicate run stole durable ownership: %#v", afterRejection)
			}
		})
	}
}

func TestStopBetweenPhasesPersistsCursorMarkersAndResumesAfterRestart(t *testing.T) {
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project := durableExecutionProject(t, service)
	plan := resolvedPipeline(t, config.PhaseQA)

	ctx, cancel := context.WithCancel(context.Background())
	runner := &finiteRunner{}
	events := &cancelOnSuccessEventSink{cancel: cancel}
	req := resumeRequest(t, project, plan)
	controller := orchestrator.NewController(
		orchestrator.WithRunner(runner), orchestrator.WithPhaseState(service),
		orchestrator.WithEventSink(events), orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	if _, err := controller.Execute(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v, want cancellation", err)
	}
	if !reflect.DeepEqual(runner.calls, []string{"acceptance_criteria/"}) {
		t.Fatalf("dispatches=%v, want only completed first phase", runner.calls)
	}
	persisted, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != state.StatusStopped || persisted.CurrentPhase != string(pipeline.PhaseDevelopment) || persisted.CurrentSubphase != "implementation" {
		t.Fatalf("stopped cursor=%#v", persisted)
	}
	if persisted.ActiveRunID != "" || persisted.StopRequested || persisted.StopRequestID != "" || persisted.DispatchClaimRunID != "" || persisted.RunReservationToken != "" {
		t.Fatalf("active markers not cleaned: %#v", persisted)
	}
	if len(events.types) < 3 || events.types[len(events.types)-1] != orchestrator.EventProjectStopped {
		t.Fatalf("events=%v, want project_stopped terminal event", events.types)
	}

	restartedStore, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	restartedService := state.NewLifecycleService(restartedStore, nil, restartedStore.Locker())
	resumeRunner := &finiteRunner{}
	resumeReq := resumeRequest(t, persisted, plan)
	resumeReq.RunID = "run-2"
	resumeController := orchestrator.NewController(
		orchestrator.WithRunner(resumeRunner), orchestrator.WithPhaseState(restartedService),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	if _, err := resumeController.Resume(context.Background(), orchestrator.ResumeRequest{ProjectSlug: project.Slug, RunID: "run-2", Execution: resumeReq}); err != nil {
		t.Fatalf("Resume() after restart error = %v", err)
	}
	if len(resumeRunner.calls) == 0 || resumeRunner.calls[0] != "development/implementation" {
		t.Fatalf("resume dispatches=%v, want stopped cursor first", resumeRunner.calls)
	}
}

func TestPhaseStartEventFailureClosesRunningStateAndIsResumable(t *testing.T) {
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project := durableExecutionProject(t, service)
	phaseErr := errors.New("phase-start event sink failed")
	events := &failFirstStartedEventSink{cause: phaseErr}
	runner := &finiteRunner{}
	plan := resolvedPipeline(t, config.PhaseQA)
	req := resumeRequest(t, project, plan)
	controller := orchestrator.NewController(
		orchestrator.WithRunner(runner), orchestrator.WithPhaseState(service),
		orchestrator.WithEventSink(events), orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	if _, err := controller.Execute(context.Background(), req); !errors.Is(err, phaseErr) {
		t.Fatalf("Execute() error = %v, want event sink cause", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner dispatched after phase-start event failure: %v", runner.calls)
	}
	persisted, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != state.StatusFailed || persisted.ActiveRunID != "" || persisted.StopRequested || persisted.DispatchClaimRunID != "" || persisted.RunReservationToken != "" {
		t.Fatalf("failed state/markers=%#v", persisted)
	}
	if len(persisted.PhaseHistory) == 0 || persisted.PhaseHistory[len(persisted.PhaseHistory)-1].Status != state.StatusFailed {
		t.Fatalf("phase history=%#v, want failed terminal record", persisted.PhaseHistory)
	}
	if !reflect.DeepEqual(events.types, []orchestrator.EventType{orchestrator.EventPhaseStarted, orchestrator.EventPhaseFailed}) {
		t.Fatalf("events=%v, want started attempt then phase_failed", events.types)
	}

	restartedStore, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	restartedService := state.NewLifecycleService(restartedStore, nil, restartedStore.Locker())
	resumeRunner := &finiteRunner{}
	resumeReq := resumeRequest(t, persisted, plan)
	resumeReq.RunID = "run-2"
	resumeController := orchestrator.NewController(
		orchestrator.WithRunner(resumeRunner), orchestrator.WithPhaseState(restartedService),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	if _, err := resumeController.Resume(context.Background(), orchestrator.ResumeRequest{ProjectSlug: project.Slug, RunID: "run-2", Execution: resumeReq}); err != nil {
		t.Fatalf("Resume() after event failure error = %v", err)
	}
	if len(resumeRunner.calls) == 0 || resumeRunner.calls[0] != "acceptance_criteria/" {
		t.Fatalf("resume dispatches=%v, want failed phase first (persisted cursor=%s/%s history=%#v)", resumeRunner.calls, persisted.CurrentPhase, persisted.CurrentSubphase, persisted.PhaseHistory)
	}
}

func TestPhaseSuccessEventFailureClosesFinishedRunWithoutDuplicateHistory(t *testing.T) {
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project := durableExecutionProject(t, service)
	cause := errors.New("phase-success event sink failed")
	events := &failSuccessEventSink{cause: cause}
	runner := &finiteRunner{}
	controller := orchestrator.NewController(
		orchestrator.WithRunner(runner), orchestrator.WithPhaseState(service),
		orchestrator.WithEventSink(events), orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	_, err = controller.Execute(context.Background(), resumeRequest(t, project, resolvedPipeline(t, config.PhaseQA)))
	if !errors.Is(err, cause) {
		t.Fatalf("Execute() error = %v, want wrapped event sink cause", err)
	}
	persisted, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != state.StatusFailed {
		t.Fatalf("status=%s, want failed", persisted.Status)
	}
	if persisted.ActiveRunID != "" || persisted.StopRequested || persisted.StopRequestID != "" || persisted.DispatchClaimRunID != "" || persisted.RunReservationToken != "" {
		t.Fatalf("active markers not cleaned: %#v", persisted)
	}
	if len(persisted.PhaseHistory) != 2 || persisted.PhaseHistory[len(persisted.PhaseHistory)-1].Status != state.StatusFinished {
		t.Fatalf("phase history=%#v, want one completed phase record without duplicate failure record", persisted.PhaseHistory)
	}
	if !reflect.DeepEqual(events.types, []orchestrator.EventType{orchestrator.EventPhaseStarted, orchestrator.EventPhaseSucceeded, orchestrator.EventPhaseFailed}) {
		t.Fatalf("events=%v, want success attempt followed by failure cleanup event", events.types)
	}
}

func TestQAFixCursorAndBudgetSurviveStopAndControllerRestart(t *testing.T) {
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project := durableExecutionProject(t, service)
	plan := resolvedPipeline(t, config.PhaseQA)
	firstRunner := &feedbackRunner{
		statuses: []state.LifecycleStatus{
			state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished,
			state.StatusFailed, state.StatusStopped,
		},
		artifacts: []string{"qa-report.md"},
	}
	req := resumeRequest(t, project, plan)
	req.MaxIterations = 2
	controller := orchestrator.NewController(
		orchestrator.WithRunner(firstRunner),
		orchestrator.WithPhaseState(service),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	if _, err := controller.Execute(context.Background(), req); !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v, want stopped fix Development", err)
	}
	stopped, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status != state.StatusStopped || stopped.QACompletedAttempts != 1 || stopped.QALoopStage != "fix" {
		t.Fatalf("stopped QA cursor = %#v", stopped)
	}
	if !reflect.DeepEqual(stopped.QAFeedbackArtifactPaths, []string{"qa-report.md"}) {
		t.Fatalf("stopped feedback = %v", stopped.QAFeedbackArtifactPaths)
	}

	resumeRunner := &finiteRunner{}
	resumeController := orchestrator.NewController(
		orchestrator.WithRunner(resumeRunner),
		orchestrator.WithPhaseState(service),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	resumeReq := resumeRequest(t, stopped, plan)
	resumeReq.RunID = "run-2"
	resumeReq.MaxIterations = 2
	if _, err := resumeController.Resume(context.Background(), orchestrator.ResumeRequest{
		ProjectSlug: project.Slug,
		RunID:       "run-2",
		Execution:   resumeReq,
	}); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	want := []string{"development/implementation", "rebase/", "qa/", "test_document/"}
	if !reflect.DeepEqual(resumeRunner.calls, want) {
		t.Fatalf("resume dispatches = %v, want exact fix cursor %v", resumeRunner.calls, want)
	}
	for i, artifacts := range resumeRunner.artifacts {
		if !reflect.DeepEqual(artifacts, []string{"qa-report.md"}) {
			t.Fatalf("resume dispatch %d artifacts = %v, want preserved QA feedback", i, artifacts)
		}
	}
	finished, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != state.StatusFinished || finished.MaxQAAttempts != 2 || finished.QACompletedAttempts != 0 || finished.QALoopStage != "" {
		t.Fatalf("finished resumed state = %#v", finished)
	}
}

func TestExhaustedQABudgetRemainsExhaustedAfterRestart(t *testing.T) {
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
			state.StatusFinished, state.StatusFinished, state.StatusFinished,
			state.StatusFinished, state.StatusFailed, state.StatusFinished,
			state.StatusFinished, state.StatusFailed,
		},
		artifacts: []string{"qa-report.md"},
	}
	req := resumeRequest(t, project, plan)
	req.MaxIterations = 2
	controller := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(service),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	if _, err := controller.Execute(context.Background(), req); err == nil {
		t.Fatal("Execute() unexpectedly passed exhausted QA")
	}
	exhausted, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if exhausted.Status != state.StatusFailed || exhausted.QACompletedAttempts != 2 || exhausted.QALoopStage != "exhausted" {
		t.Fatalf("exhausted state = %#v", exhausted)
	}

	resumeRunner := &finiteRunner{}
	resumeController := orchestrator.NewController(
		orchestrator.WithRunner(resumeRunner),
		orchestrator.WithPhaseState(service),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	resumeReq := resumeRequest(t, exhausted, plan)
	resumeReq.RunID = "run-2"
	resumeReq.MaxIterations = 2
	_, err = resumeController.Resume(context.Background(), orchestrator.ResumeRequest{ProjectSlug: project.Slug, RunID: "run-2", Execution: resumeReq})
	if err == nil || !strings.Contains(err.Error(), "exhausted after 2 attempt") {
		t.Fatalf("Resume() error = %v, want persisted exhaustion", err)
	}
	if len(resumeRunner.calls) != 0 {
		t.Fatalf("resume dispatched after exhaustion: %v", resumeRunner.calls)
	}
}

func TestResumeRejectsStoredQACursorWhenQAIsDisabled(t *testing.T) {
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project := durableExecutionProject(t, service)
	if err := service.ConfigureOrchestration(context.Background(), project.Slug, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateQALoop(context.Background(), project.Slug, 1, "qa", []string{"qa-report.md"}); err != nil {
		t.Fatal(err)
	}
	if err := service.CloseRun(context.Background(), project.Slug, state.StatusStopped); err != nil {
		t.Fatal(err)
	}
	stopped, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	runner := &finiteRunner{}
	controller := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(service),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	req := resumeRequest(t, stopped, resolvedPipeline(t))
	req.RunID = "run-2"
	_, err = controller.Resume(context.Background(), orchestrator.ResumeRequest{
		ProjectSlug: project.Slug,
		RunID:       "run-2",
		Execution:   req,
	})
	if err == nil || !strings.Contains(err.Error(), "QA") {
		t.Fatalf("Resume() error = %v, want missing enabled QA rejection", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("resume dispatched despite missing QA phase: %v", runner.calls)
	}
}

type mutableConflictReader struct{ unresolved bool }

func (r *mutableConflictReader) HasUnresolvedConflicts(context.Context, string) (bool, error) {
	return r.unresolved, nil
}

func TestPendingRebaseConflictRefusesUnresolvedResumeThenRunsQAAfterResolution(t *testing.T) {
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project := durableExecutionProject(t, service)
	plan := resolvedPipeline(t, config.PhaseQA)
	reader := &mutableConflictReader{unresolved: true}
	firstRunner := &feedbackRunner{
		statuses: []state.LifecycleStatus{
			state.StatusFinished, state.StatusFinished, state.StatusFinished,
			state.StatusFailed,
		},
		artifacts: []string{"rebase-report.md"},
	}
	req := resumeRequest(t, project, plan)
	req.MaxIterations = 3
	controller := orchestrator.NewProductionController(
		firstRunner,
		service,
		reader,
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	if _, err := controller.Execute(context.Background(), req); err == nil {
		t.Fatal("Rebase conflict unexpectedly completed")
	}
	conflicted, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if conflicted.Status != state.StatusFailed || !conflicted.PendingRebaseConflict {
		t.Fatalf("persisted conflict = %#v", conflicted)
	}
	if !reflect.DeepEqual(conflicted.RebaseConflictArtifactPaths, []string{"rebase-report.md"}) {
		t.Fatalf("conflict artifacts = %v", conflicted.RebaseConflictArtifactPaths)
	}

	blockedRunner := &finiteRunner{}
	blockedController := orchestrator.NewProductionController(
		blockedRunner,
		service,
		reader,
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	resumeReq := resumeRequest(t, conflicted, plan)
	resumeReq.RunID = "run-2"
	if _, err := blockedController.Resume(context.Background(), orchestrator.ResumeRequest{
		ProjectSlug: project.Slug, RunID: "run-2", Execution: resumeReq,
	}); err == nil || !strings.Contains(err.Error(), "remains unresolved") {
		t.Fatalf("unresolved Resume() error = %v", err)
	}
	if len(blockedRunner.calls) != 0 {
		t.Fatalf("unresolved resume dispatched: %v", blockedRunner.calls)
	}

	reader.unresolved = false
	resolvedRunner := &finiteRunner{}
	resolvedController := orchestrator.NewProductionController(
		resolvedRunner,
		service,
		reader,
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	resumeReq.RunID = "run-3"
	if _, err := resolvedController.Resume(context.Background(), orchestrator.ResumeRequest{
		ProjectSlug: project.Slug, RunID: "run-3", Execution: resumeReq,
	}); err != nil {
		t.Fatalf("resolved Resume() error = %v", err)
	}
	want := []string{"qa/", "test_document/"}
	if !reflect.DeepEqual(resolvedRunner.calls, want) {
		t.Fatalf("resolved dispatches = %v, want QA then post-Rebase %v", resolvedRunner.calls, want)
	}
	finished, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != state.StatusFinished || finished.PendingRebaseConflict {
		t.Fatalf("resolved final state = %#v", finished)
	}
}

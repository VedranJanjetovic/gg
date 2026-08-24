package orchestrator_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/ci"
	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/pr"
	"github.com/VedranJanjetovic/gg/internal/state"
)

type fakeSeqRunner struct {
	phases    []pipeline.PhaseID
	subphases []string
	settings  []config.AgentSettings
	err       error
	failAt    int
	cancel    bool
}

func (r *fakeSeqRunner) Run(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
	r.phases = append(r.phases, req.Phase)
	r.subphases = append(r.subphases, req.Subphase)
	r.settings = append(r.settings, req.Settings)
	if r.cancel {
		return agent.RunResult{Phase: req.Phase, Status: state.StatusStopped}, context.Canceled
	}
	if r.err != nil && (r.failAt == 0 || len(r.phases) == r.failAt) {
		return agent.RunResult{Phase: req.Phase, Status: state.StatusFailed}, r.err
	}
	return agent.RunResult{Phase: req.Phase, Status: state.StatusFinished}, nil
}

type phaseCall struct {
	phase    string
	subphase string
	status   state.LifecycleStatus
}
type fakeState struct {
	calls []phaseCall
	err   error
}

func (s *fakeState) RecordPhase(_ context.Context, _ string, phase, subphase string, status state.LifecycleStatus, _ *state.ExecutionOutcome, _ []string) (state.ProjectState, error) {
	s.calls = append(s.calls, phaseCall{phase: phase, subphase: subphase, status: status})
	return state.ProjectState{}, s.err
}

type finalizationSpyState struct {
	fakeState
	transitions []state.LifecycleStatus
}

func (s *finalizationSpyState) Transition(_ context.Context, _ string, target state.LifecycleStatus, _ string, _ string, _ []string) (state.ProjectState, error) {
	s.transitions = append(s.transitions, target)
	return state.ProjectState{}, nil
}

type openPendingMonitor struct{}

func (openPendingMonitor) Monitor(_ context.Context, req orchestrator.PRCIRequest) (orchestrator.PRCIResult, error) {
	return orchestrator.PRCIResult{Polls: req.MaxPolls, State: pr.MergeStateOpen, Clean: true, Cursor: "open-cursor"}, nil
}

type fakeEvents struct {
	types     []orchestrator.EventType
	phases    []pipeline.PhaseID
	subphases []string
}

func (e *fakeEvents) Publish(_ context.Context, event orchestrator.Event) error {
	e.types = append(e.types, event.Type)
	e.phases = append(e.phases, event.Phase)
	e.subphases = append(e.subphases, event.Subphase)
	return nil
}

type cancelOnFinalSuccessEvents struct {
	cancel context.CancelFunc
	types  []orchestrator.EventType
}

func (e *cancelOnFinalSuccessEvents) Publish(_ context.Context, event orchestrator.Event) error {
	e.types = append(e.types, event.Type)
	if event.Type == orchestrator.EventPhaseSucceeded && event.Phase == pipeline.PhasePR {
		e.cancel()
	}
	return nil
}

type fakePrompt struct{}

func (fakePrompt) BuildPrompt(agent.PromptInput) (string, error) { return "prompt", nil }

func resolvedPipeline(t *testing.T, enabled ...config.Phase) pipeline.ExecutablePipeline {
	t.Helper()
	set := map[config.Phase]bool{}
	for _, p := range enabled {
		set[p] = true
	}
	phases := map[config.Phase]config.ResolvedPhase{}
	for _, p := range []config.Phase{config.PhaseGrooming, config.PhasePlanning, config.PhaseQA, config.PhaseBuildChecker, config.PhasePR, config.PhaseCI} {
		phases[p] = config.ResolvedPhase{Enabled: set[p], AgentSettings: config.AgentSettings{Agent: config.AgentClaude}}
	}
	out, err := pipeline.Resolve(pipeline.DefaultPipeline(), config.ResolvedConfig{Phases: phases})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func request(t *testing.T, p pipeline.ExecutablePipeline) orchestrator.Request {
	return orchestrator.Request{Project: state.ProjectState{Slug: "demo", OriginalGoal: "goal", AcceptanceCriteria: []string{"criterion"}, WorktreePath: "/tmp"}, Pipeline: p, PhaseContracts: map[pipeline.PhaseID]string{pipeline.PhaseAcceptanceCriteria: "contract", pipeline.PhaseQA: "contract", pipeline.PhasePR: "contract"}}
}

func TestExecuteOpenCleanPRRemainsResumableAndDoesNotFinalize(t *testing.T) {
	stateSpy := &finalizationSpyState{}
	events := &fakeEvents{}
	req := request(t, resolvedPipeline(t))
	req.PullRequestURL = "https://github.com/o/r/pull/1"
	req.RunID = "open-pr-run"
	_, err := orchestrator.NewController(
		orchestrator.WithRunner(&fakeSeqRunner{}),
		orchestrator.WithPhaseState(stateSpy),
		orchestrator.WithEventSink(events),
		orchestrator.WithPRCILifecycleMonitor(openPendingMonitor{}),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	).Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range stateSpy.transitions {
		if target == state.StatusFinished {
			t.Fatalf("open PR caused terminal transition: %v", stateSpy.transitions)
		}
	}
	for _, event := range events.types {
		if event == orchestrator.EventProjectFinished {
			t.Fatal("open PR published project_finished")
		}
	}
}

func TestExecuteBuildsValidAgentRequestsForMandatoryPhasesAndPreservesOverrides(t *testing.T) {
	resolved, err := config.Resolve(config.GlobalConfig{
		Version:  config.CurrentSchemaVersion,
		Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "global-model", Effort: config.EffortMedium},
	}, &config.ProjectConfig{
		Version:  config.CurrentSchemaVersion,
		Defaults: config.AgentSettingsOverride{Agent: config.AgentCodex, Model: "project-model"},
	}, config.RunOverrides{Defaults: config.AgentSettingsOverride{Effort: config.EffortHigh}})
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range config.RemovablePhases() {
		entry := resolved.Phases[phase]
		entry.Enabled = false
		resolved.Phases[phase] = entry
	}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeSeqRunner{}
	request := request(t, plan)
	request.PhaseContracts = map[pipeline.PhaseID]string{
		pipeline.PhaseAcceptanceCriteria: "acceptance contract",
		pipeline.PhaseDevelopment:        "development contract",
		pipeline.PhaseRebase:             "rebase contract",
		pipeline.PhaseTestDocument:       "test contract",
	}
	if _, err := orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(&fakeState{}), orchestrator.WithPromptBuilder(fakePrompt{})).Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	want := config.AgentSettings{Agent: config.AgentCodex, Model: "project-model", Effort: config.EffortHigh}
	if len(runner.settings) != 6 {
		t.Fatalf("agent requests = %d, want 6 requests across mandatory phases/subphases", len(runner.settings))
	}
	for i, settings := range runner.settings {
		if settings != want {
			t.Errorf("request %d settings = %#v, want %#v", i, settings, want)
		}
		if settings.Agent == "" || settings.Model == "" || settings.Effort == "" {
			t.Errorf("request %d has incomplete settings: %#v", i, settings)
		}
	}
}

func TestProductionAgentRunnerDiscoversAndPropagatesCanonicalArtifacts(t *testing.T) {
	stateRoot := t.TempDir()
	worktree := t.TempDir()
	script := filepath.Join(t.TempDir(), "fake-agent")
	body := `#!/bin/sh
printf '%s\n' "$*"
artifact=
case "$*" in
  *test_document*) artifact=test-document.md ;;
  *rebase*) artifact=rebase-report.md ;;
  *development*) artifact=development.md ;;
  *acceptance_criteria*) artifact=acceptance-criteria.md ;;
esac
run_id=$(printf '%s\n' "$*" | sed -n 's/^gg_run_id: "\(.*\)"$/\1/p' | head -n 1)
printf '%s\n' '---' "gg_run_id: \"$run_id\"" 'gg_disposition: passed' '---' 'phase evidence' > ".gg/$artifact"
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := state.NewFileStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := state.NewLifecycleService(store, nil, store.Locker())
	project := state.ProjectState{
		Name: "Artifact Flow", Slug: "artifact-flow", OriginalGoal: "propagate artifacts",
		AcceptanceCriteria: []string{"downstream prompts receive canonical outputs"},
		PipelineConfig:     state.PipelineConfigSnapshot{SchemaVersion: 1, Data: json.RawMessage(`{}`)},
		CurrentPhase:       "pipeline", Status: state.StatusRunning, WorktreePath: worktree, BranchName: "agent/artifact-flow",
	}
	if err := lifecycle.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	runner := agent.NewAgentRunner(agent.AgentRunnerOptions{
		Factory: agent.NewExecProcessFactory(nil, nil),
		Lookup:  func(string) (string, error) { return script, nil },
		LogRoot: stateRoot,
	})
	resolved, err := config.Resolve(config.GlobalConfig{
		Version:  config.CurrentSchemaVersion,
		Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "fake", Effort: config.EffortLow},
	}, nil, config.RunOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range config.RemovablePhases() {
		entry := resolved.Phases[phase]
		entry.Enabled = false
		resolved.Phases[phase] = entry
	}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	req := request(t, plan)
	req.Project, err = lifecycle.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	req.Project.Status = state.StatusRunning
	req.PhaseContracts = plan.PhaseContracts()
	req.RunID = "artifact-run"
	controller := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(lifecycle),
	)
	if _, err := controller.Execute(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	reloaded, err := lifecycle.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	wantArtifacts := []string{".gg/acceptance-criteria.md", ".gg/development.md", ".gg/rebase-report.md", ".gg/test-document.md"}
	for _, want := range wantArtifacts {
		if !containsString(reloaded.ArtifactPaths, want) {
			t.Fatalf("persisted artifacts = %v, missing %q", reloaded.ArtifactPaths, want)
		}
	}
	rebaseLog, err := os.ReadFile(filepath.Join(stateRoot, ".gg", "projects", project.Slug, "logs", "rebase.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rebaseLog), "acceptance-criteria.md") || !strings.Contains(string(rebaseLog), "development.md") {
		t.Fatalf("downstream Rebase prompt lost upstream artifacts:\n%s", rebaseLog)
	}
	// The worktree .gg directory is the ignored artifact workspace; runner
	// logs (projects/...) must still live only under the durable state root.
	if _, err := os.Stat(filepath.Join(worktree, ".gg", "projects")); !os.IsNotExist(err) {
		t.Fatalf("runner logs were written inside worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktree, ".gg", ".gitignore")); err != nil {
		t.Fatalf("artifact workspace gitignore missing: %v", err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestExecuteRunsEnabledPhasesInCanonicalOrderAndPersistsEvents(t *testing.T) {
	p := resolvedPipeline(t, config.PhaseQA, config.PhasePR)
	runner := &fakeSeqRunner{}
	store := &fakeState{}
	events := &fakeEvents{}
	_, err := orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(store), orchestrator.WithEventSink(events), orchestrator.WithPromptBuilder(fakePrompt{})).Execute(context.Background(), request(t, p))
	if err != nil {
		t.Fatal(err)
	}
	want := []pipeline.PhaseID{pipeline.PhaseAcceptanceCriteria, pipeline.PhaseDevelopment, pipeline.PhaseDevelopment, pipeline.PhaseDevelopment, pipeline.PhaseRebase, pipeline.PhaseQA, pipeline.PhaseTestDocument, pipeline.PhasePR}
	if !reflect.DeepEqual(runner.phases, want) {
		t.Fatalf("runner order=%v want=%v", runner.phases, want)
	}
	var statuses []state.LifecycleStatus
	for _, c := range store.calls {
		statuses = append(statuses, c.status)
	}
	wantStatuses := []state.LifecycleStatus{state.StatusRunning, state.StatusFinished, state.StatusRunning, state.StatusFinished, state.StatusRunning, state.StatusFinished, state.StatusRunning, state.StatusFinished, state.StatusRunning, state.StatusFinished, state.StatusRunning, state.StatusFinished, state.StatusRunning, state.StatusFinished, state.StatusRunning, state.StatusFinished}
	if !reflect.DeepEqual(statuses, wantStatuses) {
		t.Fatalf("statuses=%v", statuses)
	}
	wantEvents := []orchestrator.EventType{orchestrator.EventPhaseStarted, orchestrator.EventPhaseSucceeded, orchestrator.EventPhaseStarted, orchestrator.EventPhaseSucceeded, orchestrator.EventPhaseStarted, orchestrator.EventPhaseSucceeded, orchestrator.EventPhaseStarted, orchestrator.EventPhaseSucceeded, orchestrator.EventPhaseStarted, orchestrator.EventPhaseSucceeded, orchestrator.EventPhaseStarted, orchestrator.EventPhaseSucceeded, orchestrator.EventPhaseStarted, orchestrator.EventPhaseSucceeded, orchestrator.EventPhaseStarted, orchestrator.EventPhaseSucceeded, orchestrator.EventProjectFinished}
	if !reflect.DeepEqual(events.types, wantEvents) {
		t.Fatalf("events=%v", events.types)
	}
}
func TestExecuteFinishesProjectAndPublishesProjectFinished(t *testing.T) {
	store := &persistedResumeState{project: state.ProjectState{Slug: "demo", Status: state.StatusPending}}
	events := &fakeEvents{}
	request := request(t, resolvedPipeline(t, config.PhaseQA))
	request.Project = store.project
	controller := orchestrator.NewController(
		orchestrator.WithRunner(&fakeSeqRunner{}),
		orchestrator.WithPhaseState(store),
		orchestrator.WithEventSink(events),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	if _, err := controller.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if got := store.snapshot().Status; got != state.StatusFinished {
		t.Fatalf("project status = %s, want %s", got, state.StatusFinished)
	}
	if len(events.types) == 0 || events.types[len(events.types)-1] != orchestrator.EventProjectFinished {
		t.Fatalf("events=%v, want project_finished as final event", events.types)
	}
}

func TestExecuteFinalCancellationStillPersistsFinishedProject(t *testing.T) {
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := state.NewLifecycleService(store, nil, store.Locker())
	project := state.ProjectState{Name: "Final Cancel", Slug: "final-cancel", Status: state.StatusRunning, OriginalGoal: "goal", AcceptanceCriteria: []string{"criterion"}, PipelineConfig: state.PipelineConfigSnapshot{SchemaVersion: 1, Data: json.RawMessage(`{}`)}, WorktreePath: t.TempDir(), BranchName: "final-cancel-branch", CurrentPhase: "pipeline", CurrentSubphase: "run"}
	if err := lifecycle.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := &cancelOnFinalSuccessEvents{cancel: cancel}
	plan := resolvedPipeline(t, config.PhaseQA)
	request := request(t, plan)
	request.Project = project
	request.RunID = "run-final-cancel"
	controller := orchestrator.NewController(
		orchestrator.WithRunner(&fakeSeqRunner{}),
		orchestrator.WithPhaseState(lifecycle),
		orchestrator.WithEventSink(events),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	if _, err := controller.Execute(ctx, request); err != nil {
		t.Fatalf("Execute() error = %v, completion should win after final phase success", err)
	}
	got, err := lifecycle.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != state.StatusFinished {
		t.Fatalf("persisted status = %s, want finished", got.Status)
	}
	if len(events.types) == 0 || events.types[len(events.types)-1] != orchestrator.EventProjectFinished {
		t.Fatalf("events = %v, want project_finished final event", events.types)
	}
}

func TestExecuteSkipsDisabledPhases(t *testing.T) {
	runner := &fakeSeqRunner{}
	_, err := orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(&fakeState{}), orchestrator.WithPromptBuilder(fakePrompt{})).Execute(context.Background(), request(t, resolvedPipeline(t)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runner.phases, []pipeline.PhaseID{pipeline.PhaseAcceptanceCriteria, pipeline.PhaseDevelopment, pipeline.PhaseDevelopment, pipeline.PhaseDevelopment, pipeline.PhaseRebase, pipeline.PhaseTestDocument}) {
		t.Fatalf("phases=%v", runner.phases)
	}
}
func TestExecuteStopsAfterFailureAndPersistsFailure(t *testing.T) {
	runner := &fakeSeqRunner{err: errors.New("boom"), failAt: 6}
	store := &fakeState{}
	events := &fakeEvents{}
	req := request(t, resolvedPipeline(t, config.PhaseQA))
	req.MaxIterations = 1
	_, err := orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(store), orchestrator.WithEventSink(events), orchestrator.WithPromptBuilder(fakePrompt{})).Execute(context.Background(), req)
	if err == nil || !errors.Is(err, runner.err) {
		t.Fatalf("err=%v", err)
	}
	if len(runner.phases) != 6 {
		t.Fatalf("dispatches=%v", runner.phases)
	}
	if store.calls[len(store.calls)-1].status != state.StatusFailed {
		t.Fatalf("calls=%v", store.calls)
	}
	if events.types[len(events.types)-1] != orchestrator.EventPhaseFailed {
		t.Fatalf("events=%v", events.types)
	}
}
func TestExecutePropagatesCancellationWithoutDispatchingNextPhase(t *testing.T) {
	runner := &fakeSeqRunner{cancel: true}
	store := &fakeState{}
	events := &fakeEvents{}
	_, err := orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(store), orchestrator.WithEventSink(events), orchestrator.WithPromptBuilder(fakePrompt{})).Execute(context.Background(), request(t, resolvedPipeline(t, config.PhaseQA)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if len(runner.phases) != 1 {
		t.Fatalf("phases=%v", runner.phases)
	}
	if got := store.calls[len(store.calls)-1].status; got != state.StatusStopped {
		t.Fatalf("persisted status = %s, want %s; calls=%v", got, state.StatusStopped, store.calls)
	}
	if got := events.types[len(events.types)-1]; got != orchestrator.EventProjectStopped {
		t.Fatalf("last event = %s, want %s; events=%v", got, orchestrator.EventProjectStopped, events.types)
	}
}

func TestExecuteDevelopmentSubphasesAreOrderedAndPreserveIdentity(t *testing.T) {
	runner := &fakeSeqRunner{}
	store := &fakeState{}
	events := &fakeEvents{}
	request := request(t, resolvedPipeline(t, config.PhaseQA))
	request.Subphases = pipeline.DevelopmentSubphaseGeneration{Mode: pipeline.DevelopmentSubphasesOverride, Subphases: []pipeline.DevelopmentSubphaseDefinition{
		{ID: "design", DisplayName: "Design"},
		{ID: "implement", DisplayName: "Implement"},
	}}
	if _, err := orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(store), orchestrator.WithEventSink(events), orchestrator.WithPromptBuilder(fakePrompt{})).Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var got []string
	for i, phase := range runner.phases {
		if phase == pipeline.PhaseDevelopment {
			got = append(got, runner.subphases[i])
		}
	}
	if !reflect.DeepEqual(got, []string{"design", "implement"}) {
		t.Fatalf("development subphases=%v", got)
	}
	var stateSubphases []string
	for _, call := range store.calls {
		if call.phase == string(pipeline.PhaseDevelopment) && call.status == state.StatusRunning {
			stateSubphases = append(stateSubphases, call.subphase)
		}
	}
	if !reflect.DeepEqual(stateSubphases, []string{"design", "implement"}) {
		t.Fatalf("state subphases=%v", stateSubphases)
	}
	for i, phase := range events.phases {
		if phase == pipeline.PhaseDevelopment && events.subphases[i] == "" {
			t.Fatalf("development event %d lost subphase identity", i)
		}
	}
}

func TestExecuteStopsDevelopmentSubphasesAfterFailure(t *testing.T) {
	runner := &fakeSeqRunner{err: errors.New("subphase failed"), failAt: 2}
	store := &fakeState{}
	request := request(t, resolvedPipeline(t, config.PhaseQA))
	_, err := orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(store), orchestrator.WithPromptBuilder(fakePrompt{})).Execute(context.Background(), request)
	if err == nil || !errors.Is(err, runner.err) {
		t.Fatalf("err=%v", err)
	}
	if !reflect.DeepEqual(runner.subphases, []string{"", "implementation"}) {
		t.Fatalf("dispatch subphases=%v", runner.subphases)
	}
	if len(runner.phases) != 2 {
		t.Fatalf("dispatched after failure: phases=%v", runner.phases)
	}
	last := store.calls[len(store.calls)-1]
	if last.phase != string(pipeline.PhaseDevelopment) || last.subphase != "implementation" || last.status != state.StatusFailed {
		t.Fatalf("failure identity=%#v", last)
	}
}

type feedbackRunner struct {
	statuses     []state.LifecycleStatus
	dispositions []agent.Disposition
	exitCodes    []int
	artifacts    []string
	requests     []agent.RunRequest
	calls        int
}

func (r *feedbackRunner) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	r.requests = append(r.requests, req)
	status := state.StatusFinished
	if r.calls < len(r.statuses) {
		status = r.statuses[r.calls]
	}
	r.calls++
	result := agent.RunResult{ProjectSlug: req.Project.Slug, Phase: req.Phase, Subphase: req.Subphase, Status: status}
	if r.calls-1 < len(r.exitCodes) {
		result.ExitCode = r.exitCodes[r.calls-1]
	}
	if r.calls-1 < len(r.dispositions) {
		result.Disposition = r.dispositions[r.calls-1]
	} else if status == state.StatusFailed && req.Phase == pipeline.PhaseQA && result.ExitCode == 0 {
		result.Disposition = agent.DispositionFailed
	}
	if status == state.StatusFailed {
		result.ArtifactPaths = append([]string(nil), r.artifacts...)
	}
	if status == state.StatusStopped {
		return result, context.Canceled
	}
	return result, nil
}

type feedbackState struct {
	fakeState
	artifacts [][]string
}

func (s *feedbackState) RecordPhase(ctx context.Context, slug, phase, subphase string, status state.LifecycleStatus, outcome *state.ExecutionOutcome, artifacts []string) (state.ProjectState, error) {
	project, err := s.fakeState.RecordPhase(ctx, slug, phase, subphase, status, outcome, artifacts)
	s.artifacts = append(s.artifacts, append([]string(nil), artifacts...))
	return project, err
}

func pipelineWithQA(t *testing.T) pipeline.ExecutablePipeline {
	t.Helper()
	return resolvedPipeline(t, config.PhaseQA)
}

func snapshotReadyPipelineWithQA(t *testing.T) pipeline.ExecutablePipeline {
	t.Helper()
	settings := config.AgentSettings{Agent: config.AgentClaude, Model: "snapshot-model", Effort: config.EffortMedium}
	phases := map[config.Phase]config.ResolvedPhase{}
	for _, phase := range config.RemovablePhases() {
		phases[phase] = config.ResolvedPhase{Enabled: phase == config.PhaseQA, AgentSettings: settings}
	}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), config.ResolvedConfig{Defaults: settings, Phases: phases})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

type persistedPhaseFixture struct {
	ID       pipeline.PhaseID     `json:"id"`
	Settings config.AgentSettings `json:"settings"`
}

type persistedPipelineFixture struct {
	SchemaVersion    int                                    `json:"schemaVersion"`
	PlanningContract int                                    `json:"planningContractVersion,omitempty"`
	Phases           []persistedPhaseFixture                `json:"phases"`
	Subphases        pipeline.DevelopmentSubphaseGeneration `json:"developmentSubphases"`
	MaxQAAttempts    int                                    `json:"maxQaAttempts"`
	GitOps           config.GitOpsConfig                    `json:"gitOps"`
	GitOpsConfigured bool                                   `json:"gitOpsConfigured"`
}

func legacyPipelineWithQA(t *testing.T) (pipeline.ExecutablePipeline, state.PipelineConfigSnapshot) {
	t.Helper()
	current := snapshotReadyPipelineWithQA(t)
	snapshot, err := pipeline.SnapshotExecution(current, pipeline.DevelopmentSubphaseGeneration{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	var encoded persistedPipelineFixture
	if err := json.Unmarshal(snapshot.Data, &encoded); err != nil {
		t.Fatal(err)
	}
	encoded.SchemaVersion = 1
	encoded.PlanningContract = 0
	var rebaseIndex, qaIndex int
	for index, phase := range encoded.Phases {
		switch phase.ID {
		case pipeline.PhaseRebase:
			rebaseIndex = index
		case pipeline.PhaseQA:
			qaIndex = index
		}
	}
	encoded.Phases[rebaseIndex], encoded.Phases[qaIndex] = encoded.Phases[qaIndex], encoded.Phases[rebaseIndex]
	legacyData, err := json.Marshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	legacySnapshot := snapshot
	legacySnapshot.Data = legacyData
	legacy, _, _, err := pipeline.RestoreExecution(legacySnapshot)
	if err != nil {
		t.Fatal(err)
	}
	return legacy, legacySnapshot
}

func TestRestoredSnapshotsExecuteTheirPersistedOrder(t *testing.T) {
	current := snapshotReadyPipelineWithQA(t)
	newSnapshot, err := pipeline.SnapshotExecution(current, pipeline.DevelopmentSubphaseGeneration{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	newPlan, newSubphases, newMaxAttempts, err := pipeline.RestoreExecution(newSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	legacyPlan, legacySnapshot := legacyPipelineWithQA(t)

	tests := []struct {
		name      string
		plan      pipeline.ExecutablePipeline
		snapshot  state.PipelineConfigSnapshot
		subphases pipeline.DevelopmentSubphaseGeneration
		max       int
		want      []pipeline.PhaseID
	}{
		{
			name: "new snapshot uses Rebase before QA",
			plan: newPlan, snapshot: newSnapshot, subphases: newSubphases, max: newMaxAttempts,
			want: []pipeline.PhaseID{pipeline.PhaseAcceptanceCriteria, pipeline.PhaseDevelopment, pipeline.PhaseDevelopment, pipeline.PhaseDevelopment, pipeline.PhaseRebase, pipeline.PhaseQA, pipeline.PhaseTestDocument},
		},
		{
			name: "legacy snapshot keeps QA before Rebase",
			plan: legacyPlan, snapshot: legacySnapshot, subphases: pipeline.DevelopmentSubphaseGeneration{}, max: 3,
			want: []pipeline.PhaseID{pipeline.PhaseAcceptanceCriteria, pipeline.PhaseDevelopment, pipeline.PhaseDevelopment, pipeline.PhaseDevelopment, pipeline.PhaseQA, pipeline.PhaseRebase, pipeline.PhaseTestDocument},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeSeqRunner{}
			req := request(t, test.plan)
			req.Project.PipelineConfig = test.snapshot
			req.Subphases = test.subphases
			req.MaxIterations = test.max
			req.RunID = test.name
			if _, err := orchestrator.NewController(
				orchestrator.WithRunner(runner),
				orchestrator.WithPhaseState(&fakeState{}),
				orchestrator.WithPromptBuilder(fakePrompt{}),
			).Execute(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(runner.phases, test.want) {
				t.Fatalf("executed phases = %v, want persisted order %v", runner.phases, test.want)
			}
		})
	}
}

func TestLegacyQARetryDoesNotInjectNewRebaseInvariant(t *testing.T) {
	legacyPlan, legacySnapshot := legacyPipelineWithQA(t)
	runner := &feedbackRunner{
		statuses: []state.LifecycleStatus{
			state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFailed,
			state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished,
		},
		artifacts: []string{"qa-feedback.md"},
	}
	req := request(t, legacyPlan)
	req.Project.PipelineConfig = legacySnapshot
	req.MaxIterations = 2
	outcomes, err := executeFeedback(t, runner, &feedbackState{}, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 11 {
		t.Fatalf("outcomes = %d, want 11 for legacy QA-before-Rebase retry", len(outcomes))
	}
	want := []pipeline.PhaseID{
		pipeline.PhaseAcceptanceCriteria,
		pipeline.PhaseDevelopment, pipeline.PhaseDevelopment, pipeline.PhaseDevelopment,
		pipeline.PhaseQA,
		pipeline.PhaseDevelopment, pipeline.PhaseDevelopment, pipeline.PhaseDevelopment,
		pipeline.PhaseQA, pipeline.PhaseRebase, pipeline.PhaseTestDocument,
	}
	if !reflect.DeepEqual(runnerRequestsPhases(runner.requests), want) {
		t.Fatalf("legacy retry phases = %v, want %v", runnerRequestsPhases(runner.requests), want)
	}
}

func runnerRequestsPhases(requests []agent.RunRequest) []pipeline.PhaseID {
	phases := make([]pipeline.PhaseID, len(requests))
	for index, request := range requests {
		phases[index] = request.Phase
	}
	return phases
}

func executeFeedback(t *testing.T, runner *feedbackRunner, store *feedbackState, request orchestrator.Request) ([]orchestrator.PhaseOutcome, error) {
	t.Helper()
	return orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(store), orchestrator.WithEventSink(&fakeEvents{}), orchestrator.WithPromptBuilder(fakePrompt{})).Execute(context.Background(), request)
}

func TestExecuteQAPassesOnFirstAttemptWithoutFeedbackDispatch(t *testing.T) {
	runner := &feedbackRunner{}
	store := &feedbackState{}
	outcomes, err := executeFeedback(t, runner, store, request(t, pipelineWithQA(t)))
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 7 {
		t.Fatalf("outcomes=%d, want 7", len(outcomes))
	}
	if got := runner.calls; got != 7 {
		t.Fatalf("dispatches=%d, want 7", got)
	}
	for _, call := range runner.requests {
		if call.Phase == pipeline.PhaseQA && len(call.ArtifactPaths) != 0 {
			t.Fatalf("first QA unexpectedly received feedback: %v", call.ArtifactPaths)
		}
	}
}

func TestExecuteQAFailThenFixDevelopmentThenPass(t *testing.T) {
	runner := &feedbackRunner{statuses: []state.LifecycleStatus{state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFailed, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished}, artifacts: []string{"qa-feedback.md"}}
	store := &feedbackState{}
	req := request(t, pipelineWithQA(t))
	req.MaxIterations = 2
	outcomes, err := executeFeedback(t, runner, store, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 12 {
		t.Fatalf("outcomes=%d, want 12", len(outcomes))
	}
	if got := runner.calls; got != 12 {
		t.Fatalf("dispatches=%d, want 12", got)
	}
	for _, call := range runner.requests[6:9] {
		if call.Phase != pipeline.PhaseDevelopment || len(call.ArtifactPaths) != 1 || call.ArtifactPaths[0] != "qa-feedback.md" {
			t.Fatalf("fix request=%#v", call)
		}
	}
	if runner.requests[9].Phase != pipeline.PhaseRebase || runner.requests[10].Phase != pipeline.PhaseQA {
		t.Fatalf("feedback loop phases = %v, want Rebase then QA", []pipeline.PhaseID{runner.requests[9].Phase, runner.requests[10].Phase})
	}
	if got := runner.requests[10].ArtifactPaths; len(got) != 1 || got[0] != "qa-feedback.md" {
		t.Fatalf("rerun QA artifacts=%v", got)
	}
	found := false
	for _, artifacts := range store.artifacts {
		if len(artifacts) == 1 && artifacts[0] == "qa-feedback.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("feedback artifact was not persisted: %#v", store.artifacts)
	}
}

type retryPRService struct{}

func (retryPRService) Create(context.Context, pr.Request) (pr.Result, error) {
	return pr.Result{Created: true, URL: "https://github.com/example/repo/pull/1"}, nil
}

type retryCIService struct{ calls int }

func (s *retryCIService) Monitor(context.Context, ci.Config) (ci.Result, error) {
	s.calls++
	if s.calls == 1 {
		return ci.Result{Outcome: ci.OutcomeFailed}, nil
	}
	return ci.Result{Outcome: ci.OutcomePassed}, nil
}

func TestExecuteCIFailureRemediationRebasesBeforeQA(t *testing.T) {
	runner := &fakeSeqRunner{}
	checks := &retryCIService{}
	req := request(t, resolvedPipeline(t, config.PhaseQA, config.PhasePR, config.PhaseCI))
	req.ArtifactRoot = t.TempDir()
	req.GitOps = config.GitOpsConfig{Configured: true, ParentBranch: "main", BaseRef: "origin/main", EnablePR: true, EnableCI: true}
	req.MaxIterations = 2
	if _, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(&fakeState{}),
		orchestrator.WithGitOpsServices(nil, retryPRService{}, checks),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	).Execute(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	want := []pipeline.PhaseID{
		pipeline.PhaseAcceptanceCriteria,
		pipeline.PhaseDevelopment, pipeline.PhaseDevelopment, pipeline.PhaseDevelopment,
		pipeline.PhaseRebase, pipeline.PhaseQA, pipeline.PhaseTestDocument,
		pipeline.PhaseDevelopment, pipeline.PhaseDevelopment, pipeline.PhaseDevelopment,
		pipeline.PhaseRebase, pipeline.PhaseQA,
	}
	if !reflect.DeepEqual(runner.phases, want) {
		t.Fatalf("CI remediation phases = %v, want %v", runner.phases, want)
	}
	if checks.calls != 2 {
		t.Fatalf("CI checks = %d, want initial failure plus remediation pass", checks.calls)
	}
}

func TestLifecycleBackedQAFailureKeepsDispatchClaimThroughFixAndPass(t *testing.T) {
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := state.NewLifecycleService(store, nil, store.Locker())
	project := state.ProjectState{
		Name: "QA Retry", Slug: "qa-retry", OriginalGoal: "retry QA",
		AcceptanceCriteria: []string{"failed QA is fixed and rerun"},
		PipelineConfig:     state.PipelineConfigSnapshot{SchemaVersion: 1, Data: json.RawMessage(`{}`)},
		CurrentPhase:       "pipeline", Status: state.StatusRunning, WorktreePath: t.TempDir(), BranchName: "agent/qa-retry",
	}
	if err := lifecycle.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	project, err = lifecycle.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	runner := &feedbackRunner{
		statuses:  []state.LifecycleStatus{state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFailed},
		artifacts: []string{"qa-report.md"},
	}
	req := request(t, pipelineWithQA(t))
	req.Project = project
	req.MaxIterations = 2
	req.RunID = "qa-retry-run"
	controller := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(lifecycle),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	if _, err := controller.Execute(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	reloaded, err := lifecycle.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != state.StatusFinished || reloaded.ActiveRunID != "" || reloaded.DispatchClaimRunID != "" {
		t.Fatalf("finished lifecycle state = %#v", reloaded)
	}
	if reloaded.MaxQAAttempts != 2 || reloaded.QACompletedAttempts != 0 || reloaded.QALoopStage != "" {
		t.Fatalf("QA configuration/cursor = %#v", reloaded)
	}
	foundFailedQA := false
	for _, phase := range reloaded.PhaseHistory {
		if phase.Phase == string(pipeline.PhaseQA) && phase.Status == state.StatusFailed {
			foundFailedQA = true
		}
	}
	if !foundFailedQA {
		t.Fatalf("phase history lost retryable QA failure: %#v", reloaded.PhaseHistory)
	}
}

func TestExecuteQARepeatedFailureStopsAtMaxIterationsWithoutExtraDispatch(t *testing.T) {
	statuses := []state.LifecycleStatus{state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFailed, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFailed}
	runner := &feedbackRunner{statuses: statuses, artifacts: []string{"feedback.md"}}
	store := &feedbackState{}
	req := request(t, pipelineWithQA(t))
	req.MaxIterations = 2
	outcomes, err := executeFeedback(t, runner, store, req)
	if err == nil {
		t.Fatal("expected max-iteration error")
	}
	if len(outcomes) != 11 || runner.calls != 11 {
		t.Fatalf("outcomes=%d dispatches=%d, want 11", len(outcomes), runner.calls)
	}
	if runner.requests[len(runner.requests)-1].Phase != pipeline.PhaseQA {
		t.Fatalf("last request=%#v", runner.requests[len(runner.requests)-1])
	}
}

func TestExecuteQACancellationDoesNotStartFixDevelopment(t *testing.T) {
	runner := &feedbackRunner{statuses: []state.LifecycleStatus{state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusStopped}}
	store := &feedbackState{}
	req := request(t, pipelineWithQA(t))
	req.MaxIterations = 3
	outcomes, err := executeFeedback(t, runner, store, req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want cancellation", err)
	}
	if len(outcomes) != 6 || runner.calls != 6 {
		t.Fatalf("outcomes=%d dispatches=%d, want 6", len(outcomes), runner.calls)
	}
}

func TestExecuteQABlockedDispositionIsTerminalWithoutFixDispatch(t *testing.T) {
	runner := &feedbackRunner{
		statuses:     []state.LifecycleStatus{state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFailed},
		dispositions: []agent.Disposition{"", "", "", "", "", agent.DispositionBlocked},
		artifacts:    []string{"qa-report.md"},
	}
	store := &feedbackState{}
	req := request(t, pipelineWithQA(t))
	req.MaxIterations = 3
	outcomes, err := executeFeedback(t, runner, store, req)
	if err == nil {
		t.Fatal("blocked QA unexpectedly entered the fix loop")
	}
	if len(outcomes) != 6 || runner.calls != 6 {
		t.Fatalf("blocked QA outcomes=%d dispatches=%d, want exactly the initial QA boundary", len(outcomes), runner.calls)
	}
}

func TestExecuteQATransportFailureIsTerminalWithoutFixDispatch(t *testing.T) {
	runner := &feedbackRunner{
		statuses:  []state.LifecycleStatus{state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFailed},
		exitCodes: []int{0, 0, 0, 0, 0, 9},
		artifacts: []string{"qa-report.md"},
	}
	store := &feedbackState{}
	req := request(t, pipelineWithQA(t))
	req.MaxIterations = 3
	outcomes, err := executeFeedback(t, runner, store, req)
	if err == nil {
		t.Fatal("transport-failed QA unexpectedly entered the fix loop")
	}
	if len(outcomes) != 6 || runner.calls != 6 {
		t.Fatalf("transport failure outcomes=%d dispatches=%d, want exactly the initial QA boundary", len(outcomes), runner.calls)
	}
}

type conflictRouter struct {
	route orchestrator.ConflictRoute
	err   error
	seen  []orchestrator.Conflict
}

func (r *conflictRouter) Route(_ context.Context, conflict orchestrator.Conflict) (orchestrator.ConflictRoute, error) {
	r.seen = append(r.seen, conflict)
	return r.route, r.err
}

func TestExecuteRebaseConflictPersistsFailureAndStopsTerminalRoute(t *testing.T) {
	runner := &feedbackRunner{
		statuses:  []state.LifecycleStatus{state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFailed},
		artifacts: []string{"rebase-conflict.txt"},
	}
	store := &feedbackState{}
	events := &fakeEvents{}
	_, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(store),
		orchestrator.WithEventSink(events),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	).Execute(context.Background(), request(t, pipelineWithQA(t)))
	if err == nil {
		t.Fatal("expected unresolved conflict error")
	}
	if runner.calls != 5 {
		t.Fatalf("dispatches=%d, want 5 (no dispatch after terminal conflict)", runner.calls)
	}
	last := store.calls[len(store.calls)-1]
	if last.phase != string(pipeline.PhaseRebase) || last.status != state.StatusFailed {
		t.Fatalf("last state call=%#v", last)
	}
	if events.types[len(events.types)-1] != orchestrator.EventConflictDetected {
		t.Fatalf("last events=%v, want conflict event", events.types)
	}
}

func TestExecuteRebaseConflictRouterErrorPreservesFailureContext(t *testing.T) {
	routerErr := errors.New("resolver unavailable")
	runner := &feedbackRunner{
		statuses:  []state.LifecycleStatus{state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFailed},
		artifacts: []string{"rebase-conflict.txt"},
	}
	store := &feedbackState{}
	router := &conflictRouter{err: routerErr}
	_, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(store),
		orchestrator.WithPromptBuilder(fakePrompt{}),
		orchestrator.WithConflictRouter(router),
	).Execute(context.Background(), request(t, pipelineWithQA(t)))
	if !errors.Is(err, routerErr) {
		t.Fatalf("err=%v, want router error", err)
	}
	if len(router.seen) != 1 || !reflect.DeepEqual(router.seen[0].ArtifactPaths, []string{"rebase-conflict.txt"}) {
		t.Fatalf("router conflict=%#v", router.seen)
	}
	if store.calls[len(store.calls)-1].status != state.StatusFailed {
		t.Fatalf("failure was not persisted: %#v", store.calls)
	}
}

func TestExecuteDoesNotTreatRouterApprovalAsConflictResolutionInSameRun(t *testing.T) {
	runner := &feedbackRunner{
		statuses:  []state.LifecycleStatus{state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFailed, state.StatusFinished, state.StatusFinished},
		artifacts: []string{"rebase-conflict.txt"},
	}
	store := &feedbackState{}
	events := &fakeEvents{}
	router := &conflictRouter{route: orchestrator.ConflictRouteQA}
	outcomes, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(store),
		orchestrator.WithEventSink(events),
		orchestrator.WithPromptBuilder(fakePrompt{}),
		orchestrator.WithConflictRouter(router),
	).Execute(context.Background(), request(t, pipelineWithQA(t)))
	if err == nil {
		t.Fatal("failed Rebase unexpectedly continued in the same run")
	}
	if runner.calls != 5 || len(outcomes) != 5 {
		t.Fatalf("dispatches=%d outcomes=%d, want 5", runner.calls, len(outcomes))
	}
	if len(router.seen) != 1 || !reflect.DeepEqual(router.seen[0].ArtifactPaths, []string{"rebase-conflict.txt"}) {
		t.Fatalf("router conflicts=%#v", router.seen)
	}
	foundArtifact := false
	for _, artifacts := range store.artifacts {
		if reflect.DeepEqual(artifacts, []string{"rebase-conflict.txt"}) {
			foundArtifact = true
			break
		}
	}
	if !foundArtifact {
		t.Fatalf("conflict artifact was not preserved: %#v", store.artifacts)
	}
	foundEvent := false
	for _, event := range events.types {
		if event == orchestrator.EventConflictDetected {
			foundEvent = true
		}
	}
	if !foundEvent {
		t.Fatalf("events=%v", events.types)
	}
}

func TestExecuteRebaseCancellationDoesNotRouteConflict(t *testing.T) {
	runner := &feedbackRunner{statuses: []state.LifecycleStatus{state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusStopped}}
	store := &feedbackState{}
	events := &fakeEvents{}
	router := &conflictRouter{route: orchestrator.ConflictRouteQA}
	_, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(store),
		orchestrator.WithEventSink(events),
		orchestrator.WithPromptBuilder(fakePrompt{}),
		orchestrator.WithConflictRouter(router),
	).Execute(context.Background(), request(t, pipelineWithQA(t)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want cancellation", err)
	}
	if len(router.seen) != 0 {
		t.Fatalf("cancellation was routed as conflict: %#v", router.seen)
	}
	for _, event := range events.types {
		if event == orchestrator.EventConflictDetected {
			t.Fatalf("cancellation emitted conflict event: %v", events.types)
		}
	}
}

type productionConflictReader struct{ unresolved bool }

func (r productionConflictReader) HasUnresolvedConflicts(context.Context, string) (bool, error) {
	return r.unresolved, nil
}

func TestProductionControllerTreatsCleanIndexRebaseFailureAsOrdinaryFailure(t *testing.T) {
	runner := &feedbackRunner{statuses: []state.LifecycleStatus{state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFailed, state.StatusFinished, state.StatusFinished}, artifacts: []string{"rebase-conflict.txt"}}
	store := &feedbackState{}
	req := request(t, pipelineWithQA(t))
	req.Project.WorktreePath = "/worktree"
	_, err := orchestrator.NewProductionController(runner, store, productionConflictReader{}, orchestrator.WithPromptBuilder(fakePrompt{})).Execute(context.Background(), req)
	if err == nil {
		t.Fatal("ordinary clean-index Rebase failure unexpectedly continued")
	}
	if runner.calls != 5 {
		t.Fatalf("calls=%d requests=%#v", runner.calls, runner.requests)
	}
}

func TestProductionControllerKeepsUnresolvedGitConflictTerminal(t *testing.T) {
	runner := &feedbackRunner{statuses: []state.LifecycleStatus{state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFinished, state.StatusFailed}, artifacts: []string{"rebase-conflict.txt"}}
	store := &feedbackState{}
	req := request(t, pipelineWithQA(t))
	req.Project.WorktreePath = "/worktree"
	_, err := orchestrator.NewProductionController(runner, store, productionConflictReader{unresolved: true}, orchestrator.WithPromptBuilder(fakePrompt{})).Execute(context.Background(), req)
	if err == nil {
		t.Fatal("unresolved conflict unexpectedly succeeded")
	}
	if runner.calls != 5 {
		t.Fatalf("calls=%d, want 5", runner.calls)
	}
}

func TestExecuteAllowsSequentialReuseOfSameProject(t *testing.T) {
	runner := &finiteRunner{}
	controller := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(&fakeState{}),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	req := request(t, resolvedPipeline(t, config.PhaseQA))
	req.RunID = "run-a"
	if _, err := controller.Execute(context.Background(), req); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	firstRunCalls := len(runner.calls)
	if firstRunCalls == 0 {
		t.Fatal("first Execute() dispatched no phases")
	}
	if _, err := controller.Execute(context.Background(), req); err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	if got := len(runner.calls); got != 2*firstRunCalls {
		t.Fatalf("dispatches after sequential reuse = %d, want %d", got, 2*firstRunCalls)
	}
}

func TestExecuteRejectsConcurrentSameProjectRun(t *testing.T) {
	runner := &stopAwareRunner{started: make(chan struct{}), entered: make(chan struct{})}
	controller := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(&fakeState{}),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	req := request(t, resolvedPipeline(t, config.PhaseQA))
	req.RunID = "run-a"
	done := make(chan error, 1)
	go func() { _, err := controller.Execute(context.Background(), req); done <- err }()
	<-runner.entered
	second := req
	second.RunID = "run-b"
	if _, err := controller.Execute(context.Background(), second); err == nil {
		t.Fatal("concurrent same-project Execute unexpectedly succeeded")
	}
	if err := controller.Stop(context.Background(), orchestrator.StopRequest{ProjectSlug: req.Project.Slug, RunID: req.RunID}); err != nil {
		t.Fatal(err)
	}
	close(runner.started)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("first Execute() error = %v, want cancellation", err)
	}
}

type fakeDevelopmentCommits struct {
	head           string
	headErr        error
	verifyErr      error
	autoCommitErr  error
	headCalls      int
	verifyBase     []string
	required       []bool
	autoCommitMsgs []string
}

func (f *fakeDevelopmentCommits) AutoCommitUncommittedChanges(_ context.Context, _ string, message string) error {
	f.autoCommitMsgs = append(f.autoCommitMsgs, message)
	return f.autoCommitErr
}

func (f *fakeDevelopmentCommits) HeadCommit(_ context.Context, _ string) (string, error) {
	f.headCalls++
	if f.headErr != nil {
		return "", f.headErr
	}
	return f.head, nil
}

func (f *fakeDevelopmentCommits) VerifyUnsignedDevelopmentCommits(_ context.Context, _ string, previous string, requireCommit bool) error {
	f.verifyBase = append(f.verifyBase, previous)
	f.required = append(f.required, requireCommit)
	return f.verifyErr
}

func TestExecuteEnforcesUnsignedCommitProgressForEveryDevelopmentSubphase(t *testing.T) {
	commits := &fakeDevelopmentCommits{head: "base"}
	runner := &fakeSeqRunner{}
	store := &fakeState{}
	req := request(t, resolvedPipeline(t, config.PhaseQA))
	_, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(store),
		orchestrator.WithPromptBuilder(fakePrompt{}),
		orchestrator.WithDevelopmentCommitVerifier(commits),
	).Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if commits.headCalls != 3 || len(commits.verifyBase) != 3 {
		t.Fatalf("commit checks = head %d verify %d, want three each", commits.headCalls, len(commits.verifyBase))
	}
	// Every successful subphase first preserves any work the agent left
	// uncommitted, so forgetting to commit can never fail the phase.
	if len(commits.autoCommitMsgs) != 3 {
		t.Fatalf("auto-commit calls = %v, want one per development subphase", commits.autoCommitMsgs)
	}
	if len(runner.phases) != 7 {
		t.Fatalf("dispatches = %d, want all phases", len(runner.phases))
	}
}

func TestExecuteFailsSubphaseWhenAutoCommitFails(t *testing.T) {
	autoErr := errors.New("stage uncommitted development changes: disk full")
	commits := &fakeDevelopmentCommits{head: "base", autoCommitErr: autoErr}
	runner := &fakeSeqRunner{}
	store := &fakeState{}
	_, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(store),
		orchestrator.WithPromptBuilder(fakePrompt{}),
		orchestrator.WithDevelopmentCommitVerifier(commits),
	).Execute(context.Background(), request(t, resolvedPipeline(t, config.PhaseQA)))
	if !errors.Is(err, autoErr) {
		t.Fatalf("error = %v, want auto-commit failure surfaced", err)
	}
	// Verification still runs but must not require a commit after the
	// auto-commit itself failed.
	if len(commits.required) == 0 || commits.required[len(commits.required)-1] {
		t.Fatalf("required = %v, want final verification without commit requirement", commits.required)
	}
}

func TestExecuteRejectsSignedDevelopmentCommitAndPersistsFailedSubphase(t *testing.T) {
	signedErr := errors.New("development commit is signed")
	commits := &fakeDevelopmentCommits{head: "base", verifyErr: signedErr}
	runner := &fakeSeqRunner{}
	store := &fakeState{}
	events := &fakeEvents{}
	_, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(store),
		orchestrator.WithEventSink(events),
		orchestrator.WithPromptBuilder(fakePrompt{}),
		orchestrator.WithDevelopmentCommitVerifier(commits),
	).Execute(context.Background(), request(t, resolvedPipeline(t, config.PhaseQA)))
	if !errors.Is(err, signedErr) {
		t.Fatalf("error = %v, want signed rejection", err)
	}
	if len(runner.phases) != 2 || runner.phases[1] != pipeline.PhaseDevelopment {
		t.Fatalf("dispatches = %v, want acceptance then development only", runner.phases)
	}
	last := store.calls[len(store.calls)-1]
	if last.phase != string(pipeline.PhaseDevelopment) || last.subphase != "implementation" || last.status != state.StatusFailed {
		t.Fatalf("last persisted call = %#v, want failed implementation", last)
	}
	if events.types[len(events.types)-1] != orchestrator.EventPhaseFailed {
		t.Fatalf("events = %v, want phase_failed terminal event", events.types)
	}
}

func TestExecuteRejectsMissingDevelopmentCommit(t *testing.T) {
	noCommitErr := errors.New("development subphase did not create a commit")
	commits := &fakeDevelopmentCommits{head: "base", verifyErr: noCommitErr}
	runner := &fakeSeqRunner{}
	store := &fakeState{}
	_, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(store),
		orchestrator.WithPromptBuilder(fakePrompt{}),
		orchestrator.WithDevelopmentCommitVerifier(commits),
	).Execute(context.Background(), request(t, resolvedPipeline(t, config.PhaseQA)))
	if !errors.Is(err, noCommitErr) {
		t.Fatalf("error = %v, want no-commit rejection", err)
	}
	if len(runner.phases) != 2 {
		t.Fatalf("dispatches = %v, want no later subphases", runner.phases)
	}
}

func TestExecutePropagatesDevelopmentCommitVerifierCancellation(t *testing.T) {
	commits := &fakeDevelopmentCommits{headErr: context.Canceled}
	runner := &fakeSeqRunner{}
	store := &fakeState{}
	_, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(store),
		orchestrator.WithPromptBuilder(fakePrompt{}),
		orchestrator.WithDevelopmentCommitVerifier(commits),
	).Execute(context.Background(), request(t, resolvedPipeline(t, config.PhaseQA)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(runner.phases) != 1 {
		t.Fatalf("dispatches = %v, want acceptance only", runner.phases)
	}
	if got := store.calls[len(store.calls)-1].status; got != state.StatusStopped {
		t.Fatalf("status = %s, want stopped for cancellation", got)
	}
}

func TestExecuteAllowsExplicitNoDevelopmentCommitContract(t *testing.T) {
	commits := &fakeDevelopmentCommits{head: "base", verifyErr: errors.New("must not be called")}
	runner := &fakeSeqRunner{}
	req := request(t, resolvedPipeline(t, config.PhaseQA))
	req.AllowDevelopmentSubphaseWithoutCommit = true
	if _, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(&fakeState{}),
		orchestrator.WithPromptBuilder(fakePrompt{}),
		orchestrator.WithDevelopmentCommitVerifier(commits),
	).Execute(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if commits.headCalls != 0 || len(commits.verifyBase) != 0 {
		t.Fatalf("commit verifier called despite explicit exception: %#v", commits)
	}
}

type planLoopState struct {
	fakeState
	project   state.ProjectState
	completed [][]string
}

func (s *planLoopState) Load(context.Context, string) (state.ProjectState, error) {
	return s.project, nil
}

func (s *planLoopState) RecordPlan(_ context.Context, _ string, phases, completed []string) (state.ProjectState, error) {
	s.completed = append(s.completed, append([]string(nil), completed...))
	if s.project.Plan == nil {
		s.project.Plan = &state.PlanState{}
	}
	s.project.Plan.Phases = append(s.project.Plan.Phases, phases...)
	s.project.Plan.Completed = append(s.project.Plan.Completed, completed...)
	return s.project, nil
}

type scopeCapturePrompt struct{ inputs []agent.PromptInput }

func (p *scopeCapturePrompt) BuildPrompt(input agent.PromptInput) (string, error) {
	p.inputs = append(p.inputs, input)
	return "prompt", nil
}

func planLoopRequest(t *testing.T, completed ...string) (orchestrator.Request, *planLoopState) {
	t.Helper()
	store := &planLoopState{project: state.ProjectState{
		Slug: "demo", WorktreePath: "/tmp",
		Plan: &state.PlanState{Phases: []string{"P1", "P2", "P3"}, Completed: completed},
	}}
	return request(t, resolvedPipeline(t)), store
}

type planningRetryRunner struct {
	worktree  string
	validFrom int
	attempts  int
	runIDs    []string
	prompts   []string
}

func (r *planningRetryRunner) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	if req.Phase == pipeline.PhasePlanning {
		r.attempts++
		r.runIDs = append(r.runIDs, req.RunID)
		r.prompts = append(r.prompts, req.Prompt)
		if err := os.MkdirAll(filepath.Join(r.worktree, ".gg"), 0o755); err != nil {
			return agent.RunResult{Phase: req.Phase, Status: state.StatusFailed}, err
		}
		plan := planningTestArtifact(r.attempts >= r.validFrom)
		if err := os.WriteFile(filepath.Join(r.worktree, ".gg", "plan.md"), []byte(plan), 0o644); err != nil {
			return agent.RunResult{Phase: req.Phase, Status: state.StatusFailed}, err
		}
	}
	return agent.RunResult{Phase: req.Phase, Status: state.StatusFinished, Disposition: agent.DispositionPassed}, nil
}

func planningTestArtifact(valid bool) string {
	if !valid {
		return "---\ngg_run_id: \"run\"\ngg_disposition: passed\ngg_plan_complexity: \"Trivial\"\ngg_plan_complexity_evidence: [\"one outcome\"]\ngg_plan_phases: [\"Phase 1: first\", \"Phase 2: second\"]\ngg_plan_phase_boundaries: [{\"phase\":\"Phase 1: first\",\"justification\":\"split\"}, {\"phase\":\"Phase 2: second\",\"justification\":\"split\"}]\n---\n## Complexity assessment\n\n- Complexity category: **Trivial**\n- Selected phase count: **2**\n\nSupporting evidence:\n\n1. one outcome\n\n## Phase 1: first\n\nBoundary justification: split\n\n## Phase 2: second\n\nBoundary justification: split\n"
	}
	return "---\ngg_run_id: \"run\"\ngg_disposition: passed\ngg_plan_complexity: \"Trivial\"\ngg_plan_complexity_evidence: [\"one outcome\"]\ngg_plan_phases: [\"Phase 1: first\"]\ngg_plan_phase_boundaries: [{\"phase\":\"Phase 1: first\",\"justification\":\"one cohesive outcome\"}]\n---\n## Complexity assessment\n\n- Complexity category: **Trivial**\n- Selected phase count: **1**\n\nSupporting evidence:\n\n1. one outcome\n\n## Phase 1: first\n\nBoundary justification: one cohesive outcome\n"
}

func TestPlanningRetriesInvalidArtifactWithFreshInvocationAndExactEvidence(t *testing.T) {
	worktree := t.TempDir()
	retryRunner := &planningRetryRunner{worktree: worktree, validFrom: 2}
	prompts := &scopeCapturePrompt{}
	req := request(t, resolvedPipeline(t, config.PhasePlanning))
	req.Project.WorktreePath = worktree
	req.Project.PipelineConfig = state.PipelineConfigSnapshot{SchemaVersion: 1, Data: []byte(`{"planningContractVersion":1}`)}
	_, err := orchestrator.NewController(
		orchestrator.WithRunner(retryRunner),
		orchestrator.WithPhaseState(&fakeState{}),
		orchestrator.WithPromptBuilder(prompts),
	).Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if retryRunner.attempts != 2 || len(retryRunner.runIDs) != 2 || retryRunner.runIDs[0] == retryRunner.runIDs[1] {
		t.Fatalf("planning attempts=%d run IDs=%v, want two fresh invocations", retryRunner.attempts, retryRunner.runIDs)
	}
	var planningInputs []agent.PromptInput
	for _, input := range prompts.inputs {
		if input.Phase == pipeline.PhasePlanning {
			planningInputs = append(planningInputs, input)
		}
	}
	if len(planningInputs) != 2 || planningInputs[1].PlanningAttempt != 2 {
		t.Fatalf("planning prompt inputs=%#v, want corrective attempt 2", planningInputs)
	}
	if !strings.Contains(planningInputs[1].RejectedPlanningArtifact, "Phase 2: second") || len(planningInputs[1].PlanningValidationErrors) == 0 {
		t.Fatalf("retry evidence=%#v", planningInputs[1])
	}
}

func TestPlanningStopsAfterThreeInvalidAttemptsWithoutFourthDispatch(t *testing.T) {
	worktree := t.TempDir()
	retryRunner := &planningRetryRunner{worktree: worktree, validFrom: 4}
	req := request(t, resolvedPipeline(t, config.PhasePlanning))
	req.Project.WorktreePath = worktree
	req.Project.PipelineConfig = state.PipelineConfigSnapshot{SchemaVersion: 1, Data: []byte(`{"planningContractVersion":1}`)}
	_, err := orchestrator.NewController(
		orchestrator.WithRunner(retryRunner),
		orchestrator.WithPhaseState(&fakeState{}),
		orchestrator.WithPromptBuilder(&scopeCapturePrompt{}),
	).Execute(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "phase-limit-exceeded") {
		t.Fatalf("error=%v, want clear phase-limit-exceeded failure", err)
	}
	if retryRunner.attempts != agent.MaxPlanningAttempts {
		t.Fatalf("planning attempts=%d, want exactly %d", retryRunner.attempts, agent.MaxPlanningAttempts)
	}
}

func TestLegacyPlanningSnapshotBypassesNewContractGate(t *testing.T) {
	worktree := t.TempDir()
	retryRunner := &planningRetryRunner{worktree: worktree, validFrom: 4}
	req := request(t, resolvedPipeline(t, config.PhasePlanning))
	req.Project.WorktreePath = worktree
	req.Project.PipelineConfig = state.PipelineConfigSnapshot{SchemaVersion: 1, Data: []byte(`{"schemaVersion":1}`)}
	if _, err := orchestrator.NewController(
		orchestrator.WithRunner(retryRunner),
		orchestrator.WithPhaseState(&fakeState{}),
		orchestrator.WithPromptBuilder(&scopeCapturePrompt{}),
	).Execute(context.Background(), req); err != nil {
		t.Fatalf("legacy plan was revalidated: %v", err)
	}
	if retryRunner.attempts != 1 {
		t.Fatalf("legacy planning attempts=%d, want one tolerant execution", retryRunner.attempts)
	}
}

func TestDevelopmentRunsFreshScopedSequencePerPendingPlanPhase(t *testing.T) {
	req, store := planLoopRequest(t, "P1")
	runner := &fakeSeqRunner{}
	prompts := &scopeCapturePrompt{}
	if _, err := orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(store), orchestrator.WithPromptBuilder(prompts)).Execute(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	var scopes []string
	for _, input := range prompts.inputs {
		if input.Phase == pipeline.PhaseDevelopment {
			scopes = append(scopes, fmt.Sprintf("%s/%s %d/%d", input.PlanPhase, input.Subphase, input.PlanPhaseIndex, input.PlanPhaseTotal))
		}
	}
	want := []string{
		"P2/implementation 2/3", "P2/testing 2/3", "P2/review 2/3",
		"P3/implementation 3/3", "P3/testing 3/3", "P3/review 3/3",
	}
	if !reflect.DeepEqual(scopes, want) {
		t.Fatalf("scoped development runs = %v, want %v", scopes, want)
	}
	if !reflect.DeepEqual(store.completed, [][]string{{"P2"}, {"P3"}}) {
		t.Fatalf("recorded completions = %v, want P2 then P3", store.completed)
	}
}

func TestDevelopmentPlanPhaseCompletionRequiresFullSequence(t *testing.T) {
	req, store := planLoopRequest(t, "P1", "P2")
	// Runs: acceptance_criteria, then P3 implementation, then P3 testing
	// fails — review never runs and P3 must stay pending for resume.
	runner := &fakeSeqRunner{err: errors.New("tests failed"), failAt: 3}
	prompts := &scopeCapturePrompt{}
	_, err := orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(store), orchestrator.WithPromptBuilder(prompts)).Execute(context.Background(), req)
	if err == nil {
		t.Fatal("development must fail when a plan phase's testing fails")
	}
	if len(store.completed) != 0 {
		t.Fatalf("failed plan phase must not be recorded complete, got %v", store.completed)
	}
	if got := runner.subphases; !reflect.DeepEqual(got, []string{"", "implementation", "testing"}) {
		t.Fatalf("runs = %v, want stop at P3 testing", got)
	}
}

func TestDevelopmentWithoutPendingPlanRunsSingleWorktreePass(t *testing.T) {
	req, store := planLoopRequest(t, "P1", "P2", "P3")
	runner := &fakeSeqRunner{}
	prompts := &scopeCapturePrompt{}
	if _, err := orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(store), orchestrator.WithPromptBuilder(prompts)).Execute(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, input := range prompts.inputs {
		if input.Phase == pipeline.PhaseDevelopment {
			count++
			if input.PlanPhase != "" {
				t.Fatalf("completed plan must run unscoped, got scope %q", input.PlanPhase)
			}
		}
	}
	if count != 3 {
		t.Fatalf("development runs = %d, want one implementation/testing/review pass", count)
	}
}

func TestGitDisabledProjectSkipsGitOpsPhasesWithoutAgents(t *testing.T) {
	// A project in a non-git folder must run its agent phases normally while
	// rebase, PR, and CI are skipped deterministically — no agent dispatched,
	// no git service invoked, pipeline continues to completion.
	runner := &fakeSeqRunner{}
	events := &fakeEvents{}
	req := request(t, resolvedPipeline(t, config.PhaseQA, config.PhasePR, config.PhaseCI))
	req.Project.GitDisabled = true
	req.Project.WorktreePath = t.TempDir()
	req.GitOps = config.GitOpsConfig{ParentBranch: "main", EnablePR: true, EnableCI: true}
	if _, err := orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(&fakeState{}), orchestrator.WithEventSink(events), orchestrator.WithPromptBuilder(fakePrompt{})).Execute(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	for _, phase := range runner.phases {
		if phase == pipeline.PhaseRebase || phase == pipeline.PhasePR || phase == pipeline.PhaseCI {
			t.Fatalf("git-dependent phase %q was dispatched to an agent", phase)
		}
	}
	succeeded := map[pipeline.PhaseID]bool{}
	for i, eventType := range events.types {
		if eventType == orchestrator.EventPhaseSucceeded {
			succeeded[events.phases[i]] = true
		}
	}
	for _, phase := range []pipeline.PhaseID{pipeline.PhaseRebase, pipeline.PhasePR, pipeline.PhaseCI} {
		if !succeeded[phase] {
			t.Fatalf("phase %q did not complete as skipped: %v", phase, events.types)
		}
	}
}

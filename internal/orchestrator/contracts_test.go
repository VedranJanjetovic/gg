package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

func TestRequestReusesExistingDomainModels(t *testing.T) {
	request := orchestrator.Request{
		Project:        state.ProjectState{Slug: "example"},
		Pipeline:       pipeline.ExecutablePipeline{},
		PhaseContracts: map[pipeline.PhaseID]string{pipeline.PhaseQA: "run QA"},
		Subphases:      pipeline.DevelopmentSubphaseGeneration{Mode: pipeline.DevelopmentSubphasesDisabled},
	}
	if request.Project.Slug != "example" || request.PhaseContracts[pipeline.PhaseQA] != "run QA" {
		t.Fatalf("request did not preserve existing domain contracts: %#v", request)
	}
}

func TestEventTypesAndRoutingContracts(t *testing.T) {
	cases := []struct {
		event orchestrator.EventType
		want  string
	}{
		{orchestrator.EventProjectCreated, "project_created"},
		{orchestrator.EventPhaseStarted, "phase_started"},
		{orchestrator.EventPhaseSucceeded, "phase_succeeded"},
		{orchestrator.EventPhaseFailed, "phase_failed"},
		{orchestrator.EventFeedbackCreated, "feedback_created"},
		{orchestrator.EventPhaseRetried, "phase_retried"},
		{orchestrator.EventProjectStopped, "project_stopped"},
		{orchestrator.EventProjectFinished, "project_finished"},
	}
	for _, test := range cases {
		if string(test.event) != test.want {
			t.Errorf("event type = %q, want %q", test.event, test.want)
		}
	}
	if orchestrator.ConflictRouteQA != "qa" {
		t.Errorf("QA conflict route = %q", orchestrator.ConflictRouteQA)
	}
	if orchestrator.EventConflictDetected != "conflict_detected" {
		t.Errorf("conflict event = %q", orchestrator.EventConflictDetected)
	}
}

func TestContractsCompileWithoutOrchestrationImplementation(t *testing.T) {
	var _ orchestrator.PhaseRunner = fakeRunner{}
	var _ orchestrator.EventSink = fakeEventSink{}
	var _ orchestrator.FeedbackLoop = fakeFeedbackLoop{}
	var _ orchestrator.ConflictRouter = fakeConflictRouter{}
}

type fakeRunner struct{}

func (fakeRunner) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	return agent.RunResult{}, nil
}

type fakeEventSink struct{}

func (fakeEventSink) Publish(context.Context, orchestrator.Event) error { return nil }

type fakeFeedbackLoop struct{}

func (fakeFeedbackLoop) Decide(context.Context, orchestrator.LoopState, orchestrator.PhaseOutcome) (orchestrator.LoopDecision, error) {
	return orchestrator.LoopComplete, nil
}

type fakeConflictRouter struct{}

func (fakeConflictRouter) Route(context.Context, orchestrator.Conflict) (orchestrator.ConflictRoute, error) {
	return orchestrator.ConflictRouteQA, nil
}

type fakeConflictStateReader struct {
	unresolved bool
	err        error
}

func (r fakeConflictStateReader) HasUnresolvedConflicts(context.Context, string) (bool, error) {
	return r.unresolved, r.err
}

func TestProductionConflictRouterRoutesOnlyResolvedGitWorktreesToQA(t *testing.T) {
	resolved, err := orchestrator.NewConflictRouter(fakeConflictStateReader{}).Route(context.Background(), orchestrator.Conflict{WorktreePath: "/worktree"})
	if err != nil || resolved != orchestrator.ConflictRouteQA {
		t.Fatalf("resolved route = %q, error=%v", resolved, err)
	}
	unresolved, err := orchestrator.NewConflictRouter(fakeConflictStateReader{unresolved: true}).Route(context.Background(), orchestrator.Conflict{WorktreePath: "/worktree"})
	if err != nil || unresolved != orchestrator.ConflictRouteTerminal {
		t.Fatalf("unresolved route = %q, error=%v", unresolved, err)
	}
}

type contractObservationSource struct{}

func (contractObservationSource) ObserveAll(context.Context) ([]state.ProjectObservation, error) {
	return nil, nil
}

type contractNotificationSink struct{}

func (contractNotificationSink) Notify(context.Context, orchestrator.Notification) error { return nil }

func TestFoundationalConsumerContractsAreIndependentOfTUI(t *testing.T) {
	var _ orchestrator.ProjectObserver = contractObservationSource{}
	var _ orchestrator.NotificationSink = contractNotificationSink{}
	notification := orchestrator.Notification{Kind: orchestrator.NotificationProjectCompleted, At: time.Unix(1, 0)}
	if notification.Kind != orchestrator.NotificationProjectCompleted {
		t.Fatal("notification kind contract changed")
	}
}

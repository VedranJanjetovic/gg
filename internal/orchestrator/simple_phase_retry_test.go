package orchestrator_test

import (
	"context"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

// semanticFailRunner reports a pure failed self-verdict for the first
// `failures` acceptance-criteria invocations (-1 = every invocation) and
// passes everything else.
type semanticFailRunner struct {
	phases   []pipeline.PhaseID
	failures int
}

func (r *semanticFailRunner) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	r.phases = append(r.phases, req.Phase)
	if req.Phase == pipeline.PhaseAcceptanceCriteria {
		invocations := 0
		for _, phase := range r.phases {
			if phase == pipeline.PhaseAcceptanceCriteria {
				invocations++
			}
		}
		if r.failures < 0 || invocations <= r.failures {
			return agent.RunResult{Phase: req.Phase, Status: state.StatusFailed, Disposition: agent.DispositionFailed},
				&agent.SemanticFailureError{Phase: req.Phase, Disposition: agent.DispositionFailed}
		}
	}
	return agent.RunResult{Phase: req.Phase, Status: state.StatusFinished, Disposition: agent.DispositionPassed}, nil
}

type pauseRecordingState struct {
	fakeState
	closedStatus     state.LifecycleStatus
	pausedReason     string
	pausedNextAction string
}

func (s *pauseRecordingState) BeginRun(context.Context, string, string, string) error { return nil }
func (s *pauseRecordingState) RequestStop(context.Context, string, string) error      { return nil }
func (s *pauseRecordingState) StopRequested(context.Context, string, string) (bool, error) {
	return false, nil
}

func (s *pauseRecordingState) CloseRun(_ context.Context, _ string, target state.LifecycleStatus) error {
	s.closedStatus = target
	return nil
}

func (s *pauseRecordingState) CloseRunPaused(_ context.Context, _ string, reason, nextAction string) error {
	s.closedStatus = state.StatusStopped
	s.pausedReason = reason
	s.pausedNextAction = nextAction
	return nil
}

func TestSimplePhaseSemanticFailureIsRetriedUntilItPasses(t *testing.T) {
	runner := &semanticFailRunner{failures: 2}
	store := &fakeState{}
	events := &fakeEvents{}
	_, err := orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(store), orchestrator.WithEventSink(events), orchestrator.WithPromptBuilder(fakePrompt{})).Execute(context.Background(), request(t, resolvedPipeline(t, config.PhaseQA)))
	if err != nil {
		t.Fatalf("Execute() error = %v, want the third acceptance attempt to succeed", err)
	}
	invocations := 0
	for _, phase := range runner.phases {
		if phase == pipeline.PhaseAcceptanceCriteria {
			invocations++
		}
	}
	if invocations != 3 {
		t.Fatalf("acceptance criteria ran %d times, want 3 (original + 2 retries)", invocations)
	}
	retried := 0
	for _, event := range events.types {
		if event == orchestrator.EventPhaseRetried {
			retried++
		}
	}
	if retried != 2 {
		t.Fatalf("phase_retried events = %d, want 2", retried)
	}
}

func TestSimplePhaseSemanticExhaustionParksTheRun(t *testing.T) {
	runner := &semanticFailRunner{failures: -1}
	store := &pauseRecordingState{}
	events := &fakeEvents{}
	_, err := orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(store), orchestrator.WithEventSink(events), orchestrator.WithPromptBuilder(fakePrompt{})).Execute(context.Background(), request(t, resolvedPipeline(t, config.PhaseQA)))
	if err == nil || !strings.Contains(err.Error(), "semantic disposition") {
		t.Fatalf("Execute() error = %v, want the exhausted semantic failure", err)
	}
	invocations := 0
	for _, phase := range runner.phases {
		if phase == pipeline.PhaseAcceptanceCriteria {
			invocations++
		}
	}
	if invocations != 3 {
		t.Fatalf("acceptance criteria ran %d times, want exactly 3 attempts", invocations)
	}
	if store.closedStatus != state.StatusStopped {
		t.Fatalf("closed status = %s, want stopped (parked)", store.closedStatus)
	}
	if !strings.Contains(store.pausedReason, "kept reporting failure after 3 attempts") {
		t.Fatalf("pause reason = %q", store.pausedReason)
	}
	if store.pausedNextAction == "" {
		t.Fatal("parked run has no next action")
	}
}

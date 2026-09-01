package orchestrator_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/state"
	"github.com/VedranJanjetovic/gg/internal/verification"
)

// bootstrapLoopState adds the verification seams to the plan-loop fake so the
// Development loop can run the deferred preflight for real.
type bootstrapLoopState struct {
	*planLoopState
	cursors []string
}

func (s *bootstrapLoopState) RecordVerificationBaselineReport(_ context.Context, _ string, results []state.VerificationCommandResult, findings []state.VerificationFinding) (state.ProjectState, error) {
	s.project.Verification.ParentBaselineCaptured = true
	s.project.Verification.ParentResults = results
	s.project.Verification.ParentBaseline = findings
	return s.project, nil
}

func (s *bootstrapLoopState) RecordVerificationResultReport(_ context.Context, _ string, results []state.VerificationCommandResult, findings, warnings []state.VerificationFinding, boundary string, _ int, nextAction string) (state.ProjectState, error) {
	s.cursors = append(s.cursors, boundary)
	s.project.Verification.CurrentResults = results
	s.project.Verification.CurrentFindings = findings
	s.project.Verification.Warnings = warnings
	s.project.Verification.NextAction = nextAction
	return s.project, nil
}

func (s *bootstrapLoopState) PromoteVerificationIdentity(context.Context, string, string) (state.ProjectState, error) {
	return s.project, nil
}

func (s *bootstrapLoopState) RecordVerificationBootstrapPhase(_ context.Context, _ string, phase string) (state.ProjectState, error) {
	s.project.Verification.BootstrapPhase = phase
	return s.project, nil
}

func (s *bootstrapLoopState) CompleteVerificationBootstrap(context.Context, string) (state.ProjectState, error) {
	s.project.Verification.BaselineAfterPhase = s.project.Verification.BootstrapPhase
	s.project.Verification.BootstrapRequested = false
	return s.project, nil
}

// repairedVerifier reports the planned check as unavailable until the repair
// phase has run, then reports it green.
type repairedVerifier struct {
	repaired func() bool
	calls    int
}

func (v *repairedVerifier) Verify(context.Context, string, []verification.Step) (verification.Report, error) {
	v.calls++
	status := verification.CommandUnavailable
	if v.repaired() {
		status = verification.CommandPassed
	}
	return verification.Report{Results: []verification.CommandResult{{StepName: "unit", Command: "go", Args: []string{"test"}, Status: status}}}, nil
}

func TestTheBootstrapPhaseRunsWithoutABoundaryComparisonAndUnblocksTheBaseline(t *testing.T) {
	req, planStore := planLoopRequest(t)
	planStore.project.Verification = &state.VerificationState{
		PlannedSteps:       []state.VerificationStep{{Name: "unit", Command: "go", Args: []string{"test"}}},
		BootstrapRequested: true,
		CurrentResults:     []state.VerificationCommandResult{{CheckName: "unit", Status: "unavailable", UnavailableErr: "go: command not found"}},
	}
	store := &bootstrapLoopState{planLoopState: planStore}
	verifier := &repairedVerifier{repaired: func() bool {
		for _, done := range store.project.Plan.Completed {
			if done == "P1" {
				return true
			}
		}
		return false
	}}
	if _, err := orchestrator.NewController(
		orchestrator.WithRunner(&fakeSeqRunner{}),
		orchestrator.WithPhaseState(store),
		orchestrator.WithPromptBuilder(&scopeCapturePrompt{}),
		orchestrator.WithVerificationService(verifier),
	).Execute(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	for _, cursor := range store.cursors {
		if cursor == "P1" {
			t.Fatalf("recorded boundary cursors = %v, want no comparison for the bootstrap phase", store.cursors)
		}
	}
	if !reflect.DeepEqual(store.completed, [][]string{{"P1"}, {"P2"}, {"P3"}}) {
		t.Fatalf("recorded completions = %v, want the bootstrap phase completed first", store.completed)
	}
	if got := store.project.Verification.BaselineAfterPhase; got != "P1" {
		t.Fatalf("baselineAfterPhase = %q, want the baseline attributed to the repair phase", got)
	}
	if !store.project.Verification.ParentBaselineCaptured {
		t.Fatal("the deferred parent baseline was never captured")
	}
	if store.project.Verification.BootstrapRequested {
		t.Fatal("the bootstrap request survived a successful repair")
	}
}

func TestAStillBrokenRepairParksTheRunAgainAfterTheBootstrapPhase(t *testing.T) {
	req, planStore := planLoopRequest(t)
	planStore.project.Verification = &state.VerificationState{
		PlannedSteps:       []state.VerificationStep{{Name: "unit", Command: "go", Args: []string{"test"}}},
		BootstrapRequested: true,
		CurrentResults:     []state.VerificationCommandResult{{CheckName: "unit", Status: "unavailable", UnavailableErr: "go: command not found"}},
	}
	store := &bootstrapLoopState{planLoopState: planStore}
	verifier := &repairedVerifier{repaired: func() bool { return false }}
	_, err := orchestrator.NewController(
		orchestrator.WithRunner(&fakeSeqRunner{}),
		orchestrator.WithPhaseState(store),
		orchestrator.WithPromptBuilder(&scopeCapturePrompt{}),
		orchestrator.WithVerificationService(verifier),
	).Execute(context.Background(), req)
	if err == nil {
		t.Fatal("a repair phase that did not make the check executable was accepted")
	}
	if !strings.Contains(err.Error(), "--skip-checks") {
		t.Fatalf("error = %v, want the park to restate the skip escape hatch", err)
	}
	if !reflect.DeepEqual(store.completed, [][]string{{"P1"}}) {
		t.Fatalf("recorded completions = %v, want only the bootstrap phase", store.completed)
	}
	if store.project.Verification.ParentBaselineCaptured {
		t.Fatal("a still-blocked preflight captured a baseline")
	}
}

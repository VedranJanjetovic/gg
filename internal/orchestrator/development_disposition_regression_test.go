package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
	"github.com/VedranJanjetovic/gg/internal/verification"
)

// dispositionState is the minimal verificationReportState fake verifyBoundary
// needs during the cross-check.
type dispositionState struct {
	PhaseState
	project state.ProjectState
}

func (s *dispositionState) RecordVerificationBaselineReport(_ context.Context, _ string, results []state.VerificationCommandResult, findings []state.VerificationFinding) (state.ProjectState, error) {
	verificationState := *s.project.Verification
	verificationState.ParentBaselineCaptured = true
	verificationState.ParentResults = results
	verificationState.ParentBaseline = findings
	s.project.Verification = &verificationState
	return s.project, nil
}

func (s *dispositionState) RecordVerificationResultReport(_ context.Context, _ string, results []state.VerificationCommandResult, findings, warnings []state.VerificationFinding, boundary string, attempts int, nextAction string) (state.ProjectState, error) {
	verificationState := *s.project.Verification
	verificationState.CurrentResults = results
	verificationState.CurrentFindings = findings
	verificationState.Warnings = warnings
	verificationState.BoundaryCursor = boundary
	verificationState.RemediationAttempts = attempts
	verificationState.NextAction = nextAction
	s.project.Verification = &verificationState
	return s.project, nil
}

func (s *dispositionState) PromoteVerificationIdentity(_ context.Context, _ string, identity string) (state.ProjectState, error) {
	verificationState := *s.project.Verification
	verificationState.PromotedRequiredGreen = append(verificationState.PromotedRequiredGreen, identity)
	s.project.Verification = &verificationState
	return s.project, nil
}

type fixedVerification struct {
	report verification.Report
	calls  int
}

func (v *fixedVerification) Verify(context.Context, string, []verification.Step) (verification.Report, error) {
	v.calls++
	return v.report, nil
}

func dispositionProject() state.ProjectState {
	return state.ProjectState{
		Slug: "demo",
		Verification: &state.VerificationState{
			PlannedSteps:           []state.VerificationStep{{Name: "tests", Command: "go", Args: []string{"test", "./..."}}},
			ParentBaselineCaptured: true,
			ParentResults:          []state.VerificationCommandResult{{CheckName: "tests", Status: "passed"}},
		},
	}
}

func developmentFailed() error {
	return &agent.SemanticFailureError{Phase: pipeline.PhaseDevelopment, Disposition: agent.DispositionFailed}
}

// The internal_rpc_v2_api_migration regression: the development agent stamped
// gg_disposition: failed over baseline/environment findings although every
// check was green. A green verification boundary must override the verdict so
// the run continues instead of dying on a misused disposition.
func TestGreenBoundaryOverridesFailedDevelopmentVerdict(t *testing.T) {
	store := &dispositionState{project: dispositionProject()}
	verifier := &fixedVerification{report: verification.Report{Results: []verification.CommandResult{{StepName: "tests", Status: verification.CommandPassed}}}}
	controller := NewController(WithPhaseState(store), WithVerificationService(verifier)).(*sequentialController)
	request := &Request{Project: store.project}

	runs := 0
	var artifacts []string
	err := controller.runDevelopmentResilient(context.Background(), request, "phase-7", &artifacts, func(iteration int, feedback []string) error {
		runs++
		return developmentFailed()
	})
	if err != nil {
		t.Fatalf("green boundary did not override the failed verdict: %v", err)
	}
	if runs != 1 {
		t.Fatalf("development ran %d times, want exactly the original attempt", runs)
	}
	if verifier.calls != 1 {
		t.Fatalf("boundary verified %d times, want 1 cross-check", verifier.calls)
	}
}

func TestContradictedFailedVerdictRetriesTwiceThenParks(t *testing.T) {
	store := &dispositionState{project: dispositionProject()}
	verifier := &fixedVerification{report: verification.Report{Results: []verification.CommandResult{{
		StepName: "tests",
		Status:   verification.CommandFailed,
		Failures: []verification.IndividualFailure{{Identity: "TestBroken", Reason: "assertion failed"}},
	}}}}
	controller := NewController(WithPhaseState(store), WithVerificationService(verifier)).(*sequentialController)
	request := &Request{Project: store.project}

	var iterations []int
	var retryFeedback [][]string
	artifacts := []string{}
	err := controller.runDevelopmentResilient(context.Background(), request, "phase-7", &artifacts, func(iteration int, feedback []string) error {
		iterations = append(iterations, iteration)
		retryFeedback = append(retryFeedback, feedback)
		artifacts = []string{".gg/development.md"}
		return developmentFailed()
	})

	var pause *pauseError
	if !errors.As(err, &pause) {
		t.Fatalf("exhausted development retries returned %v, want a pause", err)
	}
	if !strings.Contains(pause.reason, "after 2 retries") {
		t.Fatalf("pause reason = %q", pause.reason)
	}
	if len(iterations) != 3 || iterations[0] != 0 || iterations[1] != 1 || iterations[2] != 2 {
		t.Fatalf("development iterations = %v, want the original attempt plus 2 retries", iterations)
	}
	if len(retryFeedback[1]) != 1 || retryFeedback[1][0] != ".gg/development.md" {
		t.Fatalf("first retry feedback = %v, want the failed development artifact", retryFeedback[1])
	}
}

func TestBlockedDevelopmentVerdictParksImmediately(t *testing.T) {
	store := &dispositionState{project: dispositionProject()}
	controller := NewController(WithPhaseState(store)).(*sequentialController)
	request := &Request{Project: store.project}

	runs := 0
	var artifacts []string
	err := controller.runDevelopmentResilient(context.Background(), request, "phase-7", &artifacts, func(int, []string) error {
		runs++
		return &agent.SemanticFailureError{Phase: pipeline.PhaseDevelopment, Disposition: agent.DispositionBlocked}
	})

	var pause *pauseError
	if !errors.As(err, &pause) || !strings.Contains(pause.reason, "blocked") {
		t.Fatalf("blocked verdict returned %v, want an immediate pause", err)
	}
	if runs != 1 {
		t.Fatalf("blocked development ran %d times, want no retries", runs)
	}
}

func TestOperationalDevelopmentFailureIsNotSecondGuessed(t *testing.T) {
	store := &dispositionState{project: dispositionProject()}
	verifier := &fixedVerification{report: verification.Report{Results: []verification.CommandResult{{StepName: "tests", Status: verification.CommandPassed}}}}
	controller := NewController(WithPhaseState(store), WithVerificationService(verifier)).(*sequentialController)
	request := &Request{Project: store.project}

	crash := errors.New("agent stderr: segmentation fault")
	var artifacts []string
	err := controller.runDevelopmentResilient(context.Background(), request, "phase-7", &artifacts, func(int, []string) error {
		return crash
	})
	if !errors.Is(err, crash) {
		t.Fatalf("operational failure returned %v, want the original crash", err)
	}
	if verifier.calls != 0 {
		t.Fatalf("boundary verified %d times for an operational failure, want 0", verifier.calls)
	}
}

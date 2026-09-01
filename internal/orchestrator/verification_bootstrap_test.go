package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/state"
	"github.com/VedranJanjetovic/gg/internal/verification"
)

// bootstrapState is a hand-rolled PhaseState carrying just enough durable
// behaviour for the deferral decision: a loadable project, plan completion,
// and the two bootstrap writes.
type bootstrapState struct {
	project          state.ProjectState
	baselineCaptured bool
	completedCalls   int
	recordedPhases   []string
}

func (s *bootstrapState) RecordPhase(context.Context, string, string, string, state.LifecycleStatus, *state.ExecutionOutcome, []string) (state.ProjectState, error) {
	return s.project, nil
}

func (s *bootstrapState) Load(context.Context, string) (state.ProjectState, error) {
	return s.project, nil
}

func (s *bootstrapState) RecordPlan(_ context.Context, _ string, _ []string, completed []string) (state.ProjectState, error) {
	if s.project.Plan != nil {
		s.project.Plan.Completed = append(append([]string(nil), s.project.Plan.Completed...), completed...)
	}
	return s.project, nil
}

func (s *bootstrapState) RecordVerificationBaselineReport(context.Context, string, []state.VerificationCommandResult, []state.VerificationFinding) (state.ProjectState, error) {
	s.baselineCaptured = true
	s.project.Verification.ParentBaselineCaptured = true
	return s.project, nil
}

func (s *bootstrapState) RecordVerificationResultReport(_ context.Context, _ string, results []state.VerificationCommandResult, _, _ []state.VerificationFinding, _ string, _ int, _ string) (state.ProjectState, error) {
	s.project.Verification.CurrentResults = results
	return s.project, nil
}

func (s *bootstrapState) PromoteVerificationIdentity(context.Context, string, string) (state.ProjectState, error) {
	return s.project, nil
}

func (s *bootstrapState) RecordVerificationBootstrapPhase(_ context.Context, _ string, phase string) (state.ProjectState, error) {
	s.recordedPhases = append(s.recordedPhases, phase)
	s.project.Verification.BootstrapPhase = phase
	return s.project, nil
}

func (s *bootstrapState) CompleteVerificationBootstrap(context.Context, string) (state.ProjectState, error) {
	s.completedCalls++
	s.project.Verification.BaselineAfterPhase = s.project.Verification.BootstrapPhase
	s.project.Verification.BootstrapRequested = false
	return s.project, nil
}

// blockedVerifier always reports the single planned check as unavailable,
// which is exactly the condition that parks the parent preflight.
type blockedVerifier struct{ calls int }

func (v *blockedVerifier) Verify(context.Context, string, []verification.Step) (verification.Report, error) {
	v.calls++
	return verification.Report{Results: []verification.CommandResult{{StepName: "unit", Status: verification.CommandUnavailable, UnavailableErr: "go: command not found"}}}, nil
}

func bootstrapProject(bootstrapRequested bool, bootstrapPhase string, completed []string) state.ProjectState {
	return state.ProjectState{
		Slug: "demo",
		Plan: &state.PlanState{Phases: []string{"Make checks executable", "Implement the feature"}, Completed: completed},
		Verification: &state.VerificationState{
			PlannedSteps:       []state.VerificationStep{{Name: "unit", Command: "go", Args: []string{"test"}}},
			BootstrapRequested: bootstrapRequested,
			BootstrapPhase:     bootstrapPhase,
			CurrentResults:     []state.VerificationCommandResult{{CheckName: "unit", Status: "unavailable", UnavailableErr: "go: command not found"}},
		},
	}
}

func TestTheParentBaselineIsDeferredWhileTheBootstrapPhaseIsStillPending(t *testing.T) {
	phaseState := &bootstrapState{project: bootstrapProject(true, "", nil)}
	verifier := &blockedVerifier{}
	controller := &sequentialController{state: phaseState, verification: verifier}
	request := &Request{Project: phaseState.project}
	if err := controller.ensureVerificationBaseline(context.Background(), request); err != nil {
		t.Fatalf("deferred preflight returned an error: %v", err)
	}
	if verifier.calls != 0 {
		t.Fatalf("verifier ran %d time(s), want the preflight deferred entirely", verifier.calls)
	}
	if got, want := request.Project.Verification.BootstrapPhase, "Make checks executable"; got != want {
		t.Fatalf("bootstrapPhase = %q, want the first pending plan phase %q", got, want)
	}
	if len(phaseState.recordedPhases) != 1 {
		t.Fatalf("recorded bootstrap phases = %#v, want exactly one", phaseState.recordedPhases)
	}
}

func TestTheParentBaselineIsNotDeferredOnceTheBootstrapPhaseIsComplete(t *testing.T) {
	phaseState := &bootstrapState{project: bootstrapProject(false, "Make checks executable", []string{"Make checks executable"})}
	verifier := &blockedVerifier{}
	controller := &sequentialController{state: phaseState, verification: verifier}
	request := &Request{Project: phaseState.project}
	err := controller.ensureVerificationBaseline(context.Background(), request)
	if err == nil {
		t.Fatal("a still-broken repair was accepted; the run must park again")
	}
	if !isVerificationPause(err) {
		t.Fatalf("error = %v, want a verification pause offering skip and fix again", err)
	}
	if verifier.calls != 1 {
		t.Fatalf("verifier ran %d time(s), want the real preflight to run exactly once", verifier.calls)
	}
}

func TestTheParentBaselineIsNeverDeferredForAProjectThatDidNotRequestABootstrap(t *testing.T) {
	phaseState := &bootstrapState{project: bootstrapProject(false, "", nil)}
	verifier := &blockedVerifier{}
	controller := &sequentialController{state: phaseState, verification: verifier}
	request := &Request{Project: phaseState.project}
	if err := controller.ensureVerificationBaseline(context.Background(), request); err == nil {
		t.Fatal("the ordinary preflight did not park on an unavailable check")
	}
	if verifier.calls != 1 {
		t.Fatalf("verifier ran %d time(s), want the ordinary preflight untouched", verifier.calls)
	}
	if len(phaseState.recordedPhases) != 0 {
		t.Fatalf("recorded bootstrap phases = %#v, want none", phaseState.recordedPhases)
	}
}

func TestABootstrapRequestWithNoPendingPlanPhaseFallsBackToTheOrdinaryPreflight(t *testing.T) {
	project := bootstrapProject(true, "", []string{"Make checks executable", "Implement the feature"})
	phaseState := &bootstrapState{project: project}
	verifier := &blockedVerifier{}
	controller := &sequentialController{state: phaseState, verification: verifier}
	request := &Request{Project: project}
	if err := controller.ensureVerificationBaseline(context.Background(), request); err == nil {
		t.Fatal("the fallback preflight did not park on an unavailable check")
	}
	if verifier.calls != 1 {
		t.Fatalf("verifier ran %d time(s), want the ordinary preflight to decide", verifier.calls)
	}
}

func TestCompletingTheBootstrapMarksThePlanPhaseCompleteAndClearsTheRequest(t *testing.T) {
	project := bootstrapProject(true, "Make checks executable", nil)
	phaseState := &bootstrapState{project: project}
	controller := &sequentialController{state: phaseState}
	request := &Request{Project: project}
	if err := controller.completeVerificationBootstrap(context.Background(), request, "Make checks executable"); err != nil {
		t.Fatal(err)
	}
	if phaseState.completedCalls != 1 {
		t.Fatalf("CompleteVerificationBootstrap called %d time(s), want 1", phaseState.completedCalls)
	}
	if got := phaseState.project.Plan.Completed; len(got) != 1 || got[0] != "Make checks executable" {
		t.Fatalf("plan completed = %#v, want the bootstrap phase recorded first", got)
	}
	if request.Project.Verification.BootstrapRequested {
		t.Fatal("bootstrapRequested is still set after completion")
	}
	if got, want := request.Project.Verification.BaselineAfterPhase, "Make checks executable"; got != want {
		t.Fatalf("baselineAfterPhase = %q, want %q", got, want)
	}
}

func TestCompletingTheBootstrapFailsWhenThePhaseStateCannotPersistIt(t *testing.T) {
	project := bootstrapProject(true, "Make checks executable", nil)
	controller := &sequentialController{state: reportOnlyState{project: project}}
	request := &Request{Project: project}
	err := controller.completeVerificationBootstrap(context.Background(), request, "Make checks executable")
	if err == nil {
		t.Fatal("a phase state without bootstrap persistence was accepted")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want a persistence capability failure", err)
	}
}

// reportOnlyState implements the ordinary verification seams but not the
// bootstrap extension, which is the shape older integrations have.
type reportOnlyState struct{ project state.ProjectState }

func (s reportOnlyState) RecordPhase(context.Context, string, string, string, state.LifecycleStatus, *state.ExecutionOutcome, []string) (state.ProjectState, error) {
	return s.project, nil
}

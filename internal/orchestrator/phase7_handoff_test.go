package orchestrator

import (
	"reflect"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/proof"
	"github.com/VedranJanjetovic/gg/internal/state"
)

func TestPhase7HandoffUsesLatestQASkipOnly(t *testing.T) {
	now := time.Date(2026, time.August, 24, 12, 0, 0, 0, time.UTC)
	project := state.ProjectState{PhaseHistory: []state.PhaseRecord{
		{Phase: string(pipeline.PhaseQA), Status: state.StatusFailed, CompletedAt: &now, Skip: &state.SkipResolution{}},
		{Phase: string(pipeline.PhaseQA), Status: state.StatusFinished, CompletedAt: &now},
	}}
	if qaWasExplicitlySkipped(project) {
		t.Fatal("an earlier QA waiver leaked past a later successful QA execution")
	}
	project.PhaseHistory = append(project.PhaseHistory, state.PhaseRecord{
		Phase: string(pipeline.PhaseQA), Status: state.StatusFailed, CompletedAt: &now,
		Skip: &state.SkipResolution{ExternalIdentity: "https://github.com/o/r/pull/1"},
	})
	if !qaWasExplicitlySkipped(project) {
		t.Fatal("latest confirmed QA skip did not waive proof")
	}
}

func TestPhase7HandoffDisclosesSkippedPrePRExecutionsInHistoryOrder(t *testing.T) {
	now := time.Date(2026, time.August, 24, 12, 0, 0, 0, time.UTC)
	project := state.ProjectState{PhaseHistory: []state.PhaseRecord{
		{Phase: string(pipeline.PhaseDevelopment), Subphase: string(pipeline.DevelopmentSubphaseTesting), Status: state.StatusFailed, CompletedAt: &now, Outcome: &state.ExecutionOutcome{Error: "test failed"}, Skip: &state.SkipResolution{}},
		{Phase: string(pipeline.PhaseQA), Status: state.StatusFailed, CompletedAt: &now, Outcome: &state.ExecutionOutcome{Error: "proof failed"}, Skip: &state.SkipResolution{}},
		{Phase: string(pipeline.PhasePR), Status: state.StatusFailed, CompletedAt: &now, Outcome: &state.ExecutionOutcome{Error: "push failed"}, Skip: &state.SkipResolution{ExternalIdentity: "pr-1"}},
		{Phase: string(pipeline.PhaseBuildChecker), Status: state.StatusFinished, CompletedAt: &now},
	}}
	got := skippedExecutionHandoff(project)
	if len(got) != 2 || got[0].Phase != string(pipeline.PhaseDevelopment) || got[1].Phase != string(pipeline.PhaseQA) {
		t.Fatalf("handoff = %#v, want Development Testing then QA", got)
	}
	if got[0].Failure != "test failed" || got[1].Failure != "proof failed" {
		t.Fatalf("handoff lost original failure evidence: %#v", got)
	}
}

func TestExecutionOutcomeRetainsExternalIdentity(t *testing.T) {
	got := executionOutcome(agent.RunResult{ExternalIdentity: "https://github.com/o/r/pull/1", Error: "CI failed"}, "")
	if got == nil || got.ExternalIdentity != "https://github.com/o/r/pull/1" {
		t.Fatalf("execution outcome = %#v, external identity was not retained", got)
	}
}

func TestPhase7HandoffDisclosesEveryEligiblePrePRExecution(t *testing.T) {
	now := time.Date(2026, time.August, 24, 12, 0, 0, 0, time.UTC)
	eligible := []struct {
		phase, subphase, id, failure, external string
	}{
		{string(pipeline.PhaseDevelopment), string(pipeline.DevelopmentSubphaseTesting), "testing-1", "focused test failed", ""},
		{string(pipeline.PhaseRebase), "", "rebase-1", "conflict remained", ""},
		{string(pipeline.PhaseQA), "", "qa-1", "proof failed", ""},
		{string(pipeline.PhaseTestDocument), "", "test-document-1", "documentation check failed", ""},
		{string(pipeline.PhaseBuildChecker), "", "build-checker-1", "lint failed", "build-run-1"},
	}
	project := state.ProjectState{}
	for _, item := range eligible {
		project.PhaseHistory = append(project.PhaseHistory, state.PhaseRecord{
			Phase: item.phase, Subphase: item.subphase, OccurrenceID: item.id,
			Status: state.StatusFailed, CompletedAt: &now,
			Outcome: &state.ExecutionOutcome{Error: item.failure, ExternalIdentity: item.external},
			Skip:    &state.SkipResolution{},
		})
	}
	project.PhaseHistory = append(project.PhaseHistory,
		state.PhaseRecord{Phase: string(pipeline.PhaseDevelopment), Subphase: string(pipeline.DevelopmentSubphaseImplementation), Status: state.StatusFailed, Skip: &state.SkipResolution{}},
		state.PhaseRecord{Phase: string(pipeline.PhasePR), Status: state.StatusFailed, Skip: &state.SkipResolution{}},
		state.PhaseRecord{Phase: string(pipeline.PhaseCI), Status: state.StatusFailed, Skip: &state.SkipResolution{}},
	)

	got := skippedExecutionHandoff(project)
	if len(got) != len(eligible) {
		t.Fatalf("handoff length = %d, want %d: %#v", len(got), len(eligible), got)
	}
	for index, want := range eligible {
		if got[index].Phase != want.phase || got[index].Subphase != want.subphase || got[index].OccurrenceID != want.id || got[index].Failure != want.failure || got[index].ExternalIdentity != want.external {
			t.Errorf("handoff[%d] = %#v, want %#v", index, got[index], want)
		}
	}
}

func TestPhase7HandoffUsesValidatedDeferredChecksAndCopiesProjectData(t *testing.T) {
	valid := proof.DeferredCheck{
		TestLocation: "internal/aws_test.go", CheckName: "TestRemoteFlow",
		FlowScenario: "call deployed API", ExpectedBehavior: "request persists",
		RemoteOnlyReason: "requires AWS credentials", RepositoryEvidence: "config/aws.go uses AWS_ENDPOINT",
		RunInstructions: "run in CI with AWS secrets",
	}
	invalid := proof.DeferredCheck{CheckName: "missing evidence"}
	project := state.ProjectState{DeferredChecks: []proof.DeferredCheck{valid}, PhaseHistory: []state.PhaseRecord{
		{DeferredChecks: []proof.DeferredCheck{invalid, valid}},
	}}

	got := deferredChecksHandoff(project)
	if !reflect.DeepEqual(got, []proof.DeferredCheck{valid}) {
		t.Fatalf("deferred handoff = %#v, want only the valid project-level check", got)
	}
	got[0].CheckName = "mutated"
	if project.DeferredChecks[0].CheckName != "TestRemoteFlow" {
		t.Fatal("deferred handoff exposed mutable project state")
	}

	project.DeferredChecks = nil
	project.PhaseHistory[0].DeferredChecks = []proof.DeferredCheck{valid, valid}
	got = deferredChecksHandoff(project)
	if !reflect.DeepEqual(got, []proof.DeferredCheck{valid}) {
		t.Fatalf("history deferred handoff = %#v, want one deduplicated valid check", got)
	}
}

func TestSkippedTestingEvidenceRequiresLatestTestingOccurrenceToBeSkipped(t *testing.T) {
	now := time.Date(2026, time.August, 24, 12, 0, 0, 0, time.UTC)
	project := state.ProjectState{PhaseHistory: []state.PhaseRecord{
		{Phase: string(pipeline.PhaseDevelopment), Subphase: string(pipeline.DevelopmentSubphaseTesting), Status: state.StatusFailed, CompletedAt: &now, Outcome: &state.ExecutionOutcome{Error: "old failure"}, Skip: &state.SkipResolution{}},
		{Phase: string(pipeline.PhaseDevelopment), Subphase: string(pipeline.DevelopmentSubphaseTesting), Status: state.StatusFinished, CompletedAt: &now},
	}}
	if got := skippedTestingEvidence(project); got != nil {
		t.Fatalf("evidence = %#v, want nil after a later successful Testing occurrence", got)
	}

	project.PhaseHistory[1] = state.PhaseRecord{
		Phase: string(pipeline.PhaseDevelopment), Subphase: string(pipeline.DevelopmentSubphaseTesting), Status: state.StatusFailed,
		CompletedAt: &now, Outcome: &state.ExecutionOutcome{Error: "new failure"}, Skip: &state.SkipResolution{},
	}
	got := skippedTestingEvidence(project)
	if got == nil || got.Outcome == nil || got.Outcome.Error != "new failure" {
		t.Fatalf("evidence = %#v, want latest skipped failure", got)
	}
}

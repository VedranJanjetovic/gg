package orchestrator

import (
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
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

package orchestrator

import (
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

func TestSkipPolicyMatrix(t *testing.T) {
	cases := []struct {
		name   string
		phase  pipeline.PhaseID
		sub    string
		allow  bool
		policy SkipCleanupPolicy
	}{
		{name: "acceptance criteria", phase: pipeline.PhaseAcceptanceCriteria},
		{name: "grooming", phase: pipeline.PhaseGrooming},
		{name: "planning", phase: pipeline.PhasePlanning},
		{name: "development implementation", phase: pipeline.PhaseDevelopment, sub: "implementation"},
		{name: "development testing", phase: pipeline.PhaseDevelopment, sub: "testing", allow: true, policy: SkipCleanupPreserveWorktree},
		{name: "development review", phase: pipeline.PhaseDevelopment, sub: "review"},
		{name: "rebase", phase: pipeline.PhaseRebase, allow: true, policy: SkipCleanupRestoreWorktree},
		{name: "qa", phase: pipeline.PhaseQA, allow: true, policy: SkipCleanupReadOnly},
		{name: "test document", phase: pipeline.PhaseTestDocument, allow: true, policy: SkipCleanupRestoreWorktree},
		{name: "build checker", phase: pipeline.PhaseBuildChecker, allow: true, policy: SkipCleanupReadOnly},
		{name: "pull request", phase: pipeline.PhasePR, allow: true, policy: SkipCleanupRetainExternal},
		{name: "ci", phase: pipeline.PhaseCI, allow: true, policy: SkipCleanupRetainExternal},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			policy, allowed := SkipCleanupFor(test.phase, test.sub)
			if allowed != test.allow || policy != test.policy {
				t.Fatalf("SkipCleanupFor(%s, %q) = (%q, %t), want (%q, %t)", test.phase, test.sub, policy, allowed, test.policy, test.allow)
			}
		})
	}
}

func TestValidateSkipTargetRequiresDurableCurrentFailure(t *testing.T) {
	finished := time.Date(2026, time.August, 1, 10, 0, 0, 0, time.UTC)
	project := state.ProjectState{
		Status: state.StatusFailed,
		PhaseHistory: []state.PhaseRecord{{
			Phase:        string(pipeline.PhaseQA),
			Status:       state.StatusFailed,
			StartedAt:    finished.Add(-time.Minute),
			CompletedAt:  &finished,
			OccurrenceID: "qa-occurrence",
		}},
	}
	if err := ValidateSkipTarget(project, pipeline.PhaseQA, "", "qa-occurrence"); err != nil {
		t.Fatalf("valid target rejected: %v", err)
	}
	for _, status := range []state.LifecycleStatus{state.StatusPending, state.StatusRunning, state.StatusStopped} {
		project.Status = status
		if err := ValidateSkipTarget(project, pipeline.PhaseQA, "", "qa-occurrence"); err == nil {
			t.Fatalf("status %s was accepted", status)
		}
	}
	project.Status = state.StatusFailed
	if err := ValidateSkipTarget(project, pipeline.PhaseDevelopment, "implementation", "qa-occurrence"); err == nil {
		t.Fatal("ineligible Development implementation was accepted")
	}
	if err := ValidateSkipTarget(project, pipeline.PhaseQA, "", "stale-occurrence"); err == nil {
		t.Fatal("stale occurrence was accepted")
	}
}

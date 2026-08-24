package orchestrator

import (
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

func skipTestPipeline(t *testing.T, enabled ...config.Phase) pipeline.ExecutablePipeline {
	t.Helper()
	selected := make(map[config.Phase]bool, len(enabled))
	for _, phase := range enabled {
		selected[phase] = true
	}
	phases := make(map[config.Phase]config.ResolvedPhase)
	for _, phase := range []config.Phase{config.PhaseGrooming, config.PhasePlanning, config.PhaseQA, config.PhaseBuildChecker, config.PhasePR, config.PhaseCI} {
		phases[phase] = config.ResolvedPhase{Enabled: selected[phase], AgentSettings: config.AgentSettings{Agent: config.AgentClaude}}
	}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), config.ResolvedConfig{Phases: phases})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

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

func TestResumeCursorContinuesAfterOnlyTheSkippedOccurrence(t *testing.T) {
	completedAt := time.Date(2026, time.August, 1, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		failedPhase  string
		failedSub    string
		currentPhase string
		currentSub   string
		nextPhase    string
		nextSubphase string
		wantPhase    string
		wantSubphase string
		wantFinalize bool
	}{
		{
			name:        "development testing enters review",
			failedPhase: string(pipeline.PhaseDevelopment), failedSub: string(pipeline.DevelopmentSubphaseTesting),
			currentPhase: string(pipeline.PhaseDevelopment), currentSub: string(pipeline.DevelopmentSubphaseReview),
			nextPhase: string(pipeline.PhaseDevelopment), nextSubphase: string(pipeline.DevelopmentSubphaseReview),
			wantPhase: string(pipeline.PhaseDevelopment), wantSubphase: string(pipeline.DevelopmentSubphaseReview),
		},
		{
			name:         "top level failure enters next phase",
			failedPhase:  string(pipeline.PhaseQA),
			currentPhase: string(pipeline.PhaseTestDocument),
			nextPhase:    string(pipeline.PhaseTestDocument),
			wantPhase:    string(pipeline.PhaseTestDocument),
		},
		{
			name:         "final failure only finalizes",
			failedPhase:  string(pipeline.PhaseTestDocument),
			currentPhase: string(pipeline.PhaseTestDocument),
			wantFinalize: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := skipTestPipeline(t, config.PhaseQA)
			project := state.ProjectState{
				Status:          state.StatusFailed,
				CurrentPhase:    test.currentPhase,
				CurrentSubphase: test.currentSub,
				PhaseHistory: []state.PhaseRecord{{
					Phase:        test.failedPhase,
					Subphase:     test.failedSub,
					Status:       state.StatusFailed,
					StartedAt:    completedAt.Add(-time.Minute),
					CompletedAt:  &completedAt,
					OccurrenceID: "qa-occurrence",
					Skip: &state.SkipResolution{
						ConfirmedAt: completedAt,
						Cleanup:     state.SkipCleanup{Status: state.SkipCleanupSucceeded},
						NextPhase:   test.nextPhase, NextSubphase: test.nextSubphase,
					},
				}},
			}
			phase, subphase, finalize, err := resumeExecutionCursor(project, plan, pipeline.DevelopmentSubphaseGeneration{}, false)
			if err != nil {
				t.Fatal(err)
			}
			if phase != test.wantPhase || subphase != test.wantSubphase || finalize != test.wantFinalize {
				t.Fatalf("cursor = %q/%q finalize=%t, want %q/%q finalize=%t", phase, subphase, finalize, test.wantPhase, test.wantSubphase, test.wantFinalize)
			}
		})
	}
}

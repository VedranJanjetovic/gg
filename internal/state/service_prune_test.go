package state_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/state"
)

func TestPruneProjectRemovesParkedProjectsAndProtectsRunning(t *testing.T) {
	cases := []struct {
		status      state.LifecycleStatus
		wantRemoved bool
	}{
		{state.StatusFailed, true},
		{state.StatusStopped, true},
		{state.StatusPending, true},
		{state.StatusFinished, true},
		{state.StatusRunning, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.status), func(t *testing.T) {
			store, err := state.NewFileStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			service := state.NewLifecycleService(store, nil, store.Locker())
			project, err := state.NewProjectState(state.ProjectState{
				Name: "Demo", Slug: "demo", OriginalGoal: "goal",
				AcceptanceCriteria: []string{"criterion"},
				WorktreePath:       "/tmp/demo", BranchName: "gg/demo",
				CurrentPhase: "pipeline", Status: tc.status,
				PipelineConfig:  state.PipelineConfigSnapshot{SchemaVersion: 1, Data: []byte(`{}`)},
				CreatedAt:       time.Now().UTC(),
				UpdatedAt:       time.Now().UTC(),
				StatusChangedAt: time.Now().UTC(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := service.Create(context.Background(), project); err != nil {
				t.Fatal(err)
			}
			cleaned := false
			if err := service.PruneProject(context.Background(), "demo", func(context.Context, state.ProjectState) error {
				cleaned = true
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			_, loadErr := store.Load(context.Background(), "demo")
			removed := errors.Is(loadErr, os.ErrNotExist)
			if removed != tc.wantRemoved || cleaned != tc.wantRemoved {
				t.Fatalf("status %s: removed=%v cleaned=%v, want %v", tc.status, removed, cleaned, tc.wantRemoved)
			}
		})
	}
}

func TestBeginFeedbackLoopReopensInterviewAndKeepsPlan(t *testing.T) {
	store, err := state.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project, err := state.NewProjectState(state.ProjectState{
		Name: "Demo", Slug: "demo", OriginalGoal: "goal",
		AcceptanceCriteria: []string{"criterion"},
		WorktreePath:       "/tmp/demo", BranchName: "gg/demo",
		CurrentPhase: "qa", Status: state.StatusStopped,
		Interview:      &state.InterviewState{Done: true, Clarifications: []state.InterviewQA{{Question: "old", Answer: "answer"}}},
		Plan:           &state.PlanState{Phases: []string{"P1", "P2"}, Completed: []string{"P1"}},
		PipelineConfig: state.PipelineConfigSnapshot{SchemaVersion: 1, Data: []byte(`{}`)},
		CreatedAt:      time.Now().UTC(), UpdatedAt: time.Now().UTC(), StatusChangedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	created, err := store.Load(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	history := len(created.PhaseHistory)

	updated, err := service.BeginFeedbackLoop(context.Background(), "demo", "make jumps higher")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Interview == nil || updated.Interview.Done {
		t.Fatalf("interview must re-open: %#v", updated.Interview)
	}
	last := updated.Interview.Clarifications[len(updated.Interview.Clarifications)-1]
	if !strings.Contains(last.Answer, "make jumps higher") {
		t.Fatalf("feedback missing from clarifications: %#v", updated.Interview.Clarifications)
	}
	if got := updated.AcceptanceCriteria[len(updated.AcceptanceCriteria)-1]; got != "User feedback — make jumps higher" {
		t.Fatalf("criteria = %q", got)
	}
	if updated.CurrentPhase != "pipeline" || updated.Status != state.StatusStopped {
		t.Fatalf("cursor = %s/%s, want full rerun from pipeline start", updated.CurrentPhase, updated.Status)
	}
	// No phantom stopped-phase record and the plan (with completed work) survives.
	if len(updated.PhaseHistory) != history {
		t.Fatalf("phase history grew: %#v", updated.PhaseHistory)
	}
	if updated.Plan == nil || len(updated.Plan.Completed) != 1 || updated.Plan.Completed[0] != "P1" {
		t.Fatalf("plan must be preserved: %#v", updated.Plan)
	}
}

package state

import (
	"context"
	"errors"
	"testing"
	"time"
)

func failedOccurrenceProject(t *testing.T) (*LifecycleService, ProjectState, PhaseRecord) {
	t.Helper()
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewLifecycleService(store, nil, store.Locker())
	project := validProjectState()
	if err := service.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Transition(context.Background(), project.Slug, StatusRunning, "development", "testing", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordPhase(context.Background(), project.Slug, "development", "testing", StatusRunning, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordPhase(context.Background(), project.Slug, "development", "testing", StatusFailed, &ExecutionOutcome{ExitCode: 1, Error: "test failed"}, []string{"failure.log"}); err != nil {
		t.Fatal(err)
	}
	if err := service.CloseRun(context.Background(), project.Slug, StatusFailed); err != nil {
		t.Fatal(err)
	}
	failed, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	return service, failed, failed.PhaseHistory[len(failed.PhaseHistory)-1]
}

func TestSkipFailedExecutionPersistsExactWaiverAndIsIdempotent(t *testing.T) {
	service, failed, occurrence := failedOccurrenceProject(t)
	confirmedAt := time.Date(2026, time.August, 1, 10, 0, 0, 0, time.UTC)
	cleanupCalls := 0
	cleanup := func(_ context.Context, project ProjectState, record PhaseRecord) (SkipCleanup, error) {
		cleanupCalls++
		if project.Slug != failed.Slug || record.Outcome == nil || record.Outcome.Error != "test failed" {
			t.Fatalf("cleanup received incomplete failure evidence: project=%#v record=%#v", project, record)
		}
		return SkipCleanup{Status: SkipCleanupSucceeded, Evidence: []string{"preserved failed testing worktree"}}, nil
	}
	request := SkipRequest{
		OccurrenceID: occurrence.OccurrenceID,
		ConfirmedAt:  confirmedAt,
		NextPhase:    "development",
		NextSubphase: "review",
	}
	got, err := service.SkipFailedExecution(context.Background(), failed.Slug, request, cleanup)
	if err != nil {
		t.Fatal(err)
	}
	if cleanupCalls != 1 {
		t.Fatalf("cleanup calls = %d, want 1", cleanupCalls)
	}
	last := got.PhaseHistory[len(got.PhaseHistory)-1]
	if got.Status != StatusFailed || got.CurrentPhase != "development" || got.CurrentSubphase != "review" {
		t.Fatalf("skipped project state = %#v", got)
	}
	if last.Status != StatusFailed || last.Outcome == nil || last.Outcome.Error != "test failed" {
		t.Fatalf("original failure evidence changed: %#v", last)
	}
	if last.Skip == nil || !last.Skip.ConfirmedAt.Equal(confirmedAt) || last.Skip.Cleanup.Status != SkipCleanupSucceeded {
		t.Fatalf("skip resolution = %#v", last.Skip)
	}

	duplicate, err := service.SkipFailedExecution(context.Background(), failed.Slug, request, func(context.Context, ProjectState, PhaseRecord) (SkipCleanup, error) {
		cleanupCalls++
		return SkipCleanup{Status: SkipCleanupSucceeded}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if cleanupCalls != 1 || duplicate.PhaseHistory[len(duplicate.PhaseHistory)-1].Skip.ConfirmedAt != confirmedAt {
		t.Fatalf("duplicate skip was not idempotent: calls=%d state=%#v", cleanupCalls, duplicate)
	}

	root := service.store.(*FileStore).Root()
	restartedStore, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewLifecycleService(restartedStore, nil, restartedStore.Locker()).Load(context.Background(), failed.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.SkipCount("development", "testing") != 1 {
		t.Fatalf("sticky skip count = %d, want 1", restarted.SkipCount("development", "testing"))
	}
	if _, err := service.Transition(context.Background(), failed.Slug, StatusRunning, "development", "review", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordPhase(context.Background(), failed.Slug, "development", "review", StatusRunning, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Transition(context.Background(), failed.Slug, StatusFinished, "development", "review", nil); err != nil {
		t.Fatal(err)
	}
	later, err := service.Load(context.Background(), failed.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if later.PhaseHistory[len(later.PhaseHistory)-1].OccurrenceID == occurrence.OccurrenceID || later.SkipCount("development", "testing") != 1 {
		t.Fatalf("later execution reused occurrence or erased sticky skip: %#v", later.PhaseHistory)
	}
}

func TestSkipFailedExecutionCleanupFailureLeavesFailureUnchanged(t *testing.T) {
	service, failed, occurrence := failedOccurrenceProject(t)
	_, err := service.SkipFailedExecution(context.Background(), failed.Slug, SkipRequest{
		OccurrenceID: occurrence.OccurrenceID,
		NextPhase:    "qa",
	}, func(context.Context, ProjectState, PhaseRecord) (SkipCleanup, error) {
		return SkipCleanup{Status: SkipCleanupSucceeded, Evidence: []string{"partial"}}, errors.New("restore checkpoint failed")
	})
	if !errors.Is(err, ErrSkipCleanup) {
		t.Fatalf("cleanup error = %v, want ErrSkipCleanup", err)
	}
	unchanged, err := service.Load(context.Background(), failed.Slug)
	if err != nil {
		t.Fatal(err)
	}
	last := unchanged.PhaseHistory[len(unchanged.PhaseHistory)-1]
	if last.Skip != nil || unchanged.CurrentPhase != failed.CurrentPhase || unchanged.Status != StatusFailed {
		t.Fatalf("cleanup failure advanced state: %#v", unchanged)
	}
}

func TestSkipFailedExecutionRejectsStaleAndNonFailedOccurrences(t *testing.T) {
	service, failed, occurrence := failedOccurrenceProject(t)
	if _, err := service.SkipFailedExecution(context.Background(), failed.Slug, SkipRequest{OccurrenceID: "missing", NextPhase: "qa"}, nil); !errors.Is(err, ErrStaleSkipOccurrence) {
		t.Fatalf("missing occurrence error = %v, want ErrStaleSkipOccurrence", err)
	}
	if _, err := service.SkipFailedExecution(context.Background(), failed.Slug, SkipRequest{OccurrenceID: occurrence.OccurrenceID, NextSubphase: "unexpected"}, nil); !errors.Is(err, ErrSkipNotEligible) {
		t.Fatalf("invalid cursor error = %v, want ErrSkipNotEligible", err)
	}

	if _, err := service.Transition(context.Background(), failed.Slug, StatusRunning, "qa", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordPhase(context.Background(), failed.Slug, "qa", "", StatusFinished, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := service.CloseRun(context.Background(), failed.Slug, StatusFailed); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SkipFailedExecution(context.Background(), failed.Slug, SkipRequest{OccurrenceID: occurrence.OccurrenceID, NextPhase: "qa"}, nil); !errors.Is(err, ErrStaleSkipOccurrence) {
		t.Fatalf("historical occurrence error = %v, want ErrStaleSkipOccurrence", err)
	}
}

func TestLegacyPhaseRecordWithoutOccurrenceAndSkipFieldsRemainsValid(t *testing.T) {
	project := validProjectState()
	project.PhaseHistory = []PhaseRecord{{Phase: "development", Subphase: "testing", Status: StatusFailed, StartedAt: project.CreatedAt}}
	if _, err := NewProjectState(project); err != nil {
		t.Fatalf("legacy phase record rejected: %v", err)
	}
}

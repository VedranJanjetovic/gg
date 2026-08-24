package state

import (
	"context"
	"encoding/json"
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
	changedRequest := request
	changedRequest.NextPhase = "qa"
	changed, err := service.SkipFailedExecution(context.Background(), failed.Slug, changedRequest, func(context.Context, ProjectState, PhaseRecord) (SkipCleanup, error) {
		t.Fatal("cleanup ran for an already-resolved occurrence")
		return SkipCleanup{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed.CurrentPhase != "development" || changed.CurrentSubphase != "review" {
		t.Fatalf("duplicate request changed the durable cursor: %#v", changed)
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
		NextPhase:    "development",
		NextSubphase: "review",
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

func TestSkipFailedExecutionPersistenceFailureLeavesFailureUnchanged(t *testing.T) {
	service, failed, occurrence := failedOccurrenceProject(t)
	store := service.store.(*FileStore)
	store.replace = func(context.Context, string, []byte) error {
		return errors.New("state write failed")
	}
	_, err := service.SkipFailedExecution(context.Background(), failed.Slug, SkipRequest{
		OccurrenceID: occurrence.OccurrenceID,
		NextPhase:    "development",
		NextSubphase: "review",
	}, func(context.Context, ProjectState, PhaseRecord) (SkipCleanup, error) {
		return SkipCleanup{Status: SkipCleanupSucceeded, Evidence: []string{"cleanup completed"}}, nil
	})
	if err == nil {
		t.Fatal("persistence failure unexpectedly succeeded")
	}
	unchanged, loadErr := service.Load(context.Background(), failed.Slug)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	last := unchanged.PhaseHistory[len(unchanged.PhaseHistory)-1]
	if last.Skip != nil || unchanged.Status != StatusFailed || unchanged.CurrentPhase != failed.CurrentPhase {
		t.Fatalf("persistence failure advanced state: %#v", unchanged)
	}
}

func TestSkipFailedExecutionRequiresFailedProject(t *testing.T) {
	for _, status := range []LifecycleStatus{StatusPending, StatusRunning, StatusStopped} {
		t.Run(string(status), func(t *testing.T) {
			service, failed, occurrence := failedOccurrenceProject(t)
			failed.Status = status
			if err := service.Save(context.Background(), failed); err != nil {
				t.Fatal(err)
			}
			if _, err := service.SkipFailedExecution(context.Background(), failed.Slug, SkipRequest{OccurrenceID: occurrence.OccurrenceID}, nil); !errors.Is(err, ErrSkipNotEligible) {
				t.Fatalf("status %s error = %v, want ErrSkipNotEligible", status, err)
			}
		})
	}
}

func TestSkipFailedExecutionRejectsInvalidCleanupResult(t *testing.T) {
	service, failed, occurrence := failedOccurrenceProject(t)
	_, err := service.SkipFailedExecution(context.Background(), failed.Slug, SkipRequest{
		OccurrenceID: occurrence.OccurrenceID,
		NextPhase:    "development",
		NextSubphase: "review",
	}, func(context.Context, ProjectState, PhaseRecord) (SkipCleanup, error) {
		return SkipCleanup{Status: "rolled_back"}, nil
	})
	if !errors.Is(err, ErrSkipCleanup) {
		t.Fatalf("invalid cleanup result = %v, want ErrSkipCleanup", err)
	}
	unchanged, err := service.Load(context.Background(), failed.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.PhaseHistory[len(unchanged.PhaseHistory)-1].Skip != nil || unchanged.CurrentPhase != failed.CurrentPhase {
		t.Fatalf("invalid cleanup result changed state: %#v", unchanged)
	}
}

func TestSkipFailedExecutionEnforcesPhaseEligibility(t *testing.T) {
	cases := []struct {
		name       string
		phase      string
		subphase   string
		wantAccept bool
		nextPhase  string
		nextSub    string
	}{
		{name: "acceptance criteria", phase: "acceptance_criteria"},
		{name: "grooming", phase: "grooming"},
		{name: "planning", phase: "planning"},
		{name: "development implementation", phase: "development", subphase: "implementation"},
		{name: "development testing", phase: "development", subphase: "testing", wantAccept: true, nextPhase: "development", nextSub: "review"},
		{name: "development review", phase: "development", subphase: "review"},
		{name: "rebase", phase: "rebase", wantAccept: true, nextPhase: "qa"},
		{name: "qa", phase: "qa", wantAccept: true, nextPhase: "test_document"},
		{name: "test document", phase: "test_document", wantAccept: true, nextPhase: "build_checker"},
		{name: "build checker", phase: "build_checker", wantAccept: true, nextPhase: "pr"},
		{name: "pr", phase: "pr", wantAccept: true, nextPhase: "ci"},
		{name: "ci", phase: "ci", wantAccept: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service, failed, occurrence := failedOccurrenceProject(t)
			failed.CurrentPhase, failed.CurrentSubphase = test.phase, test.subphase
			failed.PhaseHistory[len(failed.PhaseHistory)-1].Phase = test.phase
			failed.PhaseHistory[len(failed.PhaseHistory)-1].Subphase = test.subphase
			if err := service.Save(context.Background(), failed); err != nil {
				t.Fatal(err)
			}
			_, err := service.SkipFailedExecution(context.Background(), failed.Slug, SkipRequest{
				OccurrenceID: occurrence.OccurrenceID,
				NextPhase:    test.nextPhase,
				NextSubphase: test.nextSub,
			}, nil)
			if test.wantAccept && err != nil {
				t.Fatalf("eligible phase rejected: %v", err)
			}
			if !test.wantAccept && !errors.Is(err, ErrSkipNotEligible) {
				t.Fatalf("ineligible phase error = %v, want ErrSkipNotEligible", err)
			}
		})
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

func TestTransitionAssignsOccurrenceBeforeExecutionRecord(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewLifecycleService(store, nil, store.Locker())
	project := validProjectState()
	if err := service.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	running, err := service.Transition(context.Background(), project.Slug, StatusRunning, "qa", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(running.PhaseHistory) == 0 || running.PhaseHistory[len(running.PhaseHistory)-1].OccurrenceID == "" {
		t.Fatalf("running transition did not create an occurrence: %#v", running.PhaseHistory)
	}
	occurrenceID := running.PhaseHistory[len(running.PhaseHistory)-1].OccurrenceID
	completed, err := service.RecordPhase(context.Background(), project.Slug, "qa", "", StatusFailed, &ExecutionOutcome{ExitCode: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	last := completed.PhaseHistory[len(completed.PhaseHistory)-1]
	if last.OccurrenceID != occurrenceID {
		t.Fatalf("execution record replaced the start occurrence: got %q want %q", last.OccurrenceID, occurrenceID)
	}
}

func TestSkipFailedExecutionRejectsCanceledAndWhitespaceCursor(t *testing.T) {
	service, failed, occurrence := failedOccurrenceProject(t)
	failed.PhaseHistory[len(failed.PhaseHistory)-1].Outcome.Canceled = true
	if err := service.Save(context.Background(), failed); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SkipFailedExecution(context.Background(), failed.Slug, SkipRequest{
		OccurrenceID: occurrence.OccurrenceID,
		NextPhase:    "development",
		NextSubphase: "review",
	}, nil); !errors.Is(err, ErrSkipNotEligible) {
		t.Fatalf("canceled occurrence error = %v, want ErrSkipNotEligible", err)
	}

	service, failed, occurrence = failedOccurrenceProject(t)
	if _, err := service.SkipFailedExecution(context.Background(), failed.Slug, SkipRequest{
		OccurrenceID: occurrence.OccurrenceID,
		NextPhase:    "   ",
	}, nil); !errors.Is(err, ErrSkipNotEligible) {
		t.Fatalf("whitespace cursor error = %v, want ErrSkipNotEligible", err)
	}
}

func TestSkipFailedExecutionRejectsInvalidTestingCursor(t *testing.T) {
	service, failed, occurrence := failedOccurrenceProject(t)
	for _, request := range []SkipRequest{
		{OccurrenceID: occurrence.OccurrenceID, NextPhase: "qa"},
		{OccurrenceID: occurrence.OccurrenceID, NextPhase: "development", NextSubphase: "implementation"},
	} {
		if _, err := service.SkipFailedExecution(context.Background(), failed.Slug, request, nil); !errors.Is(err, ErrSkipNotEligible) {
			t.Fatalf("invalid Development Testing cursor request %#v returned %v", request, err)
		}
	}
}

func TestSkipFailedExecutionRejectsPreviouslySkippedOccurrenceAfterLaterFailure(t *testing.T) {
	service, failed, occurrence := failedOccurrenceProject(t)
	if _, err := service.SkipFailedExecution(context.Background(), failed.Slug, SkipRequest{
		OccurrenceID: occurrence.OccurrenceID,
		NextPhase:    "development", NextSubphase: "review",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Transition(context.Background(), failed.Slug, StatusRunning, "qa", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordPhase(context.Background(), failed.Slug, "qa", "", StatusFailed, &ExecutionOutcome{ExitCode: 2}, nil); err != nil {
		t.Fatal(err)
	}
	if err := service.CloseRun(context.Background(), failed.Slug, StatusFailed); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SkipFailedExecution(context.Background(), failed.Slug, SkipRequest{
		OccurrenceID: occurrence.OccurrenceID,
		NextPhase:    "development", NextSubphase: "review",
	}, nil); !errors.Is(err, ErrStaleSkipOccurrence) {
		t.Fatalf("previously skipped occurrence returned %v, want stale occurrence", err)
	}
}

func TestSkipFailedExecutionDoesNotRewriteFailureCompletionTime(t *testing.T) {
	service, failed, occurrence := failedOccurrenceProject(t)
	originalCompletedAt := *occurrence.CompletedAt
	if _, err := service.SkipFailedExecution(context.Background(), failed.Slug, SkipRequest{
		OccurrenceID: occurrence.OccurrenceID,
		NextPhase:    "development", NextSubphase: "review",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordPhase(context.Background(), failed.Slug, "development", "review", StatusRunning, nil, nil); err != nil {
		t.Fatal(err)
	}
	continued, err := service.Load(context.Background(), failed.Slug)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range continued.PhaseHistory {
		if record.OccurrenceID == occurrence.OccurrenceID {
			if record.CompletedAt == nil || !record.CompletedAt.Equal(originalCompletedAt) {
				t.Fatalf("skipped failure completion time changed: got %v, want %v", record.CompletedAt, originalCompletedAt)
			}
			return
		}
	}
	t.Fatalf("skipped occurrence %q was not retained", occurrence.OccurrenceID)
}

func TestLegacyPhaseRecordWithoutOccurrenceAndSkipFieldsRemainsValid(t *testing.T) {
	project := validProjectState()
	project.PhaseHistory = []PhaseRecord{{Phase: "development", Subphase: "testing", Status: StatusFailed, StartedAt: project.CreatedAt}}
	document, err := json.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ProjectState
	if err := json.Unmarshal(document, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, err := NewProjectState(decoded); err != nil {
		t.Fatalf("legacy phase record rejected: %v", err)
	}
}

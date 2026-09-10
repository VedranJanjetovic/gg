package state

import (
	"context"
	"testing"
	"time"
)

func TestCloseRunPausedRecordsWhyTheRunParkedAndResumeClearsIt(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)}
	svc := NewLifecycleService(store, clock, store.Locker())
	project := validProjectState()
	project.Status = StatusRunning
	project.RunReservationToken = "active-reservation"
	if err := svc.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}

	if err := svc.CloseRunPaused(context.Background(), project.Slug, "verification remediation exhausted at boundary", "fix the regression, then resume"); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != StatusStopped {
		t.Fatalf("paused project status = %s, want stopped", persisted.Status)
	}
	if persisted.Pause == nil || persisted.Pause.Reason != "verification remediation exhausted at boundary" ||
		persisted.Pause.NextAction != "fix the regression, then resume" || !persisted.Pause.At.Equal(clock.now) {
		t.Fatalf("pause record = %#v", persisted.Pause)
	}
	if persisted.RunReservationToken != "" || persisted.ActiveRunID != "" {
		t.Fatalf("paused close retained run ownership: %#v", persisted)
	}

	if _, err := svc.Transition(context.Background(), project.Slug, StatusRunning, "development", "", nil); err != nil {
		t.Fatal(err)
	}
	persisted, err = store.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Pause != nil {
		t.Fatalf("resumed run retained pause record %#v", persisted.Pause)
	}
}

func TestCloseRunPausedRequiresAReason(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewLifecycleService(store, &testClock{now: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)}, store.Locker())
	if err := svc.CloseRunPaused(context.Background(), "any", "  ", "resume"); err == nil {
		t.Fatal("empty pause reason was accepted")
	}
}

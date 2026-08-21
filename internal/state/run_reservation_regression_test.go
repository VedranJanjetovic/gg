package state

import (
	"context"
	"encoding/json"
	"testing"
)

func TestLifecycleSecondProcessCannotReplaceRunReservation(t *testing.T) {
	root := t.TempDir()
	storeA, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	project := validProjectState()
	project.Status = StatusStopped
	serviceA := NewLifecycleService(storeA, nil, storeA.Locker())
	if err := serviceA.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	reservedA, _, err := serviceA.ReserveRun(context.Background(), project.Slug, nil)
	if err != nil {
		t.Fatal(err)
	}

	storeB, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	serviceB := NewLifecycleService(storeB, nil, storeB.Locker())
	if _, _, err := serviceB.ReserveRun(context.Background(), project.Slug, nil); err == nil {
		t.Fatal("second process replaced an active run reservation")
	}

	persisted, err := serviceA.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.RunReservationToken != reservedA.RunReservationToken {
		t.Fatalf("reservation token changed from %q to %q", reservedA.RunReservationToken, persisted.RunReservationToken)
	}
}

func TestLifecycleBeginRunRejectsStaleReservationOwner(t *testing.T) {
	root := t.TempDir()
	storeA, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	project := validProjectState()
	project.Status = StatusStopped
	serviceA := NewLifecycleService(storeA, nil, storeA.Locker())
	if err := serviceA.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	reservedA, _, err := serviceA.ReserveRun(context.Background(), project.Slug, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Model another process acquiring a newer reservation between the CLI
	// reservation and controller BeginRun. BeginRun must prove it owns the exact
	// reservation it is attempting to claim, not merely observe StatusRunning.
	storeB, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	replaced := reservedA
	replaced.RunReservationToken = "newer-process-reservation"
	if err := storeB.Save(context.Background(), replaced); err != nil {
		t.Fatal(err)
	}

	if err := serviceA.BeginRun(context.Background(), project.Slug, "run-a", reservedA.RunReservationToken); err == nil {
		t.Fatal("BeginRun() claimed a reservation owned by another process")
	}
	persisted, err := serviceA.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ActiveRunID != "" || persisted.RunReservationToken != replaced.RunReservationToken {
		t.Fatalf("stale BeginRun changed newer ownership: %#v", persisted)
	}
}

func TestLifecycleExplicitStopRecoversOrphanedRunReservation(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	project := validProjectState()
	project.Status = StatusStopped
	service := NewLifecycleService(store, nil, store.Locker())
	if err := service.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	reserved, _, err := service.ReserveRun(context.Background(), project.Slug, nil)
	if err != nil {
		t.Fatal(err)
	}
	historyLength := len(reserved.PhaseHistory)

	canceled, err := service.CancelRunReservation(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if !canceled {
		t.Fatal("CancelRunReservation() did not recover the orphaned reservation")
	}
	recovered, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != StatusStopped || recovered.RunReservationToken != "" || recovered.ActiveRunID != "" {
		t.Fatalf("recovered reservation state = %#v", recovered)
	}
	if len(recovered.PhaseHistory) != historyLength {
		t.Fatalf("reservation recovery changed phase history: before=%d after=%d", historyLength, len(recovered.PhaseHistory))
	}
	if err := service.BeginRun(context.Background(), project.Slug, "stale-owner", reserved.RunReservationToken); err == nil {
		t.Fatal("stale reservation owner claimed the project after explicit recovery")
	}
	if _, _, err := service.ReserveRun(context.Background(), project.Slug, nil); err != nil {
		t.Fatalf("project could not be reserved after recovery: %v", err)
	}
}

func TestLifecycleSnapshotReservationStoresMatchingQABudgetAtomically(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	project := validProjectState()
	project.Status = StatusStopped
	project.MaxQAAttempts = 5
	service := NewLifecycleService(store, nil, store.Locker())
	if err := service.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	snapshot := PipelineConfigSnapshot{
		SchemaVersion: 1,
		Data:          json.RawMessage(`{"schemaVersion":1,"maxQaAttempts":3}`),
	}

	reserved, _, err := service.ReserveRunWithSnapshot(context.Background(), project.Slug, snapshot, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reserved.MaxQAAttempts != 3 || string(reserved.PipelineConfig.Data) != string(snapshot.Data) {
		t.Fatalf("atomic execution reservation = %#v, want snapshot and QA maximum 3", reserved)
	}
	if canceled, err := service.CancelRunReservation(context.Background(), project.Slug); err != nil || !canceled {
		t.Fatalf("recover reserved run: canceled=%v err=%v", canceled, err)
	}
	recovered, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.MaxQAAttempts != 3 || string(recovered.PipelineConfig.Data) != string(snapshot.Data) {
		t.Fatalf("recovered execution configuration diverged: %#v", recovered)
	}
}

func TestLifecycleSnapshotReservationRejectsBudgetChangeBeforeOverwritingSnapshot(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	project := validProjectState()
	project.Status = StatusStopped
	project.MaxQAAttempts = 5
	project.QACompletedAttempts = 1
	project.QALoopStage = "qa"
	service := NewLifecycleService(store, nil, store.Locker())
	if err := service.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	originalSnapshot := string(project.PipelineConfig.Data)
	rejected := PipelineConfigSnapshot{
		SchemaVersion: 1,
		Data:          json.RawMessage(`{"schemaVersion":1,"maxQaAttempts":3}`),
	}

	if _, _, err := service.ReserveRunWithSnapshot(context.Background(), project.Slug, rejected, 3, nil); err == nil {
		t.Fatal("ReserveRunWithSnapshot() changed an in-progress QA budget")
	}
	persisted, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != StatusStopped ||
		persisted.MaxQAAttempts != 5 ||
		string(persisted.PipelineConfig.Data) != originalSnapshot ||
		persisted.RunReservationToken != "" {
		t.Fatalf("rejected reservation mutated execution state: %#v", persisted)
	}
}

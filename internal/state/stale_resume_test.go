package state_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/state"
)

func TestRecoverStaleRunClearsOwnershipAndPreservesCursor(t *testing.T) {
	store, err := state.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := state.ProjectState{Name: "Demo", Slug: "demo", OriginalGoal: "goal", AcceptanceCriteria: []string{"criterion"}, PipelineConfig: state.PipelineConfigSnapshot{SchemaVersion: 1, Data: json.RawMessage(`{}`)}, CurrentPhase: "development", CurrentSubphase: "implement", Status: state.StatusRunning, WorktreePath: t.TempDir(), BranchName: "agent/demo", CreatedAt: now, UpdatedAt: now, StatusChangedAt: now, ActiveRunID: "dead", DispatchClaimRunID: "dead", StopRequested: true, StopRequestID: "dead"}
	if err := store.Save(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	got, recovered, err := service.RecoverStaleRun(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered || got.Status != state.StatusStopped || got.CurrentPhase != project.CurrentPhase || got.CurrentSubphase != project.CurrentSubphase {
		t.Fatalf("got=%#v recovered=%v", got, recovered)
	}
	if got.RunReservationToken != "" || got.ActiveRunID != "" || got.DispatchClaimRunID != "" || got.StopRequested {
		t.Fatalf("stale markers remain: %#v", got)
	}
	_, recovered, err = service.RecoverStaleRun(context.Background(), project.Slug)
	if err != nil || recovered {
		t.Fatalf("second recovery recovered=%v err=%v", recovered, err)
	}
}

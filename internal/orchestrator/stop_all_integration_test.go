package orchestrator_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/state"
)

func TestStopAllUsesControllerDurableStopRequests(t *testing.T) {
	store, err := state.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := state.ProjectState{
		SchemaVersion:      state.CurrentSchemaVersion,
		Name:               "Durable stop",
		Slug:               "durable-stop",
		OriginalGoal:       "verify durable stop",
		AcceptanceCriteria: []string{"stop is persisted"},
		PipelineConfig:     state.PipelineConfigSnapshot{SchemaVersion: 1, Data: json.RawMessage(`{}`)},
		CurrentPhase:       "pipeline",
		Status:             state.StatusRunning,
		WorktreePath:       t.TempDir(),
		BranchName:         "durable-stop",
		ActiveRunID:        "run-1",
		CreatedAt:          now,
		UpdatedAt:          now,
		StatusChangedAt:    now,
	}
	if err := store.Save(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	lifecycle := state.NewLifecycleService(store, nil, store.Locker())
	controller := orchestrator.NewController(orchestrator.WithPhaseState(lifecycle))

	result, err := orchestrator.StopAll(context.Background(), lifecycle, controller)
	if err != nil {
		t.Fatalf("StopAll() error = %v", err)
	}
	if result.Running != 1 || result.Stopped != 1 {
		t.Fatalf("result = %#v", result)
	}
	persisted, err := lifecycle.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.StopRequested || persisted.StopRequestID != project.ActiveRunID {
		t.Fatalf("persisted stop request = %#v, want requested run %q", persisted, project.ActiveRunID)
	}
}

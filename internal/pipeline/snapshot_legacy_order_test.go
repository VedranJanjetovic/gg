package pipeline_test

import (
	"testing"

	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

// legacyOrderSnapshot is a verbatim schema-1 snapshot as written before Rebase
// moved ahead of QA. Projects created then persist "qa" before "rebase".
func legacyOrderSnapshot() state.PipelineConfigSnapshot {
	return state.PipelineConfigSnapshot{SchemaVersion: 1, Data: []byte(`{
		"schemaVersion": 1,
		"phases": [
			{"id": "acceptance_criteria", "settings": {"agent": "codex", "model": "gpt-5.6-sol", "effort": "high"}},
			{"id": "grooming", "settings": {"agent": "codex", "model": "gpt-5.6-sol", "effort": "high"}},
			{"id": "planning", "settings": {"agent": "codex", "model": "gpt-5.6-sol", "effort": "high"}},
			{"id": "development", "settings": {"agent": "codex", "model": "gpt-5.6-luna", "effort": "high"}},
			{"id": "qa", "settings": {"agent": "codex", "model": "gpt-5.6-luna", "effort": "high"}},
			{"id": "rebase", "settings": {"agent": "codex", "model": "sonnet", "effort": "low"}},
			{"id": "test_document", "settings": {"agent": "codex", "model": "sonnet", "effort": "low"}},
			{"id": "build_checker", "settings": {"agent": "codex", "model": "sonnet", "effort": "low"}},
			{"id": "pr", "settings": {"agent": "codex", "model": "gpt-5.6-luna", "effort": "high"}},
			{"id": "ci", "settings": {"agent": "codex", "model": "gpt-5.6-sol", "effort": "medium"}}
		],
		"developmentSubphases": {"mode": 0},
		"maxQaAttempts": 3,
		"gitOps": {"parent_branch": "main", "base_ref": "HEAD", "enable_pr": true, "enable_ci": true},
		"gitOpsConfigured": false
	}`)}
}

func legacyOrderContract() state.VerificationContract {
	return state.VerificationContract{
		Steps:      []state.VerificationStep{{Name: "tests", Command: "go", Args: []string{"test", "./..."}, Adapter: state.VerificationAdapterGoTest}},
		RepairMode: true,
	}
}

func phaseIDs(plan pipeline.ExecutablePipeline) []pipeline.PhaseID {
	ids := make([]pipeline.PhaseID, 0, len(plan.Phases()))
	for _, phase := range plan.Phases() {
		ids = append(ids, phase.Phase().ID())
	}
	return ids
}

// A resumed legacy project re-snapshots its restored plan when Planning
// completes. The plan still carries the legacy phase order, so the snapshot
// must stay persistable instead of failing the run forever.
func TestSnapshotExecutionWithVerificationAcceptsRestoredLegacyOrderPlan(t *testing.T) {
	plan, subphases, maxAttempts, err := pipeline.RestoreExecution(legacyOrderSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := pipeline.SnapshotExecutionWithVerification(plan, subphases, maxAttempts, legacyOrderContract())
	if err != nil {
		t.Fatal(err)
	}
	restored, _, _, contract, err := pipeline.RestoreExecutionWithVerification(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := phaseIDs(restored), phaseIDs(plan); len(got) != len(want) {
		t.Fatalf("restored phases = %v, want %v", got, want)
	}
	for index, id := range phaseIDs(restored) {
		if id != phaseIDs(plan)[index] {
			t.Fatalf("restored phase order = %v, want %v", phaseIDs(restored), phaseIDs(plan))
		}
	}
	if len(contract.Steps) != 1 || !contract.RepairMode {
		t.Fatalf("restored contract = %#v", contract)
	}
}

// Legacy resume migrates the persisted schema-1 snapshot in place. The
// migration adds the contract only; it must not reject the legacy order it was
// written to migrate.
func TestUpgradeLegacyExecutionSnapshotAcceptsLegacyPhaseOrder(t *testing.T) {
	upgraded, err := pipeline.UpgradeLegacyExecutionSnapshot(legacyOrderSnapshot(), legacyOrderContract())
	if err != nil {
		t.Fatal(err)
	}
	restored, _, _, contract, err := pipeline.RestoreExecutionWithVerification(upgraded)
	if err != nil {
		t.Fatal(err)
	}
	legacy, _, _, err := pipeline.RestoreExecution(legacyOrderSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := phaseIDs(restored), phaseIDs(legacy); len(got) != len(want) {
		t.Fatalf("migrated phases = %v, want %v", got, want)
	}
	for index, id := range phaseIDs(restored) {
		if id != phaseIDs(legacy)[index] {
			t.Fatalf("migrated phase order = %v, want %v", phaseIDs(restored), phaseIDs(legacy))
		}
	}
	if len(contract.Steps) != 1 || !contract.RepairMode {
		t.Fatalf("migrated contract = %#v", contract)
	}
}

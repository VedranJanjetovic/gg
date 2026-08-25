package pipeline_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

func TestSnapshotExecutionWithVerificationRoundTripsContractAndCopiesInputs(t *testing.T) {
	resolved := resolvedConfig()
	resolved.Defaults = config.AgentSettings{Agent: config.AgentClaude, Model: "model", Effort: config.EffortMedium}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	contract := state.VerificationContract{
		Steps:      []state.VerificationStep{{Name: "tests", Command: "go", Args: []string{"test", "./..."}, Env: map[string]string{"GOTOOLCHAIN": "go1.22.12"}, Adapter: state.VerificationAdapterGoTest}},
		RepairMode: true,
	}
	snapshot, err := pipeline.SnapshotExecutionWithVerification(plan, pipeline.DevelopmentSubphaseGeneration{}, 3, contract)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SchemaVersion != 1 {
		t.Fatalf("wrapper schema version = %d, want 1", snapshot.SchemaVersion)
	}
	var encoded struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(snapshot.Data, &encoded); err != nil {
		t.Fatal(err)
	}
	if encoded.SchemaVersion != 2 {
		t.Fatalf("execution schema version = %d, want 2", encoded.SchemaVersion)
	}
	contract.Steps[0].Args[0] = "mutated"
	contract.Steps[0].Env["GOTOOLCHAIN"] = "mutated"
	got, err := pipeline.SnapshotVerification(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	want := state.VerificationContract{
		Steps:      []state.VerificationStep{{Name: "tests", Command: "go", Args: []string{"test", "./..."}, Env: map[string]string{"GOTOOLCHAIN": "go1.22.12"}, Adapter: state.VerificationAdapterGoTest}},
		RepairMode: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restored contract = %#v, want %#v", got, want)
	}
	if _, _, _, err := pipeline.RestoreExecution(snapshot); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaOneSnapshotRestoresWithVerificationDefaults(t *testing.T) {
	resolved := resolvedConfig()
	resolved.Defaults = config.AgentSettings{Agent: config.AgentClaude, Model: "model", Effort: config.EffortMedium}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := pipeline.SnapshotExecution(plan, pipeline.DevelopmentSubphaseGeneration{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	var encoded map[string]json.RawMessage
	if err := json.Unmarshal(snapshot.Data, &encoded); err != nil {
		t.Fatal(err)
	}
	if _, present := encoded["verificationSteps"]; present {
		t.Fatal("schema-one snapshot unexpectedly contains verification fields")
	}
	contract, err := pipeline.SnapshotVerification(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(contract.Steps) != 0 || contract.RepairMode {
		t.Fatalf("legacy defaults = %#v", contract)
	}
}

func TestUpgradeLegacyExecutionSnapshotPreservesRunPlan(t *testing.T) {
	resolved := resolvedConfig()
	resolved.Defaults = config.AgentSettings{Agent: config.AgentClaude, Model: "model", Effort: config.EffortMedium}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := pipeline.SnapshotExecution(plan, pipeline.DevelopmentSubphaseGeneration{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	contract := state.VerificationContract{
		Steps:      []state.VerificationStep{{Name: "tests", Command: "go", Args: []string{"test", "./..."}, Adapter: state.VerificationAdapterGoTest}},
		RepairMode: true,
	}
	upgraded, err := pipeline.UpgradeLegacyExecutionSnapshot(legacy, contract)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pipeline.SnapshotVerification(upgraded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, contract) {
		t.Fatalf("migrated contract = %#v, want %#v", got, contract)
	}
	restored, subphases, maxAttempts, err := pipeline.RestoreExecution(upgraded)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Phases()) != len(plan.Phases()) || !reflect.DeepEqual(subphases, pipeline.DevelopmentSubphaseGeneration{}) || maxAttempts != 3 {
		t.Fatalf("migration changed execution plan: phases=%d subphases=%#v attempts=%d", len(restored.Phases()), subphases, maxAttempts)
	}
}

func TestUpgradeLegacyExecutionSnapshotRejectsTrailingJSON(t *testing.T) {
	resolved := resolvedConfig()
	resolved.Defaults = config.AgentSettings{Agent: config.AgentClaude, Model: "model", Effort: config.EffortMedium}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := pipeline.SnapshotExecution(plan, pipeline.DevelopmentSubphaseGeneration{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Data = append(legacy.Data, []byte(` {"unexpected":true}`)...)
	contract := state.VerificationContract{
		Steps: []state.VerificationStep{{Name: "tests", Command: "go", Args: []string{"test", "./..."}, Adapter: state.VerificationAdapterGoTest}},
	}
	if _, err := pipeline.UpgradeLegacyExecutionSnapshot(legacy, contract); err == nil {
		t.Fatal("legacy snapshot with trailing JSON was accepted")
	}
}

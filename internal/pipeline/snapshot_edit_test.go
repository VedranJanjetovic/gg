package pipeline_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
)

func TestProjectSnapshotTupleEditPreservesStructureAndUpgradesLegacyRepresentation(t *testing.T) {
	project := phase9ProjectConfig()
	global := config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettings{Agent: project.Defaults.Agent, Model: project.Defaults.Model, Effort: project.Defaults.Effort, Provenance: project.Defaults.Provenance}}
	resolved, err := config.Resolve(global, &project, config.RunOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := pipeline.SnapshotProjectExecution(plan, pipeline.DevelopmentSubphaseGeneration{}, 3, project, resolved.GitOps)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := pipeline.ReadProjectExecutionConfiguration(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	before := configuration.Clone()
	for index := range configuration.Phases {
		if configuration.Phases[index].ID == pipeline.PhaseTestDocument {
			configuration.Phases[index].Settings.Model = "gpt-5.6-sol"
		}
	}
	updated, err := pipeline.UpdateProjectExecutionConfiguration(snapshot, configuration)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pipeline.ReadProjectExecutionConfiguration(updated)
	if err != nil {
		t.Fatal(err)
	}
	if got.Default != before.Default || len(got.Phases) != len(before.Phases) {
		t.Fatalf("unrelated configuration changed: before=%#v after=%#v", before, got)
	}
	for index := range got.Phases {
		if got.Phases[index].ID != before.Phases[index].ID || got.Phases[index].Enabled != before.Phases[index].Enabled || got.Phases[index].Required != before.Phases[index].Required {
			t.Fatalf("phase structure changed at %d: before=%#v after=%#v", index, before.Phases[index], got.Phases[index])
		}
	}
	restored, _, _, err := pipeline.RestoreExecution(updated)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Phases()) != len(plan.Phases()) {
		t.Fatalf("restored enabled phase count = %d, want %d", len(restored.Phases()), len(plan.Phases()))
	}
}

func TestProjectSnapshotTupleEditRejectsStructureChanges(t *testing.T) {
	project := phase9ProjectConfig()
	global := config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettings{Agent: project.Defaults.Agent, Model: project.Defaults.Model, Effort: project.Defaults.Effort, Provenance: project.Defaults.Provenance}}
	resolved, err := config.Resolve(global, &project, config.RunOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := pipeline.SnapshotProjectExecution(plan, pipeline.DevelopmentSubphaseGeneration{}, 3, project, resolved.GitOps)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := pipeline.ReadProjectExecutionConfiguration(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	configuration.Phases[0].Enabled = false
	if _, err := pipeline.UpdateProjectExecutionConfiguration(snapshot, configuration); err == nil || !strings.Contains(err.Error(), "cannot change phase structure") {
		t.Fatalf("structure change error = %v", err)
	}
}

func TestProjectSnapshotTupleEditPreservesLegacyStructureAndExecutionKnobs(t *testing.T) {
	project := phase9ProjectConfig()
	global := config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettings{Agent: project.Defaults.Agent, Model: project.Defaults.Model, Effort: project.Defaults.Effort, Provenance: project.Defaults.Provenance}}
	resolved, err := config.Resolve(global, &project, config.RunOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := pipeline.SnapshotProjectExecution(plan, pipeline.DevelopmentSubphaseGeneration{}, 3, project, resolved.GitOps)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(snapshot.Data, &document); err != nil {
		t.Fatal(err)
	}
	document["schemaVersion"] = json.RawMessage(`2`)
	delete(document, "projectDefault")
	delete(document, "phaseStructure")
	legacyData, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	legacy := snapshot
	legacy.Data = legacyData
	before, err := pipeline.ReadProjectExecutionConfiguration(legacy)
	if err != nil {
		t.Fatal(err)
	}
	updatedConfiguration := before.Clone()
	for index := range updatedConfiguration.Phases {
		if updatedConfiguration.Phases[index].ID == pipeline.PhaseTestDocument {
			updatedConfiguration.Phases[index].Settings.Model = "repaired-model"
		}
	}
	updated, err := pipeline.UpdateProjectExecutionConfiguration(legacy, updatedConfiguration)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pipeline.ReadProjectExecutionConfiguration(updated)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Phases) != len(before.Phases) {
		t.Fatalf("phase count changed from %d to %d", len(before.Phases), len(got.Phases))
	}
	for index := range got.Phases {
		if got.Phases[index].ID != before.Phases[index].ID || got.Phases[index].Enabled != before.Phases[index].Enabled || got.Phases[index].Required != before.Phases[index].Required {
			t.Fatalf("legacy phase structure changed at %d: before=%#v after=%#v", index, before.Phases[index], got.Phases[index])
		}
	}
	if got.Phases[0].Settings.Model == "repaired-model" {
		t.Fatal("unrelated legacy phase changed")
	}
	for _, phase := range got.Phases {
		if phase.ID == pipeline.PhaseTestDocument && phase.Settings.Model != "repaired-model" {
			t.Fatalf("updated legacy phase = %#v", phase)
		}
	}
	var encoded map[string]json.RawMessage
	if err := json.Unmarshal(updated.Data, &encoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(encoded["maxQaAttempts"], document["maxQaAttempts"]) || !reflect.DeepEqual(encoded["gitOps"], document["gitOps"]) {
		t.Fatal("legacy edit did not preserve execution knobs")
	}
}

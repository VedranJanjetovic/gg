package pipeline_test

import (
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

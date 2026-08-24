package cli

import (
	"context"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

func TestRepairCurrentPhaseUsesProjectDefaultAndPersistsWarningBeforeResume(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	projectConfig := phase9CompleteConfig()
	projectConfig.Defaults = config.AgentSettingsOverride{Agent: config.AgentCodex, Model: "gpt-5.6-sol", Effort: config.EffortHigh, Provenance: config.ModelProvenanceCatalog}
	for index := range projectConfig.Phases {
		projectConfig.Phases[index].AgentSettings = config.AgentSettings{Agent: config.AgentCodex, Model: "gpt-5.6-sol", Effort: config.EffortHigh, Provenance: config.ModelProvenanceCatalog}
	}
	for index := range projectConfig.Phases {
		if projectConfig.Phases[index].Phase == config.PhaseTestDocument {
			projectConfig.Phases[index].AgentSettings = config.AgentSettings{Agent: config.AgentCodex, Model: "sonnet", Effort: config.EffortLow, Provenance: config.ModelProvenanceCatalog}
		}
	}
	global := config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettings{Agent: config.AgentCodex, Model: "global-model", Effort: config.EffortLow, Provenance: config.ModelProvenanceCatalog}}
	resolved, err := config.Resolve(global, &projectConfig, config.RunOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := pipeline.SnapshotProjectExecution(plan, pipeline.DevelopmentSubphaseGeneration{}, 3, projectConfig, resolved.GitOps)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := state.ProjectState{
		SchemaVersion: state.CurrentSchemaVersion, Name: "Repair project", Slug: "repair-project", OriginalGoal: "repair the current phase", AcceptanceCriteria: []string{"repair only current phase"},
		PipelineConfig: snapshot, CurrentPhase: string(pipeline.PhaseTestDocument), Status: state.StatusFailed, WorktreePath: root, BranchName: "agent/repair-project",
		PhaseHistory: []state.PhaseRecord{{Phase: string(pipeline.PhaseTestDocument), Status: state.StatusFailed, OccurrenceID: "failed-test-document", StartedAt: now, CompletedAt: &now, Outcome: &state.ExecutionOutcome{Error: "invalid saved model"}}},
		CreatedAt:    now, UpdatedAt: now, StatusChangedAt: now,
	}
	store := mustStateStore(t, root)
	lifecycle := state.NewLifecycleService(store, nil, store.Locker())
	if err := lifecycle.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	app := New(WithRootResolver(fixedRoot{root: root}), WithConfigStore(completeConfiguredMemoryStore()), WithLifecycleService(lifecycle))
	updated, err := app.repairCurrentPhaseConfiguration(context.Background(), project.Slug, project)
	if err != nil {
		t.Fatal(err)
	}
	if updated.PhaseConfigurationWarnings[string(pipeline.PhaseTestDocument)] != "invalid saved configuration; using project default: codex / gpt-5.6-sol / high" {
		t.Fatalf("warning = %#v", updated.PhaseConfigurationWarnings)
	}
	configuration, err := pipeline.ReadProjectExecutionConfiguration(updated.PipelineConfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range configuration.Phases {
		if phase.ID == pipeline.PhaseTestDocument && phase.Settings != (config.AgentSettings{Agent: config.AgentCodex, Model: "gpt-5.6-sol", Effort: config.EffortHigh, Provenance: config.ModelProvenanceCatalog}) {
			t.Fatalf("repaired tuple = %#v", phase.Settings)
		}
	}
	reloaded, err := lifecycle.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PhaseHistory[0].Outcome == nil || reloaded.PhaseHistory[0].Outcome.Runtime != "" || reloaded.PhaseHistory[0].Outcome.Error != "invalid saved model" {
		t.Fatalf("historical outcome changed: %#v", reloaded.PhaseHistory[0].Outcome)
	}
}

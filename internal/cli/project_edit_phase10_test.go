package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
	"github.com/VedranJanjetovic/gg/internal/tui"
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

func repairProjectFixture(t *testing.T, root string, projectDefault, currentPhase config.AgentSettings) (state.ProjectState, *state.LifecycleService) {
	t.Helper()
	projectConfig := phase9CompleteConfig()
	projectConfig.Defaults = config.AgentSettingsOverride{Agent: projectDefault.Agent, Model: projectDefault.Model, Effort: projectDefault.Effort, Provenance: projectDefault.Provenance}
	for index := range projectConfig.Phases {
		projectConfig.Phases[index].AgentSettings = projectDefault
		if projectConfig.Phases[index].Phase == config.PhaseTestDocument {
			projectConfig.Phases[index].AgentSettings = currentPhase
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
	return project, lifecycle
}

func TestRepairCurrentPhaseUsesFolderThenGlobalFallbackAndRejectsSparseFolder(t *testing.T) {
	current := config.AgentSettings{Agent: config.AgentCodex, Model: "sonnet", Effort: config.EffortLow, Provenance: config.ModelProvenanceCatalog}
	projectDefault := config.AgentSettings{Agent: config.AgentCodex, Model: "opus", Effort: config.EffortHigh, Provenance: config.ModelProvenanceCatalog}
	catalog := config.NewStaticAgentCatalogSource(config.NewAgentCatalog(
		config.AgentCatalogEntry{Agent: config.AgentClaude, Models: []string{"sonnet", "opus"}, ModelListStatus: config.ModelListAvailable},
		config.AgentCatalogEntry{Agent: config.AgentCodex, Models: []string{"gpt-folder", "gpt-global"}, ModelListStatus: config.ModelListAvailable},
	))
	t.Run("complete folder wins", func(t *testing.T) {
		root := t.TempDir()
		project, lifecycle := repairProjectFixture(t, root, projectDefault, current)
		folder := completeTestProjectConfig(config.AgentCodex, "gpt-folder", config.EffortMedium)
		store := &memoryConfigureStore{global: config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettings{Agent: config.AgentCodex, Model: "gpt-global", Effort: config.EffortHigh, Provenance: config.ModelProvenanceCatalog}}, project: folder}
		app := New(WithRootResolver(fixedRoot{root: root}), WithConfigStore(store), WithLifecycleService(lifecycle), WithAgentCatalogSource(catalog))
		updated, err := app.repairCurrentPhaseConfiguration(context.Background(), project.Slug, project)
		if err != nil {
			t.Fatal(err)
		}
		want := "invalid saved configuration; using folder default: codex / gpt-folder / medium"
		if updated.PhaseConfigurationWarnings[project.CurrentPhase] != want {
			t.Fatalf("warning = %#v, want %q", updated.PhaseConfigurationWarnings, want)
		}
	})
	t.Run("sparse folder is skipped", func(t *testing.T) {
		root := t.TempDir()
		project, lifecycle := repairProjectFixture(t, root, projectDefault, current)
		store := &memoryConfigureStore{global: config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettings{Agent: config.AgentCodex, Model: "gpt-global", Effort: config.EffortHigh, Provenance: config.ModelProvenanceCatalog}}, project: config.ProjectConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettingsOverride{Agent: config.AgentCodex, Model: "gpt-folder", Effort: config.EffortMedium, Provenance: config.ModelProvenanceCatalog}}}
		app := New(WithRootResolver(fixedRoot{root: root}), WithConfigStore(store), WithLifecycleService(lifecycle), WithAgentCatalogSource(catalog))
		updated, err := app.repairCurrentPhaseConfiguration(context.Background(), project.Slug, project)
		if err != nil {
			t.Fatal(err)
		}
		want := "invalid saved configuration; using global default: codex / gpt-global / high"
		if updated.PhaseConfigurationWarnings[project.CurrentPhase] != want {
			t.Fatalf("warning = %#v, want %q", updated.PhaseConfigurationWarnings, want)
		}
	})
}

func TestRepairCurrentPhaseLeavesUnknownAndManualModelsAlone(t *testing.T) {
	catalog := config.NewStaticAgentCatalogSource(config.NewAgentCatalog(
		config.AgentCatalogEntry{Agent: config.AgentClaude, Models: []string{"sonnet"}, ModelListStatus: config.ModelListAvailable},
		config.AgentCatalogEntry{Agent: config.AgentCodex, Models: []string{"gpt-5"}, ModelListStatus: config.ModelListAvailable},
	))
	for _, test := range []struct {
		name       string
		provenance config.ModelProvenance
	}{
		{name: "unknown legacy model", provenance: ""},
		{name: "manual model", provenance: config.ModelProvenanceManual},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			current := config.AgentSettings{Agent: config.AgentCodex, Model: "future-model", Effort: config.EffortLow, Provenance: test.provenance}
			projectDefault := config.AgentSettings{Agent: config.AgentCodex, Model: "gpt-5", Effort: config.EffortHigh, Provenance: config.ModelProvenanceCatalog}
			fixtureCurrent := current
			if fixtureCurrent.Provenance == "" {
				fixtureCurrent.Provenance = config.ModelProvenanceManual
			}
			project, lifecycle := repairProjectFixture(t, root, projectDefault, fixtureCurrent)
			if test.provenance == "" {
				var document map[string]json.RawMessage
				if err := json.Unmarshal(project.PipelineConfig.Data, &document); err != nil {
					t.Fatal(err)
				}
				document["schemaVersion"] = json.RawMessage(`2`)
				delete(document, "projectDefault")
				delete(document, "phaseStructure")
				var phases []map[string]json.RawMessage
				if err := json.Unmarshal(document["phases"], &phases); err != nil {
					t.Fatal(err)
				}
				for _, phase := range phases {
					var id pipeline.PhaseID
					if err := json.Unmarshal(phase["id"], &id); err != nil {
						t.Fatal(err)
					}
					if id != pipeline.PhaseTestDocument {
						continue
					}
					settings := map[string]json.RawMessage{}
					if err := json.Unmarshal(phase["settings"], &settings); err != nil {
						t.Fatal(err)
					}
					settings["model"] = json.RawMessage(`"future-model"`)
					delete(settings, "provenance")
					encoded, err := json.Marshal(settings)
					if err != nil {
						t.Fatal(err)
					}
					phase["settings"] = encoded
				}
				document["phases"], _ = json.Marshal(phases)
				project.PipelineConfig.Data, _ = json.Marshal(document)
				if err := lifecycle.Save(context.Background(), project); err != nil {
					t.Fatal(err)
				}
			}
			app := New(WithRootResolver(fixedRoot{root: root}), WithConfigStore(completeConfiguredMemoryStore()), WithLifecycleService(lifecycle), WithAgentCatalogSource(catalog))
			updated, err := app.repairCurrentPhaseConfiguration(context.Background(), project.Slug, project)
			if err != nil {
				t.Fatal(err)
			}
			if len(updated.PhaseConfigurationWarnings) != 0 {
				t.Fatalf("unexpected repair warning = %#v", updated.PhaseConfigurationWarnings)
			}
			before, err := pipeline.ReadProjectExecutionConfiguration(project.PipelineConfig)
			if err != nil {
				t.Fatal(err)
			}
			got, err := pipeline.ReadProjectExecutionConfiguration(updated.PipelineConfig)
			if err != nil {
				t.Fatal(err)
			}
			if before.Phases[len(before.Phases)-1].Settings != got.Phases[len(got.Phases)-1].Settings {
				t.Fatal("non-repairable current tuple changed")
			}
		})
	}
}

func TestConfigureExistingProjectCancellationLeavesSnapshotUnchanged(t *testing.T) {
	root := t.TempDir()
	projectDefault := config.AgentSettings{Agent: config.AgentCodex, Model: "gpt-5", Effort: config.EffortHigh, Provenance: config.ModelProvenanceCatalog}
	current := config.AgentSettings{Agent: config.AgentCodex, Model: "gpt-5", Effort: config.EffortHigh, Provenance: config.ModelProvenanceCatalog}
	project, lifecycle := repairProjectFixture(t, root, projectDefault, current)
	before := append([]byte(nil), project.PipelineConfig.Data...)
	app := New(
		WithRootResolver(fixedRoot{root: root}),
		WithConfigStore(completeConfiguredMemoryStore()),
		WithLifecycleService(lifecycle),
		WithConfigurePicker(func(context.Context, config.AgentCatalog, tui.WizardDefaults, io.Reader, io.Writer) (tui.PickerResult, error) {
			return tui.PickerResult{}, tui.ErrPickerCancelled
		}),
	)
	err := app.configureExistingProject(context.Background(), project.Slug, project)
	if !errors.Is(err, errProjectConfigurationCancelled) {
		t.Fatalf("cancel error = %v", err)
	}
	after, err := lifecycle.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if string(after.PipelineConfig.Data) != string(before) || after.Status != state.StatusFailed {
		t.Fatal("cancelled project configuration changed persisted state")
	}
}

func TestWizardDefaultsForExistingProjectLockAllStructureControls(t *testing.T) {
	configuration := pipeline.ProjectExecutionConfiguration{
		Default: config.AgentSettings{Agent: config.AgentCodex, Model: "gpt-5", Effort: config.EffortHigh, Provenance: config.ModelProvenanceCatalog},
		Phases: []pipeline.ProjectPhaseConfiguration{
			{ID: pipeline.PhaseAcceptanceCriteria, Enabled: true, Required: true},
			{ID: pipeline.PhaseQA, Enabled: false, Required: false},
		},
	}

	defaults := wizardDefaultsFromExecution(configuration)
	if len(defaults.Phases) != len(configuration.Phases) {
		t.Fatalf("phase defaults = %d, want %d", len(defaults.Phases), len(configuration.Phases))
	}
	for _, phase := range defaults.Phases {
		if !phase.Locked {
			t.Fatalf("phase %q is not locked for existing-project editing", phase.Phase)
		}
	}
}

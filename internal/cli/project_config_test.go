package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/tui"
)

func phase9CompleteConfig() config.ProjectConfig {
	defaults := config.AgentSettings{Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortMedium, Provenance: config.ModelProvenanceCatalog}
	phases := make([]config.PhaseConfig, 0, len(config.CompletePhaseOrder()))
	for _, phase := range config.CompletePhaseOrder() {
		phases = append(phases, config.PhaseConfig{Phase: phase, Enabled: true, Required: containsConfigPhase(config.RequiredPhases(), phase), AgentSettings: defaults})
	}
	phases[5].Enabled = false
	return config.CompleteProjectConfig(config.CompleteSchemaVersion, defaults, phases, config.GitOpsOverride{})
}

func TestCompleteProjectFromPickerIsolatedAndWholeTuple(t *testing.T) {
	folder := phase9CompleteConfig()
	before := folder.Clone()
	picked := tui.PickerResult{
		Agent: config.AgentCodex, Model: "gpt-5", Effort: config.EffortHigh,
		Phases: []tui.PhaseState{{Phase: config.PhaseQA, Enabled: true, Locked: false, Agent: config.AgentCodex, Model: "qa-model", Effort: config.EffortLow}},
	}
	project := completeProjectFromPicker(folder, picked)
	if err := config.ValidateCompleteProjectConfig(project); err != nil {
		t.Fatal(err)
	}
	if project.Defaults.Model != "gpt-5" || project.Phases[5].AgentSettings.Model != "qa-model" || project.Phases[5].AgentSettings.Effort != config.EffortLow {
		t.Fatalf("project = %#v", project)
	}
	if !reflect.DeepEqual(folder, before) {
		t.Fatal("project-only selection mutated folder configuration")
	}
}

func TestWizardDefaultsSeedMissingCanonicalPhaseFromFolderDefault(t *testing.T) {
	folder := phase9CompleteConfig()
	folder.Phases = folder.Phases[:len(folder.Phases)-1]
	// This is the in-memory older folder shape used by the Pick flow; the
	// migration gate is tested by the config package, while Pick must seed the
	// newly introduced canonical phase without mutating the folder.
	defaults := wizardDefaultsFromProject(folder)
	if len(defaults.Phases) != len(config.CompletePhaseOrder()) {
		t.Fatalf("phase count = %d", len(defaults.Phases))
	}
	last := defaults.Phases[len(defaults.Phases)-1]
	if last.Phase != config.PhaseCI || last.Model != folder.Defaults.Model || last.Enabled != true {
		t.Fatalf("seeded phase = %#v", last)
	}
}

func TestChooseNewProjectConfigurationInheritsWithoutWritingFolder(t *testing.T) {
	folder := phase9CompleteConfig()
	store := &memoryConfigureStore{
		global:  config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "global", Effort: config.EffortLow, Provenance: config.ModelProvenanceCatalog}},
		project: folder,
	}
	app := New(WithConfigStore(store), WithRootResolver(fixedRoot{root: t.TempDir()}))
	chosen, err := app.chooseNewProjectConfiguration(context.Background(), io.Discard, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(chosen.project, folder) {
		t.Fatal("inherit did not preserve folder configuration")
	}
	if store.savedProject != nil || store.savedGlobal != nil {
		t.Fatal("inherit wrote folder configuration")
	}
}

func TestLoadCompleteFolderConfigurationMigratesSparseFallbackStoreExplicitly(t *testing.T) {
	root := t.TempDir()
	store := &persistedMemoryConfigureStore{memoryConfigureStore: &memoryConfigureStore{
		global: config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettings{
			Agent: config.AgentCodex, Model: "gpt-5", Effort: config.EffortHigh,
		}},
		project: config.ProjectConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettingsOverride{Model: "legacy-model"}},
	}}
	var pickerCalls int
	app := New(
		WithConfigStore(store),
		WithWorkingDirectory(func() (string, error) { return root, nil }),
		WithRootResolver(fixedRoot{root: root}),
		WithConfigurePicker(func(_ context.Context, _ config.AgentCatalog, _ tui.WizardDefaults, _ io.Reader, _ io.Writer) (tui.PickerResult, error) {
			pickerCalls++
			return tui.PickerResult{Agent: config.AgentCodex, Model: "gpt-5", Effort: config.EffortHigh}, nil
		}),
		WithAgentCatalogSource(config.NewStaticAgentCatalogSource(config.NewAgentCatalog(
			config.AgentCatalogEntry{Agent: config.AgentCodex, Models: []string{"gpt-5"}, ModelListStatus: config.ModelListAvailable},
		))),
	)

	loaded, err := app.loadCompleteFolderConfiguration(context.Background(), root, store.global, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if pickerCalls != 1 {
		t.Fatalf("configure picker calls = %d, want 1", pickerCalls)
	}
	if err := config.ValidateCompleteProjectConfig(loaded); err != nil {
		t.Fatalf("loaded configuration is not complete: %v", err)
	}
	if store.savedProject == nil {
		t.Fatal("sparse configuration was used without an explicit save")
	}
}

func TestChooseNewProjectConfigurationPickIsolatedAndSnapshotsAllTuples(t *testing.T) {
	folder := phase9CompleteConfig()
	store := &memoryConfigureStore{
		global:  config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "global", Effort: config.EffortLow, Provenance: config.ModelProvenanceCatalog}},
		project: folder,
	}
	var gotDefaults tui.WizardDefaults
	app := New(
		WithConfigStore(store),
		WithRootResolver(fixedRoot{root: t.TempDir()}),
		WithProjectConfigurationChooser(func(context.Context, io.Reader, io.Writer) (int, error) { return 1, nil }),
		WithAgentCatalogSource(config.NewStaticAgentCatalogSource(config.NewAgentCatalog(
			config.AgentCatalogEntry{Agent: config.AgentCodex, Models: []string{"gpt-5"}, ModelListStatus: config.ModelListAvailable},
		))),
		WithConfigurePicker(func(_ context.Context, _ config.AgentCatalog, defaults tui.WizardDefaults, _ io.Reader, _ io.Writer) (tui.PickerResult, error) {
			gotDefaults = defaults
			return tui.PickerResult{
				Agent: config.AgentCodex, Model: "custom-manual-model", Effort: config.EffortHigh, Manual: true,
				Phases: []tui.PhaseState{{
					Phase: config.PhaseQA, Enabled: true, Agent: config.AgentCodex, Model: "gpt-5", Effort: config.EffortLow,
				}},
			}, nil
		}),
	)

	chosen, err := app.chooseNewProjectConfiguration(context.Background(), io.Discard, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotDefaults.Phases) != len(config.CompletePhaseOrder()) || !gotDefaults.FullTuples {
		t.Fatalf("wizard defaults = %#v, want complete full-tuple phase list", gotDefaults)
	}
	if chosen.project.Defaults.Agent != config.AgentCodex || chosen.project.Defaults.Model != "custom-manual-model" || chosen.project.Defaults.Effort != config.EffortHigh || chosen.project.Defaults.Provenance != config.ModelProvenanceManual {
		t.Fatalf("project defaults = %#v", chosen.project.Defaults)
	}
	if chosen.project.Phases[5].AgentSettings.Model != "gpt-5" || chosen.project.Phases[5].AgentSettings.Effort != config.EffortLow {
		t.Fatalf("QA settings = %#v", chosen.project.Phases[5].AgentSettings)
	}
	if store.savedProject != nil || store.savedGlobal != nil {
		t.Fatal("project-only selection wrote folder configuration")
	}

	resolved, err := pipeline.RestoreResolvedConfiguration(chosen.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	wantDefaults := config.AgentSettings{
		Agent: chosen.project.Defaults.Agent, Model: chosen.project.Defaults.Model,
		Effort: chosen.project.Defaults.Effort, Provenance: chosen.project.Defaults.Provenance,
	}
	if resolved.Defaults != wantDefaults || resolved.Defaults.Provenance != config.ModelProvenanceManual || resolved.Phases[config.PhaseQA].AgentSettings != chosen.project.Phases[5].AgentSettings {
		t.Fatalf("snapshot configuration = %#v, want project configuration", resolved)
	}
	if resolved.Phases[config.PhaseQA].Enabled != chosen.project.Phases[5].Enabled {
		t.Fatal("snapshot did not preserve optional phase enabled state")
	}
}

func TestCreateProjectChoosesConfigurationBeforeDescriptionAndDoesNotCreateOnCancel(t *testing.T) {
	folder := phase9CompleteConfig()
	root := t.TempDir()
	store := &memoryConfigureStore{
		global:  config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortMedium, Provenance: config.ModelProvenanceCatalog}},
		project: folder,
	}
	var order []string
	app := New(
		WithConfigStore(store),
		WithRootResolver(fixedRoot{root: root}),
		WithProjectConfigurationChooser(func(context.Context, io.Reader, io.Writer) (int, error) {
			order = append(order, "choice")
			return 0, nil
		}),
		WithProjectPrompter(projectPromptFunc(func(context.Context, io.Writer) (orchestrator.ProjectInput, error) {
			order = append(order, "description")
			return orchestrator.ProjectInput{}, errors.New("description cancelled")
		})),
	)

	_, err := app.createProject(context.Background(), io.Discard, 3)
	if err == nil || !strings.Contains(err.Error(), "description cancelled") {
		t.Fatalf("createProject error = %v, want description cancellation", err)
	}
	if !reflect.DeepEqual(order, []string{"choice", "description"}) {
		t.Fatalf("interaction order = %v", order)
	}
	if entries, readErr := os.ReadDir(filepath.Join(root, ".gg", "projects")); readErr == nil && len(entries) != 0 {
		t.Fatalf("project state was created before description confirmation: %v", entries)
	}
}

type projectPromptFunc func(context.Context, io.Writer) (orchestrator.ProjectInput, error)

func (f projectPromptFunc) Prompt(ctx context.Context, output io.Writer) (orchestrator.ProjectInput, error) {
	return f(ctx, output)
}

type persistedMemoryConfigureStore struct{ *memoryConfigureStore }

func (s *persistedMemoryConfigureStore) SaveGlobal(value config.GlobalConfig) error {
	s.global = value
	return s.memoryConfigureStore.SaveGlobal(value)
}

func (s *persistedMemoryConfigureStore) SaveProject(root string, value config.ProjectConfig) error {
	s.project = value
	return s.memoryConfigureStore.SaveProject(root, value)
}

func (s *persistedMemoryConfigureStore) SaveConfiguration(root string, global config.GlobalConfig, project config.ProjectConfig) error {
	if err := s.SaveGlobal(global); err != nil {
		return err
	}
	return s.SaveProject(root, project)
}

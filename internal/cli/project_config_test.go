package cli

import (
	"context"
	"io"
	"reflect"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
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

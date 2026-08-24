package config_test

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
	"gopkg.in/yaml.v3"
)

func completeProject() config.ProjectConfig {
	defaults := config.AgentSettings{
		Agent: config.AgentCodex, Model: "gpt-5", Effort: config.EffortHigh,
		Provenance: config.ModelProvenanceCatalog,
	}
	phases := make([]config.PhaseConfig, 0, len(config.CompletePhaseOrder()))
	for _, phase := range config.CompletePhaseOrder() {
		settings := defaults
		phases = append(phases, config.PhaseConfig{
			Phase: phase, Enabled: true, Required: contains(config.RequiredPhases(), phase), AgentSettings: settings,
		})
	}
	phases[5].Enabled = false // QA is optional.
	phases[5].AgentSettings.Model = "qa-model"
	return config.CompleteProjectConfig(config.CompleteSchemaVersion, defaults, phases, config.GitOpsOverride{})
}

func contains(phases []config.Phase, want config.Phase) bool {
	for _, phase := range phases {
		if phase == want {
			return true
		}
	}
	return false
}

func TestCompleteProjectConfigValidatesRequiredAndOptionalPhases(t *testing.T) {
	t.Parallel()

	valid := completeProject()
	if err := config.ValidateCompleteProjectConfig(valid); err != nil {
		t.Fatalf("valid complete config rejected: %v", err)
	}

	tests := []struct {
		name string
		edit func(*config.ProjectConfig)
		want string
	}{
		{name: "missing tuple field", edit: func(cfg *config.ProjectConfig) { cfg.Phases[0].AgentSettings.Model = "" }, want: "phases[0].settings.model"},
		{name: "required disabled", edit: func(cfg *config.ProjectConfig) { cfg.Phases[0].Enabled = false }, want: "required phase"},
		{name: "required marked optional", edit: func(cfg *config.ProjectConfig) { cfg.Phases[0].Required = false }, want: "phases[0].required"},
		{name: "optional marked required", edit: func(cfg *config.ProjectConfig) { cfg.Phases[5].Required = true }, want: "phases[5].required"},
		{name: "phase order", edit: func(cfg *config.ProjectConfig) { cfg.Phases[0], cfg.Phases[1] = cfg.Phases[1], cfg.Phases[0] }, want: "expected ordered phase"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid.Clone()
			tt.edit(&cfg)
			err := config.ValidateCompleteProjectConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validation error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCompleteProjectRoundTripAndClassification(t *testing.T) {
	t.Parallel()

	store := config.NewStore()
	root := t.TempDir()
	project := completeProject()
	if err := store.SaveProject(root, project); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	loaded, err := store.LoadProjectClassified(root)
	if err != nil {
		t.Fatalf("LoadProjectClassified: %v", err)
	}
	if loaded.Classification != config.ProjectConfigComplete || loaded.ValidationErr != nil {
		t.Fatalf("classification = %#v, want complete without validation error", loaded)
	}
	if !reflect.DeepEqual(loaded.Config, project) {
		t.Fatalf("loaded complete config = %#v, want %#v", loaded.Config, project)
	}
}

func TestSaveCompleteConfigurationMaterializesOnlyAtExplicitBoundary(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := config.NewStore()
	root := t.TempDir()
	global := config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortMedium}}
	sparse := config.ProjectConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettingsOverride{Model: "folder-model"}}
	if err := store.SaveCompleteConfiguration(root, global, sparse); err != nil {
		t.Fatalf("SaveCompleteConfiguration: %v", err)
	}
	loaded, err := store.LoadProjectClassified(root)
	if err != nil {
		t.Fatalf("LoadProjectClassified: %v", err)
	}
	if loaded.Classification != config.ProjectConfigComplete {
		t.Fatalf("classification = %q, want complete", loaded.Classification)
	}
	if loaded.Config.Defaults.Model != "folder-model" || loaded.Config.Defaults.Provenance != config.ModelProvenanceManual {
		t.Fatalf("materialized default = %#v", loaded.Config.Defaults)
	}
}

func TestSparseProjectRequiresMigrationWithoutRewriteAndCanBePrefilled(t *testing.T) {
	t.Parallel()

	store := config.NewStore()
	root := t.TempDir()
	legacy := []byte("version: 1\ndefaults:\n  model: old-model\nphase_overrides:\n  qa:\n    enabled: false\n")
	if err := os.MkdirAll(root+"/.gg", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ProjectConfigPath(root), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadProjectClassified(root)
	if err != nil {
		t.Fatalf("LoadProjectClassified: %v", err)
	}
	if loaded.Classification != config.ProjectConfigMigrationRequired {
		t.Fatalf("classification = %q, want migration_required", loaded.Classification)
	}
	after, err := os.ReadFile(store.ProjectConfigPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, legacy) {
		t.Fatal("loading sparse config rewrote the legacy file")
	}

	global := config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettings{Agent: config.AgentCodex, Model: "global-model", Effort: config.EffortMedium}}
	prefilled, err := config.MaterializeCompleteProjectConfig(global, &loaded.Config)
	if err != nil {
		t.Fatalf("MaterializeCompleteProjectConfig: %v", err)
	}
	if err := config.ValidateCompleteProjectConfig(prefilled); err != nil {
		t.Fatalf("prefilled config invalid: %v", err)
	}
	if prefilled.Phases[5].Enabled {
		t.Fatal("prefill lost the sparse QA disable")
	}
}

func TestSparseCompleteShapeRequiresMigrationAndPrefillsMissingNewerPhase(t *testing.T) {
	t.Parallel()

	store := config.NewStore()
	root := t.TempDir()
	project := completeProject()
	project.Version = config.CompleteSchemaVersion - 1
	project.Phases = project.Phases[:len(project.Phases)-1]
	if err := os.MkdirAll(root+"/.gg", 0700); err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(project)
	if err != nil {
		t.Fatalf("marshal legacy complete shape: %v", err)
	}
	if err := os.WriteFile(store.ProjectConfigPath(root), data, 0600); err != nil {
		t.Fatalf("write legacy complete shape: %v", err)
	}
	before, err := os.ReadFile(store.ProjectConfigPath(root))
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := store.LoadProjectClassified(root)
	if err != nil {
		t.Fatalf("LoadProjectClassified: %v", err)
	}
	if loaded.Classification != config.ProjectConfigMigrationRequired {
		t.Fatalf("classification = %q, want migration_required", loaded.Classification)
	}

	global := config.GlobalConfig{
		Version:  config.CurrentSchemaVersion,
		Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "global-model", Effort: config.EffortMedium},
	}
	prefilled, err := config.MaterializeCompleteProjectConfig(global, &loaded.Config)
	if err != nil {
		t.Fatalf("MaterializeCompleteProjectConfig: %v", err)
	}
	if err := config.ValidateCompleteProjectConfig(prefilled); err != nil {
		t.Fatalf("prefilled config invalid: %v", err)
	}
	if got := prefilled.Phases[len(prefilled.Phases)-1].Phase; got != config.PhaseCI {
		t.Fatalf("prefilled final phase = %q, want %q", got, config.PhaseCI)
	}
	after, err := os.ReadFile(store.ProjectConfigPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("loading or prefilling sparse config rewrote the legacy file")
	}
}

func TestPartialPhaseTupleRequiresMigrationAndInvalidPhaseDataIsMalformed(t *testing.T) {
	t.Parallel()

	partial := completeProject()
	partial.Version = config.CompleteSchemaVersion - 1
	partial.Phases[0].AgentSettings.Model = ""
	if got := config.ClassifyProjectConfig(partial); got != config.ProjectConfigMigrationRequired {
		t.Fatalf("partial tuple classification = %q, want migration_required", got)
	}

	malformed := completeProject()
	malformed.Phases[0].AgentSettings.Agent = config.Agent("unsupported")
	if got := config.ClassifyProjectConfig(malformed); got != config.ProjectConfigMalformed {
		t.Fatalf("invalid agent classification = %q, want malformed", got)
	}

	legacyWithBadOverride := config.ProjectConfig{
		Version:        config.CurrentSchemaVersion,
		PhaseOverrides: map[config.Phase]config.PhaseOverride{config.Phase("unknown"): {}},
	}
	if got := config.ClassifyProjectConfig(legacyWithBadOverride); got != config.ProjectConfigMalformed {
		t.Fatalf("invalid sparse override classification = %q, want malformed", got)
	}
}

func TestCompleteResolutionIsSelfContainedAndCloneIsolated(t *testing.T) {
	t.Parallel()

	project := completeProject()
	global := config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "changed-global", Effort: config.EffortLow}}
	resolved, err := config.Resolve(global, &project, config.RunOverrides{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := resolved.Phases[config.PhaseQA].Model; got != "qa-model" {
		t.Fatalf("complete phase model = %q, want qa-model", got)
	}
	if got := resolved.Defaults.Model; got != "gpt-5" {
		t.Fatalf("complete default model = %q, want gpt-5", got)
	}

	clone := project.Clone()
	clone.Phases[0].AgentSettings.Model = "changed"
	if project.Phases[0].AgentSettings.Model == "changed" {
		t.Fatal("ProjectConfig.Clone aliases phase settings")
	}
}

func TestCompleteResolutionPreservesOptionalPhaseStateAgainstAmbientGitOps(t *testing.T) {
	t.Parallel()

	project := completeProject()
	project.Phases[8].Enabled = false // PR is optional in the complete schema.
	project.Phases[9].Enabled = false // CI is optional in the complete schema.
	global := config.GlobalConfig{
		Version:  config.CurrentSchemaVersion,
		Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "changed-global", Effort: config.EffortLow},
		GitOps:   config.GitOpsOverride{EnablePR: boolPtr(true), EnableCI: boolPtr(true)},
	}

	resolved, err := config.Resolve(global, &project, config.RunOverrides{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Phases[config.PhasePR].Enabled || resolved.Phases[config.PhaseCI].Enabled {
		t.Fatalf("complete optional phase state was overwritten: PR=%v CI=%v", resolved.Phases[config.PhasePR].Enabled, resolved.Phases[config.PhaseCI].Enabled)
	}

	resolved, err = config.Resolve(global, &project, config.RunOverrides{GitOps: config.GitOpsOverride{EnablePR: boolPtr(true)}})
	if err != nil {
		t.Fatalf("Resolve with run override: %v", err)
	}
	if !resolved.Phases[config.PhasePR].Enabled || resolved.Phases[config.PhaseCI].Enabled {
		t.Fatalf("run GitOps override was not applied: PR=%v CI=%v", resolved.Phases[config.PhasePR].Enabled, resolved.Phases[config.PhaseCI].Enabled)
	}
}

func TestCatalogValidatesOwnershipButAllowsManualModels(t *testing.T) {
	t.Parallel()

	catalog := config.NewAgentCatalog(
		config.AgentCatalogEntry{Agent: config.AgentClaude, Models: []string{"sonnet"}, ModelListStatus: config.ModelListAvailable},
		config.AgentCatalogEntry{Agent: config.AgentCodex, Models: []string{"gpt-5"}, ModelListStatus: config.ModelListAvailable},
	)
	settings := config.AgentSettings{Agent: config.AgentCodex, Model: "sonnet", Effort: config.EffortHigh, Provenance: config.ModelProvenanceCatalog}
	if err := catalog.ValidateSettings(settings); err == nil {
		t.Fatal("catalog accepted a known cross-agent model")
	}
	settings.Provenance = config.ModelProvenanceManual
	if err := catalog.ValidateSettings(settings); err != nil {
		t.Fatalf("manual model rejected: %v", err)
	}
}

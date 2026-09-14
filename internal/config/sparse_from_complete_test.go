package config_test

import (
	"reflect"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
)

func TestSparseFromCompleteDropsPinsMatchingDefaultsAndKeepsDifferences(t *testing.T) {
	sparse := config.SparseFromComplete(completeProject())

	if sparse.Phases != nil {
		t.Fatalf("phases = %#v, want nil in the sparse form", sparse.Phases)
	}
	if sparse.Version != config.CurrentSchemaVersion {
		t.Fatalf("version = %d, want the sparse schema version %d", sparse.Version, config.CurrentSchemaVersion)
	}
	if sparse.Defaults.Agent != config.AgentCodex || sparse.Defaults.Model != "gpt-5" || sparse.Defaults.Effort != config.EffortHigh {
		t.Fatalf("defaults = %#v, want the folder defaults preserved", sparse.Defaults)
	}
	qa := sparse.PhaseOverrides[config.PhaseQA]
	if qa.Enabled == nil || *qa.Enabled {
		t.Fatalf("qa enabled = %#v, want the baked disabled state pinned", qa.Enabled)
	}
	if qa.Agent != "" || qa.Model != "qa-model" || qa.Effort != "" {
		t.Fatalf("qa pin = %#v, want only the differing model pinned", qa.AgentSettingsOverride)
	}
	planning := sparse.PhaseOverrides[config.PhasePlanning]
	if planning.Enabled == nil || !*planning.Enabled {
		t.Fatalf("planning enabled = %#v, want the baked enabled state pinned", planning.Enabled)
	}
	if planning.AgentSettingsOverride != (config.AgentSettingsOverride{}) {
		t.Fatalf("planning pin = %#v, want inherit: its tuple matches the folder defaults", planning.AgentSettingsOverride)
	}
	if override, ok := sparse.PhaseOverrides[config.PhaseDevelopment]; ok {
		t.Fatalf("development override = %#v, want none for a fixed phase matching the defaults", override)
	}
}

func TestSparseFromCompleteKeepsExistingSparseOverridePrecedence(t *testing.T) {
	enabled := true
	project := completeProject()
	project.PhaseOverrides = map[config.Phase]config.PhaseOverride{
		config.PhaseQA: {Enabled: &enabled, AgentSettingsOverride: config.AgentSettingsOverride{Agent: config.AgentClaude, Model: "edited-model", Effort: config.EffortLow}},
	}

	sparse := config.SparseFromComplete(project)

	qa := sparse.PhaseOverrides[config.PhaseQA]
	if qa.Enabled == nil || !*qa.Enabled {
		t.Fatalf("qa enabled = %#v, want the existing override kept over the baked disabled entry", qa.Enabled)
	}
	if qa.Agent != config.AgentClaude || qa.Model != "edited-model" || qa.Effort != config.EffortLow {
		t.Fatalf("qa pin = %#v, want the existing override kept over the baked tuple", qa.AgentSettingsOverride)
	}
}

func TestMaterializeCompleteProjectConfigPrefersFreshOverridesOverBakedPhases(t *testing.T) {
	enabled := true
	global := config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortMedium}}
	project := completeProject()
	project.PhaseOverrides = map[config.Phase]config.PhaseOverride{
		config.PhaseQA: {Enabled: &enabled, AgentSettingsOverride: config.AgentSettingsOverride{Agent: config.AgentClaude, Model: "edited-model", Effort: config.EffortLow}},
	}

	materialized, err := config.MaterializeCompleteProjectConfig(global, &project)
	if err != nil {
		t.Fatalf("MaterializeCompleteProjectConfig: %v", err)
	}
	for _, entry := range materialized.Phases {
		if entry.Phase != config.PhaseQA {
			continue
		}
		if !entry.Enabled {
			t.Fatalf("qa = %#v, want the fresh enabled override, not the baked disabled entry", entry)
		}
		if entry.AgentSettings.Agent != config.AgentClaude || entry.AgentSettings.Model != "edited-model" || entry.AgentSettings.Effort != config.EffortLow {
			t.Fatalf("qa settings = %#v, want the fresh override, not the baked tuple", entry.AgentSettings)
		}
		return
	}
	t.Fatal("materialized configuration is missing the qa phase")
}

func TestMaterializeCompleteProjectConfigRoundTripsUnchangedCompleteProject(t *testing.T) {
	global := config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortMedium}}
	project := completeProject()

	materialized, err := config.MaterializeCompleteProjectConfig(global, &project)
	if err != nil {
		t.Fatalf("MaterializeCompleteProjectConfig: %v", err)
	}
	if !reflect.DeepEqual(materialized, completeProject()) {
		t.Fatalf("materialized = %#v, want the complete configuration unchanged by a round trip", materialized)
	}
}

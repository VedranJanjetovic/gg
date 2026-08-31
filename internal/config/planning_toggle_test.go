package config_test

import (
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
)

func containsPhase(phases []config.Phase, want config.Phase) bool {
	for _, phase := range phases {
		if phase == want {
			return true
		}
	}
	return false
}

func TestPlanningIsUserDisableableAndSurvivesResolution(t *testing.T) {
	if containsPhase(config.RequiredPhases(), config.PhasePlanning) {
		t.Fatalf("planning must not be required; it is a user-toggleable phase")
	}
	if !containsPhase(config.OptionalPhases(), config.PhasePlanning) {
		t.Fatalf("planning is missing from OptionalPhases(): %v", config.OptionalPhases())
	}

	disabled := false
	settings := config.AgentSettings{Agent: config.AgentClaude, Model: "m", Effort: config.EffortHigh, Provenance: config.ModelProvenanceManual}
	global := config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: settings}
	project := config.ProjectConfig{
		Version:        config.CurrentSchemaVersion,
		PhaseOverrides: map[config.Phase]config.PhaseOverride{config.PhasePlanning: {Enabled: &disabled}},
	}

	resolved, err := config.Resolve(global, &project, config.RunOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Phases[config.PhasePlanning].Enabled {
		t.Fatalf("planning stayed enabled despite an explicit disable override")
	}

	complete, err := config.MaterializeCompleteProjectConfig(global, &project)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range complete.Phases {
		if entry.Phase != config.PhasePlanning {
			continue
		}
		if entry.Required || entry.Enabled {
			t.Fatalf("materialized planning entry = %+v, want required=false enabled=false", entry)
		}
	}
	if err := config.ValidateCompleteProjectConfig(complete); err != nil {
		t.Fatalf("complete configuration with planning disabled must validate: %v", err)
	}
}

func TestStaleRequiredPlanningFlagMigratesRatherThanFailing(t *testing.T) {
	settings := config.AgentSettings{Agent: config.AgentClaude, Model: "m", Effort: config.EffortHigh, Provenance: config.ModelProvenanceManual}
	phases := make([]config.PhaseConfig, 0, len(config.CompletePhaseOrder()))
	for _, phase := range config.CompletePhaseOrder() {
		// Mirrors a folder file written before planning became toggleable:
		// planning still carries required: true.
		required := containsPhase(config.RequiredPhases(), phase) || phase == config.PhasePlanning
		phases = append(phases, config.PhaseConfig{Phase: phase, Enabled: true, Required: required, AgentSettings: settings})
	}
	stale := config.CompleteProjectConfig(config.CompleteSchemaVersion, settings, phases, config.GitOpsOverride{})

	if got := config.ClassifyProjectConfig(stale); got != config.ProjectConfigMigrationRequired {
		t.Fatalf("classification = %v, want %v", got, config.ProjectConfigMigrationRequired)
	}
}

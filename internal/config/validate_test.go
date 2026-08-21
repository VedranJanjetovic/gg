package config_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
)

func validGlobalConfig() config.GlobalConfig {
	return config.GlobalConfig{
		Version:  config.CurrentSchemaVersion,
		Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "claude-sonnet", Effort: config.EffortMedium},
	}
}

func TestValidateGlobalConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		modify    func(*config.GlobalConfig)
		wantError string
	}{
		{name: "valid"},
		{name: "unsupported version", modify: func(cfg *config.GlobalConfig) { cfg.Version = 2 }, wantError: "version"},
		{name: "missing agent", modify: func(cfg *config.GlobalConfig) { cfg.Defaults.Agent = "" }, wantError: "defaults.agent"},
		{name: "unsupported agent", modify: func(cfg *config.GlobalConfig) { cfg.Defaults.Agent = "gemini" }, wantError: "defaults.agent"},
		{name: "missing model", modify: func(cfg *config.GlobalConfig) { cfg.Defaults.Model = "" }, wantError: "defaults.model"},
		{name: "blank model", modify: func(cfg *config.GlobalConfig) { cfg.Defaults.Model = "  \t" }, wantError: "defaults.model"},
		{name: "missing effort", modify: func(cfg *config.GlobalConfig) { cfg.Defaults.Effort = "" }, wantError: "defaults.effort"},
		{name: "unsupported effort", modify: func(cfg *config.GlobalConfig) { cfg.Defaults.Effort = "extreme" }, wantError: "defaults.effort"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := validGlobalConfig()
			if tt.modify != nil {
				tt.modify(&cfg)
			}
			assertValidationError(t, config.ValidateGlobalConfig(cfg), tt.wantError)
		})
	}
}

func TestValidateGlobalConfigAcceptsSupportedAgentsAndEfforts(t *testing.T) {
	t.Parallel()

	for _, agent := range []config.Agent{config.AgentClaude, config.AgentCodex} {
		for _, effort := range []config.Effort{config.EffortLow, config.EffortMedium, config.EffortHigh} {
			cfg := validGlobalConfig()
			cfg.Defaults.Agent = agent
			cfg.Defaults.Effort = effort
			if err := config.ValidateGlobalConfig(cfg); err != nil {
				t.Errorf("ValidateGlobalConfig(agent=%q, effort=%q): %v", agent, effort, err)
			}
		}
	}
}

func TestValidateProjectConfigAcceptsCanonicalRemovablePhases(t *testing.T) {
	t.Parallel()

	for _, phase := range config.RemovablePhases() {
		cfg := config.ProjectConfig{
			Version:        config.CurrentSchemaVersion,
			PhaseOverrides: map[config.Phase]config.PhaseOverride{phase: {}},
		}
		if err := config.ValidateProjectConfig(cfg); err != nil {
			t.Errorf("ValidateProjectConfig(phase=%q): %v", phase, err)
		}
	}
}

func TestValidateProjectConfig(t *testing.T) {
	t.Parallel()
	enabled := true
	tests := []struct {
		name      string
		config    config.ProjectConfig
		wantError string
	}{
		{name: "valid canonical phases and partial overrides", config: config.ProjectConfig{
			Version:  config.CurrentSchemaVersion,
			Defaults: config.AgentSettingsOverride{Agent: config.AgentCodex, Model: "gpt-5", Effort: config.EffortHigh},
			PhaseOverrides: map[config.Phase]config.PhaseOverride{
				config.PhaseQA: {Enabled: &enabled},
				config.PhaseCI: {AgentSettingsOverride: config.AgentSettingsOverride{Model: "fast-model"}},
			},
		}},
		{name: "valid empty overrides", config: config.ProjectConfig{Version: config.CurrentSchemaVersion}},
		{name: "valid linting alias", config: config.ProjectConfig{Version: config.CurrentSchemaVersion, PhaseOverrides: map[config.Phase]config.PhaseOverride{config.PhaseLintingAlias: {Enabled: &enabled}}}},
		{name: "unsupported version", config: config.ProjectConfig{Version: 99}, wantError: "version"},
		{name: "invalid defaults agent", config: config.ProjectConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettingsOverride{Agent: "gemini"}}, wantError: "defaults.agent"},
		{name: "blank defaults model", config: config.ProjectConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettingsOverride{Model: "  "}}, wantError: "defaults.model"},
		{name: "invalid defaults effort", config: config.ProjectConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettingsOverride{Effort: "maximum"}}, wantError: "defaults.effort"},
		{name: "unknown phase", config: config.ProjectConfig{Version: config.CurrentSchemaVersion, PhaseOverrides: map[config.Phase]config.PhaseOverride{"deploy": {}}}, wantError: "phase_overrides[deploy]"},
		{name: "invalid phase agent", config: config.ProjectConfig{Version: config.CurrentSchemaVersion, PhaseOverrides: map[config.Phase]config.PhaseOverride{config.PhasePlanning: {AgentSettingsOverride: config.AgentSettingsOverride{Agent: "other"}}}}, wantError: "phase_overrides[planning].agent"},
		{name: "blank phase model", config: config.ProjectConfig{Version: config.CurrentSchemaVersion, PhaseOverrides: map[config.Phase]config.PhaseOverride{config.PhasePR: {AgentSettingsOverride: config.AgentSettingsOverride{Model: "\n"}}}}, wantError: "phase_overrides[pr].model"},
		{name: "invalid phase effort", config: config.ProjectConfig{Version: config.CurrentSchemaVersion, PhaseOverrides: map[config.Phase]config.PhaseOverride{config.PhaseGrooming: {AgentSettingsOverride: config.AgentSettingsOverride{Effort: "huge"}}}}, wantError: "phase_overrides[grooming].effort"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertValidationError(t, config.ValidateProjectConfig(tt.config), tt.wantError)
		})
	}
}

func TestValidateRunOverrides(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		overrides config.RunOverrides
		wantError string
	}{
		{name: "empty"},
		{name: "valid", overrides: config.RunOverrides{Defaults: config.AgentSettingsOverride{Agent: config.AgentClaude}}},
		{name: "invalid defaults", overrides: config.RunOverrides{Defaults: config.AgentSettingsOverride{Effort: "turbo"}}, wantError: "defaults.effort"},
		{name: "unknown phase", overrides: config.RunOverrides{PhaseOverrides: map[config.Phase]config.PhaseOverride{"release": {}}}, wantError: "phase_overrides[release]"},
		{name: "invalid phase field", overrides: config.RunOverrides{PhaseOverrides: map[config.Phase]config.PhaseOverride{config.PhaseCI: {AgentSettingsOverride: config.AgentSettingsOverride{Agent: "unknown"}}}}, wantError: "phase_overrides[ci].agent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertValidationError(t, config.ValidateRunOverrides(tt.overrides), tt.wantError)
		})
	}
}

func TestNormalizePhaseOverrides(t *testing.T) {
	t.Parallel()
	disabled := false
	original := map[config.Phase]config.PhaseOverride{
		config.PhaseLintingAlias: {Enabled: &disabled},
		config.PhaseQA:           {AgentSettingsOverride: config.AgentSettingsOverride{Model: "qa-model"}},
	}
	normalized := config.NormalizePhaseOverrides(original)
	want := map[config.Phase]config.PhaseOverride{
		config.PhaseBuildChecker: {Enabled: &disabled},
		config.PhaseQA:           {AgentSettingsOverride: config.AgentSettingsOverride{Model: "qa-model"}},
	}
	if !reflect.DeepEqual(normalized, want) {
		t.Fatalf("NormalizePhaseOverrides() = %#v, want %#v", normalized, want)
	}
	if _, exists := original[config.PhaseLintingAlias]; !exists {
		t.Fatal("NormalizePhaseOverrides mutated its input")
	}
	normalized[config.PhaseQA] = config.PhaseOverride{}
	if original[config.PhaseQA].Model != "qa-model" {
		t.Fatal("NormalizePhaseOverrides result aliases the input map")
	}
}

func TestNormalizePhaseOverridesCanonicalValueWinsAliasCollision(t *testing.T) {
	t.Parallel()
	enabled, disabled := true, false
	canonical := config.PhaseOverride{Enabled: &enabled}
	original := map[config.Phase]config.PhaseOverride{
		config.PhaseLintingAlias: {Enabled: &disabled},
		config.PhaseBuildChecker: canonical,
	}
	normalized := config.NormalizePhaseOverrides(original)
	if got := normalized[config.PhaseBuildChecker]; !reflect.DeepEqual(got, canonical) {
		t.Fatalf("canonical build_checker override = %#v, want %#v", got, canonical)
	}
	if len(normalized) != 1 {
		t.Fatalf("normalized phase count = %d, want 1", len(normalized))
	}
}

func TestNormalizePhaseOverridesPreservesNil(t *testing.T) {
	t.Parallel()
	if got := config.NormalizePhaseOverrides(nil); got != nil {
		t.Fatalf("NormalizePhaseOverrides(nil) = %#v, want nil", got)
	}
}

func assertValidationError(t *testing.T, err error, wantField string) {
	t.Helper()
	if wantField == "" {
		if err != nil {
			t.Fatalf("validation returned unexpected error: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("validation returned nil, want error containing %q", wantField)
	}
	if !strings.Contains(err.Error(), wantField) {
		t.Fatalf("validation error = %q, want field %q", err, wantField)
	}
}

func TestValidateGitOpsRejectsBlankOverrides(t *testing.T) {
	for _, field := range []config.GitOpsOverride{{ParentBranch: " "}, {BaseRef: "\t"}} {
		if err := config.ValidateProjectConfig(config.ProjectConfig{Version: config.CurrentSchemaVersion, GitOps: field}); err == nil {
			t.Fatalf("ValidateProjectConfig(%#v) accepted blank GitOps value", field)
		}
	}
}

package config_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
)

func TestResolveLayerPrecedence(t *testing.T) {
	t.Parallel()

	disabled := false
	enabled := true
	global := validGlobalConfig()
	tests := []struct {
		name    string
		project *config.ProjectConfig
		run     config.RunOverrides
		want    map[config.Phase]config.ResolvedPhase
	}{
		{
			name: "missing project inherits global settings and built-in enabled defaults",
			want: map[config.Phase]config.ResolvedPhase{
				config.PhaseGrooming: resolved(true, config.AgentClaude, "claude-sonnet", config.EffortMedium),
				config.PhaseQA:       resolved(true, config.AgentClaude, "claude-sonnet", config.EffortMedium),
			},
		},
		{
			name: "project defaults and phase values override inherited fields",
			project: &config.ProjectConfig{
				Version:  config.CurrentSchemaVersion,
				Defaults: config.AgentSettingsOverride{Model: "project-model", Effort: config.EffortHigh},
				PhaseOverrides: map[config.Phase]config.PhaseOverride{
					config.PhaseQA: {
						Enabled: &disabled,
						AgentSettingsOverride: config.AgentSettingsOverride{
							Agent: config.AgentCodex,
						},
					},
				},
			},
			want: map[config.Phase]config.ResolvedPhase{
				config.PhaseGrooming: resolved(true, config.AgentClaude, "project-model", config.EffortHigh),
				config.PhaseQA:       resolved(false, config.AgentCodex, "project-model", config.EffortHigh),
			},
		},
		{
			name: "one-run layer overrides project defaults and phase values",
			project: &config.ProjectConfig{
				Version:  config.CurrentSchemaVersion,
				Defaults: config.AgentSettingsOverride{Agent: config.AgentCodex, Model: "project-model", Effort: config.EffortHigh},
				PhaseOverrides: map[config.Phase]config.PhaseOverride{
					config.PhasePlanning: {Enabled: &disabled, AgentSettingsOverride: config.AgentSettingsOverride{Model: "project-planning"}},
				},
			},
			run: config.RunOverrides{
				Defaults: config.AgentSettingsOverride{Agent: config.AgentClaude, Effort: config.EffortLow},
				PhaseOverrides: map[config.Phase]config.PhaseOverride{
					config.PhasePlanning: {Enabled: &enabled, AgentSettingsOverride: config.AgentSettingsOverride{Model: "run-planning"}},
				},
			},
			want: map[config.Phase]config.ResolvedPhase{
				config.PhaseGrooming: resolved(true, config.AgentClaude, "project-model", config.EffortLow),
				config.PhasePlanning: resolved(true, config.AgentClaude, "run-planning", config.EffortLow),
			},
		},
		{
			name: "linting aliases normalize and canonical key wins within a layer",
			project: &config.ProjectConfig{
				Version: config.CurrentSchemaVersion,
				PhaseOverrides: map[config.Phase]config.PhaseOverride{
					config.PhaseLintingAlias: {Enabled: &disabled, AgentSettingsOverride: config.AgentSettingsOverride{Model: "alias-project"}},
				},
			},
			run: config.RunOverrides{PhaseOverrides: map[config.Phase]config.PhaseOverride{
				config.PhaseLintingAlias: {Enabled: &disabled, AgentSettingsOverride: config.AgentSettingsOverride{Model: "alias-run"}},
				config.PhaseBuildChecker: {Enabled: &enabled, AgentSettingsOverride: config.AgentSettingsOverride{Model: "canonical-run"}},
			}},
			want: map[config.Phase]config.ResolvedPhase{
				config.PhaseBuildChecker: resolved(true, config.AgentClaude, "canonical-run", config.EffortMedium),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := config.Resolve(global, tt.project, tt.run)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			assertStablePhaseKeys(t, got.Phases)
			for phase, want := range tt.want {
				if actual := got.Phases[phase]; !reflect.DeepEqual(actual, want) {
					t.Errorf("Resolve().Phases[%s] = %#v, want %#v", phase, actual, want)
				}
			}
		})
	}
}

func TestResolveExposesEffectiveDefaultsForMandatoryPhases(t *testing.T) {
	t.Parallel()

	got, err := config.Resolve(
		config.GlobalConfig{
			Version:  config.CurrentSchemaVersion,
			Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "global-model", Effort: config.EffortLow},
		},
		&config.ProjectConfig{
			Version:  config.CurrentSchemaVersion,
			Defaults: config.AgentSettingsOverride{Model: "project-model"},
		},
		config.RunOverrides{Defaults: config.AgentSettingsOverride{Agent: config.AgentCodex, Effort: config.EffortHigh}},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	want := config.AgentSettings{Agent: config.AgentCodex, Model: "project-model", Effort: config.EffortHigh}
	if got.Defaults != want {
		t.Fatalf("Resolve().Defaults = %#v, want %#v", got.Defaults, want)
	}
}

func TestResolveRejectsInvalidLayers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		global    config.GlobalConfig
		project   *config.ProjectConfig
		run       config.RunOverrides
		wantError string
	}{
		{name: "global", global: config.GlobalConfig{Version: config.CurrentSchemaVersion}, wantError: "global config: defaults.agent"},
		{name: "project", global: validGlobalConfig(), project: &config.ProjectConfig{Version: 99}, wantError: "project config: version"},
		{name: "runtime defaults", global: validGlobalConfig(), run: config.RunOverrides{Defaults: config.AgentSettingsOverride{Effort: "extreme"}}, wantError: "run overrides: defaults.effort"},
		{name: "runtime phase", global: validGlobalConfig(), run: config.RunOverrides{PhaseOverrides: map[config.Phase]config.PhaseOverride{"deploy": {}}}, wantError: "run overrides: phase_overrides[deploy]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := config.Resolve(tt.global, tt.project, tt.run)
			if err == nil {
				t.Fatalf("Resolve() error = nil, want %q", tt.wantError)
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Resolve() error = %q, want substring %q", err, tt.wantError)
			}
			if got.Phases != nil {
				t.Fatalf("Resolve() on error returned phases: %#v", got.Phases)
			}
		})
	}
}

func TestResolveDoesNotMutateInputs(t *testing.T) {
	t.Parallel()

	projectEnabled := false
	runEnabled := true
	project := config.ProjectConfig{
		Version:  config.CurrentSchemaVersion,
		Defaults: config.AgentSettingsOverride{Model: "project-model"},
		PhaseOverrides: map[config.Phase]config.PhaseOverride{
			config.PhaseLintingAlias: {Enabled: &projectEnabled, AgentSettingsOverride: config.AgentSettingsOverride{Agent: config.AgentCodex}},
		},
	}
	run := config.RunOverrides{
		Defaults: config.AgentSettingsOverride{Effort: config.EffortHigh},
		PhaseOverrides: map[config.Phase]config.PhaseOverride{
			config.PhaseQA: {Enabled: &runEnabled, AgentSettingsOverride: config.AgentSettingsOverride{Model: "run-qa"}},
		},
	}
	wantProject := cloneProjectConfig(project)
	wantRun := cloneRunOverrides(run)

	resolvedConfig, err := config.Resolve(validGlobalConfig(), &project, run)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	resolvedConfig.Phases[config.PhaseBuildChecker] = config.ResolvedPhase{}

	if !reflect.DeepEqual(project, wantProject) {
		t.Errorf("Resolve() mutated project input: got %#v, want %#v", project, wantProject)
	}
	if !reflect.DeepEqual(run, wantRun) {
		t.Errorf("Resolve() mutated run input: got %#v, want %#v", run, wantRun)
	}
}

func resolved(enabled bool, agent config.Agent, model string, effort config.Effort) config.ResolvedPhase {
	return config.ResolvedPhase{
		Enabled: enabled,
		AgentSettings: config.AgentSettings{
			Agent:  agent,
			Model:  model,
			Effort: effort,
		},
	}
}

func assertStablePhaseKeys(t *testing.T, phases map[config.Phase]config.ResolvedPhase) {
	t.Helper()
	want := append(config.RemovablePhases(), config.FixedPhases()...)
	if len(phases) != len(want) {
		t.Fatalf("resolved phase count = %d, want %d", len(phases), len(want))
	}
	for _, phase := range want {
		if _, ok := phases[phase]; !ok {
			t.Errorf("resolved phases missing canonical key %q", phase)
		}
	}
	for _, phase := range config.FixedPhases() {
		if !phases[phase].Enabled {
			t.Errorf("fixed phase %q resolved as disabled", phase)
		}
	}
	if _, ok := phases[config.PhaseLintingAlias]; ok {
		t.Errorf("resolved phases contain alias key %q", config.PhaseLintingAlias)
	}
}

func cloneProjectConfig(input config.ProjectConfig) config.ProjectConfig {
	cloned := input
	cloned.PhaseOverrides = clonePhaseOverrides(input.PhaseOverrides)
	return cloned
}

func cloneRunOverrides(input config.RunOverrides) config.RunOverrides {
	cloned := input
	cloned.PhaseOverrides = clonePhaseOverrides(input.PhaseOverrides)
	return cloned
}

func clonePhaseOverrides(input map[config.Phase]config.PhaseOverride) map[config.Phase]config.PhaseOverride {
	if input == nil {
		return nil
	}
	cloned := make(map[config.Phase]config.PhaseOverride, len(input))
	for phase, override := range input {
		if override.Enabled != nil {
			enabled := *override.Enabled
			override.Enabled = &enabled
		}
		cloned[phase] = override
	}
	return cloned
}

func TestResolveGitOpsDefaultsAndLayeredOverrides(t *testing.T) {
	got, err := config.Resolve(validGlobalConfig(), &config.ProjectConfig{Version: config.CurrentSchemaVersion, GitOps: config.GitOpsOverride{ParentBranch: "develop", EnablePR: boolPtr(false)}}, config.RunOverrides{GitOps: config.GitOpsOverride{BaseRef: "origin/develop", EnableCI: boolPtr(false)}})
	if err != nil {
		t.Fatal(err)
	}
	if got.GitOps.ParentBranch != "develop" || got.GitOps.BaseRef != "origin/develop" || got.GitOps.EnablePR || got.GitOps.EnableCI {
		t.Fatalf("GitOps = %#v", got.GitOps)
	}
	if got.Phases[config.PhasePR].Enabled || got.Phases[config.PhaseCI].Enabled {
		t.Fatalf("GitOps toggles did not disable phases: %#v", got.Phases)
	}
}

func boolPtr(value bool) *bool { return &value }

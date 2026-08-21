package config_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
)

func TestSchemaConstants(t *testing.T) {
	t.Parallel()

	if config.CurrentSchemaVersion != 1 {
		t.Fatalf("CurrentSchemaVersion = %d, want 1", config.CurrentSchemaVersion)
	}

	agents := map[config.Agent]string{
		config.AgentClaude: "claude",
		config.AgentCodex:  "codex",
	}
	for got, want := range agents {
		if string(got) != want {
			t.Errorf("agent constant = %q, want %q", got, want)
		}
	}

	efforts := map[config.Effort]string{
		config.EffortLow:    "low",
		config.EffortMedium: "medium",
		config.EffortHigh:   "high",
	}
	for got, want := range efforts {
		if string(got) != want {
			t.Errorf("effort constant = %q, want %q", got, want)
		}
	}
}

func TestRemovablePhases(t *testing.T) {
	t.Parallel()

	want := []config.Phase{
		config.PhaseGrooming,
		config.PhasePlanning,
		config.PhaseQA,
		config.PhaseBuildChecker,
		config.PhasePR,
		config.PhaseCI,
	}
	got := config.RemovablePhases()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RemovablePhases() = %v, want %v", got, want)
	}
	if config.PhaseLintingAlias != "linting" {
		t.Fatalf("PhaseLintingAlias = %q, want linting", config.PhaseLintingAlias)
	}

	got[0] = "changed"
	if next := config.RemovablePhases(); !reflect.DeepEqual(next, want) {
		t.Fatalf("RemovablePhases() exposed mutable package state: %v", next)
	}
}

func TestConfigurationContracts(t *testing.T) {
	t.Parallel()

	disabled := false
	global := config.GlobalConfig{
		Version: config.CurrentSchemaVersion,
		Defaults: config.AgentSettings{
			Agent:  config.AgentCodex,
			Model:  "gpt",
			Effort: config.EffortHigh,
		},
	}
	project := config.ProjectConfig{
		Version:  config.CurrentSchemaVersion,
		Defaults: config.AgentSettingsOverride{Model: "project-model"},
		PhaseOverrides: map[config.Phase]config.PhaseOverride{
			config.PhaseQA: {
				Enabled: &disabled,
				AgentSettingsOverride: config.AgentSettingsOverride{
					Agent: config.AgentClaude,
				},
			},
		},
	}
	run := config.RunOverrides{
		Defaults: config.AgentSettingsOverride{Effort: config.EffortLow},
		PhaseOverrides: map[config.Phase]config.PhaseOverride{
			config.PhaseCI: {Enabled: &disabled},
		},
	}

	if global.Defaults.Agent != config.AgentCodex {
		t.Errorf("global default agent = %q, want codex", global.Defaults.Agent)
	}
	if project.PhaseOverrides[config.PhaseQA].Agent != config.AgentClaude {
		t.Errorf("project QA agent = %q, want claude", project.PhaseOverrides[config.PhaseQA].Agent)
	}
	if run.Defaults.Effort != config.EffortLow {
		t.Errorf("run effort = %q, want low", run.Defaults.Effort)
	}

	runJSON, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("marshal run overrides: %v", err)
	}
	if got, want := string(runJSON), `{}`; got != want {
		t.Errorf("run override JSON = %s, want %s", got, want)
	}

	encoded, err := json.Marshal(project.PhaseOverrides[config.PhaseQA])
	if err != nil {
		t.Fatalf("marshal phase override: %v", err)
	}
	if got, want := string(encoded), `{"enabled":false,"agent":"claude"}`; got != want {
		t.Errorf("phase override JSON = %s, want %s", got, want)
	}
}

func TestMissingConfigurationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "global",
			err:  config.ErrGlobalConfigNotFound,
			want: `gg global configuration is missing; run "gg configure"`,
		},
		{
			name: "project",
			err:  config.ErrProjectNotConfigured,
			want: `current project is not configured; run "gg configure" in the project folder`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !errors.Is(fmt.Errorf("load config: %w", tt.err), tt.err) {
				t.Fatalf("wrapped error must support sentinel comparison")
			}
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("error = %q, want %q", got, tt.want)
			}
		})
	}
}

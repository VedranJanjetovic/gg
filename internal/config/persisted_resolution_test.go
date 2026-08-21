package config_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
)

type pipelineConfigSource struct {
	global      config.GlobalConfig
	globalErr   error
	project     config.ProjectConfig
	projectErr  error
	projectRoot string
}

func (s *pipelineConfigSource) LoadGlobal() (config.GlobalConfig, error) {
	return s.global, s.globalErr
}

func (s *pipelineConfigSource) LoadProject(root string) (config.ProjectConfig, error) {
	s.projectRoot = root
	return s.project, s.projectErr
}

func TestResolvePipelineConfigUsesPersistedDefaultsAndPhaseOverrides(t *testing.T) {
	t.Parallel()

	disabled := false
	enabled := true
	source := &pipelineConfigSource{
		global: config.GlobalConfig{
			Version:  config.CurrentSchemaVersion,
			Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "global-model", Effort: config.EffortMedium},
		},
		project: config.ProjectConfig{
			Version:  config.CurrentSchemaVersion,
			Defaults: config.AgentSettingsOverride{Model: "project-model"},
			PhaseOverrides: map[config.Phase]config.PhaseOverride{
				config.PhaseQA: {Enabled: &disabled, AgentSettingsOverride: config.AgentSettingsOverride{Effort: config.EffortHigh}},
			},
		},
	}
	overrides := config.RunOverrides{PhaseOverrides: map[config.Phase]config.PhaseOverride{
		config.PhaseQA: {Enabled: &enabled, AgentSettingsOverride: config.AgentSettingsOverride{Agent: config.AgentCodex}},
	}}

	resolved, err := config.ResolvePipelineConfig(source, "/project", overrides)
	if err != nil {
		t.Fatalf("ResolvePipelineConfig() error = %v", err)
	}
	if source.projectRoot != "/project" {
		t.Fatalf("LoadProject() root = %q, want %q", source.projectRoot, "/project")
	}
	want := config.ResolvedPhase{Enabled: true, AgentSettings: config.AgentSettings{
		Agent: config.AgentCodex, Model: "project-model", Effort: config.EffortHigh,
	}}
	if got := resolved.Phases[config.PhaseQA]; !reflect.DeepEqual(got, want) {
		t.Errorf("resolved qa = %#v, want %#v", got, want)
	}
}

func TestResolvePipelineConfigReturnsSourceAndResolutionErrors(t *testing.T) {
	t.Parallel()

	globalErr := errors.New("global unavailable")
	projectErr := errors.New("project unavailable")
	tests := []struct {
		name    string
		source  *pipelineConfigSource
		run     config.RunOverrides
		wantErr error
		want    string
	}{
		{name: "global load", source: &pipelineConfigSource{globalErr: globalErr}, wantErr: globalErr, want: "load global configuration"},
		{name: "project load", source: &pipelineConfigSource{global: validGlobalConfig(), projectErr: projectErr}, wantErr: projectErr, want: "load project configuration"},
		{name: "run override validation", source: &pipelineConfigSource{global: validGlobalConfig(), project: config.ProjectConfig{Version: config.CurrentSchemaVersion}}, run: config.RunOverrides{Defaults: config.AgentSettingsOverride{Effort: "extreme"}}, want: "resolve pipeline configuration: run overrides"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, err := config.ResolvePipelineConfig(tt.source, "/project", tt.run)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ResolvePipelineConfig() error = %v, want %q", err, tt.want)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("ResolvePipelineConfig() error = %v, want wrapped %v", err, tt.wantErr)
			}
			if resolved.Phases != nil {
				t.Fatalf("ResolvePipelineConfig() result = %#v, want zero result", resolved)
			}
		})
	}
}

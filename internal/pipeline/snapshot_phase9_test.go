package pipeline_test

import (
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
)

func phase9ProjectConfig() config.ProjectConfig {
	defaults := config.AgentSettings{Agent: config.AgentCodex, Model: "gpt-default", Effort: config.EffortHigh, Provenance: config.ModelProvenanceCatalog}
	phases := make([]config.PhaseConfig, 0, len(config.CompletePhaseOrder()))
	for _, phase := range config.CompletePhaseOrder() {
		settings := defaults
		settings.Model = "model-" + string(phase)
		phases = append(phases, config.PhaseConfig{Phase: phase, Enabled: true, Required: containsPhase(config.RequiredPhases(), phase), AgentSettings: settings})
	}
	phases[5].Enabled = false
	phases[5].AgentSettings.Provenance = config.ModelProvenanceManual
	return config.CompleteProjectConfig(config.CompleteSchemaVersion, defaults, phases, config.GitOpsOverride{})
}

func containsPhase(phases []config.Phase, want config.Phase) bool {
	for _, phase := range phases {
		if phase == want {
			return true
		}
	}
	return false
}

func TestProjectSnapshotRetainsCompleteConfigurationAndDisabledPhases(t *testing.T) {
	project := phase9ProjectConfig()
	global := config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettings{Agent: project.Defaults.Agent, Model: project.Defaults.Model, Effort: project.Defaults.Effort, Provenance: project.Defaults.Provenance}}
	resolved, err := config.Resolve(global, &project, config.RunOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := pipeline.SnapshotProjectExecution(plan, pipeline.DevelopmentSubphaseGeneration{}, 3, project, resolved.GitOps)
	if err != nil {
		t.Fatal(err)
	}
	restored, _, attempts, err := pipeline.RestoreExecution(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	for _, phase := range restored.Phases() {
		if phase.Phase().ID() == pipeline.PhaseQA {
			t.Fatal("disabled QA phase was restored as executable")
		}
		if phase.Phase().ID() == pipeline.PhasePlanning {
			settings, _ := phase.Settings()
			if settings.Model != "model-planning" {
				t.Fatalf("planning model = %q", settings.Model)
			}
		}
	}
	resolvedSnapshot, err := pipeline.RestoreResolvedConfiguration(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if resolvedSnapshot.Defaults.Model != "gpt-default" || resolvedSnapshot.Phases[config.PhaseQA].Enabled {
		t.Fatalf("resolved snapshot = %#v", resolvedSnapshot)
	}
}

package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/git"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

func TestCLIResumeUsesPersistedResolvedRunPlanAfterAmbientConfigChanges(t *testing.T) {
	t.Skip("transient phase flags are removed; project snapshots now own creation configuration")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "commit.gpgsign")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")
	root := t.TempDir()
	initTestRepository(t, root)
	configStore := configuredMemoryStore()
	stateStore := mustStateStore(t, root)
	lifecycle := state.NewLifecycleService(stateStore, nil, stateStore.Locker())
	controller := &captureController{}
	app := New(
		WithRootResolver(fixedRoot{root: root}),
		WithConfigStore(configStore),
		WithLifecycleService(lifecycle),
		WithGitClient(git.NewClient(root, nil)),
		WithOrchestratorController(controller),
	)

	var stdout, stderr bytes.Buffer
	args := []string{
		"run", "snapshot-project",
		"--disable-phase", "planning",
		"--phase-model", "grooming=run-only-model",
	}
	if code := app.Run(context.Background(), args, &stdout, &stderr); code != 0 {
		t.Fatalf("run exit=%d stderr=%q", code, stderr.String())
	}
	if len(controller.executes) != 1 {
		t.Fatalf("execute requests = %d, want 1", len(controller.executes))
	}
	assertPersistedRunPlan(t, controller.executes[0].Pipeline)

	if err := lifecycle.CloseRun(context.Background(), "snapshot-project", state.StatusStopped); err != nil {
		t.Fatal(err)
	}

	// Simulate configuration changes made after the original run. Resume must
	// reconstruct the original executable plan/settings from ProjectState, not
	// resolve these ambient layers again.
	disabled := false
	enabled := true
	configStore.global.Defaults.Model = "ambient-model"
	configStore.project.PhaseOverrides = map[config.Phase]config.PhaseOverride{
		config.PhaseGrooming: {Enabled: &disabled},
		config.PhasePlanning: {
			Enabled: &enabled,
			AgentSettingsOverride: config.AgentSettingsOverride{
				Model: "ambient-planning-model",
			},
		},
	}

	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"resume", "snapshot-project"}, &stdout, &stderr); code != 0 {
		t.Fatalf("resume exit=%d stderr=%q", code, stderr.String())
	}
	if len(controller.resumes) != 1 {
		t.Fatalf("resume requests = %d, want 1", len(controller.resumes))
	}
	assertPersistedRunPlan(t, controller.resumes[0].Execution.Pipeline)
}

func assertPersistedRunPlan(t *testing.T, plan pipeline.ExecutablePipeline) {
	t.Helper()
	seenPlanning := false
	seenGrooming := false
	for _, executable := range plan.Phases() {
		switch executable.Phase().ID() {
		case pipeline.PhasePlanning:
			seenPlanning = true
		case pipeline.PhaseGrooming:
			seenGrooming = true
			settings, ok := executable.Settings()
			if !ok || settings.Model != "run-only-model" {
				t.Fatalf("grooming settings = %#v, present=%v; want persisted run-only model", settings, ok)
			}
		}
	}
	if seenPlanning {
		t.Fatal("persisted run-disabled Planning phase was re-enabled")
	}
	if !seenGrooming {
		t.Fatal("persisted Grooming phase was disabled by later ambient config")
	}
}

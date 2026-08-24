package pipeline_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

func TestResolveBuildsExecutablePipeline(t *testing.T) {
	t.Parallel()

	disabled := false
	enabled := true

	tests := []struct {
		name     string
		resolved config.ResolvedConfig
		wantIDs  []pipeline.PhaseID
		wantQA   config.AgentSettings
	}{
		{
			name:     "preserves the canonical default pipeline",
			resolved: resolvedConfig(),
			wantIDs: []pipeline.PhaseID{
				pipeline.PhaseAcceptanceCriteria, pipeline.PhaseGrooming, pipeline.PhasePlanning,
				pipeline.PhaseDevelopment, pipeline.PhaseRebase, pipeline.PhaseQA,
				pipeline.PhaseTestDocument, pipeline.PhaseBuildChecker, pipeline.PhasePR, pipeline.PhaseCI,
			},
			wantQA: config.AgentSettings{Agent: config.AgentClaude, Model: "default-model", Effort: config.EffortMedium},
		},
		{
			name: "omits disabled optional phases while preserving required phases",
			resolved: resolvedConfigWith(func(phases map[config.Phase]config.ResolvedPhase) {
				qa := phases[config.PhaseQA]
				qa.Enabled = false
				phases[config.PhaseQA] = qa
				buildChecker := phases[config.PhaseBuildChecker]
				buildChecker.Enabled = false
				phases[config.PhaseBuildChecker] = buildChecker
			}),
			wantIDs: []pipeline.PhaseID{
				pipeline.PhaseAcceptanceCriteria, pipeline.PhaseGrooming, pipeline.PhasePlanning,
				pipeline.PhaseDevelopment, pipeline.PhaseRebase, pipeline.PhaseTestDocument,
				pipeline.PhasePR, pipeline.PhaseCI,
			},
		},
		{
			name: "uses enabled run override after a project disabled the phase",
			resolved: resolveConfig(t,
				&config.ProjectConfig{
					Version: config.CurrentSchemaVersion,
					PhaseOverrides: map[config.Phase]config.PhaseOverride{
						config.PhaseQA: {Enabled: &disabled},
					},
				},
				config.RunOverrides{PhaseOverrides: map[config.Phase]config.PhaseOverride{
					config.PhaseQA: {
						Enabled: &enabled,
						AgentSettingsOverride: config.AgentSettingsOverride{
							Agent:  config.AgentCodex,
							Model:  "qa-model",
							Effort: config.EffortHigh,
						},
					},
				}}),
			wantIDs: []pipeline.PhaseID{
				pipeline.PhaseAcceptanceCriteria, pipeline.PhaseGrooming, pipeline.PhasePlanning,
				pipeline.PhaseDevelopment, pipeline.PhaseRebase, pipeline.PhaseQA,
				pipeline.PhaseTestDocument, pipeline.PhaseBuildChecker, pipeline.PhasePR, pipeline.PhaseCI,
			},
			wantQA: config.AgentSettings{Agent: config.AgentCodex, Model: "qa-model", Effort: config.EffortHigh},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := pipeline.Resolve(pipeline.DefaultPipeline(), tt.resolved)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if ids := executableIDs(got); !reflect.DeepEqual(ids, tt.wantIDs) {
				t.Errorf("Resolve() phase IDs = %v, want %v", ids, tt.wantIDs)
			}

			qa, exists := executablePhase(got, pipeline.PhaseQA)
			if tt.wantQA == (config.AgentSettings{}) {
				if exists {
					t.Errorf("Resolve() includes disabled QA phase")
				}
				return
			}
			if !exists {
				t.Fatalf("Resolve() does not include QA phase")
			}
			settings, configured := qa.Settings()
			if !configured || settings != tt.wantQA {
				t.Errorf("QA settings = (%#v, %t), want (%#v, true)", settings, configured, tt.wantQA)
			}
		})
	}
}

func TestResolveUsesResolvedDefaultsForMandatoryPhases(t *testing.T) {
	resolved, err := config.Resolve(config.GlobalConfig{
		Version:  config.CurrentSchemaVersion,
		Defaults: config.AgentSettings{Agent: config.AgentCodex, Model: "mandatory-model", Effort: config.EffortHigh},
	}, nil, config.RunOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []pipeline.PhaseID{pipeline.PhaseAcceptanceCriteria, pipeline.PhaseDevelopment, pipeline.PhaseRebase, pipeline.PhaseTestDocument} {
		phase, ok := executablePhase(got, id)
		if !ok {
			t.Fatalf("mandatory phase %q missing", id)
		}
		settings, configured := phase.Settings()
		if !configured || settings != resolved.Defaults {
			t.Errorf("phase %q settings = (%#v, %t), want (%#v, true)", id, settings, configured, resolved.Defaults)
		}
	}
}

func TestResolveRejectsInvalidDefinitionsAndConfiguration(t *testing.T) {
	t.Parallel()

	unknown := pipeline.DefaultPipeline().Phases()
	unknown[0] = pipeline.NewPhase("release", pipeline.PhaseMetadata{DisplayName: "Release"})
	duplicate := pipeline.DefaultPipeline().Phases()
	duplicate = append(duplicate, duplicate[len(duplicate)-1])
	inconsistent := pipeline.DefaultPipeline().Phases()
	inconsistent[1] = pipeline.NewPhase(pipeline.PhaseGrooming, pipeline.PhaseMetadata{DisplayName: "Grooming"})

	tests := []struct {
		name      string
		defaults  pipeline.Pipeline
		resolved  config.ResolvedConfig
		wantError string
	}{
		{
			name:      "unknown canonical definition",
			defaults:  pipeline.NewPipeline(unknown),
			resolved:  resolvedConfig(),
			wantError: "has unknown ID \"release\"",
		},
		{
			name:     "unknown resolved phase",
			defaults: pipeline.DefaultPipeline(),
			resolved: resolvedConfigWith(func(phases map[config.Phase]config.ResolvedPhase) {
				phases["release"] = config.ResolvedPhase{Enabled: true}
			}),
			wantError: "unknown phase \"release\"",
		},
		{
			name:      "duplicate canonical definition",
			defaults:  pipeline.NewPipeline(duplicate),
			resolved:  resolvedConfig(),
			wantError: "defines phase \"ci\" more than once",
		},
		{
			name:      "inconsistent optional definition",
			defaults:  pipeline.NewPipeline(inconsistent),
			resolved:  resolvedConfig(),
			wantError: "phase \"grooming\" optional=false; expected optional=true",
		},
		{
			name:     "missing resolved optional phase",
			defaults: pipeline.DefaultPipeline(),
			resolved: resolvedConfigWith(func(phases map[config.Phase]config.ResolvedPhase) {
				delete(phases, config.PhaseQA)
			}),
			wantError: "missing optional phase \"qa\"",
		},
		{
			name:     "CI cannot run without PR",
			defaults: pipeline.DefaultPipeline(),
			resolved: resolvedConfigWith(func(phases map[config.Phase]config.ResolvedPhase) {
				pr := phases[config.PhasePR]
				pr.Enabled = false
				phases[config.PhasePR] = pr
			}),
			wantError: "CI phase is enabled while PR phase is disabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := pipeline.Resolve(tt.defaults, tt.resolved)
			if err == nil {
				t.Fatalf("Resolve() error = nil, want %q", tt.wantError)
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Errorf("Resolve() error = %q, want substring %q", err, tt.wantError)
			}
			if got.Phases() != nil {
				t.Errorf("Resolve() result = %#v, want zero executable pipeline", got)
			}
		})
	}
}

func TestResolveDoesNotMutateInputs(t *testing.T) {
	t.Parallel()

	defaults := pipeline.DefaultPipeline()
	resolved := resolvedConfig()
	wantDefaults := defaults.Phases()
	wantResolved := cloneResolvedConfig(resolved)

	got, err := pipeline.Resolve(defaults, resolved)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	phases := got.Phases()
	phases[0] = pipeline.ExecutablePhase{}

	if !reflect.DeepEqual(defaults.Phases(), wantDefaults) {
		t.Errorf("Resolve() mutated default pipeline")
	}
	if !reflect.DeepEqual(resolved, wantResolved) {
		t.Errorf("Resolve() mutated resolved configuration")
	}
	if got.Phases()[0].Phase().ID() != pipeline.PhaseAcceptanceCriteria {
		t.Errorf("ExecutablePipeline.Phases() did not return a copy")
	}
}

func resolvedConfig() config.ResolvedConfig {
	return resolvedConfigWith(nil)
}

func resolvedConfigWith(change func(map[config.Phase]config.ResolvedPhase)) config.ResolvedConfig {
	settings := config.AgentSettings{Agent: config.AgentClaude, Model: "default-model", Effort: config.EffortMedium}
	phases := make(map[config.Phase]config.ResolvedPhase)
	for _, phase := range config.RemovablePhases() {
		phases[phase] = config.ResolvedPhase{Enabled: true, AgentSettings: settings}
	}
	if change != nil {
		change(phases)
	}
	return config.ResolvedConfig{Phases: phases}
}

func resolveConfig(t *testing.T, project *config.ProjectConfig, run config.RunOverrides) config.ResolvedConfig {
	t.Helper()

	resolved, err := config.Resolve(config.GlobalConfig{
		Version:  config.CurrentSchemaVersion,
		Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "default-model", Effort: config.EffortMedium},
	}, project, run)
	if err != nil {
		t.Fatalf("config.Resolve() error = %v", err)
	}
	return resolved
}

func cloneResolvedConfig(input config.ResolvedConfig) config.ResolvedConfig {
	cloned := config.ResolvedConfig{Phases: make(map[config.Phase]config.ResolvedPhase, len(input.Phases))}
	for phase, settings := range input.Phases {
		cloned.Phases[phase] = settings
	}
	return cloned
}

func executableIDs(executable pipeline.ExecutablePipeline) []pipeline.PhaseID {
	phases := executable.Phases()
	ids := make([]pipeline.PhaseID, len(phases))
	for index, phase := range phases {
		ids[index] = phase.Phase().ID()
	}
	return ids
}

func executablePhase(executable pipeline.ExecutablePipeline, id pipeline.PhaseID) (pipeline.ExecutablePhase, bool) {
	for _, phase := range executable.Phases() {
		if phase.Phase().ID() == id {
			return phase, true
		}
	}
	return pipeline.ExecutablePhase{}, false
}

func TestPhaseContractsUseCanonicalSkillTextForEnabledPhases(t *testing.T) {
	resolved := resolvedConfig()
	resolved.Defaults = config.AgentSettings{Agent: config.AgentClaude, Model: "default-model", Effort: config.EffortMedium}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	contracts := plan.PhaseContracts()
	for _, executable := range plan.Phases() {
		id := executable.Phase().ID()
		contract := contracts[id]
		if strings.TrimSpace(contract) == "" {
			t.Fatalf("phase %q has no executable contract", id)
		}
		if contract == executable.Phase().Metadata().DisplayName {
			t.Fatalf("phase %q uses display name as contract", id)
		}
		if !strings.Contains(contract, "Stable phase ID:") || !strings.Contains(contract, "Success Criteria") {
			t.Fatalf("phase %q contract lacks canonical skill sections: %q", id, contract[:min(len(contract), 80)])
		}
	}
	contracts[pipeline.PhaseQA] = "mutated"
	if fresh := plan.PhaseContracts()[pipeline.PhaseQA]; fresh == "mutated" {
		t.Fatal("PhaseContracts returned a map backed by mutable state")
	}
}

func TestPhaseContractUnknownPhaseHasNoInstructionFallback(t *testing.T) {
	if contract, ok := pipeline.PhaseContract("unknown"); ok || contract != "" {
		t.Fatalf("unknown phase contract = (%q, %t), want empty and false", contract, ok)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestSnapshotExecutionPersistsGitOpsSettings(t *testing.T) {
	resolved := resolvedConfig()
	resolved.Defaults = config.AgentSettings{Agent: config.AgentClaude, Model: "default-model", Effort: config.EffortMedium}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	want := config.GitOpsConfig{ParentBranch: "develop", BaseRef: "origin/develop", EnablePR: false, EnableCI: false, Configured: true}
	snapshot, err := pipeline.SnapshotExecution(plan, pipeline.DevelopmentSubphaseGeneration{}, 3, want)
	if err != nil {
		t.Fatal(err)
	}
	var encoded struct {
		GitOps           config.GitOpsConfig `json:"gitOps"`
		GitOpsConfigured bool                `json:"gitOpsConfigured"`
	}
	if err := json.Unmarshal(snapshot.Data, &encoded); err != nil {
		t.Fatal(err)
	}
	if encoded.GitOps != want {
		// Configured is intentionally omitted from the nested GitOps object.
		wantNested := want
		wantNested.Configured = false
		if encoded.GitOps != wantNested {
			t.Fatalf("snapshot GitOps = %#v, want %#v", encoded.GitOps, wantNested)
		}
	}
	if !encoded.GitOpsConfigured {
		t.Fatal("snapshot omitted configured GitOps marker")
	}
	restored, err := pipeline.SnapshotGitOps(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if restored != want {
		t.Fatalf("restored GitOps = %#v, want %#v", restored, want)
	}

	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(snapshot.Data, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "gitOpsConfigured")
	legacyData, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	legacySnapshot := snapshot
	legacySnapshot.Data = legacyData
	legacyGitOps, err := pipeline.SnapshotGitOps(legacySnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if legacyGitOps.Configured {
		t.Fatal("legacy snapshot without configured marker was treated as configured")
	}
}

func TestSnapshotExecutionVersionsPipelineOrderAndRestoresLegacyOrder(t *testing.T) {
	resolved := resolvedConfig()
	resolved.Defaults = config.AgentSettings{Agent: config.AgentClaude, Model: "default-model", Effort: config.EffortMedium}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := pipeline.SnapshotExecution(plan, pipeline.DevelopmentSubphaseGeneration{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SchemaVersion != 1 {
		t.Fatalf("snapshot wrapper version = %d, want 1", snapshot.SchemaVersion)
	}

	var encoded executionSnapshotFixture
	if err := json.Unmarshal(snapshot.Data, &encoded); err != nil {
		t.Fatal(err)
	}
	if encoded.SchemaVersion != 2 {
		t.Fatalf("new execution snapshot version = %d, want 2", encoded.SchemaVersion)
	}
	if got := fixturePhaseIDs(encoded.Phases); !reflect.DeepEqual(got[3:6], []pipeline.PhaseID{pipeline.PhaseDevelopment, pipeline.PhaseRebase, pipeline.PhaseQA}) {
		t.Fatalf("new snapshot phase order = %v", got)
	}

	legacy := encoded
	legacy.SchemaVersion = 1
	legacy.Phases[4], legacy.Phases[5] = legacy.Phases[5], legacy.Phases[4]
	legacyData, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	legacySnapshot := snapshot
	legacySnapshot.Data = legacyData
	restored, _, _, err := pipeline.RestoreExecution(legacySnapshot)
	if err != nil {
		t.Fatalf("RestoreExecution(legacy) error = %v", err)
	}
	if got := executableIDs(restored); !reflect.DeepEqual(got[3:6], []pipeline.PhaseID{pipeline.PhaseDevelopment, pipeline.PhaseQA, pipeline.PhaseRebase}) {
		t.Fatalf("restored legacy phase order = %v", got)
	}
}

func TestRestoreExecutionRejectsOrderFromAnotherSnapshotGeneration(t *testing.T) {
	resolved := resolvedConfig()
	resolved.Defaults = config.AgentSettings{Agent: config.AgentClaude, Model: "default-model", Effort: config.EffortMedium}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := pipeline.SnapshotExecution(plan, pipeline.DevelopmentSubphaseGeneration{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	var encoded executionSnapshotFixture
	if err := json.Unmarshal(snapshot.Data, &encoded); err != nil {
		t.Fatal(err)
	}

	encoded.SchemaVersion = 1
	if err := restoreFixture(t, snapshot, encoded); err == nil || !strings.Contains(err.Error(), "schema-1 order") {
		t.Fatalf("legacy snapshot with new order error = %v", err)
	}
	encoded.SchemaVersion = 2
	encoded.Phases[4], encoded.Phases[5] = encoded.Phases[5], encoded.Phases[4]
	if err := restoreFixture(t, snapshot, encoded); err == nil || !strings.Contains(err.Error(), "schema-2 order") {
		t.Fatalf("new snapshot with legacy order error = %v", err)
	}
}

type executionSnapshotFixture struct {
	SchemaVersion    int                                    `json:"schemaVersion"`
	PlanningContract int                                    `json:"planningContractVersion,omitempty"`
	Phases           []executionSnapshotPhaseFixture        `json:"phases"`
	Subphases        pipeline.DevelopmentSubphaseGeneration `json:"developmentSubphases"`
	MaxQAAttempts    int                                    `json:"maxQaAttempts"`
	GitOps           config.GitOpsConfig                    `json:"gitOps"`
	GitOpsConfigured bool                                   `json:"gitOpsConfigured"`
}

type executionSnapshotPhaseFixture struct {
	ID       pipeline.PhaseID     `json:"id"`
	Settings config.AgentSettings `json:"settings"`
}

func fixturePhaseIDs(phases []executionSnapshotPhaseFixture) []pipeline.PhaseID {
	ids := make([]pipeline.PhaseID, len(phases))
	for index, phase := range phases {
		ids[index] = phase.ID
	}
	return ids
}

func restoreFixture(t *testing.T, snapshot state.PipelineConfigSnapshot, fixture executionSnapshotFixture) error {
	t.Helper()
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Data = data
	_, _, _, err = pipeline.RestoreExecution(snapshot)
	return err
}

func TestPlanningContractMarkerGrandfathersSnapshotsWithoutIt(t *testing.T) {
	resolved := resolvedConfig()
	resolved.Defaults = config.AgentSettings{Agent: config.AgentClaude, Model: "default-model", Effort: config.EffortMedium}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := pipeline.SnapshotExecution(plan, pipeline.DevelopmentSubphaseGeneration{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !pipeline.PlanningContractEnforced(snapshot) {
		t.Fatal("new execution snapshot did not carry the Planning contract marker")
	}
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(snapshot.Data, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "planningContractVersion")
	legacyData, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Data = legacyData
	if pipeline.PlanningContractEnforced(snapshot) {
		t.Fatal("snapshot without the marker was not grandfathered")
	}
	if pipeline.PlanningContractEnforced(state.PipelineConfigSnapshot{SchemaVersion: 1, Data: []byte(`{}`)}) {
		t.Fatal("empty legacy snapshot was treated as a new contract snapshot")
	}
}

func TestGitOpsOnlyKeepsOnlyPRAndCIPhases(t *testing.T) {
	resolved := resolvedConfigWith(func(phases map[config.Phase]config.ResolvedPhase) {
		yes := true
		phases[config.PhasePR] = config.ResolvedPhase{Enabled: true, AgentSettings: config.AgentSettings{Agent: config.AgentCodex, Model: "m", Effort: config.EffortLow}}
		phases[config.PhaseCI] = config.ResolvedPhase{Enabled: true, AgentSettings: config.AgentSettings{Agent: config.AgentCodex, Model: "m", Effort: config.EffortLow}}
		_ = yes
	})
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	selected := plan.GitOpsOnly()
	for _, phase := range selected.Phases() {
		if phase.Phase().ID() != pipeline.PhasePR && phase.Phase().ID() != pipeline.PhaseCI {
			t.Fatalf("unexpected rerun phase %q", phase.Phase().ID())
		}
	}
	if _, ok := selected.PhaseContracts()[pipeline.PhaseDevelopment]; ok {
		t.Fatal("development contract retained")
	}
}

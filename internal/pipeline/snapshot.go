package pipeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/state"
)

const executionSnapshotSchemaVersion = 1

// PlanningContractVersion identifies snapshots created after the strict
// Planning artifact contract became enforceable. A missing marker means the
// project predates that contract and is grandfathered on resume and updates.
const PlanningContractVersion = 1

type executionSnapshot struct {
	SchemaVersion    int                           `json:"schemaVersion"`
	PlanningContract int                           `json:"planningContractVersion,omitempty"`
	Phases           []executionSnapshotPhase      `json:"phases"`
	Subphases        DevelopmentSubphaseGeneration `json:"developmentSubphases"`
	MaxQAAttempts    int                           `json:"maxQaAttempts"`
	GitOps           config.GitOpsConfig           `json:"gitOps"`
	GitOpsConfigured bool                          `json:"gitOpsConfigured"`
}

type executionSnapshotPhase struct {
	ID       PhaseID              `json:"id"`
	Settings config.AgentSettings `json:"settings"`
}

// SnapshotExecution serializes the exact executable plan and run-level knobs.
func SnapshotExecution(plan ExecutablePipeline, subphases DevelopmentSubphaseGeneration, maxQAAttempts int, gitops ...config.GitOpsConfig) (state.PipelineConfigSnapshot, error) {
	effectiveGitOps := config.DefaultGitOpsConfig()
	if len(gitops) > 1 {
		return state.PipelineConfigSnapshot{}, errors.New("snapshot accepts at most one GitOps configuration")
	}
	if len(gitops) == 1 {
		effectiveGitOps = gitops[0]
	}
	if err := config.ValidateGitOpsConfig(effectiveGitOps); err != nil {
		return state.PipelineConfigSnapshot{}, err
	}
	snapshot := executionSnapshot{
		SchemaVersion:    executionSnapshotSchemaVersion,
		PlanningContract: PlanningContractVersion,
		Subphases:        cloneSubphaseGeneration(subphases),
		MaxQAAttempts:    maxQAAttempts,
		GitOps:           effectiveGitOps,
		GitOpsConfigured: effectiveGitOps.Configured,
	}
	for _, executable := range plan.Phases() {
		settings, ok := executable.Settings()
		if !ok {
			return state.PipelineConfigSnapshot{}, fmt.Errorf("phase %q has no effective agent settings", executable.Phase().ID())
		}
		snapshot.Phases = append(snapshot.Phases, executionSnapshotPhase{ID: executable.Phase().ID(), Settings: settings})
	}
	if err := validateExecutionSnapshot(snapshot); err != nil {
		return state.PipelineConfigSnapshot{}, err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return state.PipelineConfigSnapshot{}, fmt.Errorf("encode pipeline execution snapshot: %w", err)
	}
	return state.PipelineConfigSnapshot{SchemaVersion: executionSnapshotSchemaVersion, Data: data}, nil
}

// PlanningContractEnforced reports whether a persisted execution snapshot was
// created under the strict Planning contract. Legacy snapshots intentionally
// return false so accepted plans remain resumable and updateable unchanged.
func PlanningContractEnforced(snapshot state.PipelineConfigSnapshot) bool {
	if snapshot.SchemaVersion != executionSnapshotSchemaVersion || len(bytes.TrimSpace(snapshot.Data)) == 0 {
		return false
	}
	var persisted executionSnapshot
	if err := json.Unmarshal(snapshot.Data, &persisted); err != nil {
		return false
	}
	return persisted.PlanningContract >= PlanningContractVersion
}

// RestoreExecution restores only persisted execution data. Ambient
// configuration is deliberately not consulted.
func RestoreExecution(snapshot state.PipelineConfigSnapshot) (ExecutablePipeline, DevelopmentSubphaseGeneration, int, error) {
	if snapshot.SchemaVersion != executionSnapshotSchemaVersion {
		return ExecutablePipeline{}, DevelopmentSubphaseGeneration{}, 0, fmt.Errorf("unsupported pipeline snapshot wrapper version %d", snapshot.SchemaVersion)
	}
	if len(bytes.TrimSpace(snapshot.Data)) == 0 || bytes.Equal(bytes.TrimSpace(snapshot.Data), []byte("{}")) {
		return ExecutablePipeline{}, DevelopmentSubphaseGeneration{}, 0, errors.New("project has no persisted executable pipeline snapshot; start a new run to initialize it")
	}
	var persisted executionSnapshot
	decoder := json.NewDecoder(bytes.NewReader(snapshot.Data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&persisted); err != nil {
		return ExecutablePipeline{}, DevelopmentSubphaseGeneration{}, 0, fmt.Errorf("decode pipeline execution snapshot: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ExecutablePipeline{}, DevelopmentSubphaseGeneration{}, 0, errors.New("pipeline execution snapshot contains trailing JSON")
	}
	if err := validateExecutionSnapshot(persisted); err != nil {
		return ExecutablePipeline{}, DevelopmentSubphaseGeneration{}, 0, err
	}
	canonical := DefaultPipeline().Phases()
	byID := make(map[PhaseID]Phase, len(canonical))
	for _, phase := range canonical {
		byID[phase.ID()] = phase
	}
	executable := make([]ExecutablePhase, 0, len(persisted.Phases))
	for _, phase := range persisted.Phases {
		settings := phase.Settings
		executable = append(executable, ExecutablePhase{phase: byID[phase.ID], settings: &settings})
	}
	return ExecutablePipeline{phases: executable}, cloneSubphaseGeneration(persisted.Subphases), persisted.MaxQAAttempts, nil
}

func validateExecutionSnapshot(snapshot executionSnapshot) error {
	if snapshot.SchemaVersion != executionSnapshotSchemaVersion {
		return fmt.Errorf("unsupported pipeline execution snapshot version %d", snapshot.SchemaVersion)
	}
	if snapshot.MaxQAAttempts <= 0 {
		return errors.New("pipeline execution snapshot requires a positive QA attempt maximum")
	}
	if snapshot.GitOps == (config.GitOpsConfig{}) {
		snapshot.GitOps = config.DefaultGitOpsConfig()
	}
	if err := config.ValidateGitOpsConfig(snapshot.GitOps); err != nil {
		return fmt.Errorf("pipeline execution snapshot GitOps: %w", err)
	}
	if len(snapshot.Phases) == 0 {
		return errors.New("pipeline execution snapshot has no enabled phases")
	}
	canonical := DefaultPipeline().Phases()
	indexes := make(map[PhaseID]int, len(canonical))
	mandatory := make(map[PhaseID]bool)
	for index, phase := range canonical {
		indexes[phase.ID()] = index
		if !phase.Metadata().Optional {
			mandatory[phase.ID()] = false
		}
	}
	previous := -1
	for index, phase := range snapshot.Phases {
		canonicalIndex, ok := indexes[phase.ID]
		if !ok {
			return fmt.Errorf("pipeline execution snapshot phase %d has unknown ID %q", index, phase.ID)
		}
		if canonicalIndex <= previous {
			return fmt.Errorf("pipeline execution snapshot phases are not in canonical increasing order at %q", phase.ID)
		}
		if err := config.ValidateAgentSettings(phase.Settings); err != nil {
			return fmt.Errorf("pipeline execution snapshot phase %q: %w", phase.ID, err)
		}
		previous = canonicalIndex
		if _, required := mandatory[phase.ID]; required {
			mandatory[phase.ID] = true
		}
	}
	for phase, present := range mandatory {
		if !present {
			return fmt.Errorf("pipeline execution snapshot is missing mandatory phase %q", phase)
		}
	}
	generated, err := GenerateDevelopmentSubphases(snapshot.Subphases)
	if err != nil {
		return fmt.Errorf("pipeline execution snapshot Development subphases: %w", err)
	}
	if len(generated) == 0 {
		return errors.New("pipeline execution snapshot requires at least one Development subphase")
	}
	return nil
}

func cloneSubphaseGeneration(generation DevelopmentSubphaseGeneration) DevelopmentSubphaseGeneration {
	generation.Subphases = append([]DevelopmentSubphaseDefinition(nil), generation.Subphases...)
	return generation
}

// SnapshotGitOps returns the GitOps settings persisted with an execution snapshot.
func SnapshotGitOps(snapshot state.PipelineConfigSnapshot) (config.GitOpsConfig, error) {
	if snapshot.SchemaVersion != executionSnapshotSchemaVersion {
		return config.GitOpsConfig{}, fmt.Errorf("unsupported pipeline snapshot wrapper version %d", snapshot.SchemaVersion)
	}
	var persisted executionSnapshot
	if err := json.Unmarshal(snapshot.Data, &persisted); err != nil {
		return config.GitOpsConfig{}, fmt.Errorf("decode pipeline GitOps snapshot: %w", err)
	}
	persisted.GitOps.Configured = persisted.GitOpsConfigured
	return persisted.GitOps, nil
}

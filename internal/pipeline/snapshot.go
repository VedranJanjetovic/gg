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

const pipelineSnapshotWrapperVersion = 1
const legacyExecutionSnapshotSchemaVersion = 1
const executionSnapshotSchemaVersion = 2
const projectExecutionSnapshotSchemaVersion = 3

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
	ProjectDefault   config.AgentSettings          `json:"projectDefault,omitempty"`
	PhaseStructure   []executionSnapshotStructure  `json:"phaseStructure,omitempty"`
}

type executionSnapshotPhase struct {
	ID       PhaseID              `json:"id"`
	Settings config.AgentSettings `json:"settings"`
}

type executionSnapshotStructure struct {
	ID       PhaseID              `json:"id"`
	Enabled  bool                 `json:"enabled"`
	Required bool                 `json:"required"`
	Settings config.AgentSettings `json:"settings"`
}

// SnapshotProjectExecution persists the complete project configuration used
// to create a new project. Unlike the legacy run snapshot, it retains disabled
// optional phases, the project default, and the provenance of every tuple.
func SnapshotProjectExecution(plan ExecutablePipeline, subphases DevelopmentSubphaseGeneration, maxQAAttempts int, project config.ProjectConfig, gitops ...config.GitOpsConfig) (state.PipelineConfigSnapshot, error) {
	if err := config.ValidateCompleteProjectConfig(project); err != nil {
		return state.PipelineConfigSnapshot{}, fmt.Errorf("project configuration: %w", err)
	}
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
		SchemaVersion:    projectExecutionSnapshotSchemaVersion,
		PlanningContract: PlanningContractVersion,
		Subphases:        cloneSubphaseGeneration(subphases), MaxQAAttempts: maxQAAttempts,
		GitOps: effectiveGitOps, GitOpsConfigured: effectiveGitOps.Configured,
		ProjectDefault: config.AgentSettings{Agent: project.Defaults.Agent, Model: project.Defaults.Model, Effort: project.Defaults.Effort, Provenance: project.Defaults.Provenance},
	}
	for _, phase := range project.Phases {
		snapshot.PhaseStructure = append(snapshot.PhaseStructure, executionSnapshotStructure{
			ID: PhaseID(phase.Phase), Enabled: phase.Enabled, Required: phase.Required, Settings: phase.AgentSettings,
		})
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
		return state.PipelineConfigSnapshot{}, fmt.Errorf("encode project execution snapshot: %w", err)
	}
	return state.PipelineConfigSnapshot{SchemaVersion: pipelineSnapshotWrapperVersion, Data: data}, nil
}

// SnapshotExecutionWithConfiguration is the descriptive alias used by
// callers that already treat the project configuration as the source object.
func SnapshotExecutionWithConfiguration(plan ExecutablePipeline, subphases DevelopmentSubphaseGeneration, maxQAAttempts int, project config.ProjectConfig, gitops ...config.GitOpsConfig) (state.PipelineConfigSnapshot, error) {
	return SnapshotProjectExecution(plan, subphases, maxQAAttempts, project, gitops...)
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
	return state.PipelineConfigSnapshot{SchemaVersion: pipelineSnapshotWrapperVersion, Data: data}, nil
}

// PlanningContractEnforced reports whether a persisted execution snapshot was
// created under the strict Planning contract. Legacy snapshots intentionally
// return false so accepted plans remain resumable and updateable unchanged.
func PlanningContractEnforced(snapshot state.PipelineConfigSnapshot) bool {
	if snapshot.SchemaVersion != pipelineSnapshotWrapperVersion || len(bytes.TrimSpace(snapshot.Data)) == 0 {
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
	if snapshot.SchemaVersion != pipelineSnapshotWrapperVersion {
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
	if persisted.SchemaVersion == projectExecutionSnapshotSchemaVersion && len(persisted.PhaseStructure) > 0 {
		for _, phase := range persisted.PhaseStructure {
			if !phase.Enabled {
				continue
			}
			settings := phase.Settings
			executable = append(executable, ExecutablePhase{phase: byID[phase.ID], settings: &settings})
		}
	} else {
		for _, phase := range persisted.Phases {
			settings := phase.Settings
			executable = append(executable, ExecutablePhase{phase: byID[phase.ID], settings: &settings})
		}
	}
	return ExecutablePipeline{phases: executable}, cloneSubphaseGeneration(persisted.Subphases), persisted.MaxQAAttempts, nil
}

// RestoreResolvedConfiguration exposes the immutable configuration carried by
// a new-project snapshot to the legacy pipeline service boundary. It is only
// defined for project snapshots; older execution snapshots intentionally do
// not pretend to contain disabled-phase structure or a project default.
func RestoreResolvedConfiguration(snapshot state.PipelineConfigSnapshot) (config.ResolvedConfig, error) {
	persisted, err := decodeExecutionSnapshot(snapshot)
	if err != nil {
		return config.ResolvedConfig{}, err
	}
	if persisted.SchemaVersion != projectExecutionSnapshotSchemaVersion {
		return config.ResolvedConfig{}, errors.New("pipeline snapshot does not contain complete project configuration")
	}
	resolved := config.ResolvedConfig{
		Defaults: persisted.ProjectDefault,
		Phases:   make(map[config.Phase]config.ResolvedPhase, len(persisted.PhaseStructure)),
		GitOps:   persisted.GitOps,
	}
	for _, phase := range persisted.PhaseStructure {
		resolved.Phases[config.Phase(phase.ID)] = config.ResolvedPhase{Enabled: phase.Enabled, AgentSettings: phase.Settings}
	}
	return resolved, nil
}

func validateExecutionSnapshot(snapshot executionSnapshot) error {
	phaseOrder, err := executionPhaseOrder(snapshot.SchemaVersion)
	if err != nil {
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
	if snapshot.SchemaVersion == projectExecutionSnapshotSchemaVersion {
		if err := validateSnapshotStructure(snapshot.PhaseStructure); err != nil {
			return err
		}
		if err := config.ValidateAgentSettings(snapshot.ProjectDefault); err != nil {
			return fmt.Errorf("project execution snapshot default: %w", err)
		}
	}
	indexes := make(map[PhaseID]int, len(phaseOrder))
	mandatory := make(map[PhaseID]bool)
	for index, id := range phaseOrder {
		indexes[id] = index
		if !isOptionalPhase(id) {
			mandatory[id] = false
		}
	}
	previous := -1
	for index, phase := range snapshot.Phases {
		canonicalIndex, ok := indexes[phase.ID]
		if !ok {
			return fmt.Errorf("pipeline execution snapshot phase %d has unknown ID %q", index, phase.ID)
		}
		if canonicalIndex <= previous {
			return fmt.Errorf("pipeline execution snapshot phases are not in schema-%d order at %q", snapshot.SchemaVersion, phase.ID)
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

func decodeExecutionSnapshot(snapshot state.PipelineConfigSnapshot) (executionSnapshot, error) {
	if snapshot.SchemaVersion != pipelineSnapshotWrapperVersion {
		return executionSnapshot{}, fmt.Errorf("unsupported pipeline snapshot wrapper version %d", snapshot.SchemaVersion)
	}
	if len(bytes.TrimSpace(snapshot.Data)) == 0 || bytes.Equal(bytes.TrimSpace(snapshot.Data), []byte("{}")) {
		return executionSnapshot{}, errors.New("project has no persisted executable pipeline snapshot; start a new run to initialize it")
	}
	var persisted executionSnapshot
	decoder := json.NewDecoder(bytes.NewReader(snapshot.Data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&persisted); err != nil {
		return executionSnapshot{}, fmt.Errorf("decode pipeline execution snapshot: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return executionSnapshot{}, errors.New("pipeline execution snapshot contains trailing JSON")
	}
	if err := validateExecutionSnapshot(persisted); err != nil {
		return executionSnapshot{}, err
	}
	return persisted, nil
}

func executionPhaseOrder(version int) ([]PhaseID, error) {
	switch version {
	case legacyExecutionSnapshotSchemaVersion:
		return []PhaseID{
			PhaseAcceptanceCriteria, PhaseGrooming, PhasePlanning, PhaseDevelopment,
			PhaseQA, PhaseRebase, PhaseTestDocument, PhaseBuildChecker, PhasePR, PhaseCI,
		}, nil
	case executionSnapshotSchemaVersion:
		return []PhaseID{
			PhaseAcceptanceCriteria, PhaseGrooming, PhasePlanning, PhaseDevelopment,
			PhaseRebase, PhaseQA, PhaseTestDocument, PhaseBuildChecker, PhasePR, PhaseCI,
		}, nil
	case projectExecutionSnapshotSchemaVersion:
		return []PhaseID{
			PhaseAcceptanceCriteria, PhaseGrooming, PhasePlanning, PhaseDevelopment,
			PhaseRebase, PhaseQA, PhaseTestDocument, PhaseBuildChecker, PhasePR, PhaseCI,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported pipeline execution snapshot version %d", version)
	}
}

func validateSnapshotStructure(phases []executionSnapshotStructure) error {
	if len(phases) != len(config.CompletePhaseOrder()) {
		return fmt.Errorf("project execution snapshot requires all %d phase structure entries", len(config.CompletePhaseOrder()))
	}
	seen := make(map[config.Phase]bool, len(phases))
	for index, phase := range phases {
		want := config.Phase(config.CompletePhaseOrder()[index])
		if config.Phase(phase.ID) != want {
			return fmt.Errorf("project execution snapshot phase structure %d is %q; expected %q", index, phase.ID, want)
		}
		if seen[config.Phase(phase.ID)] {
			return fmt.Errorf("project execution snapshot phase structure repeats %q", phase.ID)
		}
		seen[config.Phase(phase.ID)] = true
		if phase.Required != containsConfigPhase(config.RequiredPhases(), config.Phase(phase.ID)) {
			return fmt.Errorf("project execution snapshot phase %q has invalid required state", phase.ID)
		}
		if phase.Required && !phase.Enabled {
			return fmt.Errorf("project execution snapshot required phase %q is disabled", phase.ID)
		}
		if err := config.ValidateAgentSettings(phase.Settings); err != nil {
			return fmt.Errorf("project execution snapshot phase %q: %w", phase.ID, err)
		}
	}
	return nil
}

func containsConfigPhase(phases []config.Phase, want config.Phase) bool {
	for _, phase := range phases {
		if phase == want {
			return true
		}
	}
	return false
}

func isOptionalPhase(id PhaseID) bool {
	switch id {
	case PhaseGrooming, PhasePlanning, PhaseQA, PhaseBuildChecker, PhasePR, PhaseCI:
		return true
	default:
		return false
	}
}

func cloneSubphaseGeneration(generation DevelopmentSubphaseGeneration) DevelopmentSubphaseGeneration {
	generation.Subphases = append([]DevelopmentSubphaseDefinition(nil), generation.Subphases...)
	return generation
}

// SnapshotGitOps returns the GitOps settings persisted with an execution snapshot.
func SnapshotGitOps(snapshot state.PipelineConfigSnapshot) (config.GitOpsConfig, error) {
	if snapshot.SchemaVersion != pipelineSnapshotWrapperVersion {
		return config.GitOpsConfig{}, fmt.Errorf("unsupported pipeline snapshot wrapper version %d", snapshot.SchemaVersion)
	}
	var persisted executionSnapshot
	if err := json.Unmarshal(snapshot.Data, &persisted); err != nil {
		return config.GitOpsConfig{}, fmt.Errorf("decode pipeline GitOps snapshot: %w", err)
	}
	persisted.GitOps.Configured = persisted.GitOpsConfigured
	return persisted.GitOps, nil
}

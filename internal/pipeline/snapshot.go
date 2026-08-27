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
	SchemaVersion     int                           `json:"schemaVersion"`
	LegacyOrder       bool                          `json:"legacyOrder,omitempty"`
	PlanningContract  int                           `json:"planningContractVersion,omitempty"`
	Phases            []executionSnapshotPhase      `json:"phases"`
	Subphases         DevelopmentSubphaseGeneration `json:"developmentSubphases"`
	MaxQAAttempts     int                           `json:"maxQaAttempts"`
	GitOps            config.GitOpsConfig           `json:"gitOps"`
	GitOpsConfigured  bool                          `json:"gitOpsConfigured"`
	ProjectDefault    config.AgentSettings          `json:"projectDefault,omitempty"`
	PhaseStructure    []executionSnapshotStructure  `json:"phaseStructure,omitempty"`
	VerificationSteps []state.VerificationStep      `json:"verificationSteps,omitempty"`
	RepairMode        bool                          `json:"repairMode,omitempty"`
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

// ProjectPhaseConfiguration is the mutable part of a persisted project
// snapshot. Pipeline structure is intentionally not represented as mutable
// input here; callers can change only the complete tuple for an existing
// phase.
type ProjectPhaseConfiguration struct {
	ID       PhaseID
	Enabled  bool
	Required bool
	Settings config.AgentSettings
}

// ProjectExecutionConfiguration is the complete configuration carried by a
// project snapshot. It is a defensive, transport-independent view used by
// project editing and legacy repair.
type ProjectExecutionConfiguration struct {
	Default config.AgentSettings
	Phases  []ProjectPhaseConfiguration
}

func (configuration ProjectExecutionConfiguration) Clone() ProjectExecutionConfiguration {
	configuration.Phases = append([]ProjectPhaseConfiguration(nil), configuration.Phases...)
	return configuration
}

// ReadProjectExecutionConfiguration reads a project snapshot without consulting
// ambient configuration. Legacy execution snapshots are upgraded in memory so
// an explicit edit or repair can persist them as a complete project snapshot.
func ReadProjectExecutionConfiguration(snapshot state.PipelineConfigSnapshot) (ProjectExecutionConfiguration, error) {
	persisted, err := decodeExecutionSnapshot(snapshot)
	if err != nil {
		return ProjectExecutionConfiguration{}, err
	}
	if persisted.SchemaVersion == projectExecutionSnapshotSchemaVersion && len(persisted.PhaseStructure) > 0 {
		phases := make([]ProjectPhaseConfiguration, 0, len(persisted.PhaseStructure))
		for _, phase := range persisted.PhaseStructure {
			phases = append(phases, ProjectPhaseConfiguration{ID: phase.ID, Enabled: phase.Enabled, Required: phase.Required, Settings: phase.Settings})
		}
		return ProjectExecutionConfiguration{Default: persisted.ProjectDefault, Phases: phases}, nil
	}

	active := make(map[PhaseID]config.AgentSettings, len(persisted.Phases))
	for _, phase := range persisted.Phases {
		active[phase.ID] = phase.Settings
	}
	phaseOrder, _ := executionPhaseOrderForSnapshot(persisted)
	phases := make([]ProjectPhaseConfiguration, 0, len(config.CompletePhaseOrder()))
	var projectDefault config.AgentSettings
	for _, id := range phaseOrder {
		settings, enabled := active[id]
		if enabled && projectDefault.Agent == "" {
			projectDefault = settings
		}
		if !enabled {
			settings = projectDefault
		}
		phases = append(phases, ProjectPhaseConfiguration{ID: id, Enabled: enabled, Required: containsConfigPhase(config.RequiredPhases(), config.Phase(id)), Settings: settings})
	}
	if projectDefault.Agent == "" {
		return ProjectExecutionConfiguration{}, errors.New("project execution snapshot has no complete phase settings")
	}
	return ProjectExecutionConfiguration{Default: projectDefault, Phases: phases}, nil
}

// UpdateProjectExecutionConfiguration replaces only tuple values in a
// snapshot. The existing phase IDs, order, enabled state, and required state
// are retained. Saving an edited legacy snapshot upgrades its representation
// to the complete project snapshot schema.
func UpdateProjectExecutionConfiguration(snapshot state.PipelineConfigSnapshot, configuration ProjectExecutionConfiguration) (state.PipelineConfigSnapshot, error) {
	persisted, err := decodeExecutionSnapshot(snapshot)
	if err != nil {
		return state.PipelineConfigSnapshot{}, err
	}
	original, err := ReadProjectExecutionConfiguration(snapshot)
	if err != nil {
		return state.PipelineConfigSnapshot{}, err
	}
	if err := config.ValidateAgentSettings(configuration.Default); err != nil {
		return state.PipelineConfigSnapshot{}, fmt.Errorf("project default: %w", err)
	}
	if configuration.Default.Provenance == "" {
		configuration.Default.Provenance = config.ModelProvenanceManual
	}
	if len(configuration.Phases) == 0 {
		return state.PipelineConfigSnapshot{}, errors.New("project configuration has no phases")
	}
	if len(configuration.Phases) != len(original.Phases) {
		return state.PipelineConfigSnapshot{}, errors.New("project configuration cannot change phase structure")
	}
	legacyOrder := persisted.SchemaVersion == legacyExecutionSnapshotSchemaVersion || persisted.LegacyOrder
	byID := make(map[PhaseID]ProjectPhaseConfiguration, len(configuration.Phases))
	for index, phase := range configuration.Phases {
		before := original.Phases[index]
		if phase.ID != before.ID || phase.Enabled != before.Enabled || phase.Required != before.Required {
			return state.PipelineConfigSnapshot{}, fmt.Errorf("project configuration cannot change phase structure at %q", phase.ID)
		}
		if _, exists := byID[phase.ID]; exists {
			return state.PipelineConfigSnapshot{}, fmt.Errorf("project configuration repeats phase %q", phase.ID)
		}
		if phase.Settings.Provenance == "" {
			phase.Settings.Provenance = config.ModelProvenanceManual
		}
		if err := config.ValidateAgentSettings(phase.Settings); err != nil {
			return state.PipelineConfigSnapshot{}, fmt.Errorf("phase %q: %w", phase.ID, err)
		}
		byID[phase.ID] = phase
	}
	if persisted.SchemaVersion == projectExecutionSnapshotSchemaVersion && len(persisted.PhaseStructure) > 0 {
		for index := range persisted.PhaseStructure {
			phase := persisted.PhaseStructure[index]
			updated, ok := byID[phase.ID]
			if !ok {
				return state.PipelineConfigSnapshot{}, fmt.Errorf("project configuration is missing phase %q", phase.ID)
			}
			persisted.PhaseStructure[index].Settings = updated.Settings
		}
	} else {
		structure := make([]executionSnapshotStructure, 0, len(original.Phases))
		for _, phase := range original.Phases {
			updated := byID[phase.ID]
			structure = append(structure, executionSnapshotStructure{ID: phase.ID, Enabled: phase.Enabled, Required: phase.Required, Settings: updated.Settings})
		}
		persisted.PhaseStructure = structure
	}
	for index := range persisted.Phases {
		if updated, ok := byID[persisted.Phases[index].ID]; ok {
			persisted.Phases[index].Settings = updated.Settings
		}
	}
	persisted.SchemaVersion = projectExecutionSnapshotSchemaVersion
	persisted.LegacyOrder = legacyOrder
	persisted.PlanningContract = PlanningContractVersion
	persisted.ProjectDefault = configuration.Default
	data, err := json.Marshal(persisted)
	if err != nil {
		return state.PipelineConfigSnapshot{}, fmt.Errorf("encode project execution configuration: %w", err)
	}
	result := state.PipelineConfigSnapshot{SchemaVersion: pipelineSnapshotWrapperVersion, Data: data}
	if _, err := decodeExecutionSnapshot(result); err != nil {
		return state.PipelineConfigSnapshot{}, err
	}
	return result, nil
}

// SnapshotProjectExecution persists the complete project configuration used
// to create a new project. Unlike the legacy run snapshot, it retains disabled
// optional phases, the project default, and the provenance of every tuple.
func SnapshotProjectExecution(plan ExecutablePipeline, subphases DevelopmentSubphaseGeneration, maxQAAttempts int, project config.ProjectConfig, gitops ...config.GitOpsConfig) (state.PipelineConfigSnapshot, error) {
	if err := validateProjectSnapshotConfiguration(project); err != nil {
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

// SnapshotExecutionWithVerification persists a Planning-produced executable
// verification contract. Both it and SnapshotExecution stamp schema 2; the
// difference is only whether verification steps are present, which is what
// LoadExecution keys off when it decides to rehydrate a contract.
func SnapshotExecutionWithVerification(plan ExecutablePipeline, subphases DevelopmentSubphaseGeneration, maxQAAttempts int, contract state.VerificationContract, gitops ...config.GitOpsConfig) (state.PipelineConfigSnapshot, error) {
	if err := contract.Validate(); err != nil {
		return state.PipelineConfigSnapshot{}, err
	}
	wrapped, err := SnapshotExecution(plan, subphases, maxQAAttempts, gitops...)
	if err != nil {
		return state.PipelineConfigSnapshot{}, err
	}
	var snapshot executionSnapshot
	if err := json.Unmarshal(wrapped.Data, &snapshot); err != nil {
		return state.PipelineConfigSnapshot{}, fmt.Errorf("decode pipeline execution snapshot: %w", err)
	}
	snapshot.VerificationSteps = cloneVerificationSteps(contract.Steps)
	snapshot.RepairMode = contract.RepairMode
	if err := validateExecutionSnapshot(snapshot); err != nil {
		return state.PipelineConfigSnapshot{}, err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return state.PipelineConfigSnapshot{}, fmt.Errorf("encode pipeline verification snapshot: %w", err)
	}
	return state.PipelineConfigSnapshot{SchemaVersion: pipelineSnapshotWrapperVersion, Data: data}, nil
}

// UpgradeLegacyExecutionSnapshot adds a Planning-produced verification
// contract to a schema-1 execution snapshot without changing its resolved
// phases, subphases, or run limits. Callers must obtain the contract from the
// completed worktree Planning artifact rather than infer it from project text.
func UpgradeLegacyExecutionSnapshot(snapshot state.PipelineConfigSnapshot, contract state.VerificationContract) (state.PipelineConfigSnapshot, error) {
	if snapshot.SchemaVersion != legacyExecutionSnapshotSchemaVersion {
		return state.PipelineConfigSnapshot{}, fmt.Errorf("unsupported pipeline snapshot wrapper version %d", snapshot.SchemaVersion)
	}
	if err := contract.Validate(); err != nil {
		return state.PipelineConfigSnapshot{}, fmt.Errorf("legacy execution snapshot verification contract: %w", err)
	}
	var persisted executionSnapshot
	decoder := json.NewDecoder(bytes.NewReader(snapshot.Data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&persisted); err != nil {
		return state.PipelineConfigSnapshot{}, fmt.Errorf("decode legacy pipeline execution snapshot: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return state.PipelineConfigSnapshot{}, errors.New("legacy pipeline execution snapshot contains trailing JSON")
	}
	if err := validateExecutionSnapshot(persisted); err != nil {
		return state.PipelineConfigSnapshot{}, err
	}
	if persisted.SchemaVersion != legacyExecutionSnapshotSchemaVersion && persisted.SchemaVersion != executionSnapshotSchemaVersion {
		return state.PipelineConfigSnapshot{}, fmt.Errorf("unsupported legacy pipeline execution snapshot schema %d", persisted.SchemaVersion)
	}
	persisted.SchemaVersion = executionSnapshotSchemaVersion
	persisted.VerificationSteps = cloneVerificationSteps(contract.Steps)
	persisted.RepairMode = contract.RepairMode
	if err := validateExecutionSnapshot(persisted); err != nil {
		return state.PipelineConfigSnapshot{}, err
	}
	data, err := json.Marshal(persisted)
	if err != nil {
		return state.PipelineConfigSnapshot{}, fmt.Errorf("encode upgraded pipeline execution snapshot: %w", err)
	}
	return state.PipelineConfigSnapshot{SchemaVersion: legacyExecutionSnapshotSchemaVersion, Data: data}, nil
}

// RestoreExecution restores only persisted execution data. Ambient
// configuration is deliberately not consulted.
func RestoreExecution(snapshot state.PipelineConfigSnapshot) (ExecutablePipeline, DevelopmentSubphaseGeneration, int, error) {
	executable, subphases, maxAttempts, _, err := RestoreExecutionWithVerification(snapshot)
	return executable, subphases, maxAttempts, err
}

// RestoreExecutionWithVerification restores schema 2 verification data and
// supplies an empty legacy contract for schema-1 snapshots.
func RestoreExecutionWithVerification(snapshot state.PipelineConfigSnapshot) (ExecutablePipeline, DevelopmentSubphaseGeneration, int, state.VerificationContract, error) {
	if snapshot.SchemaVersion != pipelineSnapshotWrapperVersion {
		return ExecutablePipeline{}, DevelopmentSubphaseGeneration{}, 0, state.VerificationContract{}, fmt.Errorf("unsupported pipeline snapshot wrapper version %d", snapshot.SchemaVersion)
	}
	if len(bytes.TrimSpace(snapshot.Data)) == 0 || bytes.Equal(bytes.TrimSpace(snapshot.Data), []byte("{}")) {
		return ExecutablePipeline{}, DevelopmentSubphaseGeneration{}, 0, state.VerificationContract{}, errors.New("project has no persisted executable pipeline snapshot; start a new run to initialize it")
	}
	var persisted executionSnapshot
	decoder := json.NewDecoder(bytes.NewReader(snapshot.Data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&persisted); err != nil {
		return ExecutablePipeline{}, DevelopmentSubphaseGeneration{}, 0, state.VerificationContract{}, fmt.Errorf("decode pipeline execution snapshot: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ExecutablePipeline{}, DevelopmentSubphaseGeneration{}, 0, state.VerificationContract{}, errors.New("pipeline execution snapshot contains trailing JSON")
	}
	if err := validateExecutionSnapshot(persisted); err != nil {
		return ExecutablePipeline{}, DevelopmentSubphaseGeneration{}, 0, state.VerificationContract{}, err
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
	contract := state.VerificationContract{Steps: cloneVerificationSteps(persisted.VerificationSteps), RepairMode: persisted.RepairMode}
	if len(persisted.VerificationSteps) > 0 {
		if err := contract.Validate(); err != nil {
			return ExecutablePipeline{}, DevelopmentSubphaseGeneration{}, 0, state.VerificationContract{}, fmt.Errorf("pipeline verification contract: %w", err)
		}
	}
	return ExecutablePipeline{phases: executable}, cloneSubphaseGeneration(persisted.Subphases), persisted.MaxQAAttempts, contract, nil
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
	for _, phase := range config.CompletePhaseOrder() {
		resolved.Phases[phase] = config.ResolvedPhase{AgentSettings: persisted.ProjectDefault}
	}
	for _, phase := range persisted.PhaseStructure {
		resolved.Phases[config.Phase(phase.ID)] = config.ResolvedPhase{Enabled: phase.Enabled, AgentSettings: phase.Settings}
	}
	return resolved, nil
}

func validateExecutionSnapshot(snapshot executionSnapshot) error {
	phaseOrder, err := executionPhaseOrderForSnapshot(snapshot)
	if err != nil {
		return fmt.Errorf("unsupported pipeline execution snapshot version %d", snapshot.SchemaVersion)
	}
	if snapshot.MaxQAAttempts <= 0 {
		return errors.New("pipeline execution snapshot requires a positive QA attempt maximum")
	}
	if snapshot.SchemaVersion == executionSnapshotSchemaVersion && len(snapshot.VerificationSteps) > 0 {
		contract := state.VerificationContract{Steps: snapshot.VerificationSteps, RepairMode: snapshot.RepairMode}
		if err := contract.Validate(); err != nil {
			return fmt.Errorf("pipeline execution snapshot verification contract: %w", err)
		}
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
		if err := validateSnapshotStructure(snapshot.PhaseStructure, phaseOrder); err != nil {
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

func executionPhaseOrderForSnapshot(snapshot executionSnapshot) ([]PhaseID, error) {
	if snapshot.SchemaVersion == projectExecutionSnapshotSchemaVersion && snapshot.LegacyOrder {
		return executionPhaseOrder(legacyExecutionSnapshotSchemaVersion)
	}
	return executionPhaseOrder(snapshot.SchemaVersion)
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

func validateSnapshotStructure(phases []executionSnapshotStructure, order []PhaseID) error {
	if len(phases) == 0 || len(phases) > len(config.CompletePhaseOrder()) {
		return fmt.Errorf("project execution snapshot requires between one and %d phase structure entries", len(config.CompletePhaseOrder()))
	}
	seen := make(map[config.Phase]bool, len(phases))
	for index, phase := range phases {
		if index >= len(order) || phase.ID != order[index] {
			want := ""
			if index < len(order) {
				want = string(order[index])
			}
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
		if phase.Settings.Provenance != config.ModelProvenanceCatalog && phase.Settings.Provenance != config.ModelProvenanceManual {
			return fmt.Errorf("project execution snapshot phase %q has invalid model provenance", phase.ID)
		}
	}
	for _, required := range config.RequiredPhases() {
		if !seen[required] {
			return fmt.Errorf("project execution snapshot is missing required phase %q", required)
		}
	}
	return nil
}

func validateProjectSnapshotConfiguration(project config.ProjectConfig) error {
	completeErr := config.ValidateCompleteProjectConfig(project)
	if completeErr == nil {
		return nil
	}
	if project.Version != config.CompleteSchemaVersion || project.Phases == nil || project.PhaseOverrides != nil || len(project.Phases) == 0 || len(project.Phases) > len(config.CompletePhaseOrder()) {
		return fmt.Errorf("project configuration: %w", completeErr)
	}
	for index, entry := range project.Phases {
		if entry.Phase != config.CompletePhaseOrder()[index] {
			return fmt.Errorf("project configuration phase %d is %q; expected %q", index, entry.Phase, config.CompletePhaseOrder()[index])
		}
	}
	// Pad only for validation. The serialized snapshot retains the original
	// prefix, which is what makes Inherit preserve an older folder structure.
	padded := project.Clone()
	for _, phase := range config.CompletePhaseOrder()[len(project.Phases):] {
		padded.Phases = append(padded.Phases, config.PhaseConfig{
			Phase: phase, Enabled: false, Required: false,
			AgentSettings: config.AgentSettings{
				Agent: project.Defaults.Agent, Model: project.Defaults.Model,
				Effort: project.Defaults.Effort, Provenance: project.Defaults.Provenance,
			},
		})
	}
	if err := config.ValidateCompleteProjectConfig(padded); err != nil {
		return fmt.Errorf("project configuration: %w", err)
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

func cloneVerificationSteps(steps []state.VerificationStep) []state.VerificationStep {
	if steps == nil {
		return nil
	}
	cloned := make([]state.VerificationStep, len(steps))
	for index, step := range steps {
		cloned[index] = step
		cloned[index].Args = append([]string(nil), step.Args...)
		if step.Env != nil {
			cloned[index].Env = make(map[string]string, len(step.Env))
			for key, value := range step.Env {
				cloned[index].Env[key] = value
			}
		}
	}
	return cloned
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

// SnapshotVerification returns the persisted contract. Legacy schema-1
// snapshots intentionally return defaults so resume can report the migration
// requirement without inventing executable checks.
func SnapshotVerification(snapshot state.PipelineConfigSnapshot) (state.VerificationContract, error) {
	_, _, _, contract, err := RestoreExecutionWithVerification(snapshot)
	return contract, err
}

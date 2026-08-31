package config

import "errors"

// SchemaVersion identifies the persisted configuration schema.
type SchemaVersion uint

// CurrentSchemaVersion is the schema version written by this release.
const CurrentSchemaVersion SchemaVersion = 1

// CompleteSchemaVersion is the version of the self-contained folder
// configuration introduced after the original sparse override format.
const CompleteSchemaVersion SchemaVersion = 2

// ModelProvenance records how a model was selected. Empty provenance is kept
// for legacy configurations whose origin was not persisted.
type ModelProvenance string

const (
	ModelProvenanceCatalog ModelProvenance = "catalog"
	ModelProvenanceManual  ModelProvenance = "manual"
)

// Agent identifies a supported agent runtime.
type Agent string

const (
	AgentClaude Agent = "claude"
	AgentCodex  Agent = "codex"
)

// Effort identifies the reasoning effort requested from an agent runtime.
type Effort string

const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
)

// Phase identifies a configurable pipeline phase. Pipeline ordering and
// lifecycle behavior are owned by the pipeline package.
type Phase string

const (
	PhaseGrooming     Phase = "grooming"
	PhasePlanning     Phase = "planning"
	PhaseQA           Phase = "qa"
	PhaseBuildChecker Phase = "build_checker"
	PhasePR           Phase = "pr"
	PhaseCI           Phase = "ci"

	// Fixed pipeline phases: they always run and cannot be disabled, but
	// still accept per-phase agent/model/effort overrides.
	PhaseAcceptanceCriteria Phase = "acceptance_criteria"
	PhaseDevelopment        Phase = "development"
	PhaseRebase             Phase = "rebase"
	PhaseTestDocument       Phase = "test_document"

	// PhaseLintingAlias is an accepted alias for PhaseBuildChecker. Alias
	// normalization is performed when configuration validation is implemented.
	PhaseLintingAlias Phase = "linting"
)

var removablePhases = [...]Phase{
	PhaseGrooming,
	PhasePlanning,
	PhaseQA,
	PhaseBuildChecker,
	PhasePR,
	PhaseCI,
}

var fixedPhases = [...]Phase{
	PhaseAcceptanceCriteria,
	PhaseDevelopment,
	PhaseRebase,
	PhaseTestDocument,
}

// RemovablePhases returns the canonical phases that configuration may disable.
func RemovablePhases() []Phase {
	phases := make([]Phase, len(removablePhases))
	copy(phases, removablePhases[:])
	return phases
}

// FixedPhases returns the always-on phases that accept agent/model/effort
// overrides but no enabled toggle.
func FixedPhases() []Phase {
	phases := make([]Phase, len(fixedPhases))
	copy(phases, fixedPhases[:])
	return phases
}

// IsFixedPhase reports whether phase always runs (no enabled toggle).
func IsFixedPhase(phase Phase) bool {
	for _, fixed := range fixedPhases {
		if fixed == phase {
			return true
		}
	}
	return false
}

// AgentSettings contains a complete agent selection.
type AgentSettings struct {
	Agent      Agent           `json:"agent" yaml:"agent"`
	Model      string          `json:"model" yaml:"model"`
	Effort     Effort          `json:"effort" yaml:"effort"`
	Provenance ModelProvenance `json:"provenance,omitempty" yaml:"provenance,omitempty"`
}

// AgentSettingsOverride contains optional agent setting replacements. A zero
// value means the corresponding setting is inherited.
type AgentSettingsOverride struct {
	Agent      Agent           `json:"agent,omitempty" yaml:"agent,omitempty"`
	Model      string          `json:"model,omitempty" yaml:"model,omitempty"`
	Effort     Effort          `json:"effort,omitempty" yaml:"effort,omitempty"`
	Provenance ModelProvenance `json:"provenance,omitempty" yaml:"provenance,omitempty"`
}

// PhaseConfig is one complete, ordered folder configuration entry. Required
// phases must be enabled and cannot be removed from a complete configuration.
type PhaseConfig struct {
	Phase         Phase         `json:"phase" yaml:"phase"`
	Enabled       bool          `json:"enabled" yaml:"enabled"`
	Required      bool          `json:"required" yaml:"required"`
	AgentSettings AgentSettings `json:"settings" yaml:"settings"`
}

// PhaseOverride contains project- or run-specific settings for one phase.
// Enabled is a pointer so omitted is distinct from an explicit false value.
type PhaseOverride struct {
	Enabled               *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	AgentSettingsOverride `yaml:",inline"`
}

// GitOpsConfig contains effective repository integration settings.
type GitOpsConfig struct {
	ParentBranch string `json:"parent_branch" yaml:"parent_branch"`
	BaseRef      string `json:"base_ref" yaml:"base_ref"`
	EnablePR     bool   `json:"enable_pr" yaml:"enable_pr"`
	EnableCI     bool   `json:"enable_ci" yaml:"enable_ci"`
	// Configured distinguishes legacy configs with no GitOps section.
	Configured bool `json:"-" yaml:"-"`
}

// GitOpsOverride contains optional GitOps setting replacements.
type GitOpsOverride struct {
	ParentBranch string `json:"parent_branch,omitempty" yaml:"parent_branch,omitempty"`
	BaseRef      string `json:"base_ref,omitempty" yaml:"base_ref,omitempty"`
	EnablePR     *bool  `json:"enable_pr,omitempty" yaml:"enable_pr,omitempty"`
	EnableCI     *bool  `json:"enable_ci,omitempty" yaml:"enable_ci,omitempty"`
}

// DefaultGitOpsConfig returns the compatibility defaults used by old configs.
func DefaultGitOpsConfig() GitOpsConfig {
	return GitOpsConfig{ParentBranch: "main", BaseRef: "HEAD", EnablePR: true, EnableCI: true}
}

// GlobalConfig is the versioned, persisted global gg configuration.
type GlobalConfig struct {
	Version  SchemaVersion  `json:"version" yaml:"version"`
	Defaults AgentSettings  `json:"defaults" yaml:"defaults"`
	GitOps   GitOpsOverride `json:"gitops,omitempty" yaml:"gitops,omitempty"`
	// Folders is the machine-wide registry of configured project folders,
	// maintained by gg configure; the global project view lists projects
	// from every registered folder regardless of the current directory.
	Folders []string `json:"folders,omitempty" yaml:"folders,omitempty"`
}

// Clone returns a defensive copy of global configuration, including the
// folder registry slice.
func (config GlobalConfig) Clone() GlobalConfig {
	clone := config
	clone.Folders = append([]string(nil), config.Folders...)
	clone.GitOps = cloneGitOpsOverride(config.GitOps)
	return clone
}

// ProjectConfig is the versioned configuration persisted for a project folder.
type ProjectConfig struct {
	Version        SchemaVersion           `json:"version" yaml:"version"`
	Defaults       AgentSettingsOverride   `json:"defaults,omitempty" yaml:"defaults,omitempty"`
	PhaseOverrides map[Phase]PhaseOverride `json:"phase_overrides,omitempty" yaml:"phase_overrides,omitempty"`
	// Phases is non-nil for the complete self-contained schema. A nil value is
	// the legacy sparse format and is never silently expanded by a load.
	Phases []PhaseConfig  `json:"phases,omitempty" yaml:"phases,omitempty"`
	GitOps GitOpsOverride `json:"gitops,omitempty" yaml:"gitops,omitempty"`
}

// RunOverrides contains ephemeral overrides for one invocation. It is kept
// separate from persisted configuration contracts and must not be saved by a
// configuration store.
type RunOverrides struct {
	Defaults       AgentSettingsOverride   `json:"-" yaml:"-"`
	PhaseOverrides map[Phase]PhaseOverride `json:"-" yaml:"-"`
	GitOps         GitOpsOverride          `json:"-" yaml:"-"`
}

var (
	// ErrGlobalConfigNotFound indicates that gg has not been configured globally.
	ErrGlobalConfigNotFound = errors.New(`gg global configuration is missing; run "gg configure"`)

	// ErrProjectNotConfigured indicates that the current project folder has no gg configuration.
	ErrProjectNotConfigured = errors.New(`current project is not configured; run "gg configure" in the project folder`)
)

// RequiredPhases returns the phases that must be present and enabled in a
// newly saved complete configuration.
func RequiredPhases() []Phase {
	return []Phase{PhaseAcceptanceCriteria, PhaseGrooming, PhaseDevelopment, PhaseRebase, PhaseTestDocument}
}

// OptionalPhases returns the phases whose enabled state may be changed, in
// canonical pipeline order.
func OptionalPhases() []Phase {
	return []Phase{PhasePlanning, PhaseQA, PhaseBuildChecker, PhasePR, PhaseCI}
}

// CompletePhaseOrder returns the current folder-schema order. It is kept in
// config rather than inferred from map iteration so persistence is stable.
func CompletePhaseOrder() []Phase {
	return []Phase{PhaseAcceptanceCriteria, PhaseGrooming, PhasePlanning, PhaseDevelopment, PhaseRebase, PhaseQA, PhaseTestDocument, PhaseBuildChecker, PhasePR, PhaseCI}
}

// Clone returns a defensive copy of the project configuration.
func (config ProjectConfig) Clone() ProjectConfig {
	clone := config
	clone.PhaseOverrides = NormalizePhaseOverrides(config.PhaseOverrides)
	clone.GitOps = cloneGitOpsOverride(config.GitOps)
	if config.Phases != nil {
		clone.Phases = append([]PhaseConfig(nil), config.Phases...)
	}
	return clone
}

// CompleteProjectConfig constructs a self-contained folder configuration.
// The input phase order and values are copied before being stored.
func CompleteProjectConfig(version SchemaVersion, defaults AgentSettings, phases []PhaseConfig, gitops GitOpsOverride) ProjectConfig {
	return ProjectConfig{
		Version:  version,
		Defaults: AgentSettingsOverride{Agent: defaults.Agent, Model: defaults.Model, Effort: defaults.Effort, Provenance: defaults.Provenance},
		Phases:   append([]PhaseConfig(nil), phases...),
		GitOps:   cloneGitOpsOverride(gitops),
	}
}

func cloneGitOpsOverride(value GitOpsOverride) GitOpsOverride {
	clone := value
	if value.EnablePR != nil {
		enablePR := *value.EnablePR
		clone.EnablePR = &enablePR
	}
	if value.EnableCI != nil {
		enableCI := *value.EnableCI
		clone.EnableCI = &enableCI
	}
	return clone
}

// MaterializeCompleteProjectConfig resolves a legacy sparse folder only for
// an explicit configure/save boundary. It does not write anything and never
// mutates the input configuration.
func MaterializeCompleteProjectConfig(global GlobalConfig, project *ProjectConfig) (ProjectConfig, error) {
	classification := ProjectConfigMigrationRequired
	if project != nil {
		classification = ClassifyProjectConfig(*project)
		if classification == ProjectConfigMalformed {
			return ProjectConfig{}, errors.New("cannot materialize malformed project configuration")
		}
	}
	legacy := classification == ProjectConfigMigrationRequired
	resolutionProject := project
	if project != nil && project.Phases != nil {
		converted := project.Clone()
		converted.PhaseOverrides = NormalizePhaseOverrides(converted.PhaseOverrides)
		if converted.PhaseOverrides == nil {
			converted.PhaseOverrides = make(map[Phase]PhaseOverride, len(converted.Phases))
		}
		for _, entry := range converted.Phases {
			override := converted.PhaseOverrides[entry.Phase]
			if !IsFixedPhase(entry.Phase) {
				enabled := entry.Enabled
				override.Enabled = &enabled
			}
			override.AgentSettingsOverride = AgentSettingsOverride{
				Agent:      entry.AgentSettings.Agent,
				Model:      entry.AgentSettings.Model,
				Effort:     entry.AgentSettings.Effort,
				Provenance: entry.AgentSettings.Provenance,
			}
			converted.PhaseOverrides[entry.Phase] = override
		}
		converted.Phases = nil
		resolutionProject = &converted
	}
	resolved, err := Resolve(global, resolutionProject, RunOverrides{})
	if err != nil {
		return ProjectConfig{}, err
	}
	defaults := resolved.Defaults
	if legacy || defaults.Provenance == "" {
		defaults.Provenance = ModelProvenanceManual
	}
	phases := make([]PhaseConfig, 0, len(CompletePhaseOrder()))
	for _, phase := range CompletePhaseOrder() {
		settings := resolved.Phases[phase].AgentSettings
		if legacy || settings.Provenance == "" {
			settings.Provenance = ModelProvenanceManual
		}
		phases = append(phases, PhaseConfig{
			Phase:         phase,
			Enabled:       isRequiredPhase(phase) || resolved.Phases[phase].Enabled,
			Required:      isRequiredPhase(phase),
			AgentSettings: settings,
		})
	}
	var gitops GitOpsOverride
	if project != nil {
		gitops = project.GitOps
	}
	return CompleteProjectConfig(CompleteSchemaVersion, defaults, phases, gitops), nil
}

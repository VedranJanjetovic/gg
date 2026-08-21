package config

import "errors"

// SchemaVersion identifies the persisted configuration schema.
type SchemaVersion uint

// CurrentSchemaVersion is the schema version written by this release.
const CurrentSchemaVersion SchemaVersion = 1

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
	Agent  Agent  `json:"agent" yaml:"agent"`
	Model  string `json:"model" yaml:"model"`
	Effort Effort `json:"effort" yaml:"effort"`
}

// AgentSettingsOverride contains optional agent setting replacements. A zero
// value means the corresponding setting is inherited.
type AgentSettingsOverride struct {
	Agent  Agent  `json:"agent,omitempty" yaml:"agent,omitempty"`
	Model  string `json:"model,omitempty" yaml:"model,omitempty"`
	Effort Effort `json:"effort,omitempty" yaml:"effort,omitempty"`
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

// ProjectConfig is the versioned configuration persisted for a project folder.
type ProjectConfig struct {
	Version        SchemaVersion           `json:"version" yaml:"version"`
	Defaults       AgentSettingsOverride   `json:"defaults,omitempty" yaml:"defaults,omitempty"`
	PhaseOverrides map[Phase]PhaseOverride `json:"phase_overrides,omitempty" yaml:"phase_overrides,omitempty"`
	GitOps         GitOpsOverride          `json:"gitops,omitempty" yaml:"gitops,omitempty"`
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

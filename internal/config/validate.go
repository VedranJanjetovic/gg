package config

import (
	"fmt"
	"strings"
)

// ValidateGlobalConfig validates a complete persisted global configuration.
func ValidateGlobalConfig(config GlobalConfig) error {
	if err := validateVersion(config.Version); err != nil {
		return err
	}
	if err := validateAgentSettings(config.Defaults, "defaults"); err != nil {
		return err
	}
	return validateGitOpsOverride(config.GitOps, "gitops")
}

// ValidateCompleteGlobalConfig validates the whole-tuple global contract.
// Legacy global files may omit provenance; newly saved complete folders use
// this stricter check for their copied default.
func ValidateCompleteGlobalConfig(config GlobalConfig) error {
	if err := ValidateGlobalConfig(config); err != nil {
		return err
	}
	return validateProvenance(config.Defaults.Provenance, "defaults.provenance")
}

// ValidateAgentSettings validates one complete effective agent selection.
// Persisted execution snapshots use this to fail closed when restoring a run.
func ValidateAgentSettings(settings AgentSettings) error {
	return validateAgentSettings(settings, "agent settings")
}

// ValidateProjectConfig validates persisted project overrides. The linting
// phase alias is accepted and can be canonicalized with NormalizePhaseOverrides.
func ValidateProjectConfig(config ProjectConfig) error {
	if config.Phases != nil {
		return validateCompleteProjectConfig(config)
	}
	if err := validateVersion(config.Version); err != nil {
		return err
	}
	if err := validateAgentSettingsOverride(config.Defaults, "defaults"); err != nil {
		return err
	}
	if err := validateGitOpsOverride(config.GitOps, "gitops"); err != nil {
		return err
	}
	return validatePhaseOverrides(config.PhaseOverrides)
}

// ValidateCompleteProjectConfig validates a self-contained folder
// configuration. It intentionally rejects the sparse override shape.
func ValidateCompleteProjectConfig(config ProjectConfig) error {
	return validateCompleteProjectConfig(config)
}

func validateCompleteProjectConfig(config ProjectConfig) error {
	if config.Version != CompleteSchemaVersion {
		return fmt.Errorf("version: unsupported complete schema version %d; expected %d", config.Version, CompleteSchemaVersion)
	}
	if err := validateCompleteSettings(config.Defaults, "defaults"); err != nil {
		return err
	}
	if config.PhaseOverrides != nil {
		return fmt.Errorf("phase_overrides: sparse overrides are not allowed in complete configuration")
	}
	if err := validateGitOpsOverride(config.GitOps, "gitops"); err != nil {
		return err
	}
	if len(config.Phases) == 0 {
		return fmt.Errorf("phases: complete configuration must contain phase entries")
	}
	seen := make(map[Phase]struct{}, len(config.Phases))
	order := CompletePhaseOrder()
	for index, entry := range config.Phases {
		field := fmt.Sprintf("phases[%d]", index)
		if !isSupportedPhase(entry.Phase) {
			return fmt.Errorf("%s.phase: unsupported phase %q", field, entry.Phase)
		}
		if _, ok := seen[entry.Phase]; ok {
			return fmt.Errorf("%s.phase: duplicate phase %q", field, entry.Phase)
		}
		if index >= len(order) || entry.Phase != order[index] {
			want := ""
			if index < len(order) {
				want = string(order[index])
			}
			return fmt.Errorf("%s.phase: expected ordered phase %q, got %q", field, want, entry.Phase)
		}
		seen[entry.Phase] = struct{}{}
		required := isRequiredPhase(entry.Phase)
		if entry.Required != required {
			return fmt.Errorf("%s.required: must be %t for phase %q", field, required, entry.Phase)
		}
		if required && !entry.Enabled {
			return fmt.Errorf("%s.enabled: required phase %q must be enabled", field, entry.Phase)
		}
		if err := validateCompleteAgentSettings(entry.AgentSettings, field+".settings"); err != nil {
			return err
		}
	}
	for _, phase := range append(RequiredPhases(), OptionalPhases()...) {
		if _, ok := seen[phase]; !ok {
			return fmt.Errorf("phases: missing phase %q", phase)
		}
	}
	return nil
}

// ValidateRunOverrides validates ephemeral overrides for one invocation.
func ValidateRunOverrides(overrides RunOverrides) error {
	if err := validateAgentSettingsOverride(overrides.Defaults, "defaults"); err != nil {
		return err
	}
	if err := validateGitOpsOverride(overrides.GitOps, "gitops"); err != nil {
		return err
	}
	return validatePhaseOverrides(overrides.PhaseOverrides)
}

func validateGitOpsOverride(settings GitOpsOverride, field string) error {
	if settings.ParentBranch != "" && strings.TrimSpace(settings.ParentBranch) == "" {
		return fmt.Errorf("%s.parent_branch: must be non-empty", field)
	}
	if settings.BaseRef != "" && strings.TrimSpace(settings.BaseRef) == "" {
		return fmt.Errorf("%s.base_ref: must be non-empty", field)
	}
	return nil
}
func ValidateGitOpsConfig(settings GitOpsConfig) error {
	if strings.TrimSpace(settings.ParentBranch) == "" {
		return fmt.Errorf("gitops.parent_branch: must be non-empty")
	}
	if strings.TrimSpace(settings.BaseRef) == "" {
		return fmt.Errorf("gitops.base_ref: must be non-empty")
	}
	return nil
}

// NormalizePhaseOverrides returns a copy whose linting phase key is replaced by
// build_checker. If both keys are present, the canonical build_checker value
// wins. Nil input remains nil.
func NormalizePhaseOverrides(overrides map[Phase]PhaseOverride) map[Phase]PhaseOverride {
	if overrides == nil {
		return nil
	}

	normalized := make(map[Phase]PhaseOverride, len(overrides))
	_, hasCanonical := overrides[PhaseBuildChecker]
	for phase, override := range overrides {
		if phase == PhaseLintingAlias {
			if hasCanonical {
				continue
			}
			phase = PhaseBuildChecker
		}
		normalized[phase] = clonePhaseOverride(override)
	}
	return normalized
}

func validateVersion(version SchemaVersion) error {
	if version != CurrentSchemaVersion {
		return fmt.Errorf("version: unsupported schema version %d; expected %d", version, CurrentSchemaVersion)
	}
	return nil
}

func validateAgentSettings(settings AgentSettings, field string) error {
	if err := validateAgent(settings.Agent, field+".agent", false); err != nil {
		return err
	}
	if err := validateModel(settings.Model, field+".model", false); err != nil {
		return err
	}
	return validateEffort(settings.Effort, field+".effort", false)
}

func validateCompleteSettings(settings AgentSettingsOverride, field string) error {
	if err := validateAgentSettingsOverride(settings, field); err != nil {
		return err
	}
	if err := validateProvenance(settings.Provenance, field+".provenance"); err != nil {
		return err
	}
	return nil
}

func validateCompleteAgentSettings(settings AgentSettings, field string) error {
	if err := validateAgentSettings(settings, field); err != nil {
		return err
	}
	return validateProvenance(settings.Provenance, field+".provenance")
}

func validateProvenance(provenance ModelProvenance, field string) error {
	switch provenance {
	case ModelProvenanceCatalog, ModelProvenanceManual:
		return nil
	default:
		return fmt.Errorf("%s: must be %q or %q", field, ModelProvenanceCatalog, ModelProvenanceManual)
	}
}

func validateAgentSettingsOverride(settings AgentSettingsOverride, field string) error {
	if err := validateAgent(settings.Agent, field+".agent", true); err != nil {
		return err
	}
	if err := validateModel(settings.Model, field+".model", true); err != nil {
		return err
	}
	return validateEffort(settings.Effort, field+".effort", true)
}

func isRequiredPhase(phase Phase) bool {
	for _, required := range RequiredPhases() {
		if required == phase {
			return true
		}
	}
	return false
}

func validateAgent(agent Agent, field string, optional bool) error {
	if agent == "" && optional {
		return nil
	}
	if agent != AgentClaude && agent != AgentCodex {
		return fmt.Errorf("%s: unsupported agent %q; use %q or %q", field, agent, AgentClaude, AgentCodex)
	}
	return nil
}

func validateModel(model, field string, optional bool) error {
	if model == "" && optional {
		return nil
	}
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("%s: model must be non-empty", field)
	}
	return nil
}

func validateEffort(effort Effort, field string, optional bool) error {
	if effort == "" && optional {
		return nil
	}
	switch effort {
	case EffortLow, EffortMedium, EffortHigh:
		return nil
	default:
		return fmt.Errorf("%s: unsupported effort %q; use %q, %q, or %q", field, effort, EffortLow, EffortMedium, EffortHigh)
	}
}

func validatePhaseOverrides(overrides map[Phase]PhaseOverride) error {
	for phase, override := range overrides {
		if !isSupportedPhase(phase) {
			return fmt.Errorf("phase_overrides[%s]: unsupported phase; use a removable phase (grooming, planning, qa, build_checker (or linting), pr, ci) or a fixed one (acceptance_criteria, development, rebase, test_document)", phase)
		}
		if IsFixedPhase(phase) && override.Enabled != nil {
			return fmt.Errorf("phase_overrides[%s]: this phase always runs and cannot be enabled or disabled; only agent, model, and effort may be overridden", phase)
		}
		field := fmt.Sprintf("phase_overrides[%s]", phase)
		if err := validateAgentSettingsOverride(override.AgentSettingsOverride, field); err != nil {
			return err
		}
	}
	return nil
}

func isSupportedPhase(phase Phase) bool {
	switch phase {
	case PhaseGrooming, PhasePlanning, PhaseQA, PhaseBuildChecker, PhaseLintingAlias, PhasePR, PhaseCI:
		return true
	default:
		return IsFixedPhase(phase)
	}
}

func clonePhaseOverride(override PhaseOverride) PhaseOverride {
	if override.Enabled == nil {
		return override
	}
	enabled := *override.Enabled
	override.Enabled = &enabled
	return override
}

package config

import "fmt"

// ResolvedPhase contains the effective settings for one canonical phase.
type ResolvedPhase struct {
	Enabled bool
	AgentSettings
}

// ResolvedConfig contains the effective configuration keyed by canonical phase.
// It always contains the phases returned by RemovablePhases plus the fixed
// phases (FixedPhases), whose Enabled is always true.
type ResolvedConfig struct {
	// Defaults contains the effective settings for phases that are mandatory
	// and therefore cannot have phase-specific configuration entries.
	Defaults AgentSettings
	Phases   map[Phase]ResolvedPhase
	GitOps   GitOpsConfig
}

// Resolve applies configuration layers from least to most specific:
// built-in phase defaults, global defaults, project overrides, then one-run
// overrides. A nil project means no project layer is applied.
func Resolve(global GlobalConfig, project *ProjectConfig, run RunOverrides) (ResolvedConfig, error) {
	if err := ValidateGlobalConfig(global); err != nil {
		return ResolvedConfig{}, fmt.Errorf("global config: %w", err)
	}
	if project != nil {
		if err := ValidateProjectConfig(*project); err != nil {
			return ResolvedConfig{}, fmt.Errorf("project config: %w", err)
		}
	}
	if err := ValidateRunOverrides(run); err != nil {
		return ResolvedConfig{}, fmt.Errorf("run overrides: %w", err)
	}

	defaults := global.Defaults
	gitops := DefaultGitOpsConfig()
	gitops.Configured = hasGitOpsOverride(global.GitOps)
	gitops = mergeGitOps(gitops, global.GitOps)
	if project != nil {
		defaults = mergeAgentSettings(defaults, project.Defaults)
		gitops.Configured = gitops.Configured || hasGitOpsOverride(project.GitOps)
		gitops = mergeGitOps(gitops, project.GitOps)
	}
	defaults = mergeAgentSettings(defaults, run.Defaults)
	gitops.Configured = gitops.Configured || hasGitOpsOverride(run.GitOps)
	gitops = mergeGitOps(gitops, run.GitOps)

	phases := builtInPhaseDefaults()
	applyDefaults(phases, AgentSettingsOverride{
		Agent:  global.Defaults.Agent,
		Model:  global.Defaults.Model,
		Effort: global.Defaults.Effort,
	})
	phases[PhasePR] = withEnabled(phases[PhasePR], gitops.EnablePR)
	phases[PhaseCI] = withEnabled(phases[PhaseCI], gitops.EnableCI)

	if project != nil {
		applyDefaults(phases, project.Defaults)
		applyPhaseOverrides(phases, NormalizePhaseOverrides(project.PhaseOverrides))
	}
	applyDefaults(phases, run.Defaults)
	applyPhaseOverrides(phases, NormalizePhaseOverrides(run.PhaseOverrides))

	return ResolvedConfig{Defaults: defaults, Phases: phases, GitOps: gitops}, nil
}

func builtInPhaseDefaults() map[Phase]ResolvedPhase {
	phases := make(map[Phase]ResolvedPhase, len(removablePhases)+len(fixedPhases))
	for _, phase := range removablePhases {
		phases[phase] = ResolvedPhase{Enabled: true}
	}
	for _, phase := range fixedPhases {
		phases[phase] = ResolvedPhase{Enabled: true}
	}
	return phases
}

func applyDefaults(phases map[Phase]ResolvedPhase, override AgentSettingsOverride) {
	for phase, resolved := range phases {
		resolved.AgentSettings = mergeAgentSettings(resolved.AgentSettings, override)
		phases[phase] = resolved
	}
}

func applyPhaseOverrides(phases map[Phase]ResolvedPhase, overrides map[Phase]PhaseOverride) {
	for phase, override := range overrides {
		resolved := phases[phase]
		// Fixed phases always run; validation rejects their Enabled
		// overrides, and this guard keeps resolution fail-safe regardless.
		if override.Enabled != nil && !IsFixedPhase(phase) {
			resolved.Enabled = *override.Enabled
		}
		resolved.AgentSettings = mergeAgentSettings(resolved.AgentSettings, override.AgentSettingsOverride)
		phases[phase] = resolved
	}
}

func mergeGitOps(base GitOpsConfig, override GitOpsOverride) GitOpsConfig {
	if override.ParentBranch != "" {
		base.ParentBranch = override.ParentBranch
	}
	if override.BaseRef != "" {
		base.BaseRef = override.BaseRef
	}
	if override.EnablePR != nil {
		base.EnablePR = *override.EnablePR
	}
	if override.EnableCI != nil {
		base.EnableCI = *override.EnableCI
	}
	return base
}
func withEnabled(p ResolvedPhase, enabled bool) ResolvedPhase { p.Enabled = enabled; return p }

func mergeAgentSettings(base AgentSettings, override AgentSettingsOverride) AgentSettings {
	if override.Agent != "" {
		base.Agent = override.Agent
	}
	if override.Model != "" {
		base.Model = override.Model
	}
	if override.Effort != "" {
		base.Effort = override.Effort
	}
	return base
}

func hasGitOpsOverride(value GitOpsOverride) bool {
	return value.ParentBranch != "" || value.BaseRef != "" || value.EnablePR != nil || value.EnableCI != nil
}

package pipeline

import (
	"fmt"

	"github.com/VedranJanjetovic/gg/internal/config"
)

// ExecutablePhase is one enabled phase with its effective optional settings.
type ExecutablePhase struct {
	phase    Phase
	settings *config.AgentSettings
}

// Phase returns the canonical definition for the executable phase.
func (p ExecutablePhase) Phase() Phase {
	return p.phase
}

// Settings returns the resolved settings when the phase is configurable.
func (p ExecutablePhase) Settings() (config.AgentSettings, bool) {
	if p.settings == nil {
		return config.AgentSettings{}, false
	}
	return *p.settings, true
}

// ExecutablePipeline is an ordered plan containing only enabled phases.
type ExecutablePipeline struct {
	phases []ExecutablePhase
	// legacyOrder marks a plan restored from a snapshot written before Rebase
	// moved ahead of QA. Re-snapshotting such a plan must record that order as
	// deliberate instead of validating it against the current phase order.
	legacyOrder bool
}

// Phases returns the executable phases in order.
func (p ExecutablePipeline) Phases() []ExecutablePhase {
	if p.phases == nil {
		return nil
	}
	phases := make([]ExecutablePhase, len(p.phases))
	copy(phases, p.phases)
	return phases
}

// GitOpsOnly returns the monitoring/pipeline phases that may be rerun after
// development has finished. It never reactivates the development flow.
func (p ExecutablePipeline) GitOpsOnly() ExecutablePipeline {
	selected := make([]ExecutablePhase, 0, 2)
	for _, phase := range p.phases {
		if phase.Phase().ID() == PhasePR || phase.Phase().ID() == PhaseCI {
			selected = append(selected, phase)
		}
	}
	return ExecutablePipeline{phases: selected, legacyOrder: p.legacyOrder}
}

// PhaseContracts returns the canonical contract text for each enabled phase.
// The executable pipeline is already filtered by resolved configuration, so
// this map cannot contain contracts for disabled phases.
func (p ExecutablePipeline) PhaseContracts() map[PhaseID]string {
	contracts := make(map[PhaseID]string, len(p.phases))
	for _, phase := range p.phases {
		id := phase.Phase().ID()
		contract, ok := PhaseContract(id)
		if !ok {
			// Resolve validates canonical phase IDs before an executable pipeline is
			// created. Keep this method total for zero-value/manual plans without
			// falling back to a display label as executable instructions.
			continue
		}
		contracts[id] = contract
	}
	return contracts
}

// Resolve combines the canonical pipeline with VED-17 resolved configuration
// into an executable ordered plan. It is pure and does not mutate either input.
func Resolve(defaults Pipeline, resolved config.ResolvedConfig) (ExecutablePipeline, error) {
	phases := defaults.Phases()
	if err := validateDefaultPipeline(phases); err != nil {
		return ExecutablePipeline{}, err
	}
	if err := validateResolvedPhases(phases, resolved.Phases); err != nil {
		return ExecutablePipeline{}, err
	}
	if err := validatePRCICompatibility(resolved.Phases); err != nil {
		return ExecutablePipeline{}, err
	}

	executable := make([]ExecutablePhase, 0, len(phases))
	for _, phase := range phases {
		if !phase.Metadata().Optional {
			// Fixed phases always run; a resolved entry carries their
			// per-phase agent/model/effort, defaults otherwise.
			settings := resolved.Defaults
			if configured, ok := resolved.Phases[config.Phase(phase.ID())]; ok {
				settings = configured.AgentSettings
			}
			executable = append(executable, ExecutablePhase{phase: phase, settings: &settings})
			continue
		}

		settings := resolved.Phases[config.Phase(phase.ID())]
		if !settings.Enabled {
			continue
		}
		effectiveSettings := settings.AgentSettings
		executable = append(executable, ExecutablePhase{phase: phase, settings: &effectiveSettings})
	}
	return ExecutablePipeline{phases: executable}, nil
}

func validateDefaultPipeline(phases []Phase) error {
	seen := make(map[PhaseID]struct{}, len(phases))
	for index, phase := range phases {
		id := phase.ID()
		if !isCanonicalPhase(id) {
			return fmt.Errorf("default pipeline phase %d has unknown ID %q; use a canonical phase ID", index, id)
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("default pipeline defines phase %q more than once; each canonical phase must appear once", id)
		}
		seen[id] = struct{}{}
		if phase.Metadata().DisplayName == "" {
			return fmt.Errorf("default pipeline phase %q has no display name", id)
		}
	}

	canonical := DefaultPipeline().Phases()
	if len(phases) != len(canonical) {
		return fmt.Errorf("default pipeline defines %d phases; expected all %d canonical phases", len(phases), len(canonical))
	}
	for index, expected := range canonical {
		actual := phases[index]
		if actual.ID() != expected.ID() {
			return fmt.Errorf("default pipeline phase %d is %q; expected canonical phase %q", index, actual.ID(), expected.ID())
		}
		if actual.Metadata().Optional != expected.Metadata().Optional {
			return fmt.Errorf("default pipeline phase %q optional=%t; expected optional=%t to match the configuration contract", actual.ID(), actual.Metadata().Optional, expected.Metadata().Optional)
		}
	}
	return nil
}

func validateResolvedPhases(phases []Phase, resolved map[config.Phase]config.ResolvedPhase) error {
	optional := make(map[config.Phase]struct{}, len(phases))
	canonical := make(map[config.Phase]struct{}, len(phases))
	for _, phase := range phases {
		id := config.Phase(phase.ID())
		canonical[id] = struct{}{}
		if phase.Metadata().Optional {
			optional[id] = struct{}{}
		}
	}

	for phase, settings := range resolved {
		if _, exists := optional[phase]; exists {
			continue
		}
		if _, exists := canonical[phase]; exists {
			// Fixed phases may carry agent/model/effort settings but can
			// never be disabled.
			if !settings.Enabled {
				return fmt.Errorf("resolved configuration disables non-optional phase %q; fixed phases always run", phase)
			}
			continue
		}
		return fmt.Errorf("resolved configuration references unknown phase %q; resolve configuration before building the pipeline", phase)
	}
	for phase := range optional {
		if _, exists := resolved[phase]; !exists {
			return fmt.Errorf("resolved configuration is missing optional phase %q; resolve configuration before building the pipeline", phase)
		}
	}
	return nil
}

func validatePRCICompatibility(resolved map[config.Phase]config.ResolvedPhase) error {
	pr := resolved[config.PhasePR]
	ci := resolved[config.PhaseCI]
	if ci.Enabled && !pr.Enabled {
		return fmt.Errorf("CI phase is enabled while PR phase is disabled; enable PR or disable CI because no alternate CI execution mode exists")
	}
	return nil
}

func isCanonicalPhase(id PhaseID) bool {
	switch id {
	case PhaseAcceptanceCriteria, PhaseGrooming, PhasePlanning, PhaseDevelopment,
		PhaseQA, PhaseRebase, PhaseTestDocument, PhaseBuildChecker, PhasePR, PhaseCI:
		return true
	default:
		return false
	}
}

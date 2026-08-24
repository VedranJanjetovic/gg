package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
	"github.com/VedranJanjetovic/gg/internal/tui"
)

type projectSnapshotEditor interface {
	CompareAndUpdateProjectSnapshot(context.Context, string, state.PipelineConfigSnapshot, func(*state.ProjectState) error) (state.ProjectState, error)
}

// configureExistingProject edits only the complete tuples in the immutable
// project snapshot. The picker runs after the progress TUI has released the
// terminal, and cancellation is deliberately a no-op.
func (a *App) configureExistingProject(ctx context.Context, selector string, _ state.ProjectState) error {
	service, err := a.projectService(ctx)
	if err != nil {
		return err
	}
	project, err := service.Load(ctx, selector)
	if err != nil {
		return err
	}
	if project.Status != state.StatusFailed && project.Status != state.StatusStopped {
		return errors.New("project configuration can be edited only while failed or stopped")
	}
	editor, ok := service.(projectSnapshotEditor)
	if !ok {
		return errors.New("project service does not support project configuration editing")
	}
	configuration, err := pipeline.ReadProjectExecutionConfiguration(project.PipelineConfig)
	if err != nil {
		return fmt.Errorf("read project configuration: %w", err)
	}
	catalog, err := a.agentCatalog(ctx)
	if err != nil {
		return err
	}
	picker := a.configurePicker
	if picker == nil {
		picker = tui.RunConfigureWizard
	}
	picked, err := picker(ctx, catalog, wizardDefaultsFromExecution(configuration), a.input, a.output.Stdout)
	if errors.Is(err, tui.ErrPickerCancelled) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := validateCompletePickerSelection(catalog, picked); err != nil {
		return fmt.Errorf("validate project configuration: %w", err)
	}
	updated := executionConfigurationFromPicker(configuration, picked)
	updatedSnapshot, err := pipeline.UpdateProjectExecutionConfiguration(project.PipelineConfig, updated)
	if err != nil {
		return fmt.Errorf("update project configuration: %w", err)
	}
	changed := changedExecutionPhases(configuration, updated)
	if _, err := editor.CompareAndUpdateProjectSnapshot(ctx, selector, project.PipelineConfig, func(current *state.ProjectState) error {
		if current.Status != state.StatusFailed && current.Status != state.StatusStopped {
			return errors.New("project started while configuration was being edited")
		}
		current.PipelineConfig = updatedSnapshot
		for phase := range changed {
			delete(current.PhaseConfigurationWarnings, phase)
		}
		if len(current.PhaseConfigurationWarnings) == 0 {
			current.PhaseConfigurationWarnings = nil
		}
		return nil
	}); err != nil {
		return fmt.Errorf("save project configuration: %w", err)
	}
	return nil
}

func (a *App) agentCatalog(ctx context.Context) (config.AgentCatalog, error) {
	source := a.catalogSource
	if source == nil {
		source = config.NewDefaultAgentCatalogSource()
	}
	catalog, err := source.AgentCatalog(ctx)
	if err != nil {
		return config.AgentCatalog{}, fmt.Errorf("load agent/model catalog: %w", err)
	}
	return catalog, nil
}

func wizardDefaultsFromExecution(configuration pipeline.ProjectExecutionConfiguration) tui.WizardDefaults {
	defaults := tui.WizardDefaults{
		Agent: configuration.Default.Agent, Model: configuration.Default.Model, Effort: configuration.Default.Effort,
		FullTuples: true, Manual: configuration.Default.Provenance == config.ModelProvenanceManual,
	}
	for _, phase := range configuration.Phases {
		defaults.Phases = append(defaults.Phases, tui.PhaseState{
			Phase: config.Phase(phase.ID), Name: string(phase.ID), Enabled: phase.Enabled, Locked: phase.Required,
			Agent: phase.Settings.Agent, Model: phase.Settings.Model, Effort: phase.Settings.Effort,
			Manual: phase.Settings.Provenance == config.ModelProvenanceManual,
		})
	}
	return defaults
}

func executionConfigurationFromPicker(before pipeline.ProjectExecutionConfiguration, picked tui.PickerResult) pipeline.ProjectExecutionConfiguration {
	result := before.Clone()
	result.Default = config.AgentSettings{Agent: picked.Agent, Model: picked.Model, Effort: picked.Effort, Provenance: config.ModelProvenanceCatalog}
	if picked.Manual {
		result.Default.Provenance = config.ModelProvenanceManual
	}
	byPhase := make(map[config.Phase]tui.PhaseState, len(picked.Phases))
	for _, phase := range picked.Phases {
		byPhase[phase.Phase] = phase
	}
	for index := range result.Phases {
		phase := &result.Phases[index]
		pickedPhase, ok := byPhase[config.Phase(phase.ID)]
		if !ok {
			continue
		}
		phase.Settings = config.AgentSettings{Agent: pickedPhase.Agent, Model: pickedPhase.Model, Effort: pickedPhase.Effort, Provenance: config.ModelProvenanceCatalog}
		if pickedPhase.Manual {
			phase.Settings.Provenance = config.ModelProvenanceManual
		}
	}
	return result
}

func changedExecutionPhases(before, after pipeline.ProjectExecutionConfiguration) map[string]struct{} {
	changed := make(map[string]struct{})
	byID := make(map[pipeline.PhaseID]config.AgentSettings, len(after.Phases))
	for _, phase := range after.Phases {
		byID[phase.ID] = phase.Settings
	}
	for _, phase := range before.Phases {
		if settings, ok := byID[phase.ID]; ok && settings != phase.Settings {
			changed[string(phase.ID)] = struct{}{}
		}
	}
	return changed
}

func (a *App) repairCurrentPhaseConfiguration(ctx context.Context, selector string, project state.ProjectState) (state.ProjectState, error) {
	if project.Status != state.StatusFailed || len(project.PhaseHistory) == 0 {
		return project, nil
	}
	record := project.PhaseHistory[len(project.PhaseHistory)-1]
	if record.Status != state.StatusFailed || record.Phase != project.CurrentPhase || record.Subphase != project.CurrentSubphase {
		return project, nil
	}
	configuration, err := pipeline.ReadProjectExecutionConfiguration(project.PipelineConfig)
	if err != nil {
		return project, err
	}
	phaseIndex := -1
	for index, phase := range configuration.Phases {
		if string(phase.ID) == record.Phase {
			phaseIndex = index
			break
		}
	}
	if phaseIndex < 0 || !needsCatalogRepair(configuration.Phases[phaseIndex].Settings, project.PipelineConfig, record.Phase) {
		return project, nil
	}
	catalog, err := a.agentCatalog(ctx)
	if err != nil {
		return project, err
	}
	if !knownCrossAgentMismatch(catalog, configuration.Phases[phaseIndex].Settings) {
		return project, nil
	}
	fallbacks, err := a.repairFallbacks(ctx, configuration.Default)
	if err != nil {
		return project, err
	}
	var fallback config.AgentSettings
	var source string
	for _, candidate := range fallbacks {
		if normalized, ok := normalizeRepairTuple(catalog, candidate.settings); ok {
			fallback, source = normalized, candidate.source
			break
		}
	}
	if source == "" {
		return project, fmt.Errorf("resume phase %q: no complete valid fallback configuration", record.Phase)
	}
	configuration.Phases[phaseIndex].Settings = fallback
	updatedSnapshot, err := pipeline.UpdateProjectExecutionConfiguration(project.PipelineConfig, configuration)
	if err != nil {
		return project, err
	}
	warning := fmt.Sprintf("invalid saved configuration; using %s: %s", source, formatAgentSettings(fallback))
	editor, serviceErr := a.projectService(ctx)
	if serviceErr != nil {
		return project, serviceErr
	}
	atomic, ok := editor.(projectSnapshotEditor)
	if !ok {
		return project, errors.New("project service does not support resume configuration repair")
	}
	updated, err := atomic.CompareAndUpdateProjectSnapshot(ctx, selector, project.PipelineConfig, func(current *state.ProjectState) error {
		if current.Status != state.StatusFailed || len(current.PhaseHistory) == 0 || current.PhaseHistory[len(current.PhaseHistory)-1].OccurrenceID != record.OccurrenceID {
			return errors.New("failed execution changed while preparing resume")
		}
		current.PipelineConfig = updatedSnapshot
		if current.PhaseConfigurationWarnings == nil {
			current.PhaseConfigurationWarnings = make(map[string]string)
		}
		current.PhaseConfigurationWarnings[record.Phase] = warning
		return nil
	})
	if err != nil {
		return project, fmt.Errorf("persist resume configuration repair: %w", err)
	}
	return updated, nil
}

type repairFallback struct {
	source   string
	settings config.AgentSettings
}

func (a *App) repairFallbacks(ctx context.Context, projectDefault config.AgentSettings) ([]repairFallback, error) {
	result := []repairFallback{{source: "project default", settings: projectDefault}}
	root, err := a.root.ConfiguredRoot(ctx)
	if err == nil && a.store != nil {
		if folder, loadErr := a.store.LoadProject(root); loadErr == nil {
			if settings, ok := completeOverrideSettings(folder.Defaults); ok {
				result = append(result, repairFallback{source: "folder default", settings: settings})
			}
		}
	}
	if a.store != nil {
		if global, loadErr := a.store.LoadGlobal(); loadErr == nil {
			if settings, ok := completeAgentSettings(global.Defaults); ok {
				result = append(result, repairFallback{source: "global default", settings: settings})
			}
		}
	}
	return result, nil
}

func completeOverrideSettings(settings config.AgentSettingsOverride) (config.AgentSettings, bool) {
	if settings.Agent == "" || strings.TrimSpace(settings.Model) == "" || settings.Effort == "" {
		return config.AgentSettings{}, false
	}
	provenance := settings.Provenance
	if provenance == "" {
		provenance = config.ModelProvenanceManual
	}
	return config.AgentSettings{Agent: settings.Agent, Model: settings.Model, Effort: settings.Effort, Provenance: provenance}, true
}

func completeAgentSettings(settings config.AgentSettings) (config.AgentSettings, bool) {
	if settings.Agent == "" || strings.TrimSpace(settings.Model) == "" || settings.Effort == "" {
		return config.AgentSettings{}, false
	}
	if settings.Provenance == "" {
		settings.Provenance = config.ModelProvenanceManual
	}
	return settings, true
}

func needsCatalogRepair(settings config.AgentSettings, _ state.PipelineConfigSnapshot, _ string) bool {
	return settings.Provenance != config.ModelProvenanceManual
}

func knownCrossAgentMismatch(catalog config.AgentCatalog, settings config.AgentSettings) bool {
	if settings.Provenance == config.ModelProvenanceManual {
		return false
	}
	if entry, ok := catalog.Lookup(settings.Agent); ok {
		for _, model := range entry.Models {
			if model == settings.Model {
				return false
			}
		}
	}
	for _, entry := range catalog.Entries() {
		if entry.Agent == settings.Agent {
			continue
		}
		for _, model := range entry.Models {
			if model == settings.Model {
				return true
			}
		}
	}
	return false
}

func normalizeRepairTuple(catalog config.AgentCatalog, settings config.AgentSettings) (config.AgentSettings, bool) {
	if err := config.ValidateAgentSettings(settings); err != nil {
		return config.AgentSettings{}, false
	}
	if settings.Provenance == config.ModelProvenanceManual {
		return settings, true
	}
	if settings.Provenance == config.ModelProvenanceCatalog {
		return settings, catalog.ValidateSettings(settings) == nil
	}
	if entry, ok := catalog.Lookup(settings.Agent); ok {
		for _, model := range entry.Models {
			if model == settings.Model {
				settings.Provenance = config.ModelProvenanceCatalog
				return settings, true
			}
		}
	}
	for _, entry := range catalog.Entries() {
		if entry.Agent == settings.Agent {
			continue
		}
		for _, model := range entry.Models {
			if model == settings.Model {
				return config.AgentSettings{}, false
			}
		}
	}
	settings.Provenance = config.ModelProvenanceManual
	return settings, true
}

func formatAgentSettings(settings config.AgentSettings) string {
	return fmt.Sprintf("%s / %s / %s", settings.Agent, settings.Model, settings.Effort)
}

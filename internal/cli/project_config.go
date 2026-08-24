package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
	"github.com/VedranJanjetovic/gg/internal/tui"
)

type projectCreationConfiguration struct {
	project  config.ProjectConfig
	snapshot state.PipelineConfigSnapshot
}

func (a *App) chooseNewProjectConfiguration(ctx context.Context, output io.Writer, maxQAAttempts int) (projectCreationConfiguration, error) {
	root, err := a.root.ConfiguredRoot(ctx)
	if err != nil {
		return projectCreationConfiguration{}, fmt.Errorf("resolve configured root: %w", err)
	}
	global, err := a.store.LoadGlobal()
	if err != nil {
		return projectCreationConfiguration{}, fmt.Errorf("load global configuration: %w", err)
	}
	folder, err := a.loadCompleteFolderConfiguration(ctx, root, global, output)
	if err != nil {
		return projectCreationConfiguration{}, err
	}
	selected := folder
	if tui.InteractiveTerminal(a.input, output) {
		choice, choiceErr := a.chooseProjectConfig(ctx, output)
		if choiceErr != nil {
			return projectCreationConfiguration{}, choiceErr
		}
		if choice == 1 {
			selected, err = a.pickProjectConfiguration(ctx, folder, output)
			if err != nil {
				return projectCreationConfiguration{}, err
			}
		} else if choice != 0 {
			return projectCreationConfiguration{}, fmt.Errorf("unknown project configuration choice %d", choice)
		}
	}
	resolved, err := config.Resolve(global, &selected, config.RunOverrides{})
	if err != nil {
		return projectCreationConfiguration{}, fmt.Errorf("resolve project configuration: %w", err)
	}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		return projectCreationConfiguration{}, fmt.Errorf("resolve project pipeline: %w", err)
	}
	snapshot, err := pipeline.SnapshotProjectExecution(plan, pipeline.DevelopmentSubphaseGeneration{}, maxQAAttempts, selected, resolved.GitOps)
	if err != nil {
		return projectCreationConfiguration{}, fmt.Errorf("snapshot project configuration: %w", err)
	}
	return projectCreationConfiguration{project: selected, snapshot: snapshot}, nil
}

func (a *App) chooseProjectConfig(ctx context.Context, output io.Writer) (int, error) {
	if a.projectConfigChooser != nil {
		return a.projectConfigChooser(ctx, a.input, output)
	}
	return tui.RunChoicePrompt(ctx, "Configure this new project", []tui.ChoiceOption{
		{Label: "Inherit folder configuration", Description: "Use the folder's saved defaults and phase structure"},
		{Label: "Pick configuration for this project", Description: "Edit a complete isolated configuration for this project only"},
	}, a.input, output)
}

func (a *App) loadCompleteFolderConfiguration(ctx context.Context, root string, global config.GlobalConfig, output io.Writer) (config.ProjectConfig, error) {
	if classified, ok := a.store.(classifiedConfigureStore); ok {
		loaded, err := classified.LoadProjectClassified(root)
		if err != nil {
			return config.ProjectConfig{}, fmt.Errorf("load folder configuration: %w", err)
		}
		if loaded.ValidationErr != nil || loaded.Classification == config.ProjectConfigMalformed {
			if loaded.ValidationErr != nil {
				return config.ProjectConfig{}, fmt.Errorf("load folder configuration: %w", loaded.ValidationErr)
			}
			return config.ProjectConfig{}, errors.New("load folder configuration: malformed configuration")
		}
		if loaded.Classification == config.ProjectConfigMigrationRequired {
			picker := a.configurePicker
			if picker == nil {
				picker = tui.RunConfigureWizard
			}
			workflow := NewConfigureWorkflowWithPicker(a.input, output, a.cwd, a.store, a.catalogSource, picker)
			if err := workflow.Run(ctx); err != nil {
				return config.ProjectConfig{}, fmt.Errorf("reconfigure sparse folder: %w", err)
			}
			loaded, err = classified.LoadProjectClassified(root)
			if err != nil {
				return config.ProjectConfig{}, fmt.Errorf("reload folder configuration: %w", err)
			}
			if loaded.Classification == config.ProjectConfigMigrationRequired {
				saver, canSaveComplete := a.store.(completeConfigurationStore)
				if !canSaveComplete {
					return config.ProjectConfig{}, errors.New("folder reconfiguration did not produce a complete configuration")
				}
				materialized, materializeErr := config.MaterializeCompleteProjectConfig(global, &loaded.Config)
				if materializeErr != nil {
					return config.ProjectConfig{}, fmt.Errorf("complete sparse folder configuration: %w", materializeErr)
				}
				if err := saver.SaveCompleteConfiguration(root, global, materialized); err != nil {
					return config.ProjectConfig{}, fmt.Errorf("save complete folder configuration: %w", err)
				}
				loaded.Config = materialized
				loaded.Classification = config.ProjectConfigComplete
			}
		}
		if loaded.Classification != config.ProjectConfigComplete {
			return config.ProjectConfig{}, fmt.Errorf("folder configuration requires reconfiguration (classification %q)", loaded.Classification)
		}
		return loaded.Config.Clone(), nil
	}
	folder, err := a.store.LoadProject(root)
	if err != nil {
		return config.ProjectConfig{}, fmt.Errorf("load folder configuration: %w", err)
	}
	if config.ClassifyProjectConfig(folder) == config.ProjectConfigComplete {
		return folder.Clone(), nil
	}
	return config.MaterializeCompleteProjectConfig(global, &folder)
}

func (a *App) pickProjectConfiguration(ctx context.Context, folder config.ProjectConfig, output io.Writer) (config.ProjectConfig, error) {
	catalog, err := a.catalogSource.AgentCatalog(ctx)
	if err != nil {
		return config.ProjectConfig{}, fmt.Errorf("load agent/model catalog: %w", err)
	}
	defaults := wizardDefaultsFromProject(folder)
	picker := a.configurePicker
	if picker == nil {
		picker = tui.RunConfigureWizard
	}
	picked, err := picker(ctx, catalog, defaults, a.input, output)
	if err != nil {
		return config.ProjectConfig{}, fmt.Errorf("configure project: %w", err)
	}
	if err := validateCompletePickerSelection(catalog, picked); err != nil {
		return config.ProjectConfig{}, fmt.Errorf("validate project configuration: %w", err)
	}
	return completeProjectFromPicker(folder, picked), nil
}

func wizardDefaultsFromProject(project config.ProjectConfig) tui.WizardDefaults {
	defaults := tui.WizardDefaults{
		Agent: project.Defaults.Agent, Model: project.Defaults.Model, Effort: project.Defaults.Effort,
		FullTuples: true, Manual: project.Defaults.Provenance == config.ModelProvenanceManual,
	}
	byPhase := make(map[config.Phase]config.PhaseConfig, len(project.Phases))
	for _, phase := range project.Phases {
		byPhase[phase.Phase] = phase
	}
	for _, phase := range config.CompletePhaseOrder() {
		entry, ok := byPhase[phase]
		if !ok {
			entry = config.PhaseConfig{Phase: phase, Enabled: true, Required: containsConfigPhase(config.RequiredPhases(), phase), AgentSettings: config.AgentSettings{Agent: project.Defaults.Agent, Model: project.Defaults.Model, Effort: project.Defaults.Effort, Provenance: project.Defaults.Provenance}}
		}
		name := string(phase)
		for _, canonical := range pipeline.DefaultPipeline().Phases() {
			if config.Phase(canonical.ID()) == phase {
				name = canonical.Metadata().DisplayName
				break
			}
		}
		defaults.Phases = append(defaults.Phases, tui.PhaseState{
			Phase: phase, Name: name, Enabled: entry.Enabled, Locked: entry.Required,
			Description: phaseDescription(phase), Agent: entry.AgentSettings.Agent, Model: entry.AgentSettings.Model,
			Effort: entry.AgentSettings.Effort, Manual: entry.AgentSettings.Provenance == config.ModelProvenanceManual,
		})
	}
	return defaults
}

func completeProjectFromPicker(folder config.ProjectConfig, picked tui.PickerResult) config.ProjectConfig {
	defaults := config.AgentSettings{Agent: picked.Agent, Model: picked.Model, Effort: picked.Effort, Provenance: config.ModelProvenanceCatalog}
	if picked.Manual {
		defaults.Provenance = config.ModelProvenanceManual
	}
	before := wizardDefaultsFromProject(folder)
	byPhase := make(map[config.Phase]tui.PhaseState, len(before.Phases))
	for _, phase := range before.Phases {
		byPhase[phase.Phase] = phase
	}
	for _, phase := range picked.Phases {
		if phase.Phase != "" {
			byPhase[phase.Phase] = phase
		}
	}
	phases := make([]config.PhaseConfig, 0, len(config.CompletePhaseOrder()))
	for _, phase := range config.CompletePhaseOrder() {
		state := byPhase[phase]
		settings := config.AgentSettings{Agent: state.Agent, Model: state.Model, Effort: state.Effort, Provenance: config.ModelProvenanceCatalog}
		if settings.Agent == "" {
			settings = defaults
		}
		if state.Manual {
			settings.Provenance = config.ModelProvenanceManual
		}
		phases = append(phases, config.PhaseConfig{Phase: phase, Enabled: state.Enabled, Required: containsConfigPhase(config.RequiredPhases(), phase), AgentSettings: settings})
	}
	return config.CompleteProjectConfig(config.CompleteSchemaVersion, defaults, phases, folder.GitOps)
}

func validateCompletePickerSelection(catalog config.AgentCatalog, picked tui.PickerResult) error {
	if err := validatePickerSelection(catalog, picked); err != nil {
		return err
	}
	for _, phase := range picked.Phases {
		if phase.Phase == "" || phase.Manual {
			continue
		}
		if err := catalog.ValidateSettings(config.AgentSettings{Agent: phase.Agent, Model: phase.Model, Effort: phase.Effort, Provenance: config.ModelProvenanceCatalog}); err != nil {
			return fmt.Errorf("phase %q: %w", phase.Phase, err)
		}
	}
	return nil
}

func phaseDescription(phase config.Phase) string {
	if description, ok := phaseDescriptions[phase]; ok {
		return description
	}
	return "Required pipeline phase"
}

func containsConfigPhase(phases []config.Phase, want config.Phase) bool {
	for _, phase := range phases {
		if phase == want {
			return true
		}
	}
	return false
}

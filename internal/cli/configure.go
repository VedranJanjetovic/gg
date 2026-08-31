package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/tui"
)

// ConfigureStore is the persistence boundary used by the interactive workflow.
type ConfigureStore interface {
	LoadGlobal() (config.GlobalConfig, error)
	LoadProject(string) (config.ProjectConfig, error)
	SaveConfiguration(string, config.GlobalConfig, config.ProjectConfig) error
}

type classifiedConfigureStore interface {
	LoadProjectClassified(string) (config.ProjectConfigLoad, error)
}

type completeConfigurationStore interface {
	SaveCompleteConfiguration(string, config.GlobalConfig, config.ProjectConfig) error
}

// ProjectConfigurationChooser is the TUI boundary used before a new project
// description is collected. Index zero is inheritance; index one starts the
// isolated project wizard.
type ProjectConfigurationChooser func(context.Context, io.Reader, io.Writer) (int, error)

// ConfigurePicker is the composition boundary for the interactive configure
// wizard: agent, model, effort, and per-phase enable toggles in one flow.
type ConfigurePicker func(context.Context, config.AgentCatalog, tui.WizardDefaults, io.Reader, io.Writer) (tui.PickerResult, error)

// ConfigureWorkflow collects and persists global and project configuration.
// All input is staged and validated before either configuration is written.
type ConfigureWorkflow struct {
	input            io.Reader
	output           io.Writer
	workingDirectory func() (string, error)
	store            ConfigureStore
	catalogSource    config.AgentCatalogSource
	picker           ConfigurePicker
}

// NewConfigureWorkflow constructs a callable interactive configuration workflow.
func NewConfigureWorkflow(in io.Reader, out io.Writer, cwd func() (string, error), store ConfigureStore) *ConfigureWorkflow {
	return &ConfigureWorkflow{input: in, output: out, workingDirectory: cwd, store: store}
}

// NewConfigureWorkflowWithPicker constructs the production workflow with an injected catalog and picker.
func NewConfigureWorkflowWithPicker(in io.Reader, out io.Writer, cwd func() (string, error), store ConfigureStore, source config.AgentCatalogSource, picker ConfigurePicker) *ConfigureWorkflow {
	return &ConfigureWorkflow{input: in, output: out, workingDirectory: cwd, store: store, catalogSource: source, picker: picker}
}

// Run executes the interactive workflow in the current directory.
func (w *ConfigureWorkflow) Run(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := w.workingDirectory()
	if err != nil {
		return fmt.Errorf("determine current directory: %w", err)
	}
	global, err := w.store.LoadGlobal()
	firstTime := errors.Is(err, config.ErrGlobalConfigNotFound)
	if err != nil && !firstTime {
		return fmt.Errorf("load global configuration: %w", err)
	}
	project, err := w.store.LoadProject(root)
	projectMissing := errors.Is(err, config.ErrProjectNotConfigured)
	if err != nil && !projectMissing {
		return fmt.Errorf("load project configuration: %w", err)
	}
	if projectMissing {
		project = config.ProjectConfig{Version: config.CurrentSchemaVersion}
	} else {
		project = cloneProjectConfig(project)
	}

	if firstTime {
		global = config.GlobalConfig{Version: config.CurrentSchemaVersion}
	}
	handled := false
	if w.picker != nil && w.catalogSource != nil {
		catalog, catalogErr := w.catalogSource.AgentCatalog(ctx)
		if catalogErr != nil {
			return fmt.Errorf("load agent/model catalog: %w", catalogErr)
		}
		handled, err = w.runWizard(ctx, catalog, &global, &project)
		if err != nil {
			return err
		}
	}
	if !handled {
		p := promptSession{ctx: ctx, reader: bufio.NewReader(w.input), output: w.output}
		if firstTime {
			if err := p.firstTime(&global); err != nil {
				return err
			}
		} else if err := w.reconfigure(&global, &project, &p); err != nil {
			return err
		}
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := config.ValidateGlobalConfig(global); err != nil {
		return fmt.Errorf("validate staged global configuration: %w", err)
	}
	if config.ClassifyProjectConfig(project) == config.ProjectConfigMigrationRequired {
		materialized, materializeErr := config.MaterializeCompleteProjectConfig(global, &project)
		if materializeErr != nil {
			return fmt.Errorf("materialize staged project configuration: %w", materializeErr)
		}
		project = materialized
	}
	if err := config.ValidateProjectConfig(project); err != nil {
		return fmt.Errorf("validate staged project configuration: %w", err)
	}
	if err := w.store.SaveConfiguration(root, global, project); err != nil {
		return fmt.Errorf("persist configuration: %w", err)
	}
	message := "Configuration updated."
	if firstTime || projectMissing {
		message = "Configuration saved. This project is ready in .gg/projects."
	}
	_, err = fmt.Fprintln(w.output, message)
	return err
}

// runWizard drives the full-screen configure wizard prefilled with the
// currently effective values. It reports handled=false without an error when
// the terminal is non-interactive so line-oriented prompts can take over.
func (w *ConfigureWorkflow) runWizard(ctx context.Context, catalog config.AgentCatalog, global *config.GlobalConfig, project *config.ProjectConfig) (bool, error) {
	defaults := tui.WizardDefaults{
		Agent:  global.Defaults.Agent,
		Model:  global.Defaults.Model,
		Effort: global.Defaults.Effort,
		Phases: currentPhaseStates(*global, project),
	}
	picked, err := w.picker(ctx, catalog, defaults, w.input, w.output)
	if errors.Is(err, tui.ErrPickerNonInteractive) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("select agent and model: %w", err)
	}
	if err := validatePickerSelection(catalog, picked); err != nil {
		return false, fmt.Errorf("validate staged global configuration: %w", err)
	}
	global.Defaults = config.AgentSettings{Agent: picked.Agent, Model: picked.Model, Effort: picked.Effort}
	applyPhaseSelections(project, defaults.Phases, picked)
	return true, nil
}

// phaseDescriptions annotate the wizard's toggleable phase rows.
var phaseDescriptions = map[config.Phase]string{
	config.PhasePlanning:     "Plan implementation phases and deliverables",
	config.PhaseQA:           "Independent QA review after each development phase",
	config.PhaseBuildChecker: "Build, lint, and format verification",
	config.PhasePR:           "Push the branch and open a pull request",
	config.PhaseCI:           "Monitor CI on the pull request and fix failures",
}

// lockedPhaseDescriptions annotate the fixed pipeline steps shown for context.
var lockedPhaseDescriptions = map[pipeline.PhaseID]string{
	pipeline.PhaseAcceptanceCriteria: "Capture the goal and acceptance criteria (always runs)",
	pipeline.PhaseGrooming:           "Requirement grilling (always runs)",
	pipeline.PhaseDevelopment:        "Implement each planned phase with subagents (always runs)",
	pipeline.PhaseRebase:             "Fetch the parent branch and rebase (always runs)",
	pipeline.PhaseTestDocument:       "Close test coverage gaps and update docs (always runs)",
}

// currentPhaseStates lists the full canonical pipeline in execution order for
// the wizard's phase screen. Fixed steps are locked context rows; the
// configurable ones carry their effective enabled state. Before the first
// configuration the global defaults cannot resolve, so every phase starts
// from its built-in enabled default.
func currentPhaseStates(global config.GlobalConfig, project *config.ProjectConfig) []tui.PhaseState {
	enabled := func(config.Phase) bool { return true }
	if resolved, err := config.Resolve(global, project, config.RunOverrides{}); err == nil {
		enabled = func(phase config.Phase) bool { return resolved.Phases[phase].Enabled }
	}
	var pins map[config.Phase]config.PhaseOverride
	if project != nil {
		pins = config.NormalizePhaseOverrides(project.PhaseOverrides)
	}
	phases := pipeline.DefaultPipeline().Phases()
	states := make([]tui.PhaseState, 0, len(phases))
	for _, phase := range phases {
		state := tui.PhaseState{Name: phase.Metadata().DisplayName}
		if key, ok := configurableKey(phase.ID()); ok {
			state.Phase, state.Description = key, phaseDescriptions[key]
		} else {
			state.Locked, state.Enabled, state.Description = true, true, lockedPhaseDescriptions[phase.ID()]
			// Locked phases always run, but grooming and the fixed pipeline
			// steps still accept per-phase agent/model/effort overrides.
			if candidate := config.Phase(phase.ID()); candidate == config.PhaseGrooming || config.IsFixedPhase(candidate) {
				state.Phase = candidate
			}
		}
		if state.Phase != "" {
			if !state.Locked {
				state.Enabled = enabled(state.Phase)
			}
			// Only explicit per-phase pins are carried; empty fields inherit
			// the global defaults picked in the wizard, so a new global
			// selection immediately applies to unpinned phases.
			pin := pins[state.Phase]
			state.Agent, state.Model, state.Effort = pin.Agent, pin.Model, pin.Effort
		}
		states = append(states, state)
	}
	return states
}

// configurableKey maps a canonical pipeline phase to its configuration key
// when the wizard offers it for toggling.
func configurableKey(id pipeline.PhaseID) (config.Phase, bool) {
	candidate := config.Phase(id)
	for _, phase := range configurablePhases() {
		if phase == candidate {
			return candidate, true
		}
	}
	return "", false
}

// applyPhaseSelections stages overrides for every phase the user changed in
// the wizard: an Enabled override when a toggle moved, and agent/model/effort
// pins when the per-phase settings were edited. Phase states carry only the
// pinned fields (empty = inherit the global defaults), so the wizard's
// selections translate directly into configuration overrides; an override
// that ends up empty is removed so the phase inherits again.
func applyPhaseSelections(project *config.ProjectConfig, before []tui.PhaseState, picked tui.PickerResult) {
	current := make(map[config.Phase]tui.PhaseState, len(before))
	for _, state := range before {
		if state.Phase != "" {
			current[state.Phase] = state
		}
	}
	for _, state := range picked.Phases {
		was, known := current[state.Phase]
		if state.Phase == "" || !known {
			continue
		}
		override, exists := project.PhaseOverrides[state.Phase]
		changed := false
		if !state.Locked && was.Enabled != state.Enabled {
			override.Enabled = boolPtr(state.Enabled)
			changed = true
		}
		if was.Agent != state.Agent || was.Model != state.Model || was.Effort != state.Effort {
			override.AgentSettingsOverride = config.AgentSettingsOverride{Agent: state.Agent, Model: state.Model, Effort: state.Effort}
			changed = true
		}
		if !changed {
			continue
		}
		if override == (config.PhaseOverride{}) {
			if exists {
				delete(project.PhaseOverrides, state.Phase)
			}
			continue
		}
		if project.PhaseOverrides == nil {
			project.PhaseOverrides = make(map[config.Phase]config.PhaseOverride)
		}
		project.PhaseOverrides[state.Phase] = override
	}
}

// validatePickerSelection checks a picker result against the catalog. A
// manually typed model is intentionally not a catalog member; it only has to
// name a supported agent and be non-empty.
func validatePickerSelection(catalog config.AgentCatalog, picked tui.PickerResult) error {
	if !picked.Manual {
		return catalog.ValidateSelection(picked.Agent, picked.Model)
	}
	if _, ok := catalog.Lookup(picked.Agent); !ok {
		return fmt.Errorf("agent %q is not in the catalog", picked.Agent)
	}
	if strings.TrimSpace(picked.Model) == "" {
		return errors.New("selected model must be non-empty")
	}
	return nil
}

type promptSession struct {
	ctx    context.Context
	reader *bufio.Reader
	output io.Writer
}

// reconfigure is the line-oriented fallback used when the terminal cannot run
// the full-screen wizard (piped input, no TTY).
func (w *ConfigureWorkflow) reconfigure(global *config.GlobalConfig, project *config.ProjectConfig, p *promptSession) error {
	if _, err := fmt.Fprintln(p.output, "Current values are shown in brackets; press Enter to keep them."); err != nil {
		return err
	}
	agent, model, _, err := w.selectAgentModel(p, "Default agent", global.Defaults.Agent, global.Defaults.Model)
	if err != nil {
		return err
	}
	effort, err := p.effort("Default effort", global.Defaults.Effort, false)
	if err != nil {
		return err
	}
	global.Defaults = config.AgentSettings{Agent: agent, Model: model, Effort: effort}
	configurePhases, _, err := p.enabled("Configure per-phase overrides?", false)
	if err != nil {
		return err
	}
	if !configurePhases {
		return nil
	}
	return w.reconfigurePhases(global, project, p)
}

func (w *ConfigureWorkflow) reconfigurePhases(global *config.GlobalConfig, project *config.ProjectConfig, p *promptSession) error {
	resolved, err := config.Resolve(*global, project, config.RunOverrides{})
	if err != nil {
		return fmt.Errorf("resolve current phase settings: %w", err)
	}
	if project.PhaseOverrides == nil {
		project.PhaseOverrides = make(map[config.Phase]config.PhaseOverride)
	}
	for _, phase := range configurablePhases() {
		current, override := resolved.Phases[phase], project.PhaseOverrides[phase]
		if _, err := fmt.Fprintf(p.output, "\nPhase %s\n", phase); err != nil {
			return err
		}
		enabled, changed, err := p.enabled("  Enabled", current.Enabled)
		if err != nil {
			return err
		}
		if changed {
			override.Enabled = boolPtr(enabled)
		}
		agent, model, changed, err := w.selectAgentModel(p, "  Agent", current.Agent, current.Model)
		if err != nil {
			return err
		}
		if changed {
			override.Agent, override.Model = agent, model
		}
		value, changed, err := p.optional("  Effort", string(current.Effort), "low/medium/high", validEffort, "unsupported effort; enter low, medium, or high")
		if err != nil {
			return err
		}
		if changed {
			override.Effort = config.Effort(strings.ToLower(value))
		}
		if override.Enabled != nil || override.Agent != "" || override.Model != "" || override.Effort != "" {
			project.PhaseOverrides[phase] = override
		}
	}
	return nil
}

// configurablePhases returns the optional phases offered by the interactive
// workflow. Required phases remain enabled and their settings are still
// editable through the same complete configuration flow.
func configurablePhases() []config.Phase {
	return config.OptionalPhases()
}

func (w *ConfigureWorkflow) selectAgentModel(p *promptSession, label string, current config.Agent, currentModel string) (config.Agent, string, bool, error) {
	agentValue, agentChanged, err := p.optional(label, string(current), "claude/codex", validAgent, "unsupported agent; enter claude or codex")
	if err != nil {
		return "", "", false, err
	}
	modelValue, modelChanged, err := p.optional(label+" model", currentModel, "non-empty model name", func(v string) bool { return strings.TrimSpace(v) != "" }, "model must be non-empty")
	if err != nil {
		return "", "", false, err
	}
	agent, model := current, currentModel
	if agentChanged {
		agent = config.Agent(strings.ToLower(agentValue))
	}
	if modelChanged {
		model = modelValue
	}
	return agent, model, agentChanged || modelChanged, nil
}

func (p *promptSession) firstTime(global *config.GlobalConfig) error {
	if _, err := fmt.Fprintln(p.output, "No global configuration found. Set the required defaults."); err != nil {
		return err
	}
	agent, err := p.agent("Default agent", "", true)
	if err != nil {
		return err
	}
	model, err := p.model("Default model", "", true)
	if err != nil {
		return err
	}
	effort, err := p.effort("Default effort", "", true)
	if err != nil {
		return err
	}
	global.Defaults = config.AgentSettings{Agent: agent, Model: model, Effort: effort}
	return nil
}

func (p *promptSession) agent(label string, current config.Agent, required bool) (config.Agent, error) {
	for {
		value, err := p.ask(label, string(current), "claude/codex")
		if err != nil {
			return "", err
		}
		if value == "" && current != "" {
			return current, nil
		}
		if validAgent(value) {
			return config.Agent(strings.ToLower(value)), nil
		}
		message := "unsupported agent; enter claude or codex"
		if value == "" && required {
			message = "agent is required; enter claude or codex"
		}
		if err := p.retry(message); err != nil {
			return "", err
		}
	}
}
func (p *promptSession) model(label, current string, required bool) (string, error) {
	for {
		value, err := p.ask(label, current, "non-empty model name")
		if err != nil {
			return "", err
		}
		if value == "" && current != "" {
			return current, nil
		}
		if value != "" {
			return value, nil
		}
		if !required {
			return current, nil
		}
		if err := p.retry("model is required; enter a non-empty model name"); err != nil {
			return "", err
		}
	}
}
func (p *promptSession) effort(label string, current config.Effort, required bool) (config.Effort, error) {
	for {
		value, err := p.ask(label, string(current), "low/medium/high")
		if err != nil {
			return "", err
		}
		if value == "" && current != "" {
			return current, nil
		}
		if validEffort(value) {
			return config.Effort(strings.ToLower(value)), nil
		}
		message := "unsupported effort; enter low, medium, or high"
		if value == "" && required {
			message = "effort is required; enter low, medium, or high"
		}
		if err := p.retry(message); err != nil {
			return "", err
		}
	}
}
func (p *promptSession) enabled(label string, current bool) (bool, bool, error) {
	shown := "yes"
	if !current {
		shown = "no"
	}
	for {
		value, err := p.ask(label, shown, "yes/no")
		if err != nil {
			return false, false, err
		}
		switch strings.ToLower(value) {
		case "":
			return current, false, nil
		case "y", "yes", "enable", "enabled", "true":
			return true, true, nil
		case "n", "no", "disable", "disabled", "false":
			return false, true, nil
		}
		if err := p.retry("invalid enabled value; enter yes or no"); err != nil {
			return false, false, err
		}
	}
}
func (p *promptSession) optional(label, current, choices string, valid func(string) bool, message string) (string, bool, error) {
	for {
		value, err := p.ask(label, current, choices)
		if err != nil {
			return "", false, err
		}
		if value == "" {
			return "", false, nil
		}
		if valid(value) {
			return value, true, nil
		}
		if err := p.retry(message); err != nil {
			return "", false, err
		}
	}
}
func (p *promptSession) ask(label, current, choices string) (string, error) {
	if err := p.ctx.Err(); err != nil {
		return "", err
	}
	var err error
	if current == "" {
		_, err = fmt.Fprintf(p.output, "%s (%s): ", label, choices)
	} else {
		_, err = fmt.Fprintf(p.output, "%s [%s] (%s): ", label, current, choices)
	}
	if err != nil {
		return "", err
	}
	line, err := p.reader.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("configuration cancelled: input ended before completion")
		}
		return "", fmt.Errorf("read terminal input: %w", err)
	}
	return strings.TrimSpace(line), nil
}
func (p *promptSession) retry(message string) error {
	_, err := fmt.Fprintf(p.output, "Invalid value: %s. Please try again.\n", message)
	return err
}
func validAgent(v string) bool {
	v = strings.ToLower(v)
	return v == string(config.AgentClaude) || v == string(config.AgentCodex)
}
func validEffort(v string) bool {
	v = strings.ToLower(v)
	return v == string(config.EffortLow) || v == string(config.EffortMedium) || v == string(config.EffortHigh)
}
func boolPtr(v bool) *bool { return &v }

func cloneProjectConfig(project config.ProjectConfig) config.ProjectConfig {
	if project.PhaseOverrides == nil {
		return project
	}
	project.PhaseOverrides = config.NormalizePhaseOverrides(project.PhaseOverrides)
	return project
}

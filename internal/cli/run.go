package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/VedranJanjetovic/gg/internal/config"
)

type runOptions struct {
	overrides     config.RunOverrides
	maxIterations int
	args          []string
	// flagArgs preserves the raw flag arguments (everything before the
	// positional arguments) so a detached run can be re-spawned as
	// `gg run <flagArgs...> <slug>` without reconstructing flags.
	flagArgs []string
}

func parseRunOptions(args []string) (runOptions, error) {
	options := runOptions{overrides: config.RunOverrides{PhaseOverrides: make(map[config.Phase]config.PhaseOverride)}, maxIterations: 3}
	flags := flag.NewFlagSet("gg run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Var(agentValue{target: &options.overrides.Defaults.Agent}, "agent", "agent for every phase in this run (claude or codex)")
	flags.StringVar(&options.overrides.Defaults.Model, "model", "", "model for every phase in this run")
	flags.Var(effortValue{target: &options.overrides.Defaults.Effort}, "effort", "reasoning effort for every phase in this run (low, medium, or high)")
	flags.Var(phaseSettingValue{overrides: options.overrides.PhaseOverrides, field: phaseAgent}, "phase-agent", "per-phase agent override, repeatable as phase=agent")
	flags.Var(phaseSettingValue{overrides: options.overrides.PhaseOverrides, field: phaseModel}, "phase-model", "per-phase model override, repeatable as phase=model")
	flags.Var(phaseSettingValue{overrides: options.overrides.PhaseOverrides, field: phaseEffort}, "phase-effort", "per-phase effort override, repeatable as phase=effort")
	flags.Var(phaseEnabledValue{overrides: options.overrides.PhaseOverrides, enabled: true}, "enable-phase", "enable a phase for this run; repeat for multiple phases")
	flags.Var(phaseEnabledValue{overrides: options.overrides.PhaseOverrides, enabled: false}, "disable-phase", "disable a phase for this run; repeat for multiple phases")
	flags.StringVar(&options.overrides.GitOps.ParentBranch, "parent-branch", "", "GitOps parent branch for this run")
	flags.StringVar(&options.overrides.GitOps.BaseRef, "base-ref", "", "GitOps worktree base ref for this run")
	flags.Var(gitOpsToggleValue{target: &options.overrides.GitOps.EnablePR, value: true}, "enable-pr", "enable the PR phase for this run")
	flags.Var(gitOpsToggleValue{target: &options.overrides.GitOps.EnablePR, value: false}, "disable-pr", "disable the PR phase for this run")
	flags.Var(gitOpsToggleValue{target: &options.overrides.GitOps.EnableCI, value: true}, "enable-ci", "enable the CI phase for this run")
	flags.Var(gitOpsToggleValue{target: &options.overrides.GitOps.EnableCI, value: false}, "disable-ci", "disable the CI phase for this run")
	flags.IntVar(&options.maxIterations, "max-iterations", 3, "maximum total QA attempts")

	orderedArgs, err := orderRunFlags(args)
	if err != nil {
		return runOptions{}, fmt.Errorf("parse run arguments: %w", err)
	}
	if err := flags.Parse(orderedArgs); err != nil {
		return runOptions{}, fmt.Errorf("parse run arguments: %w", err)
	}
	options.args = append([]string(nil), flags.Args()...)
	options.flagArgs = append([]string(nil), orderedArgs[:len(orderedArgs)-len(options.args)]...)
	if options.maxIterations <= 0 {
		return runOptions{}, errors.New("parse run arguments: --max-iterations must be positive")
	}
	if len(options.overrides.PhaseOverrides) == 0 {
		options.overrides.PhaseOverrides = nil
	}
	if err := config.ValidateRunOverrides(options.overrides); err != nil {
		return runOptions{}, fmt.Errorf("validate run overrides: %w", err)
	}
	options.overrides.PhaseOverrides = config.NormalizePhaseOverrides(options.overrides.PhaseOverrides)
	return options, nil
}

var runFlags = map[string]struct{}{
	"--agent": {}, "--model": {}, "--effort": {},
	"--phase-agent": {}, "--phase-model": {}, "--phase-effort": {},
	"--enable-phase": {}, "--disable-phase": {},
	"--parent-branch": {}, "--base-ref": {}, "--enable-pr": {}, "--disable-pr": {}, "--enable-ci": {}, "--disable-ci": {},
	"--max-iterations": {},
}

var runBoolFlags = map[string]struct{}{
	"--enable-pr": {}, "--disable-pr": {}, "--enable-ci": {}, "--disable-ci": {},
}

// orderRunFlags lets transient flags appear before or after legacy positional
// arguments while leaving unknown arguments after the first positional intact.
func orderRunFlags(args []string) ([]string, error) {
	flagArgs := make([]string, 0, len(args))
	positional := make([]string, 0, len(args))
	seenPositional := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			flagArgs = append(flagArgs, "--")
			positional = append(positional, args[i+1:]...)
			break
		}
		if _, ok := runFlags[arg]; ok {
			if _, boolFlag := runBoolFlags[arg]; boolFlag {
				flagArgs = append(flagArgs, arg)
				continue
			}
			if i+1 == len(args) {
				return nil, fmt.Errorf("flag %s requires a value", arg)
			}
			flagArgs = append(flagArgs, arg, args[i+1])
			i++
			continue
		}
		if isRunFlagAssignment(arg) {
			flagArgs = append(flagArgs, arg)
			continue
		}
		if !seenPositional && strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
			continue
		}
		seenPositional = true
		positional = append(positional, arg)
	}
	return append(flagArgs, positional...), nil
}

func isRunFlagAssignment(arg string) bool {
	name, _, ok := strings.Cut(arg, "=")
	if !ok {
		return false
	}
	_, ok = runFlags[name]
	return ok
}

type gitOpsToggleValue struct {
	target **bool
	value  bool
}

func (v gitOpsToggleValue) String() string   { return "" }
func (v gitOpsToggleValue) IsBoolFlag() bool { return true }
func (v gitOpsToggleValue) Set(string) error { value := v.value; *v.target = &value; return nil }

type agentValue struct{ target *config.Agent }

func (v agentValue) String() string { return string(*v.target) }
func (v agentValue) Set(value string) error {
	*v.target = config.Agent(value)
	return nil
}

type effortValue struct{ target *config.Effort }

func (v effortValue) String() string { return string(*v.target) }
func (v effortValue) Set(value string) error {
	*v.target = config.Effort(value)
	return nil
}

type phaseSetting uint8

const (
	phaseAgent phaseSetting = iota
	phaseModel
	phaseEffort
)

type phaseSettingValue struct {
	overrides map[config.Phase]config.PhaseOverride
	field     phaseSetting
}

func (phaseSettingValue) String() string { return "" }
func (v phaseSettingValue) Set(value string) error {
	phase, setting, err := splitPhaseSetting(value)
	if err != nil {
		return err
	}
	override := v.overrides[phase]
	switch v.field {
	case phaseAgent:
		override.Agent = config.Agent(setting)
	case phaseModel:
		override.Model = setting
	case phaseEffort:
		override.Effort = config.Effort(setting)
	}
	v.overrides[phase] = override
	return nil
}

func splitPhaseSetting(value string) (config.Phase, string, error) {
	if strings.Count(value, "=") != 1 {
		return "", "", fmt.Errorf("expected phase=value, got %q", value)
	}
	parts := strings.SplitN(value, "=", 2)
	if parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected non-empty phase=value, got %q", value)
	}
	return config.Phase(parts[0]), parts[1], nil
}

type phaseEnabledValue struct {
	overrides map[config.Phase]config.PhaseOverride
	enabled   bool
}

func (phaseEnabledValue) String() string { return "" }
func (v phaseEnabledValue) Set(value string) error {
	if value == "" || strings.Contains(value, "=") {
		return fmt.Errorf("expected a phase name, got %q", value)
	}
	override := v.overrides[config.Phase(value)]
	enabled := v.enabled
	override.Enabled = &enabled
	v.overrides[config.Phase(value)] = override
	return nil
}

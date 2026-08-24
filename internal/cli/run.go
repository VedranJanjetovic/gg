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
	"--parent-branch": {}, "--base-ref": {}, "--enable-pr": {}, "--disable-pr": {}, "--enable-ci": {}, "--disable-ci": {},
	"--max-iterations": {},
}

var removedRunFlags = map[string]struct{}{
	"--agent": {}, "--model": {}, "--effort": {},
	"--phase-agent": {}, "--phase-model": {}, "--phase-effort": {},
	"--enable-phase": {}, "--disable-phase": {},
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
		if _, removed := removedRunFlags[strings.SplitN(arg, "=", 2)[0]]; removed {
			flagArgs = append(flagArgs, arg)
			if !strings.Contains(arg, "=") && i+1 < len(args) {
				flagArgs = append(flagArgs, args[i+1])
				i++
			}
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

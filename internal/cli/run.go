package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/VedranJanjetovic/gg/internal/config"
)

// repairExistingVerificationFlag opts a run or resume into repairing the
// parent verification failures that were present before gg started.
const repairExistingVerificationFlag = "repair-existing-verification"

// skipChecksFlag quarantines named verification checks for a resume: they still
// execute, but they can never block a boundary in either direction.
const skipChecksFlag = "skip-checks"

// fixChecksFlag is the complement of skipChecksFlag: instead of excluding the
// blocked verification checks, Planning re-runs to prepend one repair phase
// that makes them executable.
const fixChecksFlag = "fix-checks"

// isFlagNamed reports whether arg names the given flag in any spelling the flag
// package accepts: one or two leading dashes, with or without an =value.
func isFlagNamed(arg, name string) bool {
	trimmed, ok := strings.CutPrefix(arg, "--")
	if !ok {
		if trimmed, ok = strings.CutPrefix(arg, "-"); !ok {
			return false
		}
	}
	flagName, _, _ := strings.Cut(trimmed, "=")
	return flagName == name
}

type runOptions struct {
	overrides                  config.RunOverrides
	maxIterations              int
	repairExistingVerification bool
	args                       []string
	// flagArgs preserves the raw flag arguments (everything before the
	// positional arguments) so a detached run can be re-spawned as
	// `gg run <flagArgs...> <slug>` without reconstructing flags.
	flagArgs []string
}

type resumeOptions struct {
	selector                   string
	repairExistingVerification bool
	// skipChecks names the verification checks to quarantine before the run
	// resumes. A quarantined check still executes but can never block.
	skipChecks []string
	// fixChecks asks gg to repair the blocked verification checks instead of
	// quarantining them: Planning re-runs and prepends one repair phase.
	fixChecks bool
}

func parseResumeOptions(args []string) (resumeOptions, error) {
	options := resumeOptions{}
	flags := flag.NewFlagSet("gg resume", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&options.repairExistingVerification, repairExistingVerificationFlag, false, "explicitly repair existing verification failures")
	flags.BoolVar(&options.fixChecks, fixChecksFlag, false, "re-run Planning to add one phase that makes the blocked verification checks executable")
	skipChecks := flags.String(skipChecksFlag, "", "comma-separated verification checks to exclude from boundary decisions")
	ordered := make([]string, 0, len(args))
	positional := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if isFlagNamed(arg, repairExistingVerificationFlag) || isFlagNamed(arg, fixChecksFlag) {
			ordered = append(ordered, arg)
			continue
		}
		if isFlagNamed(arg, skipChecksFlag) {
			ordered = append(ordered, arg)
			// The separated spelling (`--skip-checks names`) carries its value
			// in the next argument, which must be hoisted with it so it is not
			// mistaken for the project selector.
			if !strings.Contains(arg, "=") && index+1 < len(args) {
				index++
				ordered = append(ordered, args[index])
			}
			continue
		}
		positional = append(positional, arg)
	}
	ordered = append(ordered, positional...)
	if err := flags.Parse(ordered); err != nil {
		return resumeOptions{}, fmt.Errorf("parse resume arguments: %w", err)
	}
	if flags.NArg() > 1 {
		return resumeOptions{}, errors.New("resume accepts at most one project selector")
	}
	if flags.NArg() == 1 {
		options.selector = flags.Arg(0)
	}
	options.skipChecks = splitCheckNames(*skipChecks)
	if options.fixChecks && len(options.skipChecks) > 0 {
		return resumeOptions{}, fmt.Errorf("resume --%s and --%s are mutually exclusive: a check is either excluded or repaired", fixChecksFlag, skipChecksFlag)
	}
	return options, nil
}

// splitCheckNames turns the comma-separated flag value into trimmed names,
// dropping empty entries so a trailing comma is not read as a check.
func splitCheckNames(value string) []string {
	names := make([]string, 0)
	for _, part := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			names = append(names, trimmed)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return names
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
	flags.BoolVar(&options.repairExistingVerification, "repair-existing-verification", false, "explicitly repair existing parent verification failures")

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
	"--max-iterations": {}, "--repair-existing-verification": {},
}

// retiredRunFlags are flags gg no longer accepts. They are still hoisted ahead
// of the positional arguments so flag.Parse reports the removal instead of
// silently treating the flag as a project selector.
var retiredRunFlags = map[string]struct{}{
	"--agent": {}, "--model": {}, "--effort": {},
	"--phase-agent": {}, "--phase-model": {}, "--phase-effort": {},
	"--enable-phase": {}, "--disable-phase": {},
}

var runBoolFlags = map[string]struct{}{
	"--enable-pr": {}, "--disable-pr": {}, "--enable-ci": {}, "--disable-ci": {},
	"--repair-existing-verification": {},
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
		if _, retired := retiredRunFlags[strings.SplitN(arg, "=", 2)[0]]; retired {
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

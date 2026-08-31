// Package verification executes named checks and compares their individual
// failures with a previously captured baseline.
package verification

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/VedranJanjetovic/gg/internal/state"
)

// Adapter identifies the output parser used for a verification step. Adapters
// name output *shapes* rather than toolchains, so a project in any language can
// declare a complete verification contract.
type Adapter string

const (
	// AdapterFileList reads a plain list of offending file paths, one per
	// line (gofmt -l, prettier --list-different, ruff format --check).
	AdapterFileList Adapter = "file-list"
	// AdapterDiagnostic reads file:line[:col]: message, the shape emitted by
	// go vet, tsc, eslint, clippy, mypy, javac, gcc, and shellcheck.
	AdapterDiagnostic Adapter = "diagnostic"
	// AdapterCommandExit is the fallback for a command whose only stable
	// signal is its exit status, so every toolchain remains expressible even
	// when its output has no parseable per-failure identity.
	AdapterCommandExit Adapter = "command-exit"
	// AdapterGoTest reads `go test` output, which exposes per-test identities.
	AdapterGoTest Adapter = "go-test"
	// AdapterGitDiffCheck reads `git diff --check`.
	AdapterGitDiffCheck Adapter = "git-diff-check"

	// Legacy Go-named aliases, retained so snapshots written before the set
	// was generalized stay readable by `gg resume`. They behave exactly like
	// the canonical adapter they resolve to.
	AdapterGofmtEmpty   Adapter = "gofmt-empty"
	AdapterGoDiagnostic Adapter = "go-diagnostic"
)

// Canonical resolves a legacy Go-named alias to the adapter that implements it.
func (a Adapter) Canonical() Adapter {
	switch a {
	case AdapterGofmtEmpty:
		return AdapterFileList
	case AdapterGoDiagnostic:
		return AdapterDiagnostic
	default:
		return a
	}
}

func (a Adapter) IsValid() bool {
	switch a.Canonical() {
	case AdapterFileList, AdapterDiagnostic, AdapterCommandExit, AdapterGoTest, AdapterGitDiffCheck:
		return true
	default:
		return false
	}
}

// Step is the executable form of one named planned check. Args are passed
// directly to Command; no shell is involved.
type Step struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
	Adapter Adapter
}

func (s Step) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("verification step requires a name")
	}
	if strings.TrimSpace(s.Command) == "" {
		return fmt.Errorf("verification step %q requires a command", s.Name)
	}
	if !s.Adapter.IsValid() {
		return fmt.Errorf("verification step %q has unsupported adapter %q", s.Name, s.Adapter)
	}
	for key := range s.Env {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "=\x00") {
			return fmt.Errorf("verification step %q has invalid environment key", s.Name)
		}
	}
	return nil
}

// StepsFromState converts the persisted Planning contract without sharing
// mutable argument or environment collections with it.
func StepsFromState(steps []state.VerificationStep) []Step {
	converted := make([]Step, len(steps))
	for i, step := range steps {
		converted[i] = Step{Name: step.Name, Command: step.Command, Args: append([]string(nil), step.Args...), Adapter: Adapter(step.Adapter)}
		if step.Env != nil {
			converted[i].Env = make(map[string]string, len(step.Env))
			for key, value := range step.Env {
				converted[i].Env[key] = value
			}
		}
	}
	return converted
}

// IndividualFailure is a stable check failure. Reason is normalized before
// it is compared with a baseline; Evidence is bounded command output kept for
// diagnostics and is not used for equality.
type IndividualFailure struct {
	Identity string
	Reason   string
	Evidence string
}

// CommandStatus describes execution independently of the process exit code.
type CommandStatus string

const (
	CommandPassed         CommandStatus = "passed"
	CommandFailed         CommandStatus = "failed"
	CommandUnavailable    CommandStatus = "unavailable"
	CommandUnclassifiable CommandStatus = "unclassifiable"
)

// CommandResult is the bounded evidence for one step execution.
type CommandResult struct {
	StepName       string
	Command        string
	Args           []string
	ExitCode       int
	Status         CommandStatus
	Failures       []IndividualFailure
	LogPath        string
	Output         string
	RetryCount     int
	UnavailableErr string
}

// UnavailableError means the required executable could not be started. It is
// distinct from a command that ran and returned a failing exit status.
type UnavailableError struct {
	Command string
	Err     error
}

func (e *UnavailableError) Error() string {
	if e == nil {
		return "verification command unavailable"
	}
	return fmt.Sprintf("verification command %q unavailable: %v", e.Command, e.Err)
}

func (e *UnavailableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Report is one complete execution of the planned set.
type Report struct {
	Results  []CommandResult
	Warnings []Warning
}

// FailureKey is the comparison identity for an individual failure.
type FailureKey struct {
	CheckName string
	Identity  string
}

func (k FailureKey) String() string { return k.CheckName + "::" + k.Identity }

// Baseline is an immutable-in-use snapshot of the parent result.
type Baseline struct {
	Results []CommandResult
}

// Classification is deliberately enumerated so lifecycle code can make
// fail-closed decisions without parsing human text.
type Classification string

const (
	ClassificationUnchangedBaseline Classification = "unchanged_baseline"
	ClassificationNew               Classification = "new"
	ClassificationChangedReason     Classification = "changed_reason"
	ClassificationRepaired          Classification = "repaired"
	ClassificationRepairedRegressed Classification = "repaired_regressed"
	ClassificationUnclassifiable    Classification = "unclassifiable"
	ClassificationUnavailable       Classification = "unavailable"
	ClassificationFlaky             Classification = "flaky"
)

// Finding is a current failure classified against the parent baseline.
type Finding struct {
	Key            FailureKey
	Reason         string
	Classification Classification
	RequiredGreen  bool
	LogPath        string
}

// Warning records an allowed baseline failure or a confirmed flaky check.
type Warning struct {
	Key            FailureKey
	Reason         string
	Classification Classification
	LogPath        string
}

// BoundaryReport is the decision-ready result of comparing a current run to
// its original parent baseline.
type BoundaryReport struct {
	Passed     bool
	Blocked    bool
	Findings   []Finding
	Warnings   []Warning
	Promotions []FailureKey
}

// CompareOptions controls only durable policy inputs. The original baseline
// remains owned by the caller and is never replaced by Compare.
type CompareOptions struct {
	RepairMode    bool
	RepairTargets []FailureKey
	Promoted      []FailureKey
}

func failureMap(results []CommandResult) map[FailureKey]IndividualFailure {
	result := make(map[FailureKey]IndividualFailure)
	for _, command := range results {
		for _, failure := range command.Failures {
			failure.Identity = strings.TrimSpace(failure.Identity)
			failure.Reason = NormalizeReason(failure.Reason)
			result[FailureKey{CheckName: command.StepName, Identity: failure.Identity}] = failure
		}
	}
	return result
}

func keySet(keys []FailureKey) map[FailureKey]bool {
	set := make(map[FailureKey]bool, len(keys))
	for _, key := range keys {
		set[key] = true
	}
	return set
}

// CaptureBaseline makes a deep copy so later command results cannot mutate
// the parent evidence retained for resume.
func CaptureBaseline(report Report) Baseline {
	return Baseline{Results: cloneResults(report.Results)}
}

// Compare classifies individual failures. Unchanged baseline failures are
// warnings unless repair mode selects them, while new, changed, repaired, or
// unclassifiable failures block the boundary.
func Compare(baseline Baseline, current Report, options CompareOptions) BoundaryReport {
	base := failureMap(baseline.Results)
	now := failureMap(current.Results)
	checkStatus := make(map[string]CommandStatus, len(current.Results))
	seenChecks := make(map[string]bool, len(current.Results))
	flakyReasons := make(map[FailureKey]string)
	for _, warning := range current.Warnings {
		if warning.Classification == ClassificationFlaky {
			flakyReasons[warning.Key] = NormalizeReason(warning.Reason)
		}
	}
	for _, command := range current.Results {
		seenChecks[command.StepName] = true
		checkStatus[command.StepName] = command.Status
	}
	promoted := keySet(options.Promoted)
	targets := keySet(options.RepairTargets)
	report := BoundaryReport{Passed: true, Warnings: append([]Warning(nil), current.Warnings...)}
	strictChecks := make(map[string]bool)
	addBlockedFinding := func(finding Finding) {
		if finding.Classification == ClassificationUnavailable || finding.Classification == ClassificationUnclassifiable {
			strictChecks[finding.Key.CheckName] = true
		}
		report.Blocked = true
		report.Passed = false
		report.Findings = append(report.Findings, finding)
	}

	for _, command := range current.Results {
		switch command.Status {
		case CommandPassed:
			if len(command.Failures) > 0 {
				addBlockedFinding(Finding{Key: FailureKey{CheckName: command.StepName}, Classification: ClassificationUnclassifiable, Reason: "passed command exposed individual failures", LogPath: command.LogPath})
			}
		case CommandUnavailable:
			addBlockedFinding(Finding{Key: FailureKey{CheckName: command.StepName}, Classification: ClassificationUnavailable, Reason: command.UnavailableErr, LogPath: command.LogPath})
		case CommandUnclassifiable:
			addBlockedFinding(Finding{Key: FailureKey{CheckName: command.StepName}, Classification: ClassificationUnclassifiable, Reason: "failing command did not expose a stable individual failure identity", LogPath: command.LogPath})
		case CommandFailed:
			if len(command.Failures) == 0 {
				addBlockedFinding(Finding{Key: FailureKey{CheckName: command.StepName}, Classification: ClassificationUnclassifiable, Reason: "failing command did not expose a stable individual failure identity", LogPath: command.LogPath})
			}
		default:
			addBlockedFinding(Finding{Key: FailureKey{CheckName: command.StepName}, Classification: ClassificationUnclassifiable, Reason: "verification command has an unknown status", LogPath: command.LogPath})
		}
		for _, failure := range command.Failures {
			failure.Identity = strings.TrimSpace(failure.Identity)
			failure.Reason = NormalizeReason(failure.Reason)
			if failure.Identity == "" || failure.Reason == "" {
				addBlockedFinding(Finding{Key: FailureKey{CheckName: command.StepName, Identity: failure.Identity}, Classification: ClassificationUnclassifiable, Reason: "failing command exposed an incomplete individual failure identity", LogPath: command.LogPath})
				continue
			}
			key := FailureKey{CheckName: command.StepName, Identity: failure.Identity}
			if flakyReason, isFlaky := flakyReasons[key]; isFlaky && flakyReason == failure.Reason {
				report.Findings = append(report.Findings, Finding{Key: key, Reason: failure.Reason, Classification: ClassificationFlaky, LogPath: command.LogPath})
				continue
			}
			classification := ClassificationNew
			requiredGreen := false
			previous, hadBaseline := base[key]
			switch {
			case promoted[key]:
				classification = ClassificationRepairedRegressed
				requiredGreen = true
			case !hadBaseline:
				classification = ClassificationNew
			case previous.Reason != failure.Reason:
				classification = ClassificationChangedReason
			case options.RepairMode && targets[key]:
				classification = ClassificationUnchangedBaseline
				requiredGreen = true
			default:
				classification = ClassificationUnchangedBaseline
			}
			finding := Finding{Key: key, Reason: failure.Reason, Classification: classification, RequiredGreen: requiredGreen, LogPath: command.LogPath}
			report.Findings = append(report.Findings, finding)
			if classification == ClassificationUnchangedBaseline && !requiredGreen {
				report.Warnings = append(report.Warnings, Warning{Key: key, Reason: failure.Reason, Classification: classification, LogPath: command.LogPath})
			} else {
				addBlockedFinding(finding)
			}
		}
	}

	// Every baseline command must appear in the current report. Missing a
	// planned result is an execution defect, not evidence that its failures
	// were repaired.
	for _, command := range baseline.Results {
		if !seenChecks[command.StepName] {
			addBlockedFinding(Finding{Key: FailureKey{CheckName: command.StepName}, Classification: ClassificationUnclassifiable, Reason: "planned verification result is missing"})
		}
	}

	for key := range base {
		if status := checkStatus[key.CheckName]; status == CommandUnavailable || status == CommandUnclassifiable || strictChecks[key.CheckName] {
			continue
		}
		if !seenChecks[key.CheckName] {
			continue
		}
		if flakyReason, isFlaky := flakyReasons[key]; isFlaky && flakyReason == NormalizeReason(base[key].Reason) {
			continue
		}
		if _, stillFails := now[key]; stillFails {
			continue
		}
		if promoted[key] {
			continue
		}
		report.Findings = append(report.Findings, Finding{Key: key, Reason: base[key].Reason, Classification: ClassificationRepaired, RequiredGreen: true})
		report.Promotions = append(report.Promotions, key)
	}
	sort.Slice(report.Promotions, func(i, j int) bool { return report.Promotions[i].String() < report.Promotions[j].String() })
	return report
}

func cloneResults(results []CommandResult) []CommandResult {
	cloned := make([]CommandResult, len(results))
	for i, result := range results {
		cloned[i] = result
		cloned[i].Args = append([]string(nil), result.Args...)
		cloned[i].Failures = append([]IndividualFailure(nil), result.Failures...)
	}
	return cloned
}

// Executor is the narrow process seam used by Runner. Production uses the
// direct os/exec implementation; tests can provide a deterministic executor.
type Executor interface {
	Execute(context.Context, string, string, []string, []string) (stdout, stderr string, exitCode int, err error)
}

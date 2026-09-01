package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/VedranJanjetovic/gg/internal/state"
	"github.com/VedranJanjetovic/gg/internal/verification"
)

// verificationReportState is implemented by the durable lifecycle service.
// Keeping this as an optional extension preserves the small PhaseState seam
// used by orchestration unit tests and by older integrations.
type verificationReportState interface {
	RecordVerificationBaselineReport(context.Context, string, []state.VerificationCommandResult, []state.VerificationFinding) (state.ProjectState, error)
	RecordVerificationResultReport(context.Context, string, []state.VerificationCommandResult, []state.VerificationFinding, []state.VerificationFinding, string, int, string) (state.ProjectState, error)
	PromoteVerificationIdentity(context.Context, string, string) (state.ProjectState, error)
}

// verificationBootstrapState is the optional lifecycle extension behind
// --fix-checks. It stays separate from verificationReportState so controllers
// built for the ordinary boundary keep working unchanged.
type verificationBootstrapState interface {
	RecordVerificationBootstrapPhase(context.Context, string, string) (state.ProjectState, error)
	CompleteVerificationBootstrap(context.Context, string) (state.ProjectState, error)
}

// errVerificationServiceMissing fails the boundary closed. Reaching it means
// the project carries a verification contract but the controller was built
// without WithVerificationService, so the gate could not run. Skipping it
// would silently report an unverified run as verified.
var errVerificationServiceMissing = errors.New("project declares a verification contract but the controller has no verification service")

type verificationPauseError struct{ cause error }

func (e *verificationPauseError) Error() string {
	if e == nil || e.cause == nil {
		return "verification requires external resolution"
	}
	return e.cause.Error()
}

func (e *verificationPauseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func isVerificationPause(err error) bool {
	var pause *verificationPauseError
	return errors.As(err, &pause)
}

// verificationBoundaryError keeps the structured report available to the
// remediation controller. The durable state already contains a concise copy;
// this in-process form avoids reconstructing command and log evidence from an
// error string.
type verificationBoundaryError struct {
	cursor  string
	report  verification.BoundaryReport
	current verification.Report
}

func (e *verificationBoundaryError) Error() string {
	if e == nil {
		return "verification boundary failed"
	}
	return boundaryError(e.report).Error()
}

func (c *sequentialController) ensureVerificationBaseline(ctx context.Context, request *Request) error {
	if request == nil || request.Project.Verification == nil {
		return nil
	}
	if c.verification == nil {
		return errVerificationServiceMissing
	}
	persist, ok := c.state.(verificationReportState)
	if !ok {
		return errors.New("phase state cannot persist verification reports")
	}
	verificationState := request.Project.Verification
	if verificationState.ParentBaselineCaptured {
		return nil
	}
	deferred, deferErr := c.deferBaselineForBootstrap(ctx, request)
	if deferErr != nil {
		return deferErr
	}
	if deferred {
		return nil
	}
	verificationState = request.Project.Verification
	steps := verification.StepsFromState(verificationState.PlannedSteps)
	quarantined := quarantinedSet(*verificationState)
	report, runErr := c.verification.Verify(ctx, request.Project.WorktreePath, steps)
	if runErr != nil {
		if hasUnavailableOrUnclassifiable(report, steps, quarantined) {
			_, persistErr := persist.RecordVerificationResultReport(ctx, request.Project.Slug, commandResults(report), nil, nil, "parent-preflight", 0, "make every planned verification step executable, then resume")
			return &verificationPauseError{cause: errors.Join(fmt.Errorf("parent verification preflight cannot complete: %w%s", runErr, skipChecksHint(report, request.Project.Slug, quarantined)), persistErr)}
		}
		return runErr
	}
	if hasUnavailableOrUnclassifiable(report, steps, quarantined) {
		_, persistErr := persist.RecordVerificationResultReport(ctx, request.Project.Slug, commandResults(report), nil, nil, "parent-preflight", 0, "make every planned verification step executable, then resume")
		return &verificationPauseError{cause: errors.Join(preflightBlockedError(report, request.Project.Slug, quarantined), persistErr)}
	}
	baseline := verification.CaptureBaseline(report)
	findings := reportFindings(report, "")
	updated, err := persist.RecordVerificationBaselineReport(ctx, request.Project.Slug, commandResults(report), findings)
	if err != nil {
		return fmt.Errorf("persist parent verification baseline: %w", err)
	}
	request.Project = updated
	// Capture the baseline warning state as well. Repair mode intentionally
	// allows the selected parent failures to remain while Development repairs
	// them; ordinary mode records them as warnings.
	boundary := verification.Compare(baseline, report, compareOptions(*request.Project.Verification))
	updated, err = persist.RecordVerificationResultReport(ctx, request.Project.Slug, commandResults(report), findingsWithClassifications(boundary), warningFindings(boundary), "parent-preflight", 0, nextVerificationAction(boundary))
	if err != nil {
		return fmt.Errorf("persist parent verification preflight: %w", err)
	}
	request.Project = updated
	return nil
}

// deferBaselineForBootstrap reports whether the parent baseline capture must
// wait for a repair phase. The checks that blocked the preflight cannot run on
// the unmodified parent, so gg runs the repair phase first and captures the
// baseline against parent+repair instead. It returns false for every project
// that did not ask for a repair, leaving the ordinary preflight untouched.
func (c *sequentialController) deferBaselineForBootstrap(ctx context.Context, request *Request) (bool, error) {
	verificationState := request.Project.Verification
	if !verificationState.BootstrapRequested && verificationState.BootstrapPhase == "" {
		return false, nil
	}
	pending, _ := c.pendingPlanPhases(context.WithoutCancel(ctx), request.Project.Slug)
	if verificationState.BootstrapPhase != "" {
		// Still pending means the repair has not run yet. A bootstrap phase
		// that is no longer pending is either complete or no longer planned;
		// either way the real preflight is what must decide next.
		for _, name := range pending {
			if name == verificationState.BootstrapPhase {
				return true, nil
			}
		}
		return false, nil
	}
	if len(pending) == 0 {
		// Nothing to defer for: Planning produced no pending phase, so the
		// ordinary preflight must decide whether the run can continue.
		return false, nil
	}
	recorder, ok := c.state.(verificationBootstrapState)
	if !ok {
		return false, errors.New("phase state cannot persist verification bootstrap progress")
	}
	updated, err := recorder.RecordVerificationBootstrapPhase(context.WithoutCancel(ctx), request.Project.Slug, pending[0])
	if err != nil {
		return false, fmt.Errorf("persist verification bootstrap phase %q: %w", pending[0], err)
	}
	request.Project = updated
	return true, nil
}

// completeVerificationBootstrap closes the repair detour for phase: the phase
// is marked complete before anything else so a later park cannot re-run it,
// the deferral flag is cleared, and request.Project is refreshed so the
// deferred preflight sees the durable truth.
func (c *sequentialController) completeVerificationBootstrap(ctx context.Context, request *Request, phase string) error {
	if recorder, ok := c.state.(planStateRecorder); ok {
		if _, err := recorder.RecordPlan(ctx, request.Project.Slug, nil, []string{phase}); err != nil {
			return fmt.Errorf("record verification bootstrap phase %q completion: %w", phase, err)
		}
	}
	recorder, ok := c.state.(verificationBootstrapState)
	if !ok {
		return errors.New("phase state cannot persist verification bootstrap progress")
	}
	updated, err := recorder.CompleteVerificationBootstrap(ctx, request.Project.Slug)
	if err != nil {
		return fmt.Errorf("complete verification bootstrap phase %q: %w", phase, err)
	}
	request.Project = updated
	return nil
}

func (c *sequentialController) verifyFinalBoundary(ctx context.Context, request *Request) error {
	if request == nil || request.Project.Verification == nil {
		return nil
	}
	return c.verifyBoundary(ctx, request, "final")
}

func (c *sequentialController) verifyBoundary(ctx context.Context, request *Request, cursor string) error {
	if request == nil || request.Project.Verification == nil {
		return nil
	}
	if c.verification == nil {
		return errVerificationServiceMissing
	}
	persist, ok := c.state.(verificationReportState)
	if !ok {
		return errors.New("phase state cannot persist verification reports")
	}
	verificationState := request.Project.Verification
	if !verificationState.ParentBaselineCaptured {
		return errors.New("verification boundary requires a captured parent baseline")
	}
	steps := verification.StepsFromState(verificationState.PlannedSteps)
	quarantined := quarantinedSet(*verificationState)
	report, runErr := c.verification.Verify(ctx, request.Project.WorktreePath, steps)
	baseline := baselineFromState(*verificationState, steps)
	boundary := verification.Compare(baseline, report, compareOptions(*verificationState))
	findings := findingsWithClassifications(boundary)
	warnings := warningFindings(boundary)
	nextAction := nextVerificationAction(boundary)
	updated, persistErr := persist.RecordVerificationResultReport(ctx, request.Project.Slug, commandResults(report), findings, warnings, cursor, verificationState.RemediationAttempts, nextAction)
	if persistErr != nil {
		return fmt.Errorf("persist verification boundary %q: %w", cursor, persistErr)
	}
	request.Project = updated
	for _, promotion := range boundary.Promotions {
		updated, persistErr = persist.PromoteVerificationIdentity(ctx, request.Project.Slug, promotion.String())
		if persistErr != nil {
			return fmt.Errorf("persist repaired verification identity %q: %w", promotion, persistErr)
		}
		request.Project = updated
	}
	if runErr != nil && hasUnavailableOrUnclassifiable(report, steps, quarantined) {
		return &verificationPauseError{cause: errors.Join(fmt.Errorf("verification boundary %q cannot be classified: %w", cursor, runErr), &verificationBoundaryError{cursor: cursor, report: boundary, current: report})}
	}
	if hasUnavailableOrUnclassifiable(report, steps, quarantined) {
		return &verificationPauseError{cause: errors.Join(fmt.Errorf("verification boundary %q cannot be classified: %s", cursor, reportSummary(report)), &verificationBoundaryError{cursor: cursor, report: boundary, current: report})}
	}
	if runErr != nil {
		return runErr
	}
	if !boundary.Passed {
		return &verificationBoundaryError{cursor: cursor, report: boundary, current: report}
	}
	return nil
}

// quarantinedSet indexes the checks the user excluded from boundary decisions.
func quarantinedSet(verificationState state.VerificationState) map[string]struct{} {
	set := make(map[string]struct{}, len(verificationState.QuarantinedChecks))
	for _, quarantine := range verificationState.QuarantinedChecks {
		if name := strings.TrimSpace(quarantine.CheckName); name != "" {
			set[name] = struct{}{}
		}
	}
	return set
}

func quarantinedNames(verificationState state.VerificationState) []string {
	names := make([]string, 0, len(verificationState.QuarantinedChecks))
	for _, quarantine := range verificationState.QuarantinedChecks {
		if name := strings.TrimSpace(quarantine.CheckName); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// preflightBlockedError names the checks that actually blocked and spells out
// the exact command that unblocks the run. Parking without that instruction
// leaves the user with no recoverable action when the environment cannot make
// the check classifiable.
func preflightBlockedError(report verification.Report, slug string, quarantined map[string]struct{}) error {
	return fmt.Errorf("parent verification preflight contains an unavailable or unclassifiable check: %s%s", reportSummary(report), skipChecksHint(report, slug, quarantined))
}

// skipChecksHint names only the checks that actually blocked, so the suggested
// command excludes both the healthy checks and the ones already quarantined.
// It is empty when no check blocked on its status.
func skipChecksHint(report verification.Report, slug string, quarantined map[string]struct{}) string {
	blocking := make([]string, 0, len(report.Results))
	for _, result := range report.Results {
		if _, excluded := quarantined[result.StepName]; excluded {
			continue
		}
		if result.Status == verification.CommandUnavailable || result.Status == verification.CommandUnclassifiable {
			blocking = append(blocking, result.StepName)
		}
	}
	if len(blocking) == 0 {
		return ""
	}
	return fmt.Sprintf("; skip them with: gg resume %s --skip-checks=%s", slug, strings.Join(blocking, ","))
}

func compareOptions(verificationState state.VerificationState) verification.CompareOptions {
	options := verification.CompareOptions{RepairMode: verificationState.RepairMode, Quarantined: quarantinedNames(verificationState)}
	baseline := baselineFromState(verificationState, verification.StepsFromState(verificationState.PlannedSteps))
	for _, result := range baseline.Results {
		for _, failure := range result.Failures {
			key := verification.FailureKey{CheckName: result.StepName, Identity: failure.Identity}
			if verificationState.RepairMode {
				options.RepairTargets = append(options.RepairTargets, key)
			}
		}
	}
	for _, identity := range verificationState.PromotedRequiredGreen {
		parts := strings.SplitN(identity, "::", 2)
		if len(parts) == 2 {
			options.Promoted = append(options.Promoted, verification.FailureKey{CheckName: parts[0], Identity: parts[1]})
			continue
		}
		// Schema-1 lifecycle data stored only the individual identity. Keep
		// those promotions effective by resolving them against the immutable
		// baseline's check name on resume.
		for _, result := range baseline.Results {
			for _, failure := range result.Failures {
				if failure.Identity == identity {
					options.Promoted = append(options.Promoted, verification.FailureKey{CheckName: result.StepName, Identity: identity})
				}
			}
		}
	}
	return options
}

func baselineFromState(verificationState state.VerificationState, steps []verification.Step) verification.Baseline {
	if len(verificationState.ParentResults) > 0 {
		return verification.Baseline{Results: convertResults(verificationState.ParentResults)}
	}
	byCheck := make(map[string][]verification.IndividualFailure)
	for _, finding := range verificationState.ParentBaseline {
		byCheck[finding.CheckName] = append(byCheck[finding.CheckName], verification.IndividualFailure{Identity: finding.Identity, Reason: finding.Reason})
	}
	results := make([]verification.CommandResult, 0, len(steps))
	for _, step := range steps {
		failures := byCheck[step.Name]
		status := verification.CommandPassed
		if len(failures) > 0 {
			status = verification.CommandFailed
		}
		results = append(results, verification.CommandResult{StepName: step.Name, Command: step.Command, Args: append([]string(nil), step.Args...), Status: status, Failures: failures})
	}
	return verification.Baseline{Results: results}
}

func commandResults(report verification.Report) []state.VerificationCommandResult {
	results := make([]state.VerificationCommandResult, 0, len(report.Results))
	for _, result := range report.Results {
		failures := make([]state.VerificationFinding, 0, len(result.Failures))
		for _, failure := range result.Failures {
			failures = append(failures, state.VerificationFinding{CheckName: result.StepName, Identity: failure.Identity, Reason: verification.NormalizeReason(failure.Reason), LogPath: result.LogPath})
		}
		results = append(results, state.VerificationCommandResult{CheckName: result.StepName, Command: result.Command, Args: append([]string(nil), result.Args...), Status: string(result.Status), Failures: failures, LogPath: result.LogPath, RetryCount: result.RetryCount, UnavailableErr: result.UnavailableErr})
	}
	return results
}

func convertResults(results []state.VerificationCommandResult) []verification.CommandResult {
	converted := make([]verification.CommandResult, 0, len(results))
	for _, result := range results {
		failures := make([]verification.IndividualFailure, 0, len(result.Failures))
		for _, failure := range result.Failures {
			failures = append(failures, verification.IndividualFailure{Identity: failure.Identity, Reason: failure.Reason})
		}
		converted = append(converted, verification.CommandResult{StepName: result.CheckName, Command: result.Command, Args: append([]string(nil), result.Args...), Status: verification.CommandStatus(result.Status), Failures: failures, LogPath: result.LogPath, RetryCount: result.RetryCount, UnavailableErr: result.UnavailableErr})
	}
	return converted
}

func reportFindings(report verification.Report, classification verification.Classification) []state.VerificationFinding {
	findings := make([]state.VerificationFinding, 0)
	for _, result := range report.Results {
		for _, failure := range result.Failures {
			findings = append(findings, state.VerificationFinding{CheckName: result.StepName, Identity: failure.Identity, Reason: verification.NormalizeReason(failure.Reason), Classification: string(classification), LogPath: result.LogPath})
		}
	}
	return findings
}

func findingsWithClassifications(report verification.BoundaryReport) []state.VerificationFinding {
	findings := make([]state.VerificationFinding, 0, len(report.Findings))
	for _, finding := range report.Findings {
		findings = append(findings, state.VerificationFinding{CheckName: finding.Key.CheckName, Identity: finding.Key.Identity, Reason: finding.Reason, Classification: string(finding.Classification), LogPath: finding.LogPath})
	}
	return findings
}

func warningFindings(report verification.BoundaryReport) []state.VerificationFinding {
	warnings := make([]state.VerificationFinding, 0, len(report.Warnings))
	for _, warning := range report.Warnings {
		warnings = append(warnings, state.VerificationFinding{CheckName: warning.Key.CheckName, Identity: warning.Key.Identity, Reason: warning.Reason, Classification: string(warning.Classification), LogPath: warning.LogPath})
	}
	return warnings
}

// hasUnavailableOrUnclassifiable reports whether the run cannot be classified.
// Quarantined checks are exempt from the status classification only: the
// structural invariants — one result per planned step, no unknown names, no
// duplicates — still apply to them, because a malformed report is an execution
// defect rather than a signal the user chose to ignore.
func hasUnavailableOrUnclassifiable(report verification.Report, steps []verification.Step, quarantined map[string]struct{}) bool {
	expected := make(map[string]struct{}, len(steps))
	for _, step := range steps {
		expected[step.Name] = struct{}{}
	}
	if len(report.Results) != len(steps) {
		return true
	}
	seen := make(map[string]struct{}, len(report.Results))
	for _, result := range report.Results {
		if _, ok := expected[result.StepName]; !ok {
			return true
		}
		if _, duplicate := seen[result.StepName]; duplicate {
			return true
		}
		seen[result.StepName] = struct{}{}
		if _, excluded := quarantined[result.StepName]; excluded {
			continue
		}
		switch result.Status {
		case verification.CommandPassed:
			if len(result.Failures) > 0 {
				return true
			}
		case verification.CommandFailed:
			if len(result.Failures) == 0 {
				return true
			}
		case verification.CommandUnavailable, verification.CommandUnclassifiable:
			return true
		default:
			return true
		}
		for _, failure := range result.Failures {
			if strings.TrimSpace(failure.Identity) == "" || verification.NormalizeReason(failure.Reason) == "" {
				return true
			}
		}
	}
	return len(seen) != len(expected)
}

func reportSummary(report verification.Report) string {
	parts := make([]string, 0, len(report.Results))
	for _, result := range report.Results {
		part := result.StepName + "=" + string(result.Status)
		if result.UnavailableErr != "" {
			part += ": " + result.UnavailableErr
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}

func nextVerificationAction(report verification.BoundaryReport) string {
	if report.Passed {
		if len(report.Warnings) > 0 {
			return "continue; unchanged baseline failures are retained as warnings"
		}
		return "continue"
	}
	if len(report.Findings) == 0 {
		return "inspect the persisted verification log and resolve the boundary failure before continuing"
	}
	return "resolve the classified verification regression before continuing"
}

func boundaryError(report verification.BoundaryReport) error {
	if len(report.Findings) == 0 {
		return errors.New("verification boundary failed")
	}
	first := report.Findings[0]
	return fmt.Errorf("verification boundary blocked by %s %s (%s): %s", first.Classification, first.Key, first.Key.CheckName, first.Reason)
}

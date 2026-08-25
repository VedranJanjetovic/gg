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
	steps := verification.StepsFromState(verificationState.PlannedSteps)
	report, runErr := c.verification.Verify(ctx, request.Project.WorktreePath, steps)
	if runErr != nil {
		if hasUnavailableOrUnclassifiable(report, steps) {
			_, persistErr := persist.RecordVerificationResultReport(ctx, request.Project.Slug, commandResults(report), nil, nil, "parent-preflight", 0, "make every planned verification step executable, then resume")
			return &verificationPauseError{cause: errors.Join(fmt.Errorf("parent verification preflight cannot complete: %w", runErr), persistErr)}
		}
		return runErr
	}
	if hasUnavailableOrUnclassifiable(report, steps) {
		_, persistErr := persist.RecordVerificationResultReport(ctx, request.Project.Slug, commandResults(report), nil, nil, "parent-preflight", 0, "make every planned verification step executable, then resume")
		return &verificationPauseError{cause: errors.Join(fmt.Errorf("parent verification preflight contains an unavailable or unclassifiable check: %s", reportSummary(report)), persistErr)}
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
	if runErr != nil && hasUnavailableOrUnclassifiable(report, steps) {
		return &verificationPauseError{cause: errors.Join(fmt.Errorf("verification boundary %q cannot be classified: %w", cursor, runErr), &verificationBoundaryError{cursor: cursor, report: boundary, current: report})}
	}
	if hasUnavailableOrUnclassifiable(report, steps) {
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

func compareOptions(verificationState state.VerificationState) verification.CompareOptions {
	options := verification.CompareOptions{RepairMode: verificationState.RepairMode}
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

func hasUnavailableOrUnclassifiable(report verification.Report, steps []verification.Step) bool {
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

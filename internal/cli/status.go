package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/VedranJanjetovic/gg/internal/state"
)

func (a *App) statusCommand(ctx context.Context, output io.Writer, args []string) error {
	if err := a.requireConfiguredProject(); err != nil {
		return err
	}
	selector, err := statusSelector(args)
	if err != nil {
		return err
	}
	service, err := a.projectService(ctx)
	if err != nil {
		return fmt.Errorf("read project status: %w", err)
	}
	if selector != "" {
		project, err := service.Load(ctx, selector)
		if err != nil {
			return fmt.Errorf("load project %q: %w", selector, err)
		}
		if project.Status == state.StatusRunning {
			// Repair a dead run before reporting it, like attach does.
			if recoverer, ok := service.(staleRunRecoverer); ok {
				if recovered, changed, recoverErr := recoverer.RecoverIfStale(ctx, selector); recoverErr == nil && changed {
					project = recovered
				}
			}
		}
		return writeProjectDetail(output, project)
	}
	projects, err := a.Projects(ctx)
	if err != nil {
		return fmt.Errorf("list project status: %w", err)
	}
	if len(projects) == 0 {
		if _, err := fmt.Fprintln(output, "gg status: no active runs"); err != nil {
			return err
		}
	}
	return writeProjectStatusTable(output, projects)
}

func statusSelector(args []string) (string, error) {
	var selector string
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			return "", fmt.Errorf("status does not accept flag %q", arg)
		}
		if selector != "" {
			return "", errors.New("status accepts at most one project selector")
		}
		selector = arg
	}
	if selector == "" {
		return "", nil
	}
	slug, err := ParseProjectSelector(selector)
	if err != nil {
		return "", fmt.Errorf("status: %w", err)
	}
	return slug, nil
}

func writeProjectDetail(output io.Writer, project state.ProjectState) error {
	if _, err := fmt.Fprintf(output, "Name: %s\nSlug: %s\nStatus: %s\nCurrent phase: %s\nBranch: %s\nWorktree: %s\nUpdated: %s\n", project.Name, project.Slug, project.Status, displayValue(project.CurrentPhase), displayValue(project.BranchName), displayValue(project.WorktreePath), formatUpdated(project.UpdatedAt)); err != nil {
		return err
	}
	labels := make([]string, 0)
	seen := make(map[string]struct{})
	for _, record := range project.PhaseHistory {
		if record.Skip == nil {
			continue
		}
		label := skippedRecordLabel(record)
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		labels = append(labels, label)
	}
	for _, label := range labels {
		count := 0
		for _, record := range project.PhaseHistory {
			if record.Skip != nil && skippedRecordLabel(record) == label {
				count++
			}
		}
		if _, err := fmt.Fprintf(output, "Skipped count: %d (%s)\n", count, label); err != nil {
			return err
		}
	}
	for _, record := range project.PhaseHistory {
		if record.Skip == nil {
			continue
		}
		label := skippedRecordLabel(record)
		evidence := "-"
		if len(record.Skip.Cleanup.Evidence) > 0 {
			evidence = strings.Join(record.Skip.Cleanup.Evidence, "; ")
		}
		continuation := record.Skip.NextPhase
		if record.Skip.NextSubphase != "" {
			continuation += " / " + record.Skip.NextSubphase
		}
		if continuation == "" {
			continuation = "-"
		}
		externalIdentity := record.Skip.ExternalIdentity
		if externalIdentity == "" && record.Outcome != nil {
			externalIdentity = record.Outcome.ExternalIdentity
		}
		if _, err := fmt.Fprintf(output, "Skipped: %s\n  Occurrence: %s\n  Failure: %s\n  Confirmed: %s\n  Cleanup: %s\n  Cleanup evidence: %s\n  External identity: %s\n  Continuation: %s\n", label, displayValue(record.OccurrenceID), skippedFailureSummary(record), formatUpdated(record.Skip.ConfirmedAt), record.Skip.Cleanup.Status, evidence, displayValue(externalIdentity), continuation); err != nil {
			return err
		}
	}
	if err := writeVerificationSummary(output, project, "Verification"); err != nil {
		return err
	}
	return nil
}

func writeVerificationSummary(output io.Writer, project state.ProjectState, heading string) error {
	findings := state.VerificationDisplay(project)
	if len(findings) == 0 && (project.Verification == nil || strings.TrimSpace(project.Verification.NextAction) == "") {
		return nil
	}
	if _, err := fmt.Fprintf(output, "\n%s:\n", heading); err != nil {
		return err
	}
	for _, finding := range findings {
		label := "Finding"
		if finding.Warning {
			label = "Warning"
		}
		if _, err := fmt.Fprintf(output, "  %s:\n    Check: %s\n    Command: %s\n    Identity: %s\n    Reason: %s\n    Classification: %s\n    Attempts: %d/%d\n    Log: %s\n", label, displayValue(finding.CheckName), displayValue(finding.Command), displayValue(finding.Identity), displayValue(finding.Reason), displayValue(finding.Classification), finding.Attempts, finding.MaxAttempts, displayValue(finding.LogPath)); err != nil {
			return err
		}
	}
	if project.Verification != nil && strings.TrimSpace(project.Verification.NextAction) != "" {
		_, err := fmt.Fprintf(output, "  Next action: %s\n", project.Verification.NextAction)
		return err
	}
	return nil
}

func skippedRecordLabel(record state.PhaseRecord) string {
	label := record.Phase
	if record.Subphase != "" {
		label += " / " + record.Subphase
	}
	return label
}

func skippedFailureSummary(record state.PhaseRecord) string {
	if record.Outcome == nil || strings.TrimSpace(record.Outcome.Error) == "" {
		return "-"
	}
	return strings.TrimSpace(record.Outcome.Error)

}

func verificationStatusSuffix(project state.ProjectState) string {
	switch {
	case state.VerificationIsPaused(project):
		return " [paused]"
	case state.VerificationHasWarnings(project):
		return " [warning]"
	default:
		return ""
	}
}

func writeProjectStatusTable(output io.Writer, projects []state.ProjectState) error {
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "NAME\tSTATUS\tCURRENT PHASE\tBRANCH\tWORKTREE\tUPDATED"); err != nil {
		return err
	}
	for _, project := range projects {
		if _, err := fmt.Fprintf(writer, "%s\t%s%s\t%s\t%s\t%s\t%s\n", project.Name, project.Status, verificationStatusSuffix(project), displayValue(project.CurrentPhase), displayValue(project.BranchName), displayValue(project.WorktreePath), formatUpdated(project.UpdatedAt)); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func formatUpdated(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

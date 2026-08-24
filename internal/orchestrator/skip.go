package orchestrator

import (
	"errors"
	"fmt"

	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

var ErrSkipNotAllowed = errors.New("skip is not allowed for this execution unit")

// SkipCleanupPolicy tells the later cleanup owner how a confirmed waiver must
// treat the failed unit. It is policy only; it does not perform cleanup.
type SkipCleanupPolicy string

const (
	SkipCleanupPreserveWorktree SkipCleanupPolicy = "preserve_worktree"
	SkipCleanupRestoreWorktree  SkipCleanupPolicy = "restore_worktree"
	SkipCleanupReadOnly         SkipCleanupPolicy = "read_only"
	SkipCleanupRetainExternal   SkipCleanupPolicy = "retain_external"
)

// SkipAllowed reports whether the exact phase/subphase is exposed to the
// durable skip operation. Development implementation/review and all phases
// before Development are deliberately excluded.
func SkipAllowed(phase pipeline.PhaseID, subphase string) bool {
	return state.IsSkipEligible(string(phase), subphase)
}

// SkipCleanupFor returns the phase-specific cleanup policy for an eligible
// execution unit. The bool is false for units that cannot be skipped.
func SkipCleanupFor(phase pipeline.PhaseID, subphase string) (SkipCleanupPolicy, bool) {
	if !SkipAllowed(phase, subphase) {
		return "", false
	}
	if phase == pipeline.PhaseDevelopment {
		return SkipCleanupPreserveWorktree, true
	}
	switch phase {
	case pipeline.PhaseRebase, pipeline.PhaseTestDocument:
		return SkipCleanupRestoreWorktree, true
	case pipeline.PhaseQA, pipeline.PhaseBuildChecker:
		return SkipCleanupReadOnly, true
	case pipeline.PhasePR, pipeline.PhaseCI:
		return SkipCleanupRetainExternal, true
	default:
		return "", false
	}
}

// ValidateSkipTarget applies the UI-independent eligibility matrix before a
// caller invokes state.LifecycleService.SkipFailedExecution. State performs
// the locked exact-occurrence and idempotency checks again at commit time.
func ValidateSkipTarget(project state.ProjectState, phase pipeline.PhaseID, subphase, occurrenceID string) error {
	if project.Status != state.StatusFailed {
		return fmt.Errorf("%w: project status is %s", ErrSkipNotAllowed, project.Status)
	}
	if occurrenceID == "" {
		return fmt.Errorf("%w: occurrence ID is required", ErrSkipNotAllowed)
	}
	if !SkipAllowed(phase, subphase) {
		return fmt.Errorf("%w: %s/%s", ErrSkipNotAllowed, phase, subphase)
	}
	for index, record := range project.PhaseHistory {
		if record.OccurrenceID != occurrenceID {
			continue
		}
		if index != len(project.PhaseHistory)-1 {
			return fmt.Errorf("%w: occurrence %q is not the current failure", ErrSkipNotAllowed, occurrenceID)
		}
		if record.Phase != string(phase) || record.Subphase != subphase {
			return fmt.Errorf("%w: occurrence belongs to %s/%s", ErrSkipNotAllowed, record.Phase, record.Subphase)
		}
		if record.Status != state.StatusFailed || record.CompletedAt == nil || (record.Outcome != nil && record.Outcome.Canceled) {
			return fmt.Errorf("%w: occurrence status is %s", ErrSkipNotAllowed, record.Status)
		}
		if record.Skip != nil {
			return nil
		}
		return nil
	}
	return fmt.Errorf("%w: occurrence %q was not found", ErrSkipNotAllowed, occurrenceID)
}

// SkipContinuation returns the next execution unit for the current failed
// occurrence. The durable state service records this cursor with the waiver;
// keeping its calculation here prevents presentation code from duplicating
// pipeline sequencing rules.
func SkipContinuation(project state.ProjectState) (string, string, error) {
	if project.Status != state.StatusFailed || len(project.PhaseHistory) == 0 {
		return "", "", fmt.Errorf("%w: project has no current failed execution", ErrSkipNotAllowed)
	}
	last := project.PhaseHistory[len(project.PhaseHistory)-1]
	if last.Status != state.StatusFailed || last.Skip != nil {
		return "", "", fmt.Errorf("%w: latest execution is not a pending failure", ErrSkipNotAllowed)
	}
	if !state.IsSkipEligible(last.Phase, last.Subphase) {
		return "", "", fmt.Errorf("%w: %s/%s", ErrSkipNotAllowed, last.Phase, last.Subphase)
	}
	if last.Phase == string(pipeline.PhaseDevelopment) && last.Subphase == "testing" {
		return string(pipeline.PhaseDevelopment), "review", nil
	}

	plan, generation, _, err := pipeline.RestoreExecution(project.PipelineConfig)
	if err != nil {
		return "", "", fmt.Errorf("restore pipeline for skip continuation: %w", err)
	}
	phase, subphase, hasNext, err := nextExecutionCursor(plan, generation, pipeline.PhaseID(last.Phase), last.Subphase)
	if err != nil {
		return "", "", fmt.Errorf("select skip continuation: %w", err)
	}
	if !hasNext {
		return "", "", nil
	}
	return phase, subphase, nil
}

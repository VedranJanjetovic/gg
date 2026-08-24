package state

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrSkipNotEligible means the project or occurrence is not a genuine
	// failed execution that can be waived.
	ErrSkipNotEligible = errors.New("execution is not eligible for skip")
	// ErrStaleSkipOccurrence means the caller is trying to resolve an older
	// occurrence than the currently failed execution.
	ErrStaleSkipOccurrence = errors.New("skip occurrence is stale")
	// ErrSkipCleanup means cleanup did not complete, so no waiver or cursor
	// advancement was persisted.
	ErrSkipCleanup = errors.New("skip cleanup failed")
)

// SkipRequest contains the user-confirmed resolution for one exact failed
// occurrence. The caller supplies the next cursor selected from the persisted
// pipeline snapshot; an empty cursor means there is no later execution unit.
type SkipRequest struct {
	OccurrenceID     string
	ConfirmedAt      time.Time
	NextPhase        string
	NextSubphase     string
	ExternalIdentity string
}

// SkipCleanupFunc performs the phase-specific cleanup while the project lock
// is held. It must be idempotent for the supplied occurrence. Returning an
// error leaves the failed record unchanged and prevents continuation.
type SkipCleanupFunc func(context.Context, ProjectState, PhaseRecord) (SkipCleanup, error)

// SkipFailedExecution atomically confirms one failed execution, records its
// cleanup and continuation cursor, and leaves the project in its ordinary
// failed lifecycle status. A later Resume/continuation can claim the project
// from that durable cursor. Repeating the same request is idempotent and does
// not invoke cleanup again.
func (s *LifecycleService) SkipFailedExecution(ctx context.Context, slug string, request SkipRequest, cleanup SkipCleanupFunc) (ProjectState, error) {
	if err := checkContext(ctx); err != nil {
		return ProjectState{}, err
	}
	if s.store == nil || s.locker == nil {
		return ProjectState{}, errors.New("lifecycle service requires store and locker")
	}
	if strings.TrimSpace(request.OccurrenceID) == "" {
		return ProjectState{}, fmt.Errorf("%w: occurrence ID is required", ErrSkipNotEligible)
	}
	if strings.TrimSpace(request.NextPhase) == "" && strings.TrimSpace(request.NextSubphase) != "" {
		return ProjectState{}, fmt.Errorf("%w: next subphase requires next phase", ErrSkipNotEligible)
	}

	var result ProjectState
	err := s.withProjectLock(ctx, slug, func(locked context.Context) error {
		project, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		if project.Status != StatusFailed {
			return fmt.Errorf("%w: project status is %s", ErrSkipNotEligible, project.Status)
		}

		index := -1
		for i := range project.PhaseHistory {
			if project.PhaseHistory[i].OccurrenceID == request.OccurrenceID {
				index = i
				break
			}
		}
		if index < 0 {
			return fmt.Errorf("%w: occurrence %q was not found", ErrStaleSkipOccurrence, request.OccurrenceID)
		}
		record := project.PhaseHistory[index]
		if record.Skip != nil {
			// A confirmed resolution is the durable idempotency key. Do not
			// rerun cleanup or let a repeated key change the original waiver.
			result = project
			return nil
		}
		if index != len(project.PhaseHistory)-1 {
			return fmt.Errorf("%w: occurrence %q is not the current failure", ErrStaleSkipOccurrence, request.OccurrenceID)
		}
		if record.Status != StatusFailed || record.CompletedAt == nil || (record.Outcome != nil && record.Outcome.Canceled) {
			return fmt.Errorf("%w: occurrence %q has status %s", ErrSkipNotEligible, request.OccurrenceID, record.Status)
		}

		cleanupResult := SkipCleanup{Status: SkipCleanupNotRequired}
		if cleanup != nil {
			cleanupResult, err = cleanup(locked, project, record)
			if err != nil {
				return fmt.Errorf("%w for occurrence %q: %v", ErrSkipCleanup, request.OccurrenceID, err)
			}
		}
		if !cleanupResult.Status.IsValid() {
			return fmt.Errorf("%w: cleanup returned invalid status %q", ErrSkipCleanup, cleanupResult.Status)
		}

		confirmedAt := request.ConfirmedAt
		if confirmedAt.IsZero() {
			confirmedAt = s.clock.Now()
		}
		project.PhaseHistory[index].Skip = &SkipResolution{
			ConfirmedAt:      confirmedAt,
			Cleanup:          SkipCleanup{Status: cleanupResult.Status, Evidence: append([]string(nil), cleanupResult.Evidence...)},
			NextPhase:        strings.TrimSpace(request.NextPhase),
			NextSubphase:     strings.TrimSpace(request.NextSubphase),
			ExternalIdentity: strings.TrimSpace(request.ExternalIdentity),
		}
		if request.NextPhase != "" {
			project.CurrentPhase = strings.TrimSpace(request.NextPhase)
			project.CurrentSubphase = strings.TrimSpace(request.NextSubphase)
		}
		project.UpdatedAt = s.clock.Now()
		if err := s.store.Save(locked, project); err != nil {
			return err
		}
		result = project
		return nil
	})
	return result, err
}

func newOccurrenceID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return "occurrence-" + hex.EncodeToString(bytes[:]), nil
}

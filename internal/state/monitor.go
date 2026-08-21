package state

import (
	"context"
	"errors"
)

// UpdatePRCIMonitor persists a normalized monitor observation idempotently.
func (s *LifecycleService) UpdatePRCIMonitor(ctx context.Context, slug string, monitor PRCIMonitorState) (ProjectState, error) {
	if err := checkContext(ctx); err != nil {
		return ProjectState{}, err
	}
	if s.store == nil || s.locker == nil {
		return ProjectState{}, errors.New("lifecycle service requires store and locker")
	}
	var result ProjectState
	err := s.withProjectLock(ctx, slug, func(locked context.Context) error {
		project, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		project.PRCIMonitor = &monitor
		project.UpdatedAt = s.clock.Now()
		if err := s.store.Save(locked, project); err != nil {
			return err
		}
		result = project
		return nil
	})
	return result, err
}

// MarkPullRequestMerged is the only monitor-owned successful terminal transition.
func (s *LifecycleService) MarkPullRequestMerged(ctx context.Context, slug, url string) (ProjectState, error) {
	if err := checkContext(ctx); err != nil {
		return ProjectState{}, err
	}
	if s.store == nil || s.locker == nil {
		return ProjectState{}, errors.New("lifecycle service requires store and locker")
	}
	var result ProjectState
	err := s.withProjectLock(ctx, slug, func(locked context.Context) error {
		project, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		if project.Status == StatusFinished && project.Terminal != nil && project.Terminal.Kind == TerminalPullRequestMerged {
			result = project
			return nil
		}
		if project.Status != StatusRunning && project.Status != StatusFinished {
			return errors.New("pull request can only finish a running project")
		}
		now := s.clock.Now()
		project.Status, project.StatusChangedAt, project.UpdatedAt = StatusFinished, now, now
		project.ActiveRunID, project.RunReservationToken, project.DispatchClaimRunID = "", "", ""
		project.StopRequested, project.StopRequestID = false, ""
		project.Terminal = &TerminalState{Kind: TerminalPullRequestMerged, At: now, PullRequestURL: url}
		if project.PRCIMonitor == nil {
			project.PRCIMonitor = &PRCIMonitorState{}
		}
		project.PRCIMonitor.Terminal, project.PRCIMonitor.Result, project.PRCIMonitor.UpdatedAt = true, "merged", now
		if err := s.store.Save(locked, project); err != nil {
			return err
		}
		result = project
		return nil
	})
	return result, err
}

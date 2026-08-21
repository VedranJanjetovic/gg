package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/VedranJanjetovic/gg/internal/state"
)

// ProjectLister is the persistence seam used by StopAll to discover projects.
type ProjectLister interface {
	List(context.Context) ([]state.ProjectState, error)
}

// ProjectStopper is the per-project stop seam. Controller.Stop implementations
// use durable state when available, so StopAll does not bypass run ownership or
// stop-request fencing.
type ProjectStopper interface {
	Stop(context.Context, StopRequest) error
}

// StopAllFailure records a running project whose durable stop request failed.
type StopAllFailure struct {
	Project state.ProjectState
	Err     error
}

// StopAllResult reports the outcome of a stop-all attempt. Projects are
// selected only when their persisted status is exactly StatusRunning.
type StopAllResult struct {
	Running  int
	Stopped  int
	Failures []StopAllFailure
}

// Summary returns a user-facing description of the outcome, including the
// explicit zero-active case and any partial failures.
func (r StopAllResult) Summary() string {
	if r.Running == 0 {
		return "no running projects to stop"
	}
	if len(r.Failures) == 0 {
		return fmt.Sprintf("stop requested for %d running project(s)", r.Stopped)
	}
	return fmt.Sprintf("stop requested for %d of %d running project(s); %d failed", r.Stopped, r.Running, len(r.Failures))
}

// StopAllError reports individual durable stop failures after all running
// projects have been attempted. It is returned only when at least one running
// project could not be stopped.
type StopAllError struct {
	Failures []StopAllFailure
}

func (e *StopAllError) Error() string {
	if e == nil || len(e.Failures) == 0 {
		return ""
	}
	parts := make([]string, 0, len(e.Failures))
	for _, failure := range e.Failures {
		parts = append(parts, fmt.Sprintf("%s: %v", failure.Project.Slug, failure.Err))
	}
	return fmt.Sprintf("stop-all failed for %d project(s): %s", len(e.Failures), strings.Join(parts, "; "))
}

func (e *StopAllError) Unwrap() error {
	if e == nil {
		return nil
	}
	errList := make([]error, 0, len(e.Failures))
	for _, failure := range e.Failures {
		errList = append(errList, failure.Err)
	}
	return errors.Join(errList...)
}

// StopAll enumerates persisted projects, requests a durable stop for every
// exactly-running project, and continues after individual stop failures. It
// does not invoke the stopper for pending, stopped, failed, finished, or
// terminated projects.
func StopAll(ctx context.Context, lister ProjectLister, stopper ProjectStopper) (StopAllResult, error) {
	if err := ctx.Err(); err != nil {
		return StopAllResult{}, err
	}
	if lister == nil || stopper == nil {
		return StopAllResult{}, errors.New("stop-all requires project lister and stopper")
	}
	projects, err := lister.List(ctx)
	if err != nil {
		return StopAllResult{}, fmt.Errorf("list projects for stop-all: %w", err)
	}

	result := StopAllResult{}
	for _, project := range projects {
		if project.Status != state.StatusRunning {
			continue
		}
		result.Running++
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := stopper.Stop(ctx, StopRequest{ProjectSlug: project.Slug, RunID: project.ActiveRunID}); err != nil {
			result.Failures = append(result.Failures, StopAllFailure{Project: project, Err: err})
			continue
		}
		result.Stopped++
	}
	if len(result.Failures) > 0 {
		return result, &StopAllError{Failures: append([]StopAllFailure(nil), result.Failures...)}
	}
	return result, nil
}

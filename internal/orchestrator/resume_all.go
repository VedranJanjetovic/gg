package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/VedranJanjetovic/gg/internal/state"
)

type ResumeCandidate struct {
	Project     state.ProjectState
	Execution   Request
	Reservation *state.RunReservation
	Kind        state.RerunKind
}
type ResumeSource interface {
	Discover(context.Context, string) ([]ResumeCandidate, error)
}
type ResumeCoordinatorOptions struct{ Concurrency int }
type AllResumeCoordinator struct {
	source     ResumeSource
	controller Controller
	limit      int
	mu         sync.Mutex
	running    bool
}

func NewResumeCoordinator(source ResumeSource, controller Controller, options ResumeCoordinatorOptions) (*AllResumeCoordinator, error) {
	if source == nil || controller == nil {
		return nil, errors.New("resume coordinator requires source and controller")
	}
	if options.Concurrency <= 0 {
		options.Concurrency = 1
	}
	return &AllResumeCoordinator{source: source, controller: controller, limit: options.Concurrency}, nil
}

func (c *AllResumeCoordinator) ResumeAll(ctx context.Context, request ResumeAllRequest) ([]ResumeResult, error) {
	if c == nil || c.source == nil || c.controller == nil {
		return nil, errors.New("resume coordinator is not configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return nil, errors.New("resume already in progress")
	}
	c.running = true
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.running = false; c.mu.Unlock() }()
	if request.RunID == "" {
		request.RunID = fmt.Sprintf("resume-%d", time.Now().UnixNano())
	}
	candidates, err := c.source.Discover(ctx, request.RunID)
	if err != nil {
		return nil, fmt.Errorf("discover projects for resume: %w", err)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Project.Slug < candidates[j].Project.Slug })
	results := make([]ResumeResult, 0, len(candidates))
	jobs := make(chan ResumeCandidate)
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := make(map[string]struct{}, len(candidates))
	failures := make([]ResumeResult, 0)
	worker := func() {
		defer wg.Done()
		for candidate := range jobs {
			kind := candidate.Kind
			if kind == "" {
				kind = state.ClassifyRerun(candidate.Project)
			}
			result := ResumeResult{ProjectSlug: candidate.Project.Slug, Kind: kind}
			if result.Kind == state.RerunNew || (result.Kind == state.RerunFinished && len(candidate.Execution.Pipeline.Phases()) == 0) {
				if candidate.Reservation != nil {
					_ = candidate.Reservation.Rollback(context.WithoutCancel(ctx))
				}
				mu.Lock()
				results = append(results, result)
				mu.Unlock()
				continue
			}
			_, dispatchErr := c.controller.Resume(ctx, ResumeRequest{ProjectSlug: candidate.Project.Slug, RunID: request.RunID, Execution: candidate.Execution})
			result.Err = dispatchErr
			if dispatchErr != nil && candidate.Reservation != nil {
				_ = candidate.Reservation.Rollback(context.WithoutCancel(ctx))
			}
			mu.Lock()
			results = append(results, result)
			if dispatchErr != nil {
				failures = append(failures, result)
			}
			mu.Unlock()
		}
	}
	workers := c.limit
	if workers > len(candidates) {
		workers = len(candidates)
	}
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go worker()
	}
	for _, candidate := range candidates {
		if _, duplicate := seen[candidate.Project.Slug]; duplicate {
			if candidate.Reservation != nil {
				_ = candidate.Reservation.Rollback(context.WithoutCancel(ctx))
			}
			continue
		}
		seen[candidate.Project.Slug] = struct{}{}
		select {
		case jobs <- candidate:
		case <-ctx.Done():
			if candidate.Reservation != nil {
				_ = candidate.Reservation.Rollback(context.WithoutCancel(ctx))
			}
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	sort.Slice(results, func(i, j int) bool { return results[i].ProjectSlug < results[j].ProjectSlug })
	if err := ctx.Err(); err != nil {
		return results, err
	}
	if len(failures) > 0 {
		return results, &ResumeAllError{Failures: failures}
	}
	return results, nil
}

type ResumeAllError struct{ Failures []ResumeResult }

func (e *ResumeAllError) Error() string {
	if e == nil || len(e.Failures) == 0 {
		return ""
	}
	parts := make([]string, 0, len(e.Failures))
	for _, f := range e.Failures {
		parts = append(parts, fmt.Sprintf("%s: %v", f.ProjectSlug, f.Err))
	}
	return fmt.Sprintf("resume failed for %d project(s): %s", len(e.Failures), strings.Join(parts, "; "))
}
func (e *ResumeAllError) Unwrap() error {
	if e == nil {
		return nil
	}
	errs := make([]error, 0, len(e.Failures))
	for _, f := range e.Failures {
		if f.Err != nil {
			errs = append(errs, f.Err)
		}
	}
	return errors.Join(errs...)
}

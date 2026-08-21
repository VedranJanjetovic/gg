package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"github.com/VedranJanjetovic/gg/internal/ci"
	"github.com/VedranJanjetovic/gg/internal/pr"
	"github.com/VedranJanjetovic/gg/internal/state"
	"sort"
	"strings"
	"time"
)

type PRCIStateProvider interface {
	Observe(context.Context, string, string) (pr.PullRequestObservation, error)
}
type PRCICheckProvider interface {
	Observe(context.Context, string, string) (ci.CheckObservation, error)
}
type RemediationKind string

const (
	RemediationConflict  RemediationKind = "merge_conflict"
	RemediationCIFailure RemediationKind = "failed_ci"
)

type Remediation struct {
	ProjectSlug, PullRequestURL, Check string
	Kind                               RemediationKind
}
type PRCIRemediator interface {
	Remediate(context.Context, Remediation) error
}
type PRCIStateStore interface {
	UpdatePRCIMonitor(context.Context, string, state.PRCIMonitorState) (state.ProjectState, error)
	MarkPullRequestMerged(context.Context, string, string) (state.ProjectState, error)
}
type PRCIStateReader interface {
	Load(context.Context, string) (state.ProjectState, error)
}
type PRCIRequest struct {
	ProjectSlug, PullRequestURL string
	MaxPolls                    int
	PollInterval, Backoff       time.Duration
}
type PRCIResult struct {
	Polls        int
	State        pr.MergeState
	Clean        bool
	Merged       bool
	Remediations int
	Cursor       string
	Failed       bool
}
type PRCILifecycleMonitor struct {
	pullRequests PRCIStateProvider
	checks       PRCICheckProvider
	remediation  PRCIRemediator
	store        PRCIStateStore
}

func NewPRCILifecycleMonitor(pull PRCIStateProvider, checks PRCICheckProvider, remediation PRCIRemediator, store PRCIStateStore) *PRCILifecycleMonitor {
	return &PRCILifecycleMonitor{pullRequests: pull, checks: checks, remediation: remediation, store: store}
}
func (m *PRCILifecycleMonitor) Monitor(ctx context.Context, req PRCIRequest) (PRCIResult, error) {
	if m == nil || m.pullRequests == nil || m.checks == nil || m.store == nil {
		return PRCIResult{}, errors.New("PR/CI monitor requires providers and state store")
	}
	if strings.TrimSpace(req.ProjectSlug) == "" || strings.TrimSpace(req.PullRequestURL) == "" {
		return PRCIResult{}, errors.New("project and pull request identity are required")
	}
	polls := req.MaxPolls
	if polls <= 0 {
		polls = 1
	}
	if req.PollInterval < 0 || req.Backoff < 0 {
		return PRCIResult{}, errors.New("poll durations cannot be negative")
	}
	var result PRCIResult
	var cursor, lastKey string
	seen := make(map[string]struct{})
	if reader, ok := m.store.(PRCIStateReader); ok {
		if project, err := reader.Load(ctx, req.ProjectSlug); err == nil && project.PRCIMonitor != nil {
			cursor, lastKey = project.PRCIMonitor.Cursor, project.PRCIMonitor.LastRemediation
			for _, key := range project.PRCIMonitor.RemediationKeys {
				if key != "" {
					seen[key] = struct{}{}
				}
			}
			if lastKey != "" {
				seen[lastKey] = struct{}{}
			}
			if project.PRCIMonitor.Terminal {
				result.Merged, result.State = true, pr.MergeStateMerged
				result.Cursor = cursor
				return result, nil
			}
		}
	}
	for poll := 1; poll <= polls; poll++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		observed, err := m.pullRequests.Observe(ctx, req.PullRequestURL, cursor)
		if err != nil {
			if poll == polls {
				return result, m.fail(ctx, req.ProjectSlug, cursor, "PR: "+err.Error(), err, lastKey, seen)
			}
			if err := monitorWait(ctx, req.PollInterval+req.Backoff); err != nil {
				return result, err
			}
			continue
		}
		cursor = observed.Cursor
		result.Polls, result.State, result.Cursor = poll, observed.State, cursor
		if observed.State == pr.MergeStateMerged {
			result.Merged = true
			if _, err := m.merged(ctx, req.ProjectSlug, req.PullRequestURL, cursor, lastKey, seen); err != nil {
				return result, err
			}
			return result, nil
		}
		if observed.State != pr.MergeStateOpen {
			return result, m.fail(ctx, req.ProjectSlug, cursor, "PR is closed without merge", errors.New("pull request closed without merge"), lastKey, seen)
		}
		if observed.MergeConflict {
			key := "conflict:" + cursor
			if _, ok := seen[key]; !ok {
				if err := m.remediate(ctx, Remediation{ProjectSlug: req.ProjectSlug, PullRequestURL: req.PullRequestURL, Kind: RemediationConflict}); err != nil {
					return result, m.fail(ctx, req.ProjectSlug, cursor, "conflict remediation: "+err.Error(), err, lastKey, seen)
				}
				result.Remediations++
				lastKey = key
				seen[key] = struct{}{}
			}
		}
		checks, err := m.checks.Observe(ctx, req.PullRequestURL, cursor)
		if err != nil {
			if poll == polls {
				return result, m.fail(ctx, req.ProjectSlug, cursor, "CI: "+err.Error(), err, lastKey, seen)
			}
			if err := monitorWait(ctx, req.PollInterval+req.Backoff); err != nil {
				return result, err
			}
			continue
		}
		cursor = checks.Cursor
		result.Cursor = cursor
		result.Clean = len(checks.Failed) == 0 && !checks.Pending
		for _, name := range checks.Failed {
			key := "ci:" + cursor + ":" + name
			if _, ok := seen[key]; ok {
				continue
			}
			if err := m.remediate(ctx, Remediation{ProjectSlug: req.ProjectSlug, PullRequestURL: req.PullRequestURL, Check: name, Kind: RemediationCIFailure}); err != nil {
				return result, m.fail(ctx, req.ProjectSlug, cursor, "CI remediation: "+err.Error(), err, lastKey, seen)
			}
			result.Remediations++
			lastKey = key
			seen[key] = struct{}{}
		}
		if err := m.persist(ctx, req.ProjectSlug, state.PRCIMonitorState{Cursor: cursor, Result: monitorResult(result), LastRemediation: lastKey, RemediationKeys: remediationKeys(seen)}); err != nil {
			return result, err
		}
		if poll < polls {
			if err := monitorWait(ctx, req.PollInterval); err != nil {
				return result, err
			}
		}
	}
	return result, nil
}
func (m *PRCILifecycleMonitor) remediate(ctx context.Context, r Remediation) error {
	if m.remediation == nil {
		return errors.New("remediation is not configured")
	}
	return m.remediation.Remediate(ctx, r)
}
func (m *PRCILifecycleMonitor) persist(ctx context.Context, slug string, v state.PRCIMonitorState) error {
	v.UpdatedAt = time.Now().UTC()
	_, err := m.store.UpdatePRCIMonitor(ctx, slug, v)
	return err
}
func (m *PRCILifecycleMonitor) merged(ctx context.Context, slug, url, cursor, lastKey string, seen map[string]struct{}) (state.ProjectState, error) {
	if err := m.persist(ctx, slug, state.PRCIMonitorState{Cursor: cursor, Result: "merged", LastRemediation: lastKey, RemediationKeys: remediationKeys(seen), Terminal: true}); err != nil {
		return state.ProjectState{}, err
	}
	return m.store.MarkPullRequestMerged(ctx, slug, url)
}
func (m *PRCILifecycleMonitor) fail(ctx context.Context, slug, cursor, msg string, cause error, lastKey string, seen map[string]struct{}) error {
	_ = m.persist(ctx, slug, state.PRCIMonitorState{Cursor: cursor, Result: "failed", LastRemediation: lastKey, RemediationKeys: remediationKeys(seen), Failed: true, Error: msg})
	return fmt.Errorf("PR/CI monitor: %w", cause)
}
func monitorResult(r PRCIResult) string {
	if r.Merged {
		return "merged"
	}
	if r.Failed {
		return "failed"
	}
	if r.Clean {
		return "clean"
	}
	return "open"
}

func remediationKeys(seen map[string]struct{}) []string {
	keys := make([]string, 0, len(seen))
	for key := range seen {
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func monitorWait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

package ci

import (
	"context"
	"errors"
	"strings"
)

// GitHubCheckProvider adapts the existing gh command seam without exposing
// provider response details to the orchestrator.
type GitHubCheckProvider struct{ executor Executor }

func NewGitHubCheckProvider(executor Executor) *GitHubCheckProvider {
	return &GitHubCheckProvider{executor: executor}
}
func (p *GitHubCheckProvider) Observe(ctx context.Context, identity, cursor string) (CheckObservation, error) {
	if p == nil || p.executor == nil {
		return CheckObservation{}, errors.New("CI provider requires an executor")
	}
	out, err := p.executor.Execute(ctx, []string{"pr", "checks", strings.TrimSpace(identity), "--json", "name,state,bucket,link"})
	if err != nil {
		return CheckObservation{}, err
	}
	checks, err := parseChecks(out)
	if err != nil {
		return CheckObservation{}, err
	}
	result := CheckObservation{Cursor: cursor}
	for _, check := range checks {
		switch strings.ToLower(strings.TrimSpace(check.Bucket)) {
		case "fail", "error", "cancel":
			result.Failed = append(result.Failed, check.Name)
		case "pending", "skipping":
			result.Pending = true
		}
	}
	return result, nil
}

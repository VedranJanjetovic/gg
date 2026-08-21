package pr

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/VedranJanjetovic/gg/internal/gh"
	"strings"
)

type GitHubStateProvider struct{ executor gh.CommandExecutor }

func NewGitHubStateProvider(executor gh.CommandExecutor) *GitHubStateProvider {
	return &GitHubStateProvider{executor: executor}
}
func (p *GitHubStateProvider) Observe(ctx context.Context, identity, cursor string) (PullRequestObservation, error) {
	if err := ctx.Err(); err != nil {
		return PullRequestObservation{}, err
	}
	if p == nil || p.executor == nil {
		return PullRequestObservation{}, errors.New("PR provider requires an executor")
	}
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return PullRequestObservation{}, errors.New("pull request identity is required")
	}
	out, err := p.executor.Execute(ctx, gh.Command{Dir: ".", Name: "gh", Args: []string{"pr", "view", identity, "--json", "url,state,mergeable,updatedAt"}})
	if err != nil {
		return PullRequestObservation{}, err
	}
	var v struct {
		URL       string `json:"url"`
		State     string `json:"state"`
		Mergeable string `json:"mergeable"`
		UpdatedAt string `json:"updatedAt"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return PullRequestObservation{}, err
	}
	state := MergeState(strings.ToLower(v.State))
	if state != MergeStateOpen && state != MergeStateMerged && state != MergeStateClosed {
		return PullRequestObservation{}, errors.New("unsupported pull request state")
	}
	return PullRequestObservation{URL: v.URL, State: state, MergeConflict: strings.EqualFold(v.Mergeable, "conflicting"), Cursor: v.UpdatedAt}, nil
}

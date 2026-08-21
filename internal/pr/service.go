// Package pr prepares and creates pull requests from verified QA evidence.
package pr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/gh"
	"github.com/VedranJanjetovic/gg/internal/proof"
)

type Git interface {
	PushBranchToRemote(context.Context, string, string, string) error
	IsUncommittedNewFile(context.Context, string, string) (bool, error)
}
type PullRequestCreator interface {
	CreatePullRequest(context.Context, string, string, string, string, string) (string, error)
}

type Request struct {
	GitOps   config.GitOpsConfig
	Worktree string
	Remote   string
	Branch   string
	Title    string
	Why      string
	What     string
	Push     bool
	// ProofRequired states whether a QA proof artifact must exist. The
	// orchestrator sets it from the executable pipeline: with the QA phase
	// disabled no proof is ever produced, so the PR body omits the
	// validation summary instead of failing.
	ProofRequired bool
}

type Result struct {
	Skipped bool
	Created bool
	URL     string
	Body    string
}
type Service struct {
	git    Git
	github PullRequestCreator
}

func NewService(gitClient Git, githubClient PullRequestCreator) *Service {
	return &Service{git: gitClient, github: githubClient}
}
func NewGitHubService(gitClient Git, executor gh.CommandExecutor) *Service {
	return NewService(gitClient, gh.NewClient(executor))
}

var conventionalTitle = regexp.MustCompile(`^(feat|fix|chore|docs|style|refactor|perf|test|build|ci|revert)(\([^)]+\))?(!)?:\s+\S`)

func ValidateTitle(title string) error {
	if !conventionalTitle.MatchString(strings.TrimSpace(title)) {
		return errors.New("pull request title must use a conventional prefix (feat, fix, chore, docs, style, refactor, perf, test, build, ci, or revert)")
	}
	return nil
}

func (s *Service) Create(ctx context.Context, request Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if !request.GitOps.EnablePR {
		return Result{Skipped: true}, nil
	}
	if s == nil || s.git == nil || s.github == nil {
		return Result{}, errors.New("PR service requires git and GitHub clients")
	}
	if err := ValidateTitle(request.Title); err != nil {
		return Result{}, err
	}
	worktree := strings.TrimSpace(request.Worktree)
	if worktree == "" || filepath.Clean(worktree) != worktree || !filepath.IsAbs(worktree) {
		return Result{}, errors.New("PR worktree must be an absolute path")
	}
	if strings.TrimSpace(request.Branch) == "" || strings.TrimSpace(request.Remote) == "" || strings.TrimSpace(request.GitOps.ParentBranch) == "" {
		return Result{}, errors.New("PR request requires branch, remote, and parent branch")
	}
	proofPath := filepath.Join(worktree, proof.ArtifactName)
	var parsed *proof.Proof
	data, err := os.ReadFile(proofPath)
	switch {
	case err == nil:
		if ok, checkErr := s.git.IsUncommittedNewFile(ctx, worktree, proof.ArtifactName); checkErr != nil {
			return Result{}, fmt.Errorf("verify proof artifact: %w", checkErr)
		} else if !ok {
			return Result{}, errors.New("proof artifact must be an existing uncommitted PROOF.md file")
		}
		p, parseErr := proof.Parse(data)
		if parseErr != nil {
			return Result{}, fmt.Errorf("validate proof artifact: %w", parseErr)
		}
		parsed = &p
	case request.ProofRequired:
		return Result{}, fmt.Errorf("read proof artifact: %w", err)
	default:
		// The QA phase is disabled: no proof exists and none is expected.
	}
	body := formatBody(request.Why, request.What, parsed)
	if request.Push {
		if err := s.git.PushBranchToRemote(ctx, worktree, request.Remote, request.Branch); err != nil {
			return Result{}, err
		}
	}
	url, err := s.github.CreatePullRequest(ctx, worktree, strings.TrimSpace(request.Title), body, request.GitOps.ParentBranch, request.Branch)
	if err != nil {
		return Result{}, err
	}
	return Result{Created: true, URL: url, Body: body}, nil
}

func formatBody(why, what string, p *proof.Proof) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Why\n%s\n\n# What\n%s\n\n# Validation\n", strings.TrimSpace(why), strings.TrimSpace(what))
	if p == nil {
		b.WriteString("- PROOF.md: not produced (QA phase disabled)\n")
		return b.String()
	}
	fmt.Fprintf(&b, "- PROOF.md: %s\n- Validations: %d\n", p.Classify(), len(p.Validations))
	for _, validation := range p.Validations {
		fmt.Fprintf(&b, "- %s: %s\n", validation.TestName, validation.Status)
	}
	return b.String()
}

// MergeState is the minimal pull-request lifecycle needed by project
// completion. Open requests remain observable; only merged requests complete.
type MergeState string

const (
	MergeStateOpen   MergeState = "open"
	MergeStateMerged MergeState = "merged"
	MergeStateClosed MergeState = "closed"
)

// PullRequestObservation is intentionally smaller than a provider response.
type PullRequestObservation struct {
	URL           string
	State         MergeState
	MergeConflict bool
	Cursor        string
}

// StateProvider is the narrow provider boundary used by lifecycle monitoring.
// Comments are intentionally not represented.
type StateProvider interface {
	Observe(context.Context, string, string) (PullRequestObservation, error)
}

// Monitor observes an existing pull request. It does not own comments or
// remediation; later phases decide how to act on conflicts and failed checks.
type Monitor interface {
	Monitor(context.Context, string) (PullRequestObservation, error)
}

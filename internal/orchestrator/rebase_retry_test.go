package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/git"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

type retryRebaser struct {
	fetches    int
	rebases    int
	restores   int
	verifies   int
	requests   []git.RebaseRequest
	checkpoint git.RebaseCheckpoint
	fail       bool
}

func (r *retryRebaser) FetchParent(context.Context, string) (git.FetchResult, error) {
	r.fetches++
	return git.FetchResult{}, nil
}

func (r *retryRebaser) RebaseProject(_ context.Context, request git.RebaseRequest) (git.RebaseResult, error) {
	r.rebases++
	r.requests = append(r.requests, request)
	if r.fail {
		return git.RebaseResult{Branch: "feature", BaseRef: "origin/main"}, errors.New("rebase failed")
	}
	if r.rebases < 3 {
		return git.RebaseResult{
			Branch: "feature", BaseRef: "origin/main",
			Conflict: &git.ConflictEvidence{Paths: []string{"README.md"}, Output: "conflict"},
		}, git.ErrRebaseConflict
	}
	return git.RebaseResult{Branch: "feature", BaseRef: "origin/main", Output: "rebased"}, nil
}

func (r *retryRebaser) CaptureRebaseCheckpoint(context.Context, string) (git.RebaseCheckpoint, error) {
	r.checkpoint = git.RebaseCheckpoint{WorktreePath: "/worktree", Branch: "feature", Head: "0123456789abcdef"}
	return r.checkpoint, nil
}

func (r *retryRebaser) RestoreRebaseCheckpoint(context.Context, git.RebaseCheckpoint) error {
	r.restores++
	return nil
}

func (r *retryRebaser) AbortRebaseIfActive(context.Context, string) error { return nil }

func (r *retryRebaser) VerifyRebaseCheckpoint(context.Context, git.RebaseCheckpoint) error {
	return nil
}

func (r *retryRebaser) VerifyRebaseWorktree(context.Context, string) error {
	r.verifies++
	return nil
}

type retryRebasePrompt struct{}

func (retryRebasePrompt) BuildPrompt(input agent.PromptInput) (string, error) {
	return "resolve conflicts and run focused checks", nil
}

type retryRebaseAgent struct {
	prompts []string
}

func (a *retryRebaseAgent) Run(_ context.Context, request agent.RunRequest) (agent.RunResult, error) {
	a.prompts = append(a.prompts, request.Prompt)
	if len(a.prompts) == 1 {
		return agent.RunResult{Phase: pipeline.PhaseRebase, Status: state.StatusFailed}, errors.New("agent could not resolve conflict")
	}
	return agent.RunResult{Phase: pipeline.PhaseRebase, Status: state.StatusFinished, Disposition: agent.DispositionPassed}, nil
}

func TestRunRebaseRetriesConflictWithFreshAgentAndPriorEvidence(t *testing.T) {
	rebaser := &retryRebaser{}
	retryAgent := &retryRebaseAgent{}
	controller := &sequentialController{rebaser: rebaser, rebaseAgent: retryAgent, prompts: retryRebasePrompt{}}
	request := Request{
		Project:        state.ProjectState{Slug: "demo", WorktreePath: "/worktree", BranchName: "feature", AcceptanceCriteria: []string{"preserve the feature"}},
		PhaseContracts: map[pipeline.PhaseID]string{pipeline.PhaseRebase: "rebase contract"},
		GitOps:         config.GitOpsConfig{ParentBranch: "main", BaseRef: "stale/base"},
		RunID:          "run-1",
		ArtifactRoot:   t.TempDir(),
	}
	result, err := controller.runRebase(context.Background(), request, config.AgentSettings{Agent: config.AgentClaude, Model: "test", Effort: config.EffortLow})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != state.StatusFinished || rebaser.fetches != 2 || rebaser.rebases != 2 || rebaser.restores != 2 || rebaser.verifies != 1 {
		t.Fatalf("result=%#v fetches=%d rebases=%d restores=%d verifies=%d", result, rebaser.fetches, rebaser.rebases, rebaser.restores, rebaser.verifies)
	}
	if len(retryAgent.prompts) != 2 || !strings.Contains(retryAgent.prompts[1], "agent could not resolve conflict") {
		t.Fatalf("fresh agent evidence = %#v", retryAgent.prompts)
	}
	for _, rebaseRequest := range rebaser.requests {
		if rebaseRequest.BaseRef != "origin/main" || rebaseRequest.ParentBranch != "main" {
			t.Fatalf("Rebase target = %#v, want origin/main from parent branch", rebaseRequest)
		}
	}
}

func TestRunRebaseUsesThreeAttemptsForOrdinaryFailuresAndRestoresFinalCheckpoint(t *testing.T) {
	rebaser := &retryRebaser{fail: true}
	controller := &sequentialController{rebaser: rebaser, prompts: retryRebasePrompt{}}
	request := Request{
		Project: state.ProjectState{Slug: "demo", WorktreePath: "/worktree", BranchName: "feature"},
		GitOps:  config.GitOpsConfig{ParentBranch: "main", BaseRef: "origin/ignored"},
	}
	_, err := controller.runRebase(context.Background(), request, config.AgentSettings{})
	if err == nil || !strings.Contains(err.Error(), "3 attempts") {
		t.Fatalf("error = %v, want exhausted retry error", err)
	}
	if rebaser.fetches != MaxRebaseAttempts || rebaser.rebases != MaxRebaseAttempts || rebaser.restores != MaxRebaseAttempts+1 {
		t.Fatalf("attempt counts fetch=%d rebase=%d restore=%d", rebaser.fetches, rebaser.rebases, rebaser.restores)
	}
}

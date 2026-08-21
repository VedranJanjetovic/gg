package pipeline_test

import (
	"context"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
)

type captureExecutor struct{ requests []agent.ExecutionRequest }

var _ agent.Executor = (*captureExecutor)(nil)

func (e *captureExecutor) Execute(_ context.Context, request agent.ExecutionRequest) error {
	e.requests = append(e.requests, request)
	return nil
}

func TestRunForwardsPersistedWorktreeToAgentExecution(t *testing.T) {
	executor := &captureExecutor{}
	worktree := t.TempDir()
	enabled := true
	service := pipeline.NewService(pipeline.WithExecutor(executor))
	request := pipeline.RunRequest{WorktreePath: worktree, Config: config.ResolvedConfig{Phases: map[config.Phase]config.ResolvedPhase{
		config.PhaseQA: {Enabled: enabled, AgentSettings: config.AgentSettings{Agent: config.AgentCodex, Model: "gpt", Effort: config.EffortHigh}},
	}}}
	if err := service.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(executor.requests) != 1 {
		t.Fatalf("execution count = %d, want 1", len(executor.requests))
	}
	got := executor.requests[0]
	if got.WorkingDirectory != worktree {
		t.Fatalf("cwd = %q, want persisted worktree %q", got.WorkingDirectory, worktree)
	}
	if got.Agent.Name != "codex" || got.Phase != string(pipeline.PhaseQA) {
		t.Fatalf("request = %#v", got)
	}
}

func TestRunNeverFallsBackToMainRepositoryWhenWorktreeMissing(t *testing.T) {
	executor := &captureExecutor{}
	service := pipeline.NewService(pipeline.WithExecutor(executor))
	err := service.Run(context.Background(), pipeline.RunRequest{Config: config.ResolvedConfig{Phases: map[config.Phase]config.ResolvedPhase{
		config.PhaseQA: {Enabled: true, AgentSettings: config.AgentSettings{Agent: config.AgentClaude}},
	}}})
	if err == nil || !strings.Contains(err.Error(), "working directory is required") {
		t.Fatalf("error = %v", err)
	}
	if len(executor.requests) != 0 {
		t.Fatal("executor received request without a persisted worktree")
	}
}

func TestRunKeepsNoopCompatibilityWithoutExecutor(t *testing.T) {
	if err := pipeline.NewService().Run(context.Background(), pipeline.RunRequest{}); err != nil {
		t.Fatal(err)
	}
}

func TestRunRejectsInvalidPersistedWorktreeBeforeExecution(t *testing.T) {
	executor := &captureExecutor{}
	service := pipeline.NewService(pipeline.WithExecutor(executor))
	err := service.Run(context.Background(), pipeline.RunRequest{WorktreePath: "/path/that/does/not/exist", Config: config.ResolvedConfig{Phases: map[config.Phase]config.ResolvedPhase{
		config.PhaseQA: {Enabled: true, AgentSettings: config.AgentSettings{Agent: config.AgentClaude}},
	}}})
	if err == nil || !strings.Contains(err.Error(), "validate pipeline worktree") {
		t.Fatalf("error = %v", err)
	}
	if len(executor.requests) != 0 {
		t.Fatal("executor received invalid worktree")
	}
}

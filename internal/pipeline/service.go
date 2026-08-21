// Package pipeline owns gg workflow lifecycle operations.
package pipeline

import (
	"context"
	"fmt"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/execution"
)

// RunRequest captures arguments, effective configuration, and the persisted
// project worktree for starting a workflow.
type RunRequest struct {
	Args         []string
	Config       config.ResolvedConfig
	WorktreePath string
}

type StopRequest struct{ Args []string }

// Service coordinates workflow lifecycle operations.
type Service struct{ executor execution.Executor }

// ServiceOption customizes execution dependencies.
type ServiceOption func(*Service)

// WithExecutor supplies the future Claude/Codex execution boundary.
func WithExecutor(executor execution.Executor) ServiceOption {
	return func(service *Service) { service.executor = executor }
}

// NewService constructs a pipeline service. With no executor, Run retains its
// existing no-op behavior because process spawning is not implemented yet.
func NewService(options ...ServiceOption) *Service {
	service := &Service{}
	for _, option := range options {
		option(service)
	}
	return service
}

// Run starts a workflow and forwards each configured agent phase through the
// validated execution boundary.
func (s *Service) Run(ctx context.Context, request RunRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.executor == nil {
		return nil
	}
	workingDirectory, err := execution.ValidateWorkingDirectory(request.WorktreePath)
	if err != nil {
		return fmt.Errorf("validate pipeline worktree: %w", err)
	}
	for _, phase := range DefaultPipeline().Phases() {
		if !phase.Metadata().Optional {
			continue
		}
		settings, ok := request.Config.Phases[config.Phase(phase.ID())]
		if !ok || !settings.Enabled {
			continue
		}
		executionRequest := execution.ExecutionRequest{
			Agent: execution.Agent{Name: string(settings.Agent)}, Phase: string(phase.ID()),
			Model: settings.Model, Effort: string(settings.Effort), WorkingDirectory: workingDirectory,
		}
		if err := executionRequest.Validate(); err != nil {
			return fmt.Errorf("prepare phase %q execution: %w", phase.ID(), err)
		}
		if err := s.executor.Execute(ctx, executionRequest); err != nil {
			return fmt.Errorf("execute phase %q: %w", phase.ID(), err)
		}
	}
	return nil
}

func (s *Service) Stop(ctx context.Context, request StopRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_ = request
	return nil
}
func (s *Service) Prune(ctx context.Context) error { return ctx.Err() }

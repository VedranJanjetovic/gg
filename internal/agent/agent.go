// Package agent contains domain types for gg-managed agents.
package agent

import (
	"context"

	"github.com/VedranJanjetovic/gg/internal/execution"
)

// These aliases preserve the agent package compatibility surface while the
// contract lives in a package that pipeline can consume without an import
// cycle.
type Agent = execution.Agent
type ExecutionRequest = execution.ExecutionRequest
type Executor = execution.Executor

// NoopExecutor preserves current behavior while process spawning is absent.
type NoopExecutor struct{}

func (NoopExecutor) Execute(context.Context, ExecutionRequest) error { return nil }

// ValidateWorkingDirectory preserves the agent package validation API.
func ValidateWorkingDirectory(path string) (string, error) {
	return execution.ValidateWorkingDirectory(path)
}

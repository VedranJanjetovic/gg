// Package execution owns the narrow process-execution contract shared by the
// pipeline coordinator and agent implementations.
package execution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Agent identifies an agent process or profile.
type Agent struct{ Name string }

// ExecutionRequest is the process boundary for one agent invocation.
type ExecutionRequest struct {
	Agent            Agent
	Phase            string
	Model            string
	Effort           string
	WorkingDirectory string
}

// Executor starts one agent process.
type Executor interface {
	Execute(context.Context, ExecutionRequest) error
}

// ValidateWorkingDirectory returns an absolute existing directory suitable for
// use as a process cwd. It deliberately does not fall back to the caller cwd.
func ValidateWorkingDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("agent working directory is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve agent working directory %q: %w", path, err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("validate agent working directory %q: %w", absolute, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("validate agent working directory %q: path is not a directory", absolute)
	}
	return filepath.Clean(absolute), nil
}

// Validate checks the required execution boundary fields without spawning.
func (r ExecutionRequest) Validate() error {
	if strings.TrimSpace(r.Agent.Name) == "" {
		return errors.New("agent name is required")
	}
	if strings.TrimSpace(r.Phase) == "" {
		return errors.New("agent phase is required")
	}
	_, err := ValidateWorkingDirectory(r.WorkingDirectory)
	return err
}

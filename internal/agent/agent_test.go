package agent_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/agent"
)

func TestExecutionRequestValidatesRequiredWorktree(t *testing.T) {
	root := t.TempDir()
	tests := []struct{ name, path, want string }{
		{name: "missing", want: "working directory is required"},
		{name: "invalid", path: filepath.Join(root, "missing"), want: "validate agent working directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (agent.ExecutionRequest{Agent: agent.Agent{Name: "claude"}, Phase: "qa", WorkingDirectory: tt.path}).Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateWorkingDirectoryReturnsCleanAbsoluteDirectory(t *testing.T) {
	root := t.TempDir()
	got, err := agent.ValidateWorkingDirectory(filepath.Join(root, "."))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs(root)
	if got != filepath.Clean(want) {
		t.Fatalf("directory = %q, want %q", got, want)
	}
}

var _ agent.Executor = captureExecutor{}

type captureExecutor struct{ requests []agent.ExecutionRequest }

func (e captureExecutor) Execute(context.Context, agent.ExecutionRequest) error { return nil }

package agent

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/VedranJanjetovic/gg/internal/config"
)

// LookPath resolves a CLI without executing it. It is injected so detection
// and command construction can be tested without depending on the host.
type LookPath func(string) (string, error)

// Provider builds a direct, shell-free process specification for one run.
type Provider interface {
	BuildSpec(RunRequest) (ProcessSpec, error)
}

// ClaudeProvider adapts config.AgentSettings to the Claude Code CLI.
type ClaudeProvider struct{ lookup LookPath }

// CodexProvider adapts config.AgentSettings to the Codex CLI.
type CodexProvider struct{ lookup LookPath }

// NewClaudeProvider constructs a Claude adapter. A nil lookup uses exec.LookPath.
func NewClaudeProvider(lookup LookPath) ClaudeProvider { return ClaudeProvider{lookup: lookup} }

// NewCodexProvider constructs a Codex adapter. A nil lookup uses exec.LookPath.
func NewCodexProvider(lookup LookPath) CodexProvider { return CodexProvider{lookup: lookup} }

// NewProvider selects the adapter for the configured agent.
func NewProvider(settings config.AgentSettings, lookup LookPath) (Provider, error) {
	switch settings.Agent {
	case config.AgentClaude:
		return NewClaudeProvider(lookup), nil
	case config.AgentCodex:
		return NewCodexProvider(lookup), nil
	default:
		return nil, fmt.Errorf("unsupported agent %q; configure agent as %q or %q", settings.Agent, config.AgentClaude, config.AgentCodex)
	}
}

// DetectCLI resolves the configured agent's executable and returns an
// actionable error when the CLI is unavailable.
func DetectCLI(agent config.Agent, lookup LookPath) (string, error) {
	if lookup == nil {
		lookup = exec.LookPath
	}
	name := string(agent)
	if agent != config.AgentClaude && agent != config.AgentCodex {
		return "", fmt.Errorf("unsupported agent %q; configure agent as %q or %q", agent, config.AgentClaude, config.AgentCodex)
	}
	path, err := lookup(name)
	if err == nil {
		return path, nil
	}
	install := "install Claude Code and ensure `claude` is on PATH"
	if agent == config.AgentCodex {
		install = "install Codex CLI and ensure `codex` is on PATH"
	}
	return "", fmt.Errorf("%s CLI %q was not found on PATH; %s: %w", agent, name, install, err)
}

func (p ClaudeProvider) BuildSpec(request RunRequest) (ProcessSpec, error) {
	path, err := DetectCLI(config.AgentClaude, p.lookup)
	if err != nil {
		return ProcessSpec{}, err
	}
	if err := validateRequest(request); err != nil {
		return ProcessSpec{}, err
	}
	// Headless --print runs cannot answer permission prompts: without the
	// bypass, file writes are silently denied and phases fail because their
	// canonical artifacts are never written. Runs are confined to a dedicated
	// worktree, so bypassing is the intended mode. JSON output carries the
	// result text plus token usage, which gg records per phase.
	args := []string{"--print", "--dangerously-skip-permissions", "--output-format", "json"}
	if request.Settings.Model != "" {
		args = append(args, "--model", request.Settings.Model)
	}
	if request.Settings.Effort != "" {
		args = append(args, "--effort", string(request.Settings.Effort))
	}
	args = append(args, request.Prompt)
	return inheritedSpec(path, args, request.WorkingDirectory), nil
}

func (p CodexProvider) BuildSpec(request RunRequest) (ProcessSpec, error) {
	path, err := DetectCLI(config.AgentCodex, p.lookup)
	if err != nil {
		return ProcessSpec{}, err
	}
	if err := validateRequest(request); err != nil {
		return ProcessSpec{}, err
	}
	// Non-interactive exec runs cannot answer approval prompts; the sandbox
	// bypass mirrors the Claude provider and is confined to the worktree.
	args := []string{"exec", "--dangerously-bypass-approvals-and-sandbox"}
	if request.Settings.Model != "" {
		args = append(args, "--model", request.Settings.Model)
	}
	if request.Settings.Effort != "" {
		args = append(args, "--config", "model_reasoning_effort="+string(request.Settings.Effort))
	}
	args = append(args, request.Prompt)
	return inheritedSpec(path, args, request.WorkingDirectory), nil
}

func inheritedSpec(command string, args []string, workingDirectory string) ProcessSpec {
	// A nil Env is intentional: the eventual exec.Cmd inherits the complete
	// parent environment. Providers do not replace or selectively reconstruct it.
	return ProcessSpec{Command: command, Args: args, WorkingDirectory: workingDirectory}
}

func validateRequest(request RunRequest) error {
	if strings.TrimSpace(request.WorkingDirectory) == "" {
		return errors.New("agent working directory is required")
	}
	if strings.TrimSpace(request.Prompt) == "" {
		return errors.New("agent prompt is required")
	}
	return nil
}

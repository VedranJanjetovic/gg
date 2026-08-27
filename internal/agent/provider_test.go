package agent

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/testdata/fakeagent"
)

func TestProviderBuildsSafeClaudeAndCodexArguments(t *testing.T) {
	tests := []struct {
		name     string
		settings config.AgentSettings
		wantArgs []string
	}{
		{
			name:     "claude",
			settings: config.AgentSettings{Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortHigh},
			wantArgs: []string{"--print", "--dangerously-skip-permissions", "--output-format", "json", "--model", "sonnet", "--effort", "high", "prompt; $(touch unsafe) with spaces"},
		},
		{
			name:     "codex",
			settings: config.AgentSettings{Agent: config.AgentCodex, Model: "o3", Effort: config.EffortLow},
			wantArgs: []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "--model", "o3", "--config", "model_reasoning_effort=low", "prompt; $(touch unsafe) with spaces"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := NewProvider(tt.settings, func(name string) (string, error) {
				return filepath.Join("/fake", name), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			spec, err := provider.BuildSpec(RunRequest{
				Settings:         tt.settings,
				Prompt:           tt.wantArgs[len(tt.wantArgs)-1],
				WorkingDirectory: t.TempDir(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(spec.Args, tt.wantArgs) {
				t.Fatalf("args = %#v, want %#v", spec.Args, tt.wantArgs)
			}
			if spec.Command != filepath.Join("/fake", string(tt.settings.Agent)) {
				t.Fatalf("command = %q", spec.Command)
			}
			if spec.Env != nil {
				t.Fatalf("Env = %#v, want nil inherited environment", spec.Env)
			}
		})
	}
}

func TestProviderUsesInheritedEnvironmentWithFakeExecutable(t *testing.T) {
	dir := t.TempDir()
	executable, err := fakeagent.Install(dir, "fake-agent", fakeagent.Spec{
		Stdout: "${ENV:GG_AGENT_TEST_VALUE}\n${ARG1}\n${ARG5}\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GG_AGENT_TEST_VALUE", "inherited")
	provider := NewClaudeProvider(func(string) (string, error) { return executable, nil })
	spec, err := provider.BuildSpec(RunRequest{
		Settings:         config.AgentSettings{Agent: config.AgentClaude},
		Prompt:           "safe prompt; not shell syntax",
		WorkingDirectory: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Dir = spec.WorkingDirectory
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 3 || lines[0] != "inherited" {
		t.Fatalf("fake executable output = %#v", lines)
	}
	if lines[2] != "safe prompt; not shell syntax" {
		t.Fatalf("prompt was not passed as one argv value: %#v", lines)
	}
}

func TestDetectCLIReportsInstallAction(t *testing.T) {
	missing := errors.New("not found")
	for _, agent := range []config.Agent{config.AgentClaude, config.AgentCodex} {
		t.Run(string(agent), func(t *testing.T) {
			_, err := DetectCLI(agent, func(string) (string, error) { return "", missing })
			if err == nil || !strings.Contains(err.Error(), string(agent)) || !strings.Contains(err.Error(), "install") || !errors.Is(err, missing) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestProviderRejectsMissingRunInputs(t *testing.T) {
	provider := NewClaudeProvider(func(string) (string, error) { return "/fake/claude", nil })
	for _, request := range []RunRequest{{Prompt: "prompt"}, {WorkingDirectory: t.TempDir()}} {
		if _, err := provider.BuildSpec(request); err == nil {
			t.Fatalf("BuildSpec(%+v) returned nil error", request)
		}
	}
}

func TestCatalogSourceIsProviderAwareWithoutRuntimeDiscovery(t *testing.T) {
	catalog, err := NewCatalogSource(func(string) (string, error) { return "", errors.New("should not be called") }).AgentCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := catalog.Lookup(config.AgentCodex)
	if !ok || entry.CLI != "codex" || entry.Provider != "openai" || entry.Harness != "codex-cli" {
		t.Fatalf("codex catalog entry = %#v, ok=%v", entry, ok)
	}
}

package config_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
)

func TestDefaultAgentCatalogSourceListsSupportedAgentsAndModels(t *testing.T) {
	t.Parallel()

	catalog, err := config.NewDefaultAgentCatalogSource().AgentCatalog(context.Background())
	if err != nil {
		t.Fatalf("AgentCatalog: %v", err)
	}
	want := map[config.Agent][]string{
		config.AgentClaude: {"fable", "opus", "sonnet", "haiku", "claude-fable-5", "claude-opus-4-8", "claude-opus-4-7", "claude-sonnet-5", "claude-sonnet-4-6", "claude-haiku-4-5"},
		config.AgentCodex:  {"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.1-codex-max", "gpt-5.1-codex-mini", "gpt-5.1", "gpt-5-codex", "gpt-5"},
	}
	for agent, models := range want {
		entry, ok := catalog.Lookup(agent)
		if !ok {
			t.Fatalf("Lookup(%q) did not find supported agent", agent)
		}
		if entry.ModelListStatus != config.ModelListAvailable || !reflect.DeepEqual(entry.Models, models) {
			t.Fatalf("Lookup(%q) = %#v, want available models %#v", agent, entry, models)
		}
	}
	if _, ok := catalog.Lookup(config.Agent("unknown")); ok {
		t.Fatal("unknown agent unexpectedly appeared in default catalog")
	}
}

func TestUnavailableAgentCatalogSourceIsExplicit(t *testing.T) {
	t.Parallel()

	catalog, err := config.NewUnavailableAgentCatalogSource().AgentCatalog(context.Background())
	if err != nil {
		t.Fatalf("AgentCatalog: %v", err)
	}
	for _, agent := range []config.Agent{config.AgentClaude, config.AgentCodex} {
		entry, ok := catalog.Lookup(agent)
		if !ok || entry.ModelListStatus != config.ModelListUnavailable || len(entry.Models) != 0 {
			t.Fatalf("Lookup(%q) = %#v, ok=%v, want unavailable empty list", agent, entry, ok)
		}
	}
}

func TestStaticCatalogDoesNotConstrainPersistedModelValues(t *testing.T) {
	t.Parallel()

	settings := config.AgentSettings{Agent: config.AgentClaude, Model: "user-installed-model", Effort: config.EffortMedium}
	catalog, err := config.NewDefaultAgentCatalogSource().AgentCatalog(context.Background())
	if err != nil {
		t.Fatalf("AgentCatalog: %v", err)
	}
	entry, _ := catalog.Lookup(settings.Agent)
	if _, found := slicesIndex(entry.Models, settings.Model); found {
		t.Fatalf("test model unexpectedly became a catalog model: %#v", entry.Models)
	}
	if settings.Model != "user-installed-model" {
		t.Fatalf("persisted model was changed: %q", settings.Model)
	}
}

func TestServiceUsesInjectedCatalogSource(t *testing.T) {
	t.Parallel()

	custom := config.NewStaticAgentCatalogSource(config.NewAgentCatalog(config.AgentCatalogEntry{
		Agent: config.Agent("custom"), Models: []string{"custom-model"}, ModelListStatus: config.ModelListAvailable,
	}))
	catalog, err := config.NewService(custom).AgentCatalog(context.Background())
	if err != nil {
		t.Fatalf("AgentCatalog: %v", err)
	}
	if _, ok := catalog.Lookup(config.Agent("custom")); !ok {
		t.Fatal("injected catalog was not used")
	}
	if _, ok := catalog.Lookup(config.AgentClaude); ok {
		t.Fatal("default catalog leaked into injected service")
	}
}

func TestStaticCatalogSourceHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := config.NewDefaultAgentCatalogSource().AgentCatalog(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("AgentCatalog error = %v, want context.Canceled", err)
	}
}

func slicesIndex(values []string, want string) (int, bool) {
	for i, value := range values {
		if value == want {
			return i, true
		}
	}
	return -1, false
}

func TestDefaultCatalogExposesAdapterIdentityAndDescriptions(t *testing.T) {
	catalog, err := config.NewDefaultAgentCatalogSource().AgentCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	claude, _ := catalog.Lookup(config.AgentClaude)
	if claude.DisplayName != "Claude Code" || claude.Harness != "claude-code" || claude.Provider != "anthropic" || claude.CLI != "claude" {
		t.Fatalf("claude metadata = %#v", claude)
	}
	if claude.ModelDescriptions["opus"] == "" {
		t.Fatal("opus description missing")
	}
	codex, _ := catalog.Lookup(config.AgentCodex)
	if codex.DisplayName != "Codex CLI" || codex.Harness != "codex-cli" || codex.Provider != "openai" || codex.CLI != "codex" {
		t.Fatalf("codex metadata = %#v", codex)
	}
}

func TestCatalogValidationDoesNotTurnPersistedModelMembershipIntoAConstraint(t *testing.T) {
	catalog, _ := config.NewDefaultAgentCatalogSource().AgentCatalog(context.Background())
	if err := catalog.ValidateSelection(config.AgentClaude, "sonnet"); err != nil {
		t.Fatal(err)
	}
	if err := catalog.ValidateSelection(config.AgentClaude, "provider-specific-model"); err == nil {
		t.Fatal("unknown new picker model accepted")
	}
	settings := config.AgentSettings{Agent: config.AgentClaude, Model: "provider-specific-model", Effort: config.EffortMedium}
	if err := config.ValidateAgentSettings(settings); err != nil {
		t.Fatalf("arbitrary persisted model rejected: %v", err)
	}
}

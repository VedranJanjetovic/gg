package config_test

import (
	"reflect"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
)

func TestAgentCatalogLookup(t *testing.T) {
	t.Parallel()

	catalog := config.NewAgentCatalog(
		config.AgentCatalogEntry{
			Agent:           config.AgentClaude,
			Models:          []string{"sonnet", "opus"},
			ModelListStatus: config.ModelListAvailable,
		},
		config.AgentCatalogEntry{
			Agent:           config.AgentCodex,
			ModelListStatus: config.ModelListUnavailable,
		},
	)

	tests := []struct {
		name  string
		agent config.Agent
		want  config.AgentCatalogEntry
		ok    bool
	}{
		{
			name: "known agent", agent: config.AgentClaude,
			want: config.AgentCatalogEntry{
				Agent: config.AgentClaude, Models: []string{"sonnet", "opus"}, ModelListStatus: config.ModelListAvailable,
			},
			ok: true,
		},
		{
			name: "known agent unavailable models", agent: config.AgentCodex,
			want: config.AgentCatalogEntry{Agent: config.AgentCodex, ModelListStatus: config.ModelListUnavailable},
			ok:   true,
		},
		{name: "unknown agent", agent: config.Agent("unknown"), ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := catalog.Lookup(tt.agent)
			if ok != tt.ok {
				t.Fatalf("Lookup(%q) ok = %v, want %v", tt.agent, ok, tt.ok)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Lookup(%q) = %#v, want %#v", tt.agent, got, tt.want)
			}
		})
	}
}

func TestAgentCatalogEmpty(t *testing.T) {
	t.Parallel()

	catalog := config.NewAgentCatalog()
	if got := catalog.Entries(); len(got) != 0 {
		t.Fatalf("Entries() length = %d, want 0", len(got))
	}
	if _, ok := catalog.Lookup(config.AgentClaude); ok {
		t.Fatal("Lookup on empty catalog unexpectedly found an agent")
	}
}

func TestAgentCatalogDoesNotLeakMutableState(t *testing.T) {
	t.Parallel()

	models := []string{"sonnet"}
	entry := config.AgentCatalogEntry{
		Agent: config.AgentClaude, Models: models, ModelListStatus: config.ModelListAvailable,
	}
	catalog := config.NewAgentCatalog(entry)

	models[0] = "mutated input"
	entry.Models[0] = "mutated entry"

	got, ok := catalog.Lookup(config.AgentClaude)
	if !ok || !reflect.DeepEqual(got.Models, []string{"sonnet"}) {
		t.Fatalf("catalog changed through constructor inputs: %#v", got.Models)
	}

	got.Models[0] = "mutated lookup"
	entries := catalog.Entries()
	entries[0].Models[0] = "mutated entries"

	got, ok = catalog.Lookup(config.AgentClaude)
	if !ok || !reflect.DeepEqual(got.Models, []string{"sonnet"}) {
		t.Fatalf("catalog changed through returned values: %#v", got.Models)
	}
}

package config

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ModelListAvailability describes whether an agent's selectable model list is
// known. An unavailable list is intentionally represented as empty: callers
// must not invent fallback models or inspect persisted configuration to build a
// selection catalog.
type ModelListAvailability string

const (
	// ModelListAvailable indicates that Models is the complete selectable list,
	// which may legitimately be empty.
	ModelListAvailable ModelListAvailability = "available"
	// ModelListUnavailable indicates that the model list could not be detected
	// or obtained. The agent remains selectable, but no model is selectable from
	// this catalog.
	ModelListUnavailable ModelListAvailability = "unavailable"
)

// AgentCatalogEntry describes one supported agent and its models for selection.
// It is separate from AgentSettings so arbitrary model values already present
// in persisted configuration remain compatible.
type AgentCatalogEntry struct {
	Agent             Agent
	DisplayName       string
	Description       string
	Harness           string
	Provider          string
	CLI               string
	Models            []string
	ModelDescriptions map[string]string
	ModelListStatus   ModelListAvailability
}

// AgentCatalog is the consumer-owned catalog of agents and agent-specific
// models used by configuration selection. It does not validate or persist
// AgentSettings.
type AgentCatalog struct {
	entries []AgentCatalogEntry
}

// AgentCatalogSource supplies the catalog used by a configuration boundary.
// Implementations may discover entries or provide a deterministic static
// catalog. The source does not validate persisted AgentSettings.
type AgentCatalogSource interface {
	AgentCatalog(context.Context) (AgentCatalog, error)
}

// StaticAgentCatalogSource is an immutable catalog source. It is useful while
// agent CLIs do not expose a supported model-listing interface.
type StaticAgentCatalogSource struct{ catalog AgentCatalog }

// NewStaticAgentCatalogSource constructs a source from an explicit catalog.
func NewStaticAgentCatalogSource(catalog AgentCatalog) StaticAgentCatalogSource {
	return StaticAgentCatalogSource{catalog: NewAgentCatalog(catalog.Entries()...)}
}

// AgentCatalog returns a copy of the configured static catalog.
func (source StaticAgentCatalogSource) AgentCatalog(ctx context.Context) (AgentCatalog, error) {
	if err := ctx.Err(); err != nil {
		return AgentCatalog{}, err
	}
	return NewAgentCatalog(source.catalog.Entries()...), nil
}

// NewDefaultAgentCatalogSource returns the deterministic catalog currently
// supported by gg. These names are the existing configuration values; this
// source intentionally does not claim to discover provider capabilities.
func NewDefaultAgentCatalogSource() AgentCatalogSource {
	return NewStaticAgentCatalogSource(NewAgentCatalog(
		AgentCatalogEntry{
			Agent: AgentClaude, DisplayName: "Claude Code", Description: "Anthropic's Claude Code harness", Harness: "claude-code", Provider: "anthropic", CLI: "claude",
			Models: []string{"fable", "opus", "sonnet", "haiku", "claude-fable-5", "claude-opus-4-8", "claude-opus-4-7", "claude-sonnet-5", "claude-sonnet-4-6", "claude-haiku-4-5"},
			ModelDescriptions: map[string]string{
				"fable":             "Latest Fable (currently Claude Fable 5) — most capable Claude model",
				"opus":              "Latest Opus (currently Claude Opus 4.8) — highest Opus-tier capability",
				"sonnet":            "Latest Sonnet (currently Claude Sonnet 5) — best speed/intelligence balance",
				"haiku":             "Latest Haiku (currently Claude Haiku 4.5) — fastest, most cost-effective",
				"claude-fable-5":    "Claude Fable 5 (pinned)",
				"claude-opus-4-8":   "Claude Opus 4.8 (pinned)",
				"claude-opus-4-7":   "Claude Opus 4.7 (pinned)",
				"claude-sonnet-5":   "Claude Sonnet 5 (pinned)",
				"claude-sonnet-4-6": "Claude Sonnet 4.6 (pinned)",
				"claude-haiku-4-5":  "Claude Haiku 4.5 (pinned)",
			},
			ModelListStatus: ModelListAvailable,
		},
		AgentCatalogEntry{
			Agent: AgentCodex, DisplayName: "Codex CLI", Description: "OpenAI's Codex command-line harness", Harness: "codex-cli", Provider: "openai", CLI: "codex",
			Models: []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.1-codex-max", "gpt-5.1-codex-mini", "gpt-5.1", "gpt-5-codex", "gpt-5"},
			ModelDescriptions: map[string]string{
				"gpt-5.6-sol":        "GPT-5.6 Sol — flagship tier",
				"gpt-5.6-terra":      "GPT-5.6 Terra — balanced intelligence and cost",
				"gpt-5.6-luna":       "GPT-5.6 Luna — fastest, most affordable tier",
				"gpt-5.1-codex-max":  "Flagship GPT-5.1 Codex agentic coding model",
				"gpt-5.1-codex-mini": "Smaller, faster GPT-5.1 Codex model",
				"gpt-5.1":            "General-purpose GPT-5.1 model",
				"gpt-5-codex":        "GPT-5 Codex coding model",
				"gpt-5":              "OpenAI GPT-5 coding model",
			},
			ModelListStatus: ModelListAvailable,
		},
	))
}

// NewUnavailableAgentCatalogSource returns a source for supported agents whose
// model lists are not available. Agents remain selectable, but callers must
// not manufacture model choices from persisted values or remote assumptions.
func NewUnavailableAgentCatalogSource(agents ...Agent) AgentCatalogSource {
	if len(agents) == 0 {
		agents = []Agent{AgentClaude, AgentCodex}
	}
	entries := make([]AgentCatalogEntry, len(agents))
	for i, agent := range agents {
		entries[i] = AgentCatalogEntry{Agent: agent, ModelListStatus: ModelListUnavailable}
	}
	return NewStaticAgentCatalogSource(NewAgentCatalog(entries...))
}

// NewAgentCatalog constructs a catalog from entries. The input and all values
// returned by the catalog are copied, so callers retain ownership of their
// slices and cannot mutate catalog state indirectly.
func NewAgentCatalog(entries ...AgentCatalogEntry) AgentCatalog {
	catalog := AgentCatalog{entries: make([]AgentCatalogEntry, len(entries))}
	for i, entry := range entries {
		catalog.entries[i] = cloneAgentCatalogEntry(entry)
	}
	return catalog
}

// Lookup returns the catalog entry for agent. The returned entry is a copy.
// For an unknown agent, ok is false. An empty Models slice with
// ModelListAvailable is a valid empty catalog; ModelListUnavailable is the
// deterministic result for an unavailable or undetectable model list.
func (catalog AgentCatalog) Lookup(agent Agent) (entry AgentCatalogEntry, ok bool) {
	for _, candidate := range catalog.entries {
		if candidate.Agent == agent {
			return cloneAgentCatalogEntry(candidate), true
		}
	}
	return AgentCatalogEntry{}, false
}

// Entries returns all catalog entries in construction order. Both the result
// and each entry's Models slice are copies.
func (catalog AgentCatalog) Entries() []AgentCatalogEntry {
	entries := make([]AgentCatalogEntry, len(catalog.entries))
	for i, entry := range catalog.entries {
		entries[i] = cloneAgentCatalogEntry(entry)
	}
	return entries
}

func cloneAgentCatalogEntry(entry AgentCatalogEntry) AgentCatalogEntry {
	entry.Models = append([]string(nil), entry.Models...)
	entry.ModelDescriptions = mapsClone(entry.ModelDescriptions)
	return entry
}

func mapsClone(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

// ValidateSelection validates a new picker result against this catalog. It is
// deliberately narrower than persisted-config validation: existing arbitrary
// provider model strings remain valid when configurations are reloaded.
func (catalog AgentCatalog) ValidateSelection(agent Agent, model string) error {
	entry, ok := catalog.Lookup(agent)
	if !ok {
		return fmt.Errorf("agent %q is not in the catalog", agent)
	}
	if strings.TrimSpace(model) == "" {
		return errors.New("selected model must be non-empty")
	}
	if entry.ModelListStatus == ModelListUnavailable {
		return fmt.Errorf("model list is unavailable for agent %q", agent)
	}
	for _, candidate := range entry.Models {
		if candidate == model {
			return nil
		}
	}
	return fmt.Errorf("model %q is not in the catalog for agent %q", model, agent)
}

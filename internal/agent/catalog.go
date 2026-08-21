package agent

import (
	"context"

	"github.com/VedranJanjetovic/gg/internal/config"
)

// StaticCatalogSource describes the capabilities of the shipped provider
// adapters. The adapters currently have no supported model-listing command, so
// this is deterministic metadata rather than runtime discovery.
type StaticCatalogSource struct {
	lookup LookPath
}

// NewCatalogSource returns the catalog for the real Claude Code and Codex CLI
// adapters. lookup is retained for a future capability seam; catalog loading
// itself does not execute or require either CLI so configure remains usable
// before an agent is installed.
func NewCatalogSource(lookup LookPath) config.AgentCatalogSource {
	return StaticCatalogSource{lookup: lookup}
}

func (StaticCatalogSource) AgentCatalog(ctx context.Context) (config.AgentCatalog, error) {
	// The adapter catalog is the same deterministic list config owns; delegating
	// keeps the two from drifting when supported models change.
	return config.NewDefaultAgentCatalogSource().AgentCatalog(ctx)
}

var _ config.AgentCatalogSource = StaticCatalogSource{}

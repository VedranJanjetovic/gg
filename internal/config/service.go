// Package config owns gg configuration workflows and persistence.
package config

import (
	"context"
	"errors"
	"os"
)

// RootResolver is the narrow configuration dependency needed by the
// application boundary to locate durable gg state. The full configuration
// workflow remains outside this phase.
type RootResolver interface {
	ConfiguredRoot(context.Context) (string, error)
}

// Service provides configuration operations for gg.
type Service struct {
	catalogSource AgentCatalogSource
}

// NewService constructs a configuration service.
func NewService(sources ...AgentCatalogSource) *Service {
	var source AgentCatalogSource
	if len(sources) > 0 {
		source = sources[0]
	}
	if source == nil {
		source = NewDefaultAgentCatalogSource()
	}
	return &Service{catalogSource: source}
}

// AgentCatalog returns the selectable agent/model catalog at the
// configuration boundary. The source is injected so discovery can be added
// without changing configuration consumers or introducing a global registry.
func (s *Service) AgentCatalog(ctx context.Context) (AgentCatalog, error) {
	return s.catalogSource.AgentCatalog(ctx)
}

// ConfiguredRoot returns the configured folder for gg state. Until the
// interactive/configured-folder workflow is implemented, the current working
// directory is the compatibility default. Tests and future config adapters can
// replace this service with a narrow RootResolver.
func (s *Service) ConfiguredRoot(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	root, err := os.Getwd()
	if err != nil {
		return "", errors.New("resolve current working directory: " + err.Error())
	}
	return root, nil
}

// ConfiguredRoots returns all configured state roots.
func (s *Service) ConfiguredRoots(ctx context.Context) ([]string, error) {
	root, err := s.ConfiguredRoot(ctx)
	if err != nil {
		return nil, err
	}
	return []string{root}, nil
}

// Configure will run the interactive configuration workflow once implemented.
func (s *Service) Configure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

var _ RootResolver = (*Service)(nil)

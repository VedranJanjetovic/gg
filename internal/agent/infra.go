package agent

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// InfrastructureKind names the failed layer of the agent runtime.
type InfrastructureKind string

const (
	InfrastructureAuth         InfrastructureKind = "authentication"
	InfrastructureBilling      InfrastructureKind = "billing"
	InfrastructureNetwork      InfrastructureKind = "network"
	InfrastructureInstallation InfrastructureKind = "installation"
)

// InfrastructureError marks a failure of the agent runtime itself — the
// network, authentication, billing, or the installed agent CLI — rather than
// of the assigned work. It is the only failure class that may close a run as
// failed; work failures are retried or park the run for a human decision.
type InfrastructureError struct {
	Kind   InfrastructureKind
	Detail string
}

func (e *InfrastructureError) Error() string {
	detail := strings.TrimSpace(e.Detail)
	if detail == "" {
		return fmt.Sprintf("agent infrastructure failure (%s)", e.Kind)
	}
	return fmt.Sprintf("agent infrastructure failure (%s): %s", e.Kind, detail)
}

// IsInfrastructureFailure reports whether the error tree contains an
// InfrastructureError.
func IsInfrastructureFailure(err error) bool {
	var infrastructure *InfrastructureError
	return errors.As(err, &infrastructure)
}

// infrastructurePatterns maps case-insensitive substrings of agent CLI error
// output to the infrastructure layer they identify. Rate limiting and service
// overload are deliberately absent: they are transient and retryable, not
// infrastructure failures.
var infrastructurePatterns = []struct {
	kind     InfrastructureKind
	patterns []string
}{
	{InfrastructureAuth, []string{
		"authentication",
		"unauthorized",
		"invalid api key",
		"invalid x-api-key",
		"api key not found",
		"not logged in",
		"please run /login",
		"credential",
		"oauth token",
	}},
	{InfrastructureBilling, []string{
		"credit balance",
		"insufficient credits",
		"billing",
		"quota exceeded",
		"usage limit reached",
		"out of tokens",
	}},
	{InfrastructureNetwork, []string{
		"connection refused",
		"connection reset",
		"dial tcp",
		"no such host",
		"network is unreachable",
		"etimedout",
		"econnrefused",
		"enotfound",
		"tls handshake",
		"temporary failure in name resolution",
		"getaddrinfo",
	}},
}

// classifyInfrastructureDetail matches agent CLI error output against the
// pattern table and returns a typed error for the first match, or nil when
// the output does not identify an infrastructure failure.
func classifyInfrastructureDetail(detail string) *InfrastructureError {
	normalized := strings.ToLower(detail)
	if strings.TrimSpace(normalized) == "" {
		return nil
	}
	for _, entry := range infrastructurePatterns {
		for _, pattern := range entry.patterns {
			if strings.Contains(normalized, pattern) {
				return &InfrastructureError{Kind: entry.kind, Detail: strings.TrimSpace(detail)}
			}
		}
	}
	return nil
}

// classifyStartFailure types a process launch error. A missing agent binary
// is an installation failure; other launch errors stay untyped.
func classifyStartFailure(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return errors.Join(err, &InfrastructureError{Kind: InfrastructureInstallation, Detail: err.Error()})
	}
	return err
}

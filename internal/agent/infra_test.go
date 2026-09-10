package agent

import (
	"errors"
	"fmt"
	"os/exec"
	"testing"
)

func TestClassifyInfrastructureDetailMatchesOnlyRuntimeFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		detail string
		kind   InfrastructureKind
		match  bool
	}{
		{name: "claude auth envelope", detail: "Authentication failed: please run /login", kind: InfrastructureAuth, match: true},
		{name: "invalid api key", detail: "invalid x-api-key", kind: InfrastructureAuth, match: true},
		{name: "credit balance", detail: "Your credit balance is too low to access the API", kind: InfrastructureBilling, match: true},
		{name: "usage limit", detail: "usage limit reached for this billing cycle", kind: InfrastructureBilling, match: true},
		{name: "dns failure", detail: "dial tcp: lookup api.anthropic.com: no such host", kind: InfrastructureNetwork, match: true},
		{name: "connection refused", detail: "connect ECONNREFUSED 127.0.0.1:443", kind: InfrastructureNetwork, match: true},
		{name: "rate limit is transient not infrastructure", detail: "429 rate limit exceeded, retry later", match: false},
		{name: "overloaded is transient not infrastructure", detail: "overloaded_error: the API is temporarily overloaded", match: false},
		{name: "ordinary work failure", detail: "tests failed: 3 assertions did not hold", match: false},
		{name: "empty", detail: "", match: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := classifyInfrastructureDetail(test.detail)
			if !test.match {
				if got != nil {
					t.Fatalf("classified %q as %s, want no infrastructure match", test.detail, got.Kind)
				}
				return
			}
			if got == nil {
				t.Fatalf("detail %q not classified, want %s", test.detail, test.kind)
			}
			if got.Kind != test.kind {
				t.Fatalf("kind = %s, want %s", got.Kind, test.kind)
			}
			if !IsInfrastructureFailure(got) {
				t.Fatal("IsInfrastructureFailure = false for a classified error")
			}
		})
	}
}

func TestClassifyStartFailureTypesMissingBinaryAsInstallation(t *testing.T) {
	missing := fmt.Errorf("start agent: %w", exec.ErrNotFound)
	err := classifyStartFailure(missing)
	var infrastructure *InfrastructureError
	if !errors.As(err, &infrastructure) || infrastructure.Kind != InfrastructureInstallation {
		t.Fatalf("missing binary not classified as installation failure: %v", err)
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatal("classification must preserve the original error identity")
	}

	other := errors.New("start agent: permission denied")
	if got := classifyStartFailure(other); got != other {
		t.Fatalf("unrelated start failure was rewrapped: %v", got)
	}
	if classifyStartFailure(nil) != nil {
		t.Fatal("nil start error must stay nil")
	}
}

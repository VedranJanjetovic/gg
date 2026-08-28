package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratedContractsMatchEveryDefaultPipelineSource(t *testing.T) {
	phases := DefaultPipeline().Phases()
	if len(canonicalPhaseContracts) != len(phases) {
		t.Fatalf("generated contract count = %d, want %d", len(canonicalPhaseContracts), len(phases))
	}

	expected := make(map[PhaseID]struct{}, len(phases))
	for _, phase := range phases {
		id := phase.ID()
		expected[id] = struct{}{}
		name := strings.ReplaceAll(string(id), "_", "-")
		source, err := os.ReadFile(filepath.Join("..", "..", "skills", "canonical", "gg-"+name, "gg-"+name+".md"))
		if err != nil {
			t.Fatalf("read source for %s: %v", id, err)
		}
		generated, ok := PhaseContract(id)
		if !ok {
			t.Fatalf("generated contract missing for %s", id)
		}
		if generated != string(source) {
			t.Fatalf("generated %s contract is out of sync", id)
		}
	}
	for id := range canonicalPhaseContracts {
		if _, ok := expected[id]; !ok {
			t.Fatalf("generated contract has unexpected phase %s", id)
		}
	}
}

func TestGeneratedQAContractContainsRequiredStrategies(t *testing.T) {
	contract, ok := PhaseContract(PhaseQA)
	if !ok {
		t.Fatal("QA contract missing")
	}
	for _, required := range []string{"Trivial-unit-test-only evidence is explicitly prohibited", "local API with real requests", "in-memory or mock equivalents", "browser/Puppeteer-style tests with mocked calls", "most exposed callable/CLI flow", "Status: pass|fail|feedback"} {
		if !contains(contract, required) {
			t.Fatalf("QA contract missing %q", required)
		}
	}
}

func contains(s, want string) bool {
	for i := 0; i+len(want) <= len(s); i++ {
		if s[i:i+len(want)] == want {
			return true
		}
	}
	return false
}

package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedQAContractMatchesCanonicalSource(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "skills", "canonical", "qa", "qa.md"))
	if err != nil {
		t.Fatal(err)
	}
	generated, ok := PhaseContract(PhaseQA)
	if !ok {
		t.Fatal("QA contract missing")
	}
	if generated != string(source) {
		t.Fatal("generated QA contract is out of sync with skills/qa/qa.md")
	}
}

func TestGeneratedPrePRContractsMatchCanonicalSources(t *testing.T) {
	tests := []struct {
		phase, directory, file string
	}{
		{string(PhaseDevelopment), "development", "development.md"},
		{string(PhaseQA), "qa", "qa.md"},
		{string(PhaseTestDocument), "test-document", "test-document.md"},
		{string(PhaseBuildChecker), "build-checker", "build-checker.md"},
	}
	for _, test := range tests {
		t.Run(test.directory, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("..", "..", "skills", "canonical", test.directory, test.file))
			if err != nil {
				t.Fatal(err)
			}
			generated, ok := PhaseContract(PhaseID(test.phase))
			if !ok || generated != string(source) {
				t.Fatalf("generated %s contract is out of sync", test.phase)
			}
		})
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

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
	"github.com/VedranJanjetovic/gg/testdata/fakeagent"
)

func writeDevelopmentArtifact(t *testing.T, root, frontmatter string) {
	t.Helper()
	dir := filepath.Join(root, ".gg")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\n" + frontmatter + "---\n\n# Development\n"
	if err := os.WriteFile(filepath.Join(dir, "development.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadFindingClosuresParsesTheStructuredContract(t *testing.T) {
	root := t.TempDir()
	writeDevelopmentArtifact(t, root, "gg_run_id: \"run-1\"\ngg_finding_closures: [{\"id\": \"refund-flow-500\", \"evidence\": \"go test ./internal/refund -run TestRefund500: PASS\", \"covered\": [\"create\", \"void\"]}, {\"id\": \"stale-cache\", \"evidence\": \"curl /health twice: second response was fresh\"}]\n")
	closures := readFindingClosures(root)
	if len(closures) != 2 {
		t.Fatalf("closures = %#v", closures)
	}
	if closures[0].ID != "refund-flow-500" || closures[0].Evidence != "go test ./internal/refund -run TestRefund500: PASS" {
		t.Fatalf("first closure = %#v", closures[0])
	}
	if len(closures[0].Covered) != 2 || closures[0].Covered[0] != "create" || closures[0].Covered[1] != "void" {
		t.Fatalf("first closure coverage = %#v", closures[0].Covered)
	}
	if closures[1].ID != "stale-cache" || len(closures[1].Covered) != 0 {
		t.Fatalf("second closure = %#v", closures[1])
	}
}

func TestReadFindingClosuresYieldsNilForMissingOrMalformedArtifacts(t *testing.T) {
	for _, test := range []struct {
		name        string
		frontmatter string
	}{
		{name: "key absent", frontmatter: "gg_run_id: \"run-1\"\n"},
		{name: "empty value", frontmatter: "gg_finding_closures:\n"},
		{name: "malformed JSON", frontmatter: "gg_finding_closures: [{\"id\": broken\n"},
		{name: "trailing JSON", frontmatter: "gg_finding_closures: [] []\n"},
		{name: "entries without ids", frontmatter: "gg_finding_closures: [{\"evidence\": \"no id\"}]\n"},
		{name: "repeated key", frontmatter: "gg_finding_closures: [{\"id\": \"a\"}]\ngg_finding_closures: [{\"id\": \"b\"}]\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeDevelopmentArtifact(t, root, test.frontmatter)
			if closures := readFindingClosures(root); closures != nil {
				t.Fatalf("closures = %#v, want nil", closures)
			}
		})
	}
	if closures := readFindingClosures(t.TempDir()); closures != nil {
		t.Fatalf("missing artifact produced closures %#v", closures)
	}
}

func TestCompleteFindingClosuresSatisfyTheContract(t *testing.T) {
	root := t.TempDir()
	writeDevelopmentArtifact(t, root, "gg_finding_closures: [{\"id\": \"refund-flow-500\", \"evidence\": \"go test ./internal/refund: PASS\", \"covered\": [\"create\"]}, {\"id\": \"stale-cache\", \"evidence\": \"curl /health twice: fresh\"}]\n")
	closures := readFindingClosures(root)
	if violations := findingClosureViolations([]string{"refund-flow-500", "stale-cache"}, closures); violations != "" {
		t.Fatalf("complete closures reported violations %q", violations)
	}
}

func TestFindingClosureMissingAnOpenFindingBreachesTheContract(t *testing.T) {
	root := t.TempDir()
	writeDevelopmentArtifact(t, root, "gg_finding_closures: [{\"id\": \"refund-flow-500\", \"evidence\": \"go test ./internal/refund: PASS\"}]\n")
	closures := readFindingClosures(root)
	violations := findingClosureViolations([]string{"refund-flow-500", "stale-cache"}, closures)
	if !strings.Contains(violations, "does not close open QA finding(s)") || !strings.Contains(violations, `"stale-cache"`) {
		t.Fatalf("violations = %q, want the missing open finding named", violations)
	}
	if strings.Contains(violations, "refund-flow-500") {
		t.Fatalf("violations = %q, want the evidenced finding left out", violations)
	}
}

func TestFindingClosureWithEmptyEvidenceBreachesTheContract(t *testing.T) {
	for _, test := range []struct {
		name        string
		frontmatter string
	}{
		{name: "absent evidence", frontmatter: "gg_finding_closures: [{\"id\": \"refund-flow-500\", \"covered\": [\"create\"]}]\n"},
		{name: "whitespace evidence", frontmatter: "gg_finding_closures: [{\"id\": \"refund-flow-500\", \"evidence\": \"   \"}]\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeDevelopmentArtifact(t, root, test.frontmatter)
			closures := readFindingClosures(root)
			violations := findingClosureViolations([]string{"refund-flow-500"}, closures)
			if !strings.Contains(violations, "declares no evidence for open QA finding(s)") || !strings.Contains(violations, `"refund-flow-500"`) {
				t.Fatalf("violations = %q, want the unevidenced finding named", violations)
			}
		})
	}
}

func TestFindingClosureContractAppearsOnlyForDevelopmentWithOpenFindings(t *testing.T) {
	base := PromptInput{
		Project:       state.ProjectState{OriginalGoal: "ship it", AcceptanceCriteria: []string{"tests pass"}},
		Phase:         pipeline.PhaseDevelopment,
		Subphase:      string(pipeline.DevelopmentSubphaseImplementation),
		PhaseContract: "implement the change", WorkingDirectory: "/tmp/worktree",
		RunID: "run/development/implementation/iteration-1",
	}

	withFindings := base
	withFindings.OpenQAFindings = []state.QAFindingStrike{
		{ID: "refund-flow-500", Summary: "refund returns 500", Strikes: 2},
	}
	got, err := BuildPrompt(withFindings)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## QA finding closure contract",
		"`gg_finding_closures`",
		`{"id": "<the exact open finding id>", "evidence": "<the command or test that now exercises it, and its observed result>", "covered": ["<each concrete thing now covered>", ...]}`,
		"Every open finding id listed below MUST appear in the array",
		"must NAME what proves the fix",
		"Passing tests alone are NOT evidence for a coverage-completeness finding",
		`- id "refund-flow-500" summary "refund returns 500"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("fix-pass prompt missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "## Adversarial closure verification") {
		t.Fatalf("implementation subphase prompt carries the verification-only section:\n%s", got)
	}

	without, err := BuildPrompt(base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(without, "gg_finding_closures") || strings.Contains(without, "## QA finding closure contract") {
		t.Fatalf("ordinary Development prompt declares the closure contract:\n%s", without)
	}

	qa := withFindings
	qa.Phase = pipeline.PhaseQA
	qa.Subphase = ""
	qa.Development = false
	qaPrompt, err := BuildPrompt(qa)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(qaPrompt, "gg_finding_closures") {
		t.Fatalf("QA prompt declares the Development closure contract:\n%s", qaPrompt)
	}
}

func TestVerificationFixSubphasePromptDemandsDisprovingTheClosureClaims(t *testing.T) {
	got, err := BuildPrompt(PromptInput{
		Project:       state.ProjectState{OriginalGoal: "ship it", AcceptanceCriteria: []string{"tests pass"}},
		Phase:         pipeline.PhaseDevelopment,
		Subphase:      string(pipeline.DevelopmentSubphaseVerification),
		PhaseContract: "verify the change", WorkingDirectory: "/tmp/worktree",
		RunID:          "run/development/verification/iteration-1",
		OpenQAFindings: []state.QAFindingStrike{{ID: "batch-ops-coverage", Summary: "only 5 of 6 operations covered"}},
		PriorFindingClosures: []FindingClosure{{
			ID:       "batch-ops-coverage",
			Evidence: "go test ./internal/batch: PASS",
			Covered:  []string{"create", "update", "delete", "list", "get"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## QA finding closure contract",
		"## Adversarial closure verification",
		"attempt to DISPROVE every closure claim",
		"A claim you cannot independently verify is a FAILURE, not a pass",
		`- id "batch-ops-coverage" claimed evidence "go test ./internal/batch: PASS" claimed coverage "create, update, delete, list, get"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("verification fix prompt missing %q:\n%s", want, got)
		}
	}
}

func TestVerificationFixSubphasePromptWithoutClaimsDemandsOwnEvidence(t *testing.T) {
	got, err := BuildPrompt(PromptInput{
		Project:       state.ProjectState{OriginalGoal: "ship it", AcceptanceCriteria: []string{"tests pass"}},
		Phase:         pipeline.PhaseDevelopment,
		Subphase:      string(pipeline.DevelopmentSubphaseVerification),
		PhaseContract: "verify the change", WorkingDirectory: "/tmp/worktree",
		RunID:          "run/development/verification/iteration-1",
		OpenQAFindings: []state.QAFindingStrike{{ID: "batch-ops-coverage"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "The implementation pass declared no closure claims") {
		t.Fatalf("verification fix prompt without claims missing the self-evidence instruction:\n%s", got)
	}
}

// developmentFixArtifact renders the canonical development artifact a fix pass
// writes, with the closure frontmatter appended to the passing protocol.
func developmentFixArtifact(runID, closures string) string {
	artifact := "---\ngg_run_id: \"" + runID + "\"\ngg_disposition: passed\n"
	if closures != "" {
		artifact += "gg_finding_closures: " + closures + "\n"
	}
	return artifact + "---\n\nfix pass\n"
}

func runDevelopmentFixPass(t *testing.T, artifact string, openFindingIDs []string) (RunResult, error) {
	t.Helper()
	worktree := t.TempDir()
	script := fakeRunner(t, fakeagent.Spec{Files: map[string]string{".gg/development.md": artifact}})
	runner := NewAgentRunner(AgentRunnerOptions{
		Factory: NewExecProcessFactory(nil, nil),
		Lookup:  func(string) (string, error) { return script, nil },
		LogRoot: t.TempDir(),
	})
	req := runnerRequest(runnerProject(worktree), worktree, "fix prompt")
	req.ArtifactPaths = nil
	req.OpenQAFindingIDs = openFindingIDs
	return runner.Run(context.Background(), req)
}

func TestFixPassDeclaringEveryClosurePassesAndSurfacesTheClaims(t *testing.T) {
	result, err := runDevelopmentFixPass(t,
		developmentFixArtifact("runner-test", `[{"id": "batch-ops-coverage", "evidence": "go test ./internal/batch: PASS", "covered": ["create", "purge"]}]`),
		[]string{"batch-ops-coverage"})
	if err != nil {
		t.Fatalf("Run() error = %v, want a complete closure contract to pass", err)
	}
	if result.Status != state.StatusFinished || result.Disposition != DispositionPassed {
		t.Fatalf("result = %#v, want a passing fix pass", result)
	}
	want := []FindingClosure{{
		ID: "batch-ops-coverage", Evidence: "go test ./internal/batch: PASS", Covered: []string{"create", "purge"},
	}}
	if !reflect.DeepEqual(result.FindingClosures, want) {
		t.Fatalf("result closures = %#v, want %#v", result.FindingClosures, want)
	}
}

func TestFixPassBreachingTheClosureContractFailsSemanticallyEvenOnExitZero(t *testing.T) {
	for _, test := range []struct {
		name     string
		closures string
		detail   string
	}{
		{name: "missing id", closures: `[{"id": "stale-cache", "evidence": "curl /health: fresh"}]`, detail: "does not close open QA finding(s)"},
		{name: "empty evidence", closures: `[{"id": "batch-ops-coverage", "evidence": ""}, {"id": "stale-cache", "evidence": "curl /health: fresh"}]`, detail: "declares no evidence for open QA finding(s)"},
		{name: "no closures at all", closures: "", detail: "does not close open QA finding(s)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := runDevelopmentFixPass(t,
				developmentFixArtifact("runner-test", test.closures),
				[]string{"batch-ops-coverage", "stale-cache"})
			var semantic *SemanticFailureError
			if !errors.As(err, &semantic) {
				t.Fatalf("Run() error = %v, want a semantic closure-contract failure", err)
			}
			if semantic.Phase != pipeline.PhaseDevelopment || semantic.Disposition != DispositionFailed {
				t.Fatalf("semantic failure = %#v", semantic)
			}
			if !strings.Contains(semantic.Details, test.detail) || !strings.Contains(semantic.Details, `"batch-ops-coverage"`) {
				t.Fatalf("semantic failure details = %q, want %q naming the open finding", semantic.Details, test.detail)
			}
			// The orchestrator retries only pure semantic failures, which is
			// what keeps a breach inside the fix budget instead of spending a
			// metered QA attempt.
			if !IsSemanticFailure(err) {
				t.Fatalf("Run() error = %v, a closure breach must stay retryable", err)
			}
			if result.Status != state.StatusFailed || result.Disposition != DispositionFailed {
				t.Fatalf("result = %#v, want a failed fix pass", result)
			}
		})
	}
}

func TestPassWithoutOpenFindingsIgnoresTheClosureContract(t *testing.T) {
	result, err := runDevelopmentFixPass(t, developmentFixArtifact("runner-test", ""), nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want an ordinary Development pass to ignore closures", err)
	}
	if result.Status != state.StatusFinished || result.FindingClosures != nil {
		t.Fatalf("result = %#v, want a passing pass with no closures read", result)
	}
}

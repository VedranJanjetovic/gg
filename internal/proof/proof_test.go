package proof

import (
	"strings"
	"testing"
)

const validProof = "# PROOF\n\n## Validation: API request\n- Status: pass\n- Test location: internal/api/handler_test.go\n- Test name: TestCreateWidgetThroughHTTP\n- Flow/scenario: create a widget through the local HTTP API\n- What it verifies: the request is validated and persisted\n- Proof it passed: `go test ./internal/api -run TestCreateWidgetThroughHTTP -count=1` exited 0\n- Manual run instructions: start the service and run the curl request from the test.\n"

const deferredProof = "# PROOF\n\n## Validation: AWS-backed flow\n- Status: deferred\n- Test location: internal/aws/handler_test.go\n- Test name: TestCreateWidgetAgainstAWS\n- Flow/scenario: create a widget through the deployed API\n- What it verifies: the deployed API persists and returns the created widget\n- Remote-only reason: the test requires AWS credentials and the deployed API endpoint\n- Repository evidence: internal/aws/handler_test.go configures AWS_ENDPOINT and the README documents the required credentials\n- Manual run instructions: run `go test ./internal/aws -run TestCreateWidgetAgainstAWS` in CI with the AWS secrets.\n"

func TestDeferredProofClassificationAndNormalization(t *testing.T) {
	parsed, err := Parse([]byte(deferredProof))
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Classify(); got != ClassificationPass {
		t.Fatalf("classification = %q, want passed", got)
	}
	checks := parsed.DeferredChecks()
	if len(checks) != 1 || checks[0].CheckName != "TestCreateWidgetAgainstAWS" || checks[0].RemoteOnlyReason == "" || checks[0].RepositoryEvidence == "" {
		t.Fatalf("deferred checks = %#v", checks)
	}
}

func TestDeferredProofValidationTable(t *testing.T) {
	tests := []struct {
		name, old, replacement, wantErr string
	}{
		{"missing location", "- Test location: internal/aws/handler_test.go", "- Test location:   ", "test location"},
		{"missing check name", "- Test name: TestCreateWidgetAgainstAWS", "- Test name:   ", "check name"},
		{"missing flow", "- Flow/scenario: create a widget through the deployed API", "- Flow/scenario:   ", "flow/scenario"},
		{"missing expected behavior", "- What it verifies: the deployed API persists and returns the created widget", "- What it verifies:   ", "expected behavior"},
		{"missing remote reason", "- Remote-only reason: the test requires AWS credentials and the deployed API endpoint", "- Remote-only reason:   ", "remote-only reason"},
		{"missing repository evidence", "- Repository evidence: internal/aws/handler_test.go configures AWS_ENDPOINT and the README documents the required credentials", "- Repository evidence:   ", "repository evidence"},
		{"missing run instructions", "- Manual run instructions: run `go test ./internal/aws -run TestCreateWidgetAgainstAWS` in CI with the AWS secrets.", "- Manual run instructions:   ", "run instructions"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(strings.Replace(deferredProof, test.old, test.replacement, 1)))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
		})
	}
	withPassClaim := strings.Replace(deferredProof, "- Remote-only reason:", "- Proof it passed: $ go test ./... exited 0\n- Remote-only reason:", 1)
	if _, err := Parse([]byte(withPassClaim)); err == nil || !strings.Contains(err.Error(), "must not include proof it passed") {
		t.Fatalf("deferred pass claim error = %v", err)
	}
}

func TestDeferredProofMixedAndAllDeferredCollectionsPass(t *testing.T) {
	allDeferred := deferredProof + strings.Replace(deferredProof, "## Validation: AWS-backed flow", "## Validation: second AWS-backed flow", 1)
	if got, err := ClassifyMarkdown([]byte(allDeferred)); err != nil || got != ClassificationPass {
		t.Fatalf("all-deferred classification = %q, error = %v", got, err)
	}
	mixed := validProof + "\n" + deferredProof
	if got, err := ClassifyMarkdown([]byte(mixed)); err != nil || got != ClassificationPass {
		t.Fatalf("mixed classification = %q, error = %v", got, err)
	}
	failedLocal := strings.Replace(deferredProof, "Status: deferred", "Status: fail", 1)
	if got, err := ClassifyMarkdown([]byte(failedLocal)); err == nil || got != ClassificationFail {
		t.Fatalf("local failure classification = %q, error = %v", got, err)
	}
}

func TestDeferredClassificationKeepsFeedbackAndFailurePrecedence(t *testing.T) {
	feedback := deferredProof + "\n## Feedback\nThe deferred flow needs a follow-up CI run.\n"
	if got, err := ClassifyMarkdown([]byte(feedback)); err != nil || got != ClassificationFeedback {
		t.Fatalf("deferred feedback classification = %q, error = %v", got, err)
	}

	failed := validProof + "\n" + deferredProof + "\n## Feedback\nA locally runnable check failed.\n"
	failed = strings.Replace(failed, "Status: pass", "Status: fail", 1)
	if got, err := ClassifyMarkdown([]byte(failed)); err != nil || got != ClassificationFail {
		t.Fatalf("local failure precedence classification = %q, error = %v", got, err)
	}
}

func TestDeferredValidationDoesNotRequirePassEvidence(t *testing.T) {
	withoutPassField := deferredProof
	if strings.Contains(withoutPassField, "Proof it passed") {
		t.Fatal("deferred fixture unexpectedly contains pass evidence")
	}
	parsed, err := Parse([]byte(withoutPassField))
	if err != nil || parsed.Validations[0].ProofItPassed != "" {
		t.Fatalf("deferred proof pass evidence = %q, error = %v", parsed.Validations[0].ProofItPassed, err)
	}
}

func TestParseAndClassifyTable(t *testing.T) {
	tests := []struct {
		name, input string
		want        Classification
		wantErr     string
	}{
		{"valid pass", validProof, ClassificationPass, ""},
		{"node command evidence accepted", strings.Replace(validProof, "`go test ./internal/api -run TestCreateWidgetThroughHTTP -count=1` exited 0", "`$ node qa-e2e.cjs` → `PASS S1 load & modules`", 1), ClassificationPass, ""},
		{"npx command evidence accepted", strings.Replace(validProof, "`go test ./internal/api -run TestCreateWidgetThroughHTTP -count=1` exited 0", "`npx vitest run` succeeded", 1), ClassificationPass, ""},
		{"any tool accepted with prompt marker", strings.Replace(validProof, "`go test ./internal/api -run TestCreateWidgetThroughHTTP -count=1` exited 0", "`$ zig build test` → all 12 tests passed", 1), ClassificationPass, ""},
		{"unknown tool accepted without prompt", strings.Replace(validProof, "`go test ./internal/api -run TestCreateWidgetThroughHTTP -count=1` exited 0", "`gleam test` exited 0", 1), ClassificationPass, ""},
		{"unknown runner with flag accepted", strings.Replace(validProof, "`go test ./internal/api -run TestCreateWidgetThroughHTTP -count=1` exited 0", "`bunx vitest --run` → 8 tests ok", 1), ClassificationPass, ""},
		{"lone word still rejected", strings.Replace(validProof, "`go test ./internal/api -run TestCreateWidgetThroughHTTP -count=1` exited 0", "`checked` result ok", 1), ClassificationFail, "command"},
		{"past failure narrative accepted when outcome passes", strings.Replace(validProof, "`go test ./internal/api -run TestCreateWidgetThroughHTTP -count=1` exited 0", "earlier runs the test failed on flaky input; `$ node qa-e2e.cjs` → PASS S12 after retry-budget fix", 1), ClassificationPass, ""},
		{"pure failed result still rejected for pass", strings.Replace(validProof, "`go test ./internal/api -run TestCreateWidgetThroughHTTP -count=1` exited 0", "`$ node qa-e2e.cjs` → the test failed with a thrown error", 1), ClassificationFail, "failed command result"},
		{"awk accepted with prompt marker", strings.Replace(validProof, "`go test ./internal/api -run TestCreateWidgetThroughHTTP -count=1` exited 0", "`$ awk '{ print length($0) }' js/*.js` → result: max line 96", 1), ClassificationPass, ""},
		{"prose without prompt still rejected", strings.Replace(validProof, "`go test ./internal/api -run TestCreateWidgetThroughHTTP -count=1` exited 0", "`verified manually` result ok", 1), ClassificationFail, "command"},
		{"feedback status", strings.Replace(validProof, "Status: pass", "Status: feedback", 1), ClassificationFeedback, ""},
		{"failure status", strings.Replace(validProof, "Status: pass", "Status: fail", 1), ClassificationFail, ""},
		{"malformed status", strings.Replace(validProof, "Status: pass", "Status: maybe", 1), ClassificationFail, "status"},
		{"command evidence required", strings.Replace(validProof, "`go test ./internal/api -run TestCreateWidgetThroughHTTP -count=1` exited 0", "the checks passed", 1), ClassificationFail, "command"},
		{"arbitrary inline code rejected", strings.Replace(validProof, "`go test ./internal/api -run TestCreateWidgetThroughHTTP -count=1` exited 0", "`not a command`", 1), ClassificationFail, "command"},
		{"empty inline code rejected", strings.Replace(validProof, "`go test ./internal/api -run TestCreateWidgetThroughHTTP -count=1` exited 0", "``", 1), ClassificationFail, "command"},
		{"failed status result rejected for pass", strings.Replace(validProof, "exited 0", "status: failed", 1), ClassificationFail, "failed command result"},
		{"failed result text rejected for pass", strings.Replace(validProof, "exited 0", "result: fail", 1), ClassificationFail, "failed command result"},
		{"exit code colon nonzero rejected for pass", strings.Replace(validProof, "exited 0", "exit code: 1", 1), ClassificationFail, "failed command result"},
		{"exit status colon nonzero rejected for pass", strings.Replace(validProof, "exited 0", "exit status: 1", 1), ClassificationFail, "failed command result"},
		{"exit code equals nonzero rejected for pass", strings.Replace(validProof, "exited 0", "exit code = 2", 1), ClassificationFail, "failed command result"},
		{"exit status equals nonzero rejected for pass", strings.Replace(validProof, "exited 0", "exit status = 3", 1), ClassificationFail, "failed command result"},
		{"result colon nonzero rejected for pass", strings.Replace(validProof, "exited 0", "result: 4", 1), ClassificationFail, "failed command result"},
		{"status equals nonzero rejected for pass", strings.Replace(validProof, "exited 0", "status = 5", 1), ClassificationFail, "failed command result"},
		{"nonzero rejected for pass", strings.Replace(validProof, "exited 0", "non-zero exit", 1), ClassificationFail, "failed command result"},
		{"failed command prose rejected for pass", strings.Replace(validProof, "exited 0", "$ go test ./...; result: failed", 1), ClassificationFail, "failed command result"},
		{"manual instructions required", strings.Replace(validProof, "Manual run instructions: start the service and run the curl request from the test.", "Manual run instructions:   ", 1), ClassificationFail, "manual run instructions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ClassifyMarkdown([]byte(tt.input))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if got != tt.want {
					t.Fatalf("classification = %q, want %q", got, tt.want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
			if got != ClassificationFail {
				t.Fatalf("classification = %q, want failed", got)
			}
		})
	}
}

func TestMissingOrBlankRequiredFields(t *testing.T) {
	fields := []string{"Status", "Test location", "Test name", "Flow/scenario", "What it verifies", "Proof it passed", "Manual run instructions"}
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			lines := strings.Split(validProof, "\n")
			for i, line := range lines {
				if strings.HasPrefix(line, "- "+field+":") {
					lines[i] = "- " + field + ":   "
				}
			}
			_, err := Parse([]byte(strings.Join(lines, "\n")))
			if err == nil {
				t.Fatalf("Parse() error = nil for blank %s", field)
			}
		})
	}
}

func TestMalformedMarkdownAndFailureFeedback(t *testing.T) {
	failure := strings.Replace(validProof, "Status: pass", "Status: fail", 1)
	parsed, err := Parse([]byte(failure))
	if err != nil || parsed.Classify() != ClassificationFail {
		t.Fatalf("parsed failure = %#v, error = %v", parsed, err)
	}
	feedback := validProof + "\n## Feedback\nAdd a browser-flow check before retrying.\n"
	parsed, err = Parse([]byte(feedback))
	if err != nil || parsed.Classify() != ClassificationFeedback {
		t.Fatalf("parsed feedback = %#v, error = %v", parsed, err)
	}
	_, err = Parse([]byte("## Validation: broken\nnot a field\n"))
	if err == nil {
		t.Fatal("malformed markdown accepted")
	}
}

func TestParseMultipleValidationsUsesFailureBeforeFeedback(t *testing.T) {
	input := validProof + "\n## Validation: second\n- Status: fail\n- Test location: flow_test.go\n- Test name: TestFailure\n- Flow/scenario: exercise the failing boundary\n- What it verifies: the failure remains visible\n- Proof it passed: `$ go test ./flow -run TestFailure -count=1` exited 1\n- Manual run instructions: run the command and inspect the failure.\n## Feedback\nFix the failing boundary before retrying.\n"
	parsed, err := Parse([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Classify(); got != ClassificationFail {
		t.Fatalf("classification = %q, want failed", got)
	}
	if len(parsed.Validations) != 2 {
		t.Fatalf("validations = %d, want 2", len(parsed.Validations))
	}
}

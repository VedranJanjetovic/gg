package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/proof"
	"github.com/VedranJanjetovic/gg/internal/state"
	"github.com/VedranJanjetovic/gg/testdata/fakeagent"
)

func TestAgentRunnerRejectsExitZeroWithoutCanonicalPhaseArtifact(t *testing.T) {
	worktree := t.TempDir()
	script := fakeRunner(t, fakeagent.Spec{
		Files: map[string]string{"phase-result.json": "{\"schemaVersion\":1,\"disposition\":\"passed\"}\n"},
	})
	project := runnerProject(worktree)
	req := runnerRequest(project, worktree, "planning prompt")
	req.Phase = pipeline.PhasePlanning
	req.Subphase = ""
	req.ArtifactPaths = nil

	runner := NewAgentRunner(AgentRunnerOptions{
		Factory: NewExecProcessFactory(nil, nil),
		Lookup:  func(string) (string, error) { return script, nil },
		LogRoot: t.TempDir(),
	})
	result, err := runner.Run(context.Background(), req)

	if err == nil {
		t.Fatalf("Run() succeeded without canonical %q", "plan.md")
	}
	if result.Status != state.StatusFailed {
		t.Fatalf("result status = %s, want failed for missing canonical artifact", result.Status)
	}
}

func TestAgentRunnerRejectsExitZeroQANonPassingDisposition(t *testing.T) {
	for _, disposition := range []string{"failed", "blocked"} {
		t.Run(disposition, func(t *testing.T) {
			worktree := t.TempDir()
			script := fakeRunner(t, fakeagent.Spec{
				Files: map[string]string{".gg/qa-report.md": runnerCanonical("runner-test", disposition, "QA evidence")},
			})
			project := runnerProject(worktree)
			req := runnerRequest(project, worktree, "qa prompt")
			req.Phase = pipeline.PhaseQA
			req.Subphase = ""
			req.ArtifactPaths = nil

			runner := NewAgentRunner(AgentRunnerOptions{
				Factory: NewExecProcessFactory(nil, nil),
				Lookup:  func(string) (string, error) { return script, nil },
				LogRoot: t.TempDir(),
			})
			result, err := runner.Run(context.Background(), req)

			if err == nil {
				t.Fatalf("Run() succeeded with QA disposition %q", disposition)
			}
			if result.Status != state.StatusFailed {
				t.Fatalf("result status = %s, want failed for QA disposition %q", result.Status, disposition)
			}
			if !IsSemanticFailure(err) {
				t.Fatalf("Run() error = %v, want pure semantic failure", err)
			}
			if _, statErr := os.Stat(filepath.Join(worktree, ".gg", "qa-report.md")); statErr != nil {
				t.Fatalf("canonical QA evidence missing: %v", statErr)
			}
		})
	}
}

type failingResultStore struct {
	err error
}

func (s failingResultStore) Save(context.Context, RunResult) error {
	return s.err
}

func TestAgentRunnerSemanticFailureJoinedWithPersistenceFailureIsOperational(t *testing.T) {
	worktree := t.TempDir()
	script := fakeRunner(t, fakeagent.Spec{
		Files: map[string]string{".gg/qa-report.md": runnerCanonical("runner-test", "failed", "QA evidence")},
	})
	project := runnerProject(worktree)
	req := runnerRequest(project, worktree, "qa prompt")
	req.Phase = pipeline.PhaseQA
	req.Subphase = ""
	req.ArtifactPaths = nil
	persistenceErr := errors.New("persist result")

	runner := NewAgentRunner(AgentRunnerOptions{
		Factory: NewExecProcessFactory(nil, nil),
		Lookup:  func(string) (string, error) { return script, nil },
		LogRoot: t.TempDir(),
		Results: failingResultStore{err: persistenceErr},
	})
	result, err := runner.Run(context.Background(), req)

	if result.Disposition != DispositionFailed || !errors.Is(err, persistenceErr) {
		t.Fatalf("Run() result=%#v error=%v, want joined persistence failure", result, err)
	}
	if IsSemanticFailure(err) {
		t.Fatalf("Run() error = %v, want operational failure after persistence error", err)
	}
}

func TestAgentRunnerRejectsMalformedOrStaleCanonicalFrontmatter(t *testing.T) {
	tests := map[string]string{
		"malformed": "---\ngg_run_id \"runner-test\"\ngg_disposition: passed\n---\n",
		"stale":     runnerCanonical("older-run", "passed", ""),
	}
	for name, report := range tests {
		t.Run(name, func(t *testing.T) {
			worktree := t.TempDir()
			script := fakeRunner(t, fakeagent.Spec{Files: map[string]string{".gg/qa-report.md": report}})
			project := runnerProject(worktree)
			req := runnerRequest(project, worktree, "qa prompt")
			req.Phase = pipeline.PhaseQA
			req.Subphase = ""
			req.ArtifactPaths = nil
			runner := NewAgentRunner(AgentRunnerOptions{
				Factory: NewExecProcessFactory(nil, nil),
				Lookup:  func(string) (string, error) { return script, nil },
				LogRoot: t.TempDir(),
			})
			result, err := runner.Run(context.Background(), req)
			if err == nil || result.Status != state.StatusFailed {
				t.Fatalf("Run() result=%#v error=%v, want protocol failure", result, err)
			}
		})
	}
}

type runnerProofChecker struct{}

func (runnerProofChecker) IsUncommittedNewFile(context.Context, string, string) (bool, error) {
	return true, nil
}

func TestAgentRunnerMapsMissingOrMalformedProofToTerminalFailure(t *testing.T) {
	tests := map[string]fakeagent.Spec{
		"missing":   {},
		"malformed": {Files: map[string]string{".gg/PROOF.md": "## Validation: incomplete\n- Status: pass\n"}},
	}
	for name, spec := range tests {
		t.Run(name, func(t *testing.T) {
			worktree := t.TempDir()
			script := fakeRunner(t, spec)
			runner := NewAgentRunner(AgentRunnerOptions{
				Factory: NewExecProcessFactory(nil, nil),
				Lookup:  func(string) (string, error) { return script, nil },
				LogRoot: t.TempDir(),
				Proof:   proofServiceForRunnerTest(t),
			})
			project := runnerProject(worktree)
			req := runnerRequest(project, worktree, "qa prompt")
			req.Phase = pipeline.PhaseQA
			req.Subphase = ""
			req.ArtifactPaths = nil

			result, err := runner.Run(context.Background(), req)
			if err == nil {
				t.Fatal("Run() succeeded without valid PROOF.md")
			}
			if result.Disposition != DispositionFailed || result.Status != state.StatusFailed {
				t.Fatalf("result=%+v, want terminal failed disposition", result)
			}
			if IsSemanticFailure(err) {
				t.Fatalf("error=%v, malformed/missing proof must not be retryable semantic feedback", err)
			}
		})
	}
}

func TestAgentRunnerRetainsFeedbackForValidSemanticQAProof(t *testing.T) {
	worktree := t.TempDir()
	script := fakeRunner(t, fakeagent.Spec{Files: map[string]string{".gg/PROOF.md": `---
gg_run_id: "runner-test"
---

# PROOF

## Validation: flow
- Status: feedback
- Test location: qa_test.go
- Test name: TestSemanticQA
- Flow/scenario: exercise the QA flow
- What it verifies: the semantic result remains actionable
- Proof it passed: $ go test ./... exited 0
- Manual run instructions: run the QA flow again after addressing the feedback.

## Feedback
The semantic QA result needs one more browser-flow assertion.
`}})
	runner := NewAgentRunner(AgentRunnerOptions{
		Factory: NewExecProcessFactory(nil, nil),
		Lookup:  func(string) (string, error) { return script, nil },
		LogRoot: t.TempDir(),
		Proof:   proofServiceForRunnerTest(t),
	})
	project := runnerProject(worktree)
	req := runnerRequest(project, worktree, "qa prompt")
	req.Phase = pipeline.PhaseQA
	req.Subphase = ""
	req.ArtifactPaths = nil

	result, err := runner.Run(context.Background(), req)
	if err == nil || result.Disposition != DispositionFeedback {
		t.Fatalf("result=%+v error=%v, want valid feedback disposition", result, err)
	}
	if !IsSemanticFailure(err) {
		t.Fatalf("error=%v, valid semantic feedback must remain retryable", err)
	}
}

func TestAgentRunnerAcceptsDeferredQAProofAndExposesNormalizedChecks(t *testing.T) {
	worktree := t.TempDir()
	script := fakeRunner(t, fakeagent.Spec{Files: map[string]string{".gg/PROOF.md": `---
gg_run_id: "runner-test"
---

# PROOF

## Validation: remote API
- Status: deferred
- Test location: internal/aws/handler_test.go
- Test name: TestCreateWidgetAgainstAWS
- Flow/scenario: create a widget through the deployed API
- What it verifies: the deployed API persists and returns the created widget
- Remote-only reason: the test requires AWS credentials and the deployed API endpoint
- Repository evidence: internal/aws/handler_test.go configures AWS_ENDPOINT and documents the required credentials
- Manual run instructions: run the test in CI with the AWS secrets.
`}})
	runner := NewAgentRunner(AgentRunnerOptions{
		Factory: NewExecProcessFactory(nil, nil), Lookup: func(string) (string, error) { return script, nil },
		LogRoot: t.TempDir(), Proof: proofServiceForRunnerTest(t),
	})
	project := runnerProject(worktree)
	req := runnerRequest(project, worktree, "qa prompt")
	req.Phase, req.Subphase, req.ArtifactPaths = pipeline.PhaseQA, "", nil
	result, err := runner.Run(context.Background(), req)
	if err != nil || result.Status != state.StatusFinished || result.Disposition != DispositionPassed {
		t.Fatalf("result=%+v error=%v, want passed deferred proof", result, err)
	}
	if len(result.DeferredChecks) != 1 || result.DeferredChecks[0].CheckName != "TestCreateWidgetAgainstAWS" {
		t.Fatalf("deferred checks = %#v", result.DeferredChecks)
	}
}

func TestAgentRunnerRejectsMalformedDeferredQAProofAsProtocolFailure(t *testing.T) {
	for _, test := range []struct {
		name, field string
	}{
		{name: "missing repository evidence", field: "- Repository evidence: "},
		{name: "forbidden pass claim", field: "- Proof it passed: $ go test ./... exited 0\n- Repository evidence:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			worktree := t.TempDir()
			proofText := `---
gg_run_id: "runner-test"
---

## Validation: remote API
- Status: deferred
- Test location: internal/aws/handler_test.go
- Test name: TestCreateWidgetAgainstAWS
- Flow/scenario: create a widget through the deployed API
- What it verifies: the deployed API persists and returns the created widget
- Remote-only reason: the test requires AWS credentials and the deployed API endpoint
- Repository evidence: internal/aws/handler_test.go configures AWS_ENDPOINT
- Manual run instructions: run the test in CI with the AWS secrets.
`
			proofText = strings.Replace(proofText, "- Repository evidence: internal/aws/handler_test.go configures AWS_ENDPOINT", test.field, 1)
			script := fakeRunner(t, fakeagent.Spec{Files: map[string]string{".gg/PROOF.md": proofText}})
			runner := NewAgentRunner(AgentRunnerOptions{
				Factory: NewExecProcessFactory(nil, nil), Lookup: func(string) (string, error) { return script, nil },
				LogRoot: t.TempDir(), Proof: proofServiceForRunnerTest(t),
			})
			project := runnerProject(worktree)
			req := runnerRequest(project, worktree, "qa prompt")
			req.Phase, req.Subphase, req.ArtifactPaths = pipeline.PhaseQA, "", nil
			result, err := runner.Run(context.Background(), req)
			if err == nil || result.Status != state.StatusFailed || result.Disposition != DispositionFailed {
				t.Fatalf("result=%+v error=%v, want terminal protocol failure", result, err)
			}
			if IsSemanticFailure(err) {
				t.Fatalf("error=%v, malformed deferred proof must not enter semantic feedback", err)
			}
		})
	}
}

func proofServiceForRunnerTest(t *testing.T) *proof.ArtifactService {
	t.Helper()
	return proof.NewArtifactService(t.TempDir(), runnerProofChecker{})
}

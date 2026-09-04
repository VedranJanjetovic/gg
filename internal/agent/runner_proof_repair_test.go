package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/proof"
	"github.com/VedranJanjetovic/gg/internal/state"
	"github.com/VedranJanjetovic/gg/testdata/fakeagent"
)

// malformedRunnerProof is a proof whose pass entry lacks command evidence, so
// deterministic validation rejects it while parsing succeeds.
func malformedRunnerProof(runID string) string {
	return "---\ngg_run_id: \"" + runID + "\"\n---\n\n" +
		"## Validation: acceptance check\n\n" +
		"- Status: pass\n" +
		"- Test location: internal/example\n" +
		"- Test name: TestExample\n" +
		"- Flow/scenario: exercised flow\n" +
		"- What it verifies: observable behavior\n" +
		"- Proof it passed: verified manually\n" +
		"- Manual run instructions: run the suite locally\n"
}

func proofRepairQARequest(worktree string) RunRequest {
	project := runnerProject(worktree)
	// GitDisabled skips the uncommitted-file check so the proof service needs
	// no git worktree checker in this focused runner test.
	project.GitDisabled = true
	req := runnerRequest(project, worktree, "qa prompt")
	req.Phase = pipeline.PhaseQA
	req.Subphase = ""
	req.ArtifactPaths = nil
	return req
}

func TestAgentRunnerWrapsExitZeroMalformedProofInRepairableProtocolError(t *testing.T) {
	worktree := t.TempDir()
	script := fakeRunner(t, fakeagent.Spec{
		Files: map[string]string{
			".gg/qa-report.md": runnerCanonical("runner-test", "passed", "qa evidence"),
			".gg/PROOF.md":     malformedRunnerProof("runner-test"),
		},
	})
	runner := NewAgentRunner(AgentRunnerOptions{
		Factory: NewExecProcessFactory(nil, nil),
		Lookup:  func(string) (string, error) { return script, nil },
		LogRoot: t.TempDir(),
		Proof:   proof.NewArtifactService(t.TempDir()),
	})
	result, err := runner.Run(context.Background(), proofRepairQARequest(worktree))

	var protocolErr *QAProofProtocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("Run() error = %v, want repairable QAProofProtocolError", err)
	}
	violations := protocolErr.Violations()
	if len(violations) != 1 || !strings.Contains(violations[0], "proof it passed must include the command run and its result") {
		t.Fatalf("protocol violations = %v, want exact proof validation error", violations)
	}
	if result.Status != state.StatusFailed || result.Disposition != DispositionFailed {
		t.Fatalf("result = %#v, want failed protocol disposition", result)
	}
	if IsSemanticFailure(err) {
		t.Fatalf("Run() error = %v, a proof protocol failure must not enter the semantic feedback loop", err)
	}
}

func TestAgentRunnerDoesNotOfferProofRepairForFailedAgentProcess(t *testing.T) {
	worktree := t.TempDir()
	script := fakeRunner(t, fakeagent.Spec{
		ExitCode: 9,
		Files: map[string]string{
			".gg/qa-report.md": runnerCanonical("runner-test", "passed", "qa evidence"),
			".gg/PROOF.md":     malformedRunnerProof("runner-test"),
		},
	})
	runner := NewAgentRunner(AgentRunnerOptions{
		Factory: NewExecProcessFactory(nil, nil),
		Lookup:  func(string) (string, error) { return script, nil },
		LogRoot: t.TempDir(),
		Proof:   proof.NewArtifactService(t.TempDir()),
	})
	result, err := runner.Run(context.Background(), proofRepairQARequest(worktree))

	if err == nil {
		t.Fatal("Run() succeeded despite failed agent process and malformed proof")
	}
	var protocolErr *QAProofProtocolError
	if errors.As(err, &protocolErr) {
		t.Fatalf("Run() error = %v, a failed agent process must not be presented as repairable", err)
	}
	if result.Status != state.StatusFailed {
		t.Fatalf("result status = %s, want failed", result.Status)
	}
}

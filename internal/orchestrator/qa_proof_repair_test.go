package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

// proofRepairRunner fails QA dispatches with a proof protocol error until
// repairsBeforePass QA runs have happened, then passes. Every other phase
// finishes normally.
type proofRepairRunner struct {
	qaCalls           int
	repairsBeforePass int
	requests          []agent.RunRequest
}

func (r *proofRepairRunner) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	r.requests = append(r.requests, req)
	result := agent.RunResult{ProjectSlug: req.Project.Slug, Phase: req.Phase, Subphase: req.Subphase, Status: state.StatusFinished, Disposition: agent.DispositionPassed}
	if req.Phase != pipeline.PhaseQA {
		return result, nil
	}
	r.qaCalls++
	if r.qaCalls <= r.repairsBeforePass {
		result.Status = state.StatusFailed
		result.Disposition = agent.DispositionFailed
		return result, &agent.QAProofProtocolError{Cause: errors.New("validation 1: proof it passed must include the command run and its result")}
	}
	return result, nil
}

func qaPromptInputs(inputs []agent.PromptInput) []agent.PromptInput {
	var qa []agent.PromptInput
	for _, input := range inputs {
		if input.Phase == pipeline.PhaseQA {
			qa = append(qa, input)
		}
	}
	return qa
}

func TestQAProofProtocolFailureGetsExactlyOneRepairInvocation(t *testing.T) {
	runner := &proofRepairRunner{repairsBeforePass: 1}
	prompts := &scopeCapturePrompt{}
	req := request(t, pipelineWithQA(t))
	req.MaxIterations = 2
	outcomes, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(&feedbackState{}),
		orchestrator.WithEventSink(&fakeEvents{}),
		orchestrator.WithPromptBuilder(prompts),
	).Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute() error = %v, want repaired QA to pass the run", err)
	}
	// acceptance_criteria, development implementation+verification, rebase,
	// failed QA, repair QA, test_document.
	if len(outcomes) != 7 {
		t.Fatalf("outcomes = %d, want 7 including one repair dispatch", len(outcomes))
	}
	qaPrompts := qaPromptInputs(prompts.inputs)
	if len(qaPrompts) != 2 {
		t.Fatalf("QA prompt constructions = %d, want failed attempt plus repair", len(qaPrompts))
	}
	if len(qaPrompts[0].QAProofViolations) != 0 {
		t.Fatalf("first QA prompt unexpectedly carries repair violations: %v", qaPrompts[0].QAProofViolations)
	}
	repair := qaPrompts[1].QAProofViolations
	if len(repair) != 1 || !strings.Contains(repair[0], "proof it passed must include the command run and its result") {
		t.Fatalf("repair QA prompt violations = %v, want exact proof validation error", repair)
	}
	if len(qaPrompts[1].ArtifactPaths) != len(qaPrompts[0].ArtifactPaths) {
		t.Fatalf("repair QA artifacts = %v, want same declared inputs as the failed attempt", qaPrompts[1].ArtifactPaths)
	}
}

func TestQAProofProtocolFailureAfterRepairIsTerminalWithoutFixLoop(t *testing.T) {
	runner := &proofRepairRunner{repairsBeforePass: 99}
	prompts := &scopeCapturePrompt{}
	req := request(t, pipelineWithQA(t))
	req.MaxIterations = 3
	_, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(&feedbackState{}),
		orchestrator.WithEventSink(&fakeEvents{}),
		orchestrator.WithPromptBuilder(prompts),
	).Execute(context.Background(), req)
	if err == nil {
		t.Fatal("Execute() succeeded although the repair reproduced the protocol failure")
	}
	if runner.qaCalls != 2 {
		t.Fatalf("QA dispatches = %d, want exactly one attempt plus one repair", runner.qaCalls)
	}
	for _, request := range runner.requests {
		if request.Phase == pipeline.PhaseDevelopment && len(request.ArtifactPaths) != 0 {
			t.Fatalf("protocol failure entered the Development fix loop: %#v", request)
		}
	}
}

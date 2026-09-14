package orchestrator_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

// closureRunnerCall records what one dispatch observed about the QA loop's
// durable counters, so a test can prove a fix-pass failure did not move them.
type closureRunnerCall struct {
	phase             pipeline.PhaseID
	subphase          string
	openFindingIDs    []string
	completedAttempts int
	strikes           []state.QAFindingStrike
}

func (c closureRunnerCall) identity() string {
	return string(c.phase) + "/" + c.subphase
}

// closureContractRunner stands in for the agent runner's gg_finding_closures
// enforcement: a Development pass whose request carries open QA findings fails
// semantically until it declares a closure for every one of them.
type closureContractRunner struct {
	findings     []agent.QAFinding
	omitClosures int
	failedQAs    int
	qaCalls      int
	calls        []closureRunnerCall
}

func (r *closureContractRunner) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	r.calls = append(r.calls, closureRunnerCall{
		phase:             req.Phase,
		subphase:          req.Subphase,
		openFindingIDs:    append([]string(nil), req.OpenQAFindingIDs...),
		completedAttempts: req.Project.QACompletedAttempts,
		strikes:           append([]state.QAFindingStrike(nil), req.Project.QAFindingStrikes...),
	})
	result := agent.RunResult{ProjectSlug: req.Project.Slug, Phase: req.Phase, Subphase: req.Subphase, Status: state.StatusFinished}
	if req.Phase == pipeline.PhaseQA {
		r.qaCalls++
		if r.qaCalls <= r.failedQAs {
			result.Status = state.StatusFailed
			result.Disposition = agent.DispositionFailed
			result.ArtifactPaths = []string{"qa-report.md"}
			result.QAFindings = append([]agent.QAFinding(nil), r.findings...)
			return result, &agent.SemanticFailureError{Phase: req.Phase, Disposition: agent.DispositionFailed}
		}
		return result, nil
	}
	if req.Phase != pipeline.PhaseDevelopment || len(req.OpenQAFindingIDs) == 0 {
		return result, nil
	}
	if r.omitClosures > 0 {
		r.omitClosures--
		result.Status = state.StatusFailed
		result.Disposition = agent.DispositionFailed
		return result, &agent.SemanticFailureError{
			Phase:       req.Phase,
			Disposition: agent.DispositionFailed,
			Details:     `gg_finding_closures does not close open QA finding(s) "batch-ops-coverage"`,
		}
	}
	for _, id := range req.OpenQAFindingIDs {
		result.FindingClosures = append(result.FindingClosures, agent.FindingClosure{
			ID: id, Evidence: "go test ./internal/batch -run TestBatch: PASS", Covered: []string{"every batch operation"},
		})
	}
	return result, nil
}

func closureRunnerIdentities(calls []closureRunnerCall) []string {
	identities := make([]string, len(calls))
	for index, call := range calls {
		identities[index] = call.identity()
	}
	return identities
}

func TestFixPassOmittingAClosureRetriesInRoundWithoutSpendingAQAAttemptOrStrike(t *testing.T) {
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project := durableExecutionProject(t, service)
	runner := &closureContractRunner{
		findings:     []agent.QAFinding{{ID: "batch-ops-coverage", Summary: "batch delete is not covered", New: true}},
		failedQAs:    1,
		omitClosures: 1,
	}
	request := resumeRequest(t, project, resolvedPipeline(t, config.PhaseQA))
	request.MaxIterations = 3
	if _, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(service),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	).Execute(context.Background(), request); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	want := []string{
		"acceptance_criteria/",
		"development/implementation", "development/verification",
		"rebase/", "qa/",
		"development/implementation", "development/implementation", "development/verification",
		"rebase/", "qa/",
		"test_document/",
	}
	if got := closureRunnerIdentities(runner.calls); !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatches = %v, want the breached fix pass retried in round %v", got, want)
	}
	if runner.qaCalls != 2 {
		t.Fatalf("QA dispatches = %d, want exactly the failed attempt and its re-run", runner.qaCalls)
	}

	wantStrikes := []state.QAFindingStrike{{ID: "batch-ops-coverage", Summary: "batch delete is not covered", Strikes: 1}}
	// Calls 5 and 6 are the breached fix pass and its in-round retry; call 9
	// is the QA re-run. All three must still see one completed QA attempt and
	// one strike: a closure-contract breach is the fix pass's own failure.
	for _, index := range []int{5, 6, 9} {
		call := runner.calls[index]
		if call.completedAttempts != 1 {
			t.Fatalf("dispatch %d (%s) saw %d completed QA attempts, want 1", index, call.identity(), call.completedAttempts)
		}
		if !reflect.DeepEqual(call.strikes, wantStrikes) {
			t.Fatalf("dispatch %d (%s) saw strikes %#v, want %#v", index, call.identity(), call.strikes, wantStrikes)
		}
	}
	for _, index := range []int{5, 6, 7} {
		if got := runner.calls[index].openFindingIDs; !reflect.DeepEqual(got, []string{"batch-ops-coverage"}) {
			t.Fatalf("fix dispatch %d (%s) open findings = %v, want the open QA finding", index, runner.calls[index].identity(), got)
		}
	}
	for _, index := range []int{1, 2} {
		if got := runner.calls[index].openFindingIDs; len(got) != 0 {
			t.Fatalf("initial Development dispatch %d open findings = %v, want none", index, got)
		}
	}

	finished, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != state.StatusFinished || finished.QACompletedAttempts != 0 || finished.QALoopStage != "" {
		t.Fatalf("finished state = %#v", finished)
	}
}

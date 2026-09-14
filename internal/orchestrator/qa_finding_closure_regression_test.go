package orchestrator_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

// batchOperations is the complete set the QA finding requires coverage for.
// The implementation fix pass claims five of them, which is the exact shape of
// the real failure: Development asserted the finding was closed, nothing
// checked the assertion, and the metered QA gate paid to rediscover the gap
// until the finding reached three strikes and parked the run.
var batchOperations = []string{"create", "update", "delete", "list", "get", "purge"}

// incompleteClosureRunner claims closure for five of the six operations the
// finding requires and reports the claim verbatim in the development
// artifact's closure data. Its verification subphase reads the claim out of
// the prompt it was handed — which is what pins the orchestrator → prompt
// plumbing — and refuses to confirm what the claim does not cover.
type incompleteClosureRunner struct {
	calls              []closureRunnerCall
	qaCalls            int
	verificationCalls  int
	coverageFixed      bool
	missingAtFirstFail []string
}

func (r *incompleteClosureRunner) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	r.calls = append(r.calls, closureRunnerCall{
		phase:             req.Phase,
		subphase:          req.Subphase,
		openFindingIDs:    append([]string(nil), req.OpenQAFindingIDs...),
		completedAttempts: req.Project.QACompletedAttempts,
		strikes:           append([]state.QAFindingStrike(nil), req.Project.QAFindingStrikes...),
	})
	result := agent.RunResult{ProjectSlug: req.Project.Slug, Phase: req.Phase, Subphase: req.Subphase, Status: state.StatusFinished}
	switch {
	case req.Phase == pipeline.PhaseQA:
		r.qaCalls++
		if r.qaCalls == 1 {
			result.Status = state.StatusFailed
			result.Disposition = agent.DispositionFailed
			result.ArtifactPaths = []string{"qa-report.md"}
			result.QAFindings = []agent.QAFinding{{
				ID:      "batch-ops-coverage",
				Summary: "only some batch operations are covered; all six must be",
				New:     true,
			}}
			return result, &agent.SemanticFailureError{Phase: req.Phase, Disposition: agent.DispositionFailed}
		}
		return result, nil
	case req.Phase != pipeline.PhaseDevelopment || len(req.OpenQAFindingIDs) == 0:
		return result, nil
	case req.Subphase == string(pipeline.DevelopmentSubphaseVerification):
		r.verificationCalls++
		missing := operationsAbsentFromClaimedCoverage(req.Prompt)
		if len(missing) > 0 && !r.coverageFixed {
			// Fresh eyes compared the claim against what the finding requires
			// and could not confirm it. Failing here costs an unmetered fix
			// invocation; letting QA find it costs a bounded QA attempt and a
			// strike against the finding.
			r.missingAtFirstFail = missing
			r.coverageFixed = true
			result.Status = state.StatusFailed
			result.Disposition = agent.DispositionFailed
			return result, &agent.SemanticFailureError{
				Phase:       req.Phase,
				Disposition: agent.DispositionFailed,
				Details:     fmt.Sprintf("closure claim for %q does not cover %v", req.OpenQAFindingIDs[0], missing),
			}
		}
		result.FindingClosures = closureClaim(req.OpenQAFindingIDs[0], batchOperations)
		return result, nil
	default:
		result.FindingClosures = closureClaim(req.OpenQAFindingIDs[0], batchOperations[:len(batchOperations)-1])
		return result, nil
	}
}

func closureClaim(id string, covered []string) []agent.FindingClosure {
	return []agent.FindingClosure{{
		ID:       id,
		Evidence: "go test ./internal/batch -run TestBatchOperations: PASS",
		Covered:  append([]string(nil), covered...),
	}}
}

// operationsAbsentFromClaimedCoverage reads the coverage the prompt attributes
// to the implementation pass's closure claim and reports what the finding
// requires but the claim omits.
func operationsAbsentFromClaimedCoverage(prompt string) []string {
	_, claimed, found := strings.Cut(prompt, "claimed coverage ")
	if !found {
		return append([]string(nil), batchOperations...)
	}
	claimed, _, _ = strings.Cut(claimed, "\n")
	var missing []string
	for _, operation := range batchOperations {
		if !strings.Contains(claimed, operation) {
			missing = append(missing, operation)
		}
	}
	return missing
}

func closurePhaseContracts() map[pipeline.PhaseID]string {
	contracts := map[pipeline.PhaseID]string{}
	for _, phase := range []pipeline.PhaseID{
		pipeline.PhaseAcceptanceCriteria, pipeline.PhaseGrooming, pipeline.PhasePlanning,
		pipeline.PhaseDevelopment, pipeline.PhaseRebase, pipeline.PhaseQA,
		pipeline.PhaseTestDocument, pipeline.PhaseBuildChecker, pipeline.PhasePR, pipeline.PhaseCI,
	} {
		contracts[phase] = "contract for " + string(phase)
	}
	return contracts
}

func TestIncompleteClosureCoverageFailsTheFixPassVerificationBeforeQAReruns(t *testing.T) {
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project := durableExecutionProject(t, service)
	runner := &incompleteClosureRunner{}
	request := resumeRequest(t, project, resolvedPipeline(t, config.PhaseQA))
	request.PhaseContracts = closurePhaseContracts()
	request.MaxIterations = 3
	// The real prompt builder is deliberate: the claim must actually reach the
	// verification subphase's prompt for the adversarial check to be possible.
	if _, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(service),
		orchestrator.WithPromptBuilder(agent.StandalonePromptBuilder{}),
	).Execute(context.Background(), request); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	want := []string{
		"acceptance_criteria/",
		"development/implementation", "development/verification",
		"rebase/", "qa/",
		"development/implementation", "development/verification", "development/verification",
		"rebase/", "qa/",
		"test_document/",
	}
	identities := closureRunnerIdentities(runner.calls)
	if !reflect.DeepEqual(identities, want) {
		t.Fatalf("dispatches = %v, want the incomplete closure retried in round %v", identities, want)
	}
	if !reflect.DeepEqual(runner.missingAtFirstFail, []string{"purge"}) {
		t.Fatalf("verification rejected coverage gaps %v, want exactly the omitted operation", runner.missingAtFirstFail)
	}
	if runner.verificationCalls != 2 {
		t.Fatalf("findings-scoped verification dispatches = %d, want the failed closure check and its retry", runner.verificationCalls)
	}

	// The gap was caught by an unmetered fix invocation, so the second QA
	// attempt still sees one completed attempt and a single strike against the
	// finding — the loop never spent a metered attempt on the same gap.
	if runner.qaCalls != 2 {
		t.Fatalf("QA dispatches = %d, want exactly the failed attempt and its re-run", runner.qaCalls)
	}
	failedVerification, secondQA := 6, 9
	if identities[failedVerification] != "development/verification" || identities[secondQA] != "qa/" {
		t.Fatalf("dispatch indices moved: %v", identities)
	}
	wantStrikes := []state.QAFindingStrike{{
		ID: "batch-ops-coverage", Summary: "only some batch operations are covered; all six must be", Strikes: 1,
	}}
	for _, index := range []int{failedVerification, secondQA} {
		call := runner.calls[index]
		if call.completedAttempts != 1 {
			t.Fatalf("dispatch %d (%s) saw %d completed QA attempts, want 1", index, call.identity(), call.completedAttempts)
		}
		if !reflect.DeepEqual(call.strikes, wantStrikes) {
			t.Fatalf("dispatch %d (%s) saw strikes %#v, want %#v", index, call.identity(), call.strikes, wantStrikes)
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

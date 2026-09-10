package orchestrator_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

// findingsQARunner fails every QA attempt with structured findings chosen by
// findingForAttempt and passes every other phase. A nil finding for an
// attempt makes that QA attempt pass.
type findingsQARunner struct {
	qaAttempts        int
	findingForAttempt func(attempt int) []agent.QAFinding
}

func (r *findingsQARunner) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	result := agent.RunResult{ProjectSlug: req.Project.Slug, Phase: req.Phase, Subphase: req.Subphase, Status: state.StatusFinished, Disposition: agent.DispositionPassed}
	if req.Phase != pipeline.PhaseQA {
		return result, nil
	}
	r.qaAttempts++
	findings := r.findingForAttempt(r.qaAttempts)
	if findings == nil {
		return result, nil
	}
	result.Status = state.StatusFailed
	result.Disposition = agent.DispositionFeedback
	result.ArtifactPaths = []string{"qa-report.md"}
	result.QAFindings = findings
	return result, nil
}

func TestQALoopParksWhenTheSameFindingSurvivesThreeAttempts(t *testing.T) {
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project := durableExecutionProject(t, service)
	plan := resolvedPipeline(t, config.PhaseQA)
	runner := &findingsQARunner{findingForAttempt: func(attempt int) []agent.QAFinding {
		return []agent.QAFinding{{ID: "refund-flow-500", Summary: "refund returns 500", New: attempt == 1}}
	}}
	req := resumeRequest(t, project, plan)
	req.MaxIterations = 10
	controller := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(service),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	_, err = controller.Execute(context.Background(), req)
	if err == nil {
		t.Fatal("Execute() succeeded, want the recurring finding to end the loop")
	}
	if runner.qaAttempts != 3 {
		t.Fatalf("QA ran %d times, want 3 strikes of the same finding", runner.qaAttempts)
	}
	parked, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if parked.Status != state.StatusStopped {
		t.Fatalf("parked status = %s, want stopped", parked.Status)
	}
	if parked.Pause == nil || !strings.Contains(parked.Pause.Reason, "refund-flow-500") {
		t.Fatalf("pause record = %#v", parked.Pause)
	}
	if len(parked.QAFindingStrikes) != 1 || parked.QAFindingStrikes[0].Strikes != 3 {
		t.Fatalf("finding strikes = %#v", parked.QAFindingStrikes)
	}
}

func TestQALoopContinuesPastThreeAttemptsWhileEveryFindingIsFresh(t *testing.T) {
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project := durableExecutionProject(t, service)
	plan := resolvedPipeline(t, config.PhaseQA)
	runner := &findingsQARunner{findingForAttempt: func(attempt int) []agent.QAFinding {
		if attempt >= 5 {
			return nil
		}
		return []agent.QAFinding{{ID: fmt.Sprintf("fresh-issue-%d", attempt), New: true}}
	}}
	req := resumeRequest(t, project, plan)
	req.MaxIterations = 10
	controller := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(service),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	if _, err := controller.Execute(context.Background(), req); err != nil {
		t.Fatalf("Execute() error = %v, want progress to keep the loop alive", err)
	}
	if runner.qaAttempts != 5 {
		t.Fatalf("QA ran %d times, want 5 — past the historical 3-attempt budget", runner.qaAttempts)
	}
	finished, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != state.StatusFinished {
		t.Fatalf("finished status = %s", finished.Status)
	}
	if len(finished.QAFindingStrikes) != 0 {
		t.Fatalf("successful loop retained strikes %#v", finished.QAFindingStrikes)
	}
}

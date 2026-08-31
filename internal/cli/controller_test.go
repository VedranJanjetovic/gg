package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/git"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

type captureController struct {
	executes []orchestrator.Request
	stops    []orchestrator.StopRequest
	resumes  []orchestrator.ResumeRequest
	err      error
}

func (c *captureController) Execute(_ context.Context, request orchestrator.Request) ([]orchestrator.PhaseOutcome, error) {
	c.executes = append(c.executes, request)
	return nil, c.err
}
func (c *captureController) Stop(_ context.Context, request orchestrator.StopRequest) error {
	c.stops = append(c.stops, request)
	return c.err
}
func (c *captureController) Resume(_ context.Context, request orchestrator.ResumeRequest) ([]orchestrator.PhaseOutcome, error) {
	c.resumes = append(c.resumes, request)
	return nil, c.err
}

func controllerTestApp(t *testing.T, root string, controller orchestrator.Controller) *App {
	t.Helper()
	store := configuredMemoryStore()
	return New(
		WithRootResolver(fixedRoot{root: root}),
		WithConfigStore(store),
		WithOrchestratorController(controller),
	)
}

func TestControllerWiringRunStopResumeUsesCanonicalSelector(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	controller := &captureController{}
	app := controllerTestApp(t, root, controller)

	var stdout, stderr bytes.Buffer
	if code := app.Run(context.Background(), []string{"run", "Demo Project"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run code=%d stderr=%q", code, stderr.String())
	}
	if len(controller.executes) != 1 || controller.executes[0].Project.Slug != "demo-project" {
		t.Fatalf("execute requests=%#v", controller.executes)
	}
	if got := controller.executes[0].ArtifactRoot; got != root {
		t.Fatalf("fresh-run artifact root = %q, want configured root %q", got, root)
	}
	if controller.executes[0].MaxIterations != 3 {
		t.Fatalf("default max iterations = %d, want 3", controller.executes[0].MaxIterations)
	}
	phases := controller.executes[0].Pipeline.Phases()
	if phases == nil {
		t.Fatal("controller received no ordered executable pipeline")
	}
	var planningModel string
	for _, phase := range phases {
		if phase.Phase().ID() == pipeline.PhasePlanning {
			settings, ok := phase.Settings()
			if !ok {
				t.Fatal("planning phase omitted persisted settings")
			}
			planningModel = settings.Model
		}
	}
	if planningModel != "sonnet" {
		t.Fatalf("planning model = %q, want persisted configuration", planningModel)
	}
	request := controller.executes[0]
	for _, phase := range phases {
		contract := request.PhaseContracts[phase.Phase().ID()]
		if strings.TrimSpace(contract) == "" {
			t.Fatalf("phase %q has no contract", phase.Phase().ID())
		}
	}
	if _, err := agent.BuildPrompt(agent.PromptInput{
		Project: request.Project, Phase: pipeline.PhasePlanning,
		PhaseContract:    request.PhaseContracts[pipeline.PhasePlanning],
		WorkingDirectory: request.Project.WorktreePath,
		RunID:            request.RunID + "/planning/phase/iteration-0",
	}); err != nil {
		t.Fatalf("prompt boundary rejected configured run: %v", err)
	}

	projects, err := app.Projects(context.Background())
	if err != nil || len(projects) != 1 {
		t.Fatalf("projects=%#v err=%v", projects, err)
	}
	service := state.NewLifecycleService(mustStateStore(t, root), nil, mustStateStore(t, root).Locker())
	if _, err := service.Transition(context.Background(), "demo-project", state.StatusStopped, "pipeline", "run", nil); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"stop", "Demo Project"}, &stdout, &stderr); code != 0 {
		t.Fatalf("stop code=%d stderr=%q", code, stderr.String())
	}
	if len(controller.stops) != 1 || controller.stops[0].ProjectSlug != "demo-project" {
		t.Fatalf("stop requests=%#v", controller.stops)
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"run", "Demo Project"}, &stdout, &stderr); code != 0 {
		t.Fatalf("resumed run code=%d stderr=%q", code, stderr.String())
	}
	if len(controller.resumes) != 1 || controller.resumes[0].ProjectSlug != "demo-project" {
		t.Fatalf("resumed run requests=%#v", controller.resumes)
	}
	worktreeParent := filepath.Dir(controller.resumes[0].Execution.Project.WorktreePath)
	if got := controller.resumes[0].Execution.ArtifactRoot; got != root || got == worktreeParent {
		t.Fatalf("resumed run artifact root = %q, want configured root %q (not worktree parent %q)", got, root, worktreeParent)
	}

	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"resume", "Demo Project"}, &stdout, &stderr); code != 0 {
		t.Fatalf("resume code=%d stderr=%q", code, stderr.String())
	}
	if len(controller.resumes) != 2 || controller.resumes[1].ProjectSlug != "demo-project" {
		t.Fatalf("resume requests=%#v", controller.resumes)
	}
	if got := controller.resumes[1].Execution.ArtifactRoot; got != root || got == worktreeParent {
		t.Fatalf("explicit resume artifact root = %q, want configured root %q (not worktree parent %q)", got, root, worktreeParent)
	}
}

func TestControllerRunPassesMaxIterationsAfterPositionalSelector(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	controller := &captureController{}
	app := controllerTestApp(t, root, controller)
	var stdout, stderr bytes.Buffer
	if code := app.Run(context.Background(), []string{"run", "demo", "--max-iterations", "5"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run code=%d stderr=%q", code, stderr.String())
	}
	if len(controller.executes) != 1 || controller.executes[0].MaxIterations != 5 {
		t.Fatalf("execute requests=%#v, want max iterations 5", controller.executes)
	}
}

func mustStateStore(t *testing.T, root string) *state.FileStore {
	t.Helper()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestControllerErrorIsActionableAndDoesNotMutateState(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	dispatchErr := errors.New("controller unavailable")
	app := controllerTestApp(t, root, &captureController{err: dispatchErr})
	var stdout, stderr bytes.Buffer
	if code := app.Run(context.Background(), []string{"run", "error-project"}, &stdout, &stderr); code == 0 {
		t.Fatal("run unexpectedly succeeded")
	}
	if !strings.Contains(stderr.String(), "run project \"error-project\": controller unavailable") {
		t.Fatalf("stderr=%q", stderr.String())
	}
	project, err := mustStateStore(t, root).Load(context.Background(), "error-project")
	if err != nil {
		t.Fatal(err)
	}
	if project.Status != state.StatusPending {
		t.Fatalf("handler mutated lifecycle state to %s", project.Status)
	}
}

func TestControllerCommandsHonorCanceledContext(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	controller := &captureController{}
	app := controllerTestApp(t, root, controller)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if code := app.Run(ctx, []string{"stop", "project"}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "context canceled") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if len(controller.stops) != 0 {
		t.Fatal("canceled stop reached controller")
	}
}

type promptBoundaryController struct {
	prompts []agent.PromptInput
}

func (c *promptBoundaryController) Execute(_ context.Context, request orchestrator.Request) ([]orchestrator.PhaseOutcome, error) {
	for _, executable := range request.Pipeline.Phases() {
		subphases := []string{""}
		if executable.Phase().ID() == pipeline.PhaseDevelopment {
			generated, err := pipeline.GenerateDevelopmentSubphases(request.Subphases)
			if err != nil {
				return nil, err
			}
			subphases = make([]string, 0, len(generated))
			for _, subphase := range generated {
				subphases = append(subphases, string(subphase.ID()))
			}
		}
		for _, subphase := range subphases {
			input := agent.PromptInput{
				Project: request.Project, Phase: executable.Phase().ID(), Subphase: subphase,
				PhaseContract:    request.PhaseContracts[executable.Phase().ID()],
				WorkingDirectory: request.Project.WorktreePath,
				RunID:            request.RunID + "/" + string(executable.Phase().ID()) + "/" + subphase,
				Development:      executable.Phase().ID() == pipeline.PhaseDevelopment,
			}
			prompt, err := agent.BuildPrompt(input)
			if err != nil {
				return nil, err
			}
			skillName := strings.ReplaceAll(string(input.Phase), "_", "-")
			if !strings.Contains(prompt, `Before any other action, invoke the skill "gg-`+skillName+`"`) {
				return nil, errors.New("canonical phase contract skill reference did not reach prompt")
			}
			c.prompts = append(c.prompts, input)
		}
	}
	return nil, nil
}
func (c *promptBoundaryController) Stop(context.Context, orchestrator.StopRequest) error { return nil }
func (c *promptBoundaryController) Resume(ctx context.Context, request orchestrator.ResumeRequest) ([]orchestrator.PhaseOutcome, error) {
	return c.Execute(ctx, request.Execution)
}

func TestProductionRunPromptBoundaryUsesCanonicalContractsForEveryEnabledPhase(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	controller := &promptBoundaryController{}
	app := controllerTestApp(t, root, controller)
	var stdout, stderr bytes.Buffer
	if code := app.Run(context.Background(), []string{"run", "contract-project"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run code=%d stderr=%q", code, stderr.String())
	}

	if len(controller.prompts) == 0 {
		t.Fatal("production run did not reach prompt construction")
	}
	seen := make(map[pipeline.PhaseID]int)
	for _, input := range controller.prompts {
		seen[input.Phase]++
		if input.PhaseContract == "" || input.PhaseContract == phaseDisplayName(input.Phase) {
			t.Errorf("phase %q received display-name/empty contract: %q", input.Phase, input.PhaseContract)
		}
		if !strings.Contains(input.PhaseContract, "phase_id: "+string(input.Phase)) {
			t.Errorf("phase %q received non-canonical contract", input.Phase)
		}
	}
	for _, phase := range []pipeline.PhaseID{
		pipeline.PhaseAcceptanceCriteria, pipeline.PhaseGrooming, pipeline.PhasePlanning,
		pipeline.PhaseDevelopment, pipeline.PhaseQA, pipeline.PhaseRebase,
		pipeline.PhaseTestDocument, pipeline.PhaseBuildChecker, pipeline.PhasePR, pipeline.PhaseCI,
	} {
		if seen[phase] == 0 {
			t.Errorf("enabled phase %q did not reach prompt construction", phase)
		}
	}
	if seen[pipeline.PhaseDevelopment] != 3 {
		t.Errorf("development prompts=%d, want default implementation/testing/review subphases", seen[pipeline.PhaseDevelopment])
	}
}

func phaseDisplayName(id pipeline.PhaseID) string {
	for _, phase := range pipeline.DefaultPipeline().Phases() {
		if phase.ID() == id {
			return phase.Metadata().DisplayName
		}
	}
	return ""
}

type cliConflictReader struct{ unresolved bool }

func (r cliConflictReader) HasUnresolvedConflicts(context.Context, string) (bool, error) {
	return r.unresolved, nil
}

type cliConflictRunner struct {
	calls []agent.RunRequest
}

func writeValidPlanningArtifact(request agent.RunRequest) error {
	path := filepath.Join(request.WorkingDirectory, ".gg", "plan.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("---\ngg_run_id: %q\ngg_disposition: passed\ngg_plan_complexity: \"Trivial\"\ngg_plan_complexity_evidence: [\"The test scope is one cohesive outcome.\"]\ngg_plan_phases: [\"Phase 1: test scope\"]\ngg_plan_phase_boundaries: [{\"phase\":\"Phase 1: test scope\",\"justification\":\"The test scope has no dependency ordering.\"}]\ngg_verification_steps: [{\"name\":\"tests\",\"command\":\"go\",\"args\":[\"test\",\"./...\"],\"adapter\":\"go-test\"}]\ngg_repair_mode: false\n---\n# Implementation Plan\n\n## Complexity assessment\n\n- Complexity category: **Trivial**\n- Selected phase count: **1**\n\nSupporting evidence:\n\n1. The test scope is one cohesive outcome.\n\n## Phase 1: test scope\n\nBoundary justification: The test scope has no dependency ordering.\n", request.RunID)
	return os.WriteFile(path, []byte(content), 0o644)
}

func (r *cliConflictRunner) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	r.calls = append(r.calls, req)
	if req.Phase == pipeline.PhasePlanning {
		if err := writeValidPlanningArtifact(req); err != nil {
			return agent.RunResult{ProjectSlug: req.Project.Slug, Phase: req.Phase, Subphase: req.Subphase, Status: state.StatusFailed}, err
		}
	}
	result := agent.RunResult{ProjectSlug: req.Project.Slug, Phase: req.Phase, Subphase: req.Subphase, Status: state.StatusFinished}
	if req.Phase == pipeline.PhaseRebase {
		result.Status = state.StatusFailed
		result.ArtifactPaths = []string{"rebase-conflict.txt"}
		return result, errors.New("rebase conflict")
	}
	return result, nil
}

func TestProductionControllerThroughCLIStopsOnOrdinaryRebaseFailure(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	store := mustStateStore(t, root)
	lifecycle := state.NewLifecycleService(store, nil, store.Locker())
	runner := &cliConflictRunner{}
	controller := orchestrator.NewProductionController(runner, lifecycle, cliConflictReader{}, orchestrator.WithPromptBuilder(agent.StandalonePromptBuilder{}), orchestrator.WithVerificationService(passingVerification{}))
	app := New(WithRootResolver(fixedRoot{root: root}), WithConfigStore(configuredMemoryStore()), WithLifecycleService(lifecycle), WithGitClient(git.NewClient(root, nil)), WithOrchestratorController(controller))
	var stdout, stderr bytes.Buffer
	if code := app.Run(context.Background(), []string{"run", "ordinary-rebase-failure"}, &stdout, &stderr); code == 0 {
		t.Fatalf("run unexpectedly succeeded: stderr=%q", stderr.String())
	}
	var phases []pipeline.PhaseID
	for _, call := range runner.calls {
		phases = append(phases, call.Phase)
	}
	qaCount := 0
	for _, phase := range phases {
		if phase == pipeline.PhaseQA {
			qaCount++
		}
	}
	if qaCount != 0 || phases[len(phases)-1] != pipeline.PhaseRebase {
		t.Fatalf("phases=%v, want Rebase to fail before QA", phases)
	}
}

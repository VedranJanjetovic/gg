package orchestrator_test

import (
	"context"
	"errors"
	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/ci"
	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/git"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/pr"
	"github.com/VedranJanjetovic/gg/internal/proof"
	"github.com/VedranJanjetovic/gg/internal/state"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeRebaser struct {
	conflict bool
	calls    int
}

func (f *fakeRebaser) FetchParent(context.Context, string) (git.FetchResult, error) {
	f.calls++
	return git.FetchResult{ParentBranch: "main"}, nil
}
func (f *fakeRebaser) RebaseProject(context.Context, git.RebaseRequest) (git.RebaseResult, error) {
	if f.conflict {
		return git.RebaseResult{Branch: "feature", BaseRef: "origin/main", Conflict: &git.ConflictEvidence{Paths: []string{"file.go"}, Output: "CONFLICT"}}, git.ErrRebaseConflict
	}
	return git.RebaseResult{Branch: "feature", BaseRef: "origin/main", Output: "rebased"}, nil
}

type fakePullRequests struct {
	calls    int
	requests []pr.Request
}

func (f *fakePullRequests) Create(_ context.Context, request pr.Request) (pr.Result, error) {
	f.calls++
	f.requests = append(f.requests, request)
	return pr.Result{Created: true, URL: "https://github.com/example/repo/pull/7"}, nil
}

type fakeChecks struct {
	calls      int
	identities []string
	outcome    ci.Outcome
}

func (f *fakeChecks) Monitor(_ context.Context, cfg ci.Config) (ci.Result, error) {
	f.calls++
	f.identities = append(f.identities, cfg.Identity)
	return ci.Result{Outcome: f.outcome}, nil
}

type unresolvedReader struct{ unresolved bool }

func (r unresolvedReader) HasUnresolvedConflicts(context.Context, string) (bool, error) {
	return r.unresolved, nil
}

type gitOpsRunner struct{ phases []pipeline.PhaseID }

func (r *gitOpsRunner) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	r.phases = append(r.phases, req.Phase)
	return agent.RunResult{Phase: req.Phase, Status: state.StatusFinished}, nil
}
func gitOpsPlan(t *testing.T) pipeline.ExecutablePipeline {
	t.Helper()
	phases := map[config.Phase]config.ResolvedPhase{config.PhaseGrooming: {Enabled: false}, config.PhasePlanning: {Enabled: false}, config.PhaseQA: {Enabled: false}, config.PhaseBuildChecker: {Enabled: false}, config.PhasePR: {Enabled: true}, config.PhaseCI: {Enabled: true}}
	for phase, resolved := range phases {
		if resolved.Enabled {
			resolved.AgentSettings = config.AgentSettings{Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortMedium}
			phases[phase] = resolved
		}
	}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), config.ResolvedConfig{Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortMedium}, Phases: phases})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
func TestProductionGitOpsServicesRouteConfiguredPhasesWithoutAgentFallback(t *testing.T) {
	root := t.TempDir()
	rebase := &fakeRebaser{}
	prs := &fakePullRequests{}
	checks := &fakeChecks{outcome: ci.OutcomePassed}
	runner := &gitOpsRunner{}
	controller := orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(&fakeState{}), orchestrator.WithGitOpsServices(rebase, prs, checks), orchestrator.WithPromptBuilder(fakePrompt{}))
	request := orchestrator.Request{Project: state.ProjectState{Slug: "demo", OriginalGoal: "goal", AcceptanceCriteria: []string{"criterion"}, WorktreePath: root, BranchName: "feature"}, Pipeline: gitOpsPlan(t), PhaseContracts: gitOpsPlan(t).PhaseContracts(), GitOps: config.GitOpsConfig{Configured: true, ParentBranch: "main", BaseRef: "origin/main", EnablePR: true, EnableCI: true}, ArtifactRoot: root, RunID: "run-1"}
	if _, err := controller.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if rebase.calls != 1 || prs.calls != 1 || checks.calls != 1 {
		t.Fatalf("GitOps calls = rebase %d PR %d CI %d", rebase.calls, prs.calls, checks.calls)
	}
	if got := checks.identities; len(got) != 1 || got[0] != "https://github.com/example/repo/pull/7" {
		t.Fatalf("same-run CI identities = %v, want created PR URL", got)
	}
	if len(runner.phases) != 4 || runner.phases[0] != pipeline.PhaseAcceptanceCriteria || runner.phases[1] != pipeline.PhaseDevelopment || runner.phases[3] != pipeline.PhaseTestDocument {
		t.Fatalf("agent phases = %v", runner.phases)
	}
	for _, name := range []string{"rebase-report.md", "pr.md"} {
		if _, err := os.Stat(filepath.Join(root, ".gg", "projects", "demo", "artifacts", name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
}
func TestResumedConfiguredGitOpsSnapshotRoutesToInjectedServices(t *testing.T) {
	root := t.TempDir()
	configured := config.GitOpsConfig{ParentBranch: "main", BaseRef: "origin/main", EnablePR: true, EnableCI: true, Configured: true}
	originalPlan := gitOpsPlan(t)
	snapshot, err := pipeline.SnapshotExecution(originalPlan, pipeline.DevelopmentSubphaseGeneration{}, 3, configured)
	if err != nil {
		t.Fatal(err)
	}
	plan, subphases, maxIterations, err := pipeline.RestoreExecution(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	gitOps, err := pipeline.SnapshotGitOps(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !gitOps.Configured {
		t.Fatal("restored configured marker is false")
	}

	rebase := &fakeRebaser{}
	prs := &fakePullRequests{}
	checks := &fakeChecks{outcome: ci.OutcomePassed}
	runner := &gitOpsRunner{}
	store := &persistedResumeState{project: state.ProjectState{Slug: "demo", Status: state.StatusStopped, CurrentPhase: string(pipeline.PhaseAcceptanceCriteria), PhaseHistory: []state.PhaseRecord{{Phase: string(pipeline.PhaseAcceptanceCriteria), Status: state.StatusFinished}}, WorktreePath: root, BranchName: "feature", PullRequestURL: "https://github.com/example/repo/pull/7"}}
	controller := orchestrator.NewController(orchestrator.WithRunner(runner), orchestrator.WithPhaseState(store), orchestrator.WithGitOpsServices(rebase, prs, checks), orchestrator.WithPromptBuilder(fakePrompt{}))
	request := orchestrator.Request{Project: store.project, Pipeline: plan, PhaseContracts: plan.PhaseContracts(), Subphases: subphases, MaxIterations: maxIterations, GitOps: gitOps, ArtifactRoot: root, RunID: "restart-run"}
	if _, err := controller.Resume(context.Background(), orchestrator.ResumeRequest{ProjectSlug: "demo", RunID: "restart-run", Execution: request}); err != nil {
		t.Fatal(err)
	}
	if rebase.calls != 1 || prs.calls != 1 || checks.calls != 1 {
		t.Fatalf("resumed GitOps calls = rebase %d PR %d CI %d", rebase.calls, prs.calls, checks.calls)
	}
	if got := checks.identities; len(got) != 1 || got[0] != "https://github.com/example/repo/pull/7" {
		t.Fatalf("resumed CI identities = %v, want persisted PR URL", got)
	}
	if len(runner.phases) != 3 || runner.phases[0] != pipeline.PhaseDevelopment || runner.phases[2] != pipeline.PhaseTestDocument {
		t.Fatalf("resumed agent phases = %v", runner.phases)
	}
}

func TestConfiguredRebaseConflictPersistsStructuredEvidence(t *testing.T) {
	root := t.TempDir()
	rebase := &fakeRebaser{conflict: true}
	controller := orchestrator.NewController(orchestrator.WithRunner(&gitOpsRunner{}), orchestrator.WithPhaseState(&fakeState{}), orchestrator.WithGitOpsServices(rebase, nil, nil), orchestrator.WithConflictStateReader(unresolvedReader{unresolved: true}))
	request := orchestrator.Request{Project: state.ProjectState{Slug: "demo", OriginalGoal: "goal", AcceptanceCriteria: []string{"criterion"}, WorktreePath: root, BranchName: "feature"}, Pipeline: gitOpsPlan(t), PhaseContracts: gitOpsPlan(t).PhaseContracts(), GitOps: config.GitOpsConfig{Configured: true, ParentBranch: "main", BaseRef: "origin/main", EnablePR: true, EnableCI: true}, ArtifactRoot: root}
	_, err := controller.Execute(context.Background(), request)
	if err == nil || !errors.Is(err, git.ErrRebaseConflict) {
		t.Fatalf("error = %v, want rebase conflict", err)
	}
	data, readErr := os.ReadFile(filepath.Join(root, ".gg", "projects", "demo", "artifacts", "rebase-conflict.md"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(data), "file.go") || !strings.Contains(string(data), "origin/main") {
		t.Fatalf("conflict evidence = %q", data)
	}
}

func TestConfiguredPRReceivesExactWaiverAndDeferredHandoff(t *testing.T) {
	root := t.TempDir()
	planPhases := map[config.Phase]config.ResolvedPhase{
		config.PhaseGrooming:     {Enabled: false},
		config.PhasePlanning:     {Enabled: false},
		config.PhaseQA:           {Enabled: true},
		config.PhaseBuildChecker: {Enabled: false},
		config.PhasePR:           {Enabled: true},
		config.PhaseCI:           {Enabled: false},
	}
	for phase, resolved := range planPhases {
		if resolved.Enabled {
			resolved.AgentSettings = config.AgentSettings{Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortMedium}
			planPhases[phase] = resolved
		}
	}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), config.ResolvedConfig{Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortMedium}, Phases: planPhases})
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	deferred := proof.DeferredCheck{
		TestLocation: "internal/aws_test.go", CheckName: "TestRemoteFlow",
		FlowScenario: "call deployed API", ExpectedBehavior: "request persists",
		RemoteOnlyReason: "requires AWS credentials", RepositoryEvidence: "config/aws.go uses AWS_ENDPOINT",
		RunInstructions: "run in CI with AWS secrets",
	}
	prs := &fakePullRequests{}
	controller := orchestrator.NewController(
		orchestrator.WithRunner(&gitOpsRunner{}),
		orchestrator.WithPhaseState(&fakeState{}),
		orchestrator.WithGitOpsServices(&fakeRebaser{}, prs, nil),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)
	request := orchestrator.Request{
		Project: state.ProjectState{
			Slug: "demo", OriginalGoal: "goal", AcceptanceCriteria: []string{"criterion"}, WorktreePath: root, BranchName: "feature",
			DeferredChecks: []proof.DeferredCheck{deferred},
			PhaseHistory: []state.PhaseRecord{{
				Phase: string(pipeline.PhaseQA), Status: state.StatusFailed, CompletedAt: &when,
				Outcome: &state.ExecutionOutcome{Error: "proof failed"}, Skip: &state.SkipResolution{},
			}},
		},
		Pipeline: plan, PhaseContracts: plan.PhaseContracts(),
		GitOps: config.GitOpsConfig{Configured: true, ParentBranch: "main", EnablePR: true}, ArtifactRoot: root, RunID: "run-1",
	}
	if _, err := controller.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(prs.requests) != 1 {
		t.Fatalf("PR requests = %d, want 1", len(prs.requests))
	}
	got := prs.requests[0]
	if got.ProofRequired || !got.ProofWaived {
		t.Fatalf("proof waiver request = %#v", got)
	}
	if len(got.SkippedExecutions) != 1 || got.SkippedExecutions[0].Phase != string(pipeline.PhaseQA) || got.SkippedExecutions[0].Failure != "proof failed" {
		t.Fatalf("skipped handoff = %#v", got.SkippedExecutions)
	}
	if len(got.DeferredChecks) != 1 || got.DeferredChecks[0] != deferred {
		t.Fatalf("deferred handoff = %#v", got.DeferredChecks)
	}
}

package orchestrator_test

import (
	"context"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
	"github.com/VedranJanjetovic/gg/internal/verification"
)

type boundaryVerification struct {
	reports []verification.Report
	calls   int
}

func (v *boundaryVerification) Verify(context.Context, string, []verification.Step) (verification.Report, error) {
	v.calls++
	if len(v.reports) == 0 {
		return verification.Report{Results: []verification.CommandResult{{StepName: "tests", Status: verification.CommandPassed}}}, nil
	}
	report := v.reports[0]
	v.reports = v.reports[1:]
	return report, nil
}

type boundaryState struct {
	fakeState
	project state.ProjectState
}

func (s *boundaryState) Load(_ context.Context, _ string) (state.ProjectState, error) {
	return s.project, nil
}

func (s *boundaryState) Transition(_ context.Context, _ string, status state.LifecycleStatus, phase, subphase string, _ []string) (state.ProjectState, error) {
	s.project.Status = status
	s.project.CurrentPhase = phase
	s.project.CurrentSubphase = subphase
	return s.project, nil
}

func (s *boundaryState) RecordVerificationBaselineReport(_ context.Context, _ string, results []state.VerificationCommandResult, findings []state.VerificationFinding) (state.ProjectState, error) {
	verificationState := *s.project.Verification
	verificationState.ParentBaselineCaptured = true
	verificationState.ParentResults = results
	verificationState.ParentBaseline = findings
	s.project.Verification = &verificationState
	return s.project, nil
}

func (s *boundaryState) RecordVerificationResultReport(_ context.Context, _ string, results []state.VerificationCommandResult, findings, warnings []state.VerificationFinding, boundary string, attempts int, nextAction string) (state.ProjectState, error) {
	verificationState := *s.project.Verification
	verificationState.CurrentResults = results
	verificationState.CurrentFindings = findings
	verificationState.Warnings = warnings
	verificationState.BoundaryCursor = boundary
	verificationState.RemediationAttempts = attempts
	verificationState.NextAction = nextAction
	s.project.Verification = &verificationState
	return s.project, nil
}

func (s *boundaryState) PromoteVerificationIdentity(_ context.Context, _ string, identity string) (state.ProjectState, error) {
	verificationState := *s.project.Verification
	verificationState.PromotedRequiredGreen = append(verificationState.PromotedRequiredGreen, identity)
	s.project.Verification = &verificationState
	return s.project, nil
}

type boundaryRunner struct {
	calls     int
	phases    []pipeline.PhaseID
	artifacts [][]string
}

func (r *boundaryRunner) Run(_ context.Context, request agent.RunRequest) (agent.RunResult, error) {
	r.calls++
	r.phases = append(r.phases, request.Phase)
	r.artifacts = append(r.artifacts, append([]string(nil), request.ArtifactPaths...))
	return agent.RunResult{ProjectSlug: request.Project.Slug, Phase: request.Phase, Subphase: request.Subphase, Status: state.StatusFinished}, nil
}

func boundaryRequest(project state.ProjectState) orchestrator.Request {
	phases := map[config.Phase]config.ResolvedPhase{}
	for _, phase := range []config.Phase{config.PhaseGrooming, config.PhasePlanning, config.PhaseQA, config.PhaseBuildChecker, config.PhasePR, config.PhaseCI} {
		phases[phase] = config.ResolvedPhase{Enabled: false, AgentSettings: config.AgentSettings{Agent: config.AgentClaude}}
	}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), config.ResolvedConfig{Defaults: config.AgentSettings{Agent: config.AgentClaude}, Phases: phases})
	if err != nil {
		panic(err)
	}
	return orchestrator.Request{
		Project:        project,
		Pipeline:       plan,
		PhaseContracts: map[pipeline.PhaseID]string{},
		RunID:          "verification-boundary",
	}
}

func boundaryProject(repair bool) state.ProjectState {
	return state.ProjectState{
		Slug: "demo", OriginalGoal: "goal", AcceptanceCriteria: []string{"criterion"}, WorktreePath: "/tmp",
		Verification: &state.VerificationState{PlannedSteps: []state.VerificationStep{{Name: "tests", Command: "go", Args: []string{"test"}, Adapter: state.VerificationAdapterGoTest}}, RepairMode: repair},
	}
}

func passedReport() verification.Report {
	return verification.Report{Results: []verification.CommandResult{{StepName: "tests", Command: "go", Status: verification.CommandPassed}}}
}

func failedReport(identity, reason string) verification.Report {
	return verification.Report{Results: []verification.CommandResult{{StepName: "tests", Command: "go", Status: verification.CommandFailed, Failures: []verification.IndividualFailure{{Identity: identity, Reason: reason}}}}}
}

func TestVerificationBoundaryPersistsBaselineAndAllowsUnchangedWarning(t *testing.T) {
	store := &boundaryState{project: boundaryProject(false)}
	checks := &boundaryVerification{reports: []verification.Report{failedReport("TestBroken", "panic"), failedReport("TestBroken", "panic")}}
	runner := &boundaryRunner{}
	request := boundaryRequest(store.project)
	if _, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(store),
		orchestrator.WithPromptBuilder(fakePrompt{}),
		orchestrator.WithVerificationService(checks),
	).Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if checks.calls != 2 || runner.calls == 0 {
		t.Fatalf("verification calls=%d agent calls=%d, want preflight and final gate", checks.calls, runner.calls)
	}
	if store.project.Status != state.StatusFinished {
		t.Fatalf("project status=%s, want successful completion with warning", store.project.Status)
	}
	if !store.project.Verification.ParentBaselineCaptured || len(store.project.Verification.ParentResults) != 1 {
		t.Fatalf("baseline was not durably captured: %#v", store.project.Verification)
	}
	if len(store.project.Verification.Warnings) != 1 || store.project.Verification.Warnings[0].Classification != string(verification.ClassificationUnchangedBaseline) {
		t.Fatalf("warnings=%#v, want unchanged baseline warning", store.project.Verification.Warnings)
	}
}

func TestVerificationBoundaryPromotesRepairedFailure(t *testing.T) {
	store := &boundaryState{project: boundaryProject(true)}
	checks := &boundaryVerification{reports: []verification.Report{failedReport("TestBroken", "panic"), passedReport()}}
	request := boundaryRequest(store.project)
	if _, err := orchestrator.NewController(
		orchestrator.WithRunner(&boundaryRunner{}),
		orchestrator.WithPhaseState(store),
		orchestrator.WithPromptBuilder(fakePrompt{}),
		orchestrator.WithVerificationService(checks),
	).Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	want := "tests::TestBroken"
	if len(store.project.Verification.PromotedRequiredGreen) != 1 || store.project.Verification.PromotedRequiredGreen[0] != want {
		t.Fatalf("promoted identities=%v, want %q", store.project.Verification.PromotedRequiredGreen, want)
	}
}

func TestVerificationBoundaryPausesOnUnclassifiableParentCheck(t *testing.T) {
	store := &boundaryState{project: boundaryProject(false)}
	checks := &boundaryVerification{reports: []verification.Report{{Results: []verification.CommandResult{{StepName: "tests", Command: "go", Status: verification.CommandUnclassifiable}}}}}
	runner := &boundaryRunner{}
	_, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(store),
		orchestrator.WithPromptBuilder(fakePrompt{}),
		orchestrator.WithVerificationService(checks),
	).Execute(context.Background(), boundaryRequest(store.project))
	if err == nil || !strings.Contains(err.Error(), "unclassifiable") {
		t.Fatalf("Execute() error=%v, want unclassifiable preflight pause", err)
	}
	for _, phase := range runner.phases {
		if phase == pipeline.PhaseDevelopment {
			t.Fatalf("preflight pause dispatched Development: %v", runner.phases)
		}
	}
	if store.project.Verification.ParentBaselineCaptured {
		t.Fatalf("preflight pause dispatched or captured invalid baseline: calls=%d state=%#v", runner.calls, store.project.Verification)
	}
}

func TestVerificationBoundaryPausesOnMalformedParentReport(t *testing.T) {
	tests := []struct {
		name   string
		report verification.Report
	}{
		{name: "failed without identity", report: verification.Report{Results: []verification.CommandResult{{StepName: "tests", Command: "go", Status: verification.CommandFailed}}}},
		{name: "missing planned result", report: verification.Report{}},
		{name: "duplicate planned result", report: verification.Report{Results: []verification.CommandResult{
			{StepName: "tests", Command: "go", Status: verification.CommandPassed},
			{StepName: "tests", Command: "go", Status: verification.CommandPassed},
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &boundaryState{project: boundaryProject(false)}
			runner := &boundaryRunner{}
			checks := &boundaryVerification{reports: []verification.Report{test.report}}
			_, err := orchestrator.NewController(
				orchestrator.WithRunner(runner),
				orchestrator.WithPhaseState(store),
				orchestrator.WithPromptBuilder(fakePrompt{}),
				orchestrator.WithVerificationService(checks),
			).Execute(context.Background(), boundaryRequest(store.project))
			if err == nil || !strings.Contains(err.Error(), "unclassifiable") {
				t.Fatalf("Execute() error=%v, want malformed parent report to pause", err)
			}
			for _, phase := range runner.phases {
				if phase == pipeline.PhaseDevelopment {
					t.Fatalf("malformed parent report dispatched Development: %v", runner.phases)
				}
			}
			if store.project.Verification.ParentBaselineCaptured {
				t.Fatalf("malformed parent report was captured as baseline: %#v", store.project.Verification)
			}
		})
	}
}

func TestVerificationBoundaryPausesOnUnavailableCurrentCheck(t *testing.T) {
	store := &boundaryState{project: boundaryProject(false)}
	checks := &boundaryVerification{reports: []verification.Report{
		passedReport(),
		{Results: []verification.CommandResult{{StepName: "tests", Command: "go", Status: verification.CommandUnavailable, UnavailableErr: "go: executable file not found"}}},
	}}
	runner := &boundaryRunner{}
	_, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(store),
		orchestrator.WithPromptBuilder(fakePrompt{}),
		orchestrator.WithVerificationService(checks),
	).Execute(context.Background(), boundaryRequest(store.project))
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("Execute() error=%v, want unavailable boundary pause", err)
	}
	if store.project.Verification.BoundaryCursor != "final" {
		t.Fatalf("boundary cursor=%q, want final", store.project.Verification.BoundaryCursor)
	}
	if store.project.Verification.RemediationAttempts != 0 {
		t.Fatalf("remediation attempts=%d, want unavailable checks to pause without remediation", store.project.Verification.RemediationAttempts)
	}
	for _, paths := range runner.artifacts {
		for _, path := range paths {
			if strings.Contains(path, "verification-remediation-") {
				t.Fatalf("unavailable check unexpectedly dispatched remediation evidence: %v", runner.artifacts)
			}
		}
	}
	developmentSeen := false
	for _, phase := range runner.phases {
		if phase == pipeline.PhaseDevelopment {
			developmentSeen = true
		}
	}
	if !developmentSeen {
		t.Fatalf("agent phases=%v, want Development before final pause", runner.phases)
	}
}

func TestVerificationBoundaryReusesPersistedParentBaselineOnResume(t *testing.T) {
	store := &boundaryState{project: boundaryProject(false)}
	checks := &boundaryVerification{reports: []verification.Report{
		failedReport("TestBroken", "panic"),
		failedReport("TestBroken", "panic"),
		failedReport("TestBroken", "panic"),
	}}
	controller := orchestrator.NewController(
		orchestrator.WithRunner(&boundaryRunner{}),
		orchestrator.WithPhaseState(store),
		orchestrator.WithPromptBuilder(fakePrompt{}),
		orchestrator.WithVerificationService(checks),
	)
	request := boundaryRequest(store.project)
	if _, err := controller.Execute(context.Background(), request); err != nil {
		t.Fatalf("initial Execute() error=%v", err)
	}
	if !store.project.Verification.ParentBaselineCaptured {
		t.Fatal("initial execution did not persist the parent baseline")
	}
	if _, err := controller.Execute(context.Background(), boundaryRequest(store.project)); err != nil {
		t.Fatalf("resumed Execute() error=%v", err)
	}
	if checks.calls != 3 {
		t.Fatalf("verification calls=%d, want two initial checks and one resumed final check", checks.calls)
	}
	if store.project.Verification.ParentResults[0].Failures[0].Reason != "panic" {
		t.Fatalf("persisted parent baseline changed: %#v", store.project.Verification.ParentResults)
	}
}

func TestVerificationBoundaryRemediatesRepairTarget(t *testing.T) {
	project := boundaryProject(true)
	project.WorktreePath = t.TempDir()
	store := &boundaryState{project: project}
	checks := &boundaryVerification{reports: []verification.Report{failedReport("TestBroken", "panic"), failedReport("TestBroken", "panic"), passedReport()}}
	runner := &boundaryRunner{}
	_, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(store),
		orchestrator.WithPromptBuilder(fakePrompt{}),
		orchestrator.WithVerificationService(checks),
	).Execute(context.Background(), boundaryRequest(store.project))
	if err != nil {
		t.Fatalf("Execute() error=%v, want repair target to be fixed by the first remediation cycle", err)
	}
	if checks.calls != 3 {
		t.Fatalf("verification calls=%d, want parent, failed boundary, and repaired boundary", checks.calls)
	}
	if store.project.Verification.RemediationAttempts != 0 {
		t.Fatalf("remediation attempts=%d, want reset after successful boundary", store.project.Verification.RemediationAttempts)
	}
	evidencePaths := map[string]struct{}{}
	for _, paths := range runner.artifacts {
		for _, path := range paths {
			if strings.Contains(path, "verification-remediation-final-1.md") {
				evidencePaths[path] = struct{}{}
			}
		}
	}
	if len(evidencePaths) != 1 {
		t.Fatalf("remediation evidence was not dispatched: %#v", runner.artifacts)
	}
	if store.project.Verification.BoundaryCursor != "final" {
		t.Fatalf("boundary cursor=%q, want final remediation to retain its final-gate cursor", store.project.Verification.BoundaryCursor)
	}
}

func TestVerificationBoundaryStopsAfterThreeUnsuccessfulRemediations(t *testing.T) {
	project := boundaryProject(true)
	project.WorktreePath = t.TempDir()
	store := &boundaryState{project: project}
	checks := &boundaryVerification{reports: []verification.Report{
		failedReport("TestBroken", "panic"),
		failedReport("TestBroken", "panic"),
		failedReport("TestBroken", "panic"),
		failedReport("TestBroken", "panic"),
		failedReport("TestBroken", "panic"),
	}}
	runner := &boundaryRunner{}
	_, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(store),
		orchestrator.WithPromptBuilder(fakePrompt{}),
		orchestrator.WithVerificationService(checks),
	).Execute(context.Background(), boundaryRequest(store.project))
	if err == nil || !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("Execute() error=%v, want bounded remediation exhaustion", err)
	}
	if checks.calls != 5 || store.project.Verification.RemediationAttempts != state.MaxVerificationRemediationAttempts {
		t.Fatalf("verification calls=%d attempts=%d, want five calls and three consumed attempts", checks.calls, store.project.Verification.RemediationAttempts)
	}
	if len(runner.artifacts) < 3 {
		t.Fatalf("remediation dispatches=%d, want three", len(runner.artifacts))
	}
}

func TestVerificationBoundaryRepairsOnSecondAttempt(t *testing.T) {
	project := boundaryProject(true)
	project.WorktreePath = t.TempDir()
	store := &boundaryState{project: project}
	checks := &boundaryVerification{reports: []verification.Report{
		failedReport("TestBroken", "panic"),
		failedReport("TestBroken", "panic"),
		failedReport("TestBroken", "panic"),
		passedReport(),
	}}
	if _, err := orchestrator.NewController(
		orchestrator.WithRunner(&boundaryRunner{}),
		orchestrator.WithPhaseState(store),
		orchestrator.WithPromptBuilder(fakePrompt{}),
		orchestrator.WithVerificationService(checks),
	).Execute(context.Background(), boundaryRequest(store.project)); err != nil {
		t.Fatal(err)
	}
	if checks.calls != 4 || store.project.Verification.RemediationAttempts != 0 {
		t.Fatalf("verification calls=%d attempts=%d, want two remediation cycles followed by a reset", checks.calls, store.project.Verification.RemediationAttempts)
	}
}

func TestVerificationBoundaryResumeFinalGateUsesBoundedRemediation(t *testing.T) {
	project := boundaryProject(true)
	project.Status = state.StatusStopped
	project.CurrentPhase = string(pipeline.PhaseTestDocument)
	project.PhaseHistory = []state.PhaseRecord{{Phase: string(pipeline.PhaseTestDocument), Status: state.StatusFinished}}
	project.WorktreePath = t.TempDir()
	project.Verification.ParentBaselineCaptured = true
	project.Verification.ParentResults = []state.VerificationCommandResult{{
		CheckName: "tests",
		Command:   "go",
		Status:    string(verification.CommandFailed),
		Failures:  []state.VerificationFinding{{CheckName: "tests", Identity: "TestBroken", Reason: "panic"}},
	}}
	store := &boundaryState{project: project}
	checks := &boundaryVerification{reports: []verification.Report{failedReport("TestBroken", "panic"), passedReport()}}
	runner := &boundaryRunner{}
	request := boundaryRequest(store.project)
	if _, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(store),
		orchestrator.WithPromptBuilder(fakePrompt{}),
		orchestrator.WithVerificationService(checks),
	).Resume(context.Background(), orchestrator.ResumeRequest{ProjectSlug: project.Slug, RunID: "resume-final", Execution: request}); err != nil {
		t.Fatalf("Resume() error=%v, want final-gate remediation to complete", err)
	}
	if checks.calls != 2 {
		t.Fatalf("verification calls=%d, want failed final gate and one post-remediation gate", checks.calls)
	}
	if len(runner.phases) != 3 || runner.phases[0] != pipeline.PhaseDevelopment {
		t.Fatalf("remediation phases=%v, want the Development sequence", runner.phases)
	}
	if store.project.Status != state.StatusFinished {
		t.Fatalf("resumed project status=%s, want finished", store.project.Status)
	}
}

// A controller wired without WithVerificationService cannot run the gate. When
// the project carries a contract that must fail the run rather than silently
// report an unverified run as verified.
func TestVerificationBoundaryFailsClosedWithoutAVerificationService(t *testing.T) {
	store := &boundaryState{project: boundaryProject(false)}
	runner := &boundaryRunner{}
	request := boundaryRequest(store.project)
	_, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(store),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	).Execute(context.Background(), request)
	if err == nil {
		t.Fatal("Execute() succeeded without a verification service, want a fail-closed error")
	}
	if !strings.Contains(err.Error(), "no verification service") {
		t.Fatalf("Execute() error = %v, want it to name the missing verification service", err)
	}
}

// Projects without a contract are unaffected: the gate stays inert rather than
// forcing every caller to wire a verification service.
func TestVerificationBoundarySkippedWhenProjectHasNoContract(t *testing.T) {
	store := &boundaryState{project: boundaryProject(false)}
	store.project.Verification = nil
	runner := &boundaryRunner{}
	request := boundaryRequest(store.project)
	if _, err := orchestrator.NewController(
		orchestrator.WithRunner(runner),
		orchestrator.WithPhaseState(store),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	).Execute(context.Background(), request); err != nil {
		t.Fatalf("Execute() error = %v, want an unverified project to run unchanged", err)
	}
	if runner.calls == 0 {
		t.Fatal("no phases dispatched")
	}
}

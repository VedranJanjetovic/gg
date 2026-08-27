package resume_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/resume"
	"github.com/VedranJanjetovic/gg/internal/state"
)

type persistence struct {
	project state.ProjectState
}

func (p *persistence) MigrateVerificationContract(_ context.Context, _ string, contract state.VerificationContract, snapshot state.PipelineConfigSnapshot) (state.ProjectState, error) {
	verification := p.project.Verification
	if verification == nil {
		verification = &state.VerificationState{}
	} else {
		copy := *verification
		verification = &copy
	}
	verification.PlannedSteps = contract.Steps
	verification.RepairMode = contract.RepairMode
	p.project.Verification = verification
	p.project.PipelineConfig = snapshot
	return p.project, nil
}

func (p *persistence) SetVerificationContract(_ context.Context, _ string, contract state.VerificationContract, snapshot state.PipelineConfigSnapshot) (state.ProjectState, error) {
	p.project.PipelineConfig = snapshot
	p.project.Verification = &state.VerificationState{PlannedSteps: contract.Steps, RepairMode: contract.RepairMode}
	return p.project, nil
}

func (p *persistence) RecordPlan(_ context.Context, _ string, phases, completed []string) (state.ProjectState, error) {
	if p.project.Plan == nil {
		p.project.Plan = &state.PlanState{}
	}
	p.project.Plan.Phases = append([]string(nil), phases...)
	seen := make(map[string]bool, len(p.project.Plan.Completed))
	for _, name := range p.project.Plan.Completed {
		seen[name] = true
	}
	for _, name := range completed {
		if !seen[name] {
			p.project.Plan.Completed = append(p.project.Plan.Completed, name)
			seen[name] = true
		}
	}
	return p.project, nil
}

func legacyProject(t *testing.T, root, slug string) state.ProjectState {
	t.Helper()
	resolved := config.ResolvedConfig{Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "model", Effort: config.EffortMedium}}
	resolved.Phases = map[config.Phase]config.ResolvedPhase{}
	for _, phase := range []config.Phase{config.PhaseGrooming, config.PhasePlanning, config.PhaseQA, config.PhaseBuildChecker, config.PhasePR, config.PhaseCI} {
		resolved.Phases[phase] = config.ResolvedPhase{Enabled: false, AgentSettings: resolved.Defaults}
	}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := pipeline.SnapshotExecution(plan, pipeline.DevelopmentSubphaseGeneration{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	return state.ProjectState{
		Slug: slug, Status: state.StatusFailed, CurrentPhase: string(pipeline.PhaseDevelopment), WorktreePath: root,
		PipelineConfig: snapshot, Plan: &state.PlanState{Phases: []string{"P1", "P2"}, Completed: []string{"P1"}},
		PhaseHistory: []state.PhaseRecord{
			{Phase: string(pipeline.PhaseDevelopment), Subphase: "implementation", Status: state.StatusFinished, Outcome: &state.ExecutionOutcome{DevelopmentBaseCommit: "e88cf0e"}},
			{Phase: string(pipeline.PhaseDevelopment), Subphase: "testing", Status: state.StatusFailed, Outcome: &state.ExecutionOutcome{DevelopmentBaseCommit: "05f0691"}},
		},
	}
}

func writePlan(t *testing.T, root, planning string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".gg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gg", "plan.md"), []byte(planning), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gg", "development.md"), []byte("---\ngg_plan_completed: [\"P1\"]\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareMigratesLegacyVerificationAndRefreshesOnlyPendingPlan(t *testing.T) {
	for _, slug := range []string{"see-many-md-files-not", "very-project-gg-tool", "self-hosting"} {
		t.Run(slug, func(t *testing.T) {
			root := t.TempDir()
			writePlan(t, root, "---\ngg_verification_steps: [{\"name\":\"tests\",\"command\":\"go\",\"args\":[\"test\",\"./...\"],\"adapter\":\"go-test\"}]\ngg_repair_mode: true\ngg_plan_phases: [\"P1\", \"P2\", \"P3\"]\n---\n")
			persistence := &persistence{project: legacyProject(t, root, slug)}
			originalCompleted := append([]string(nil), persistence.project.Plan.Completed...)
			originalHistory := append([]state.PhaseRecord(nil), persistence.project.PhaseHistory...)
			originalPhase, originalSubphase := persistence.project.CurrentPhase, persistence.project.CurrentSubphase
			got, err := resume.Prepare(context.Background(), persistence.project, persistence)
			if err != nil {
				t.Fatal(err)
			}
			if got.Verification == nil || len(got.Verification.PlannedSteps) != 1 {
				t.Fatalf("verification migration = %#v", got.Verification)
			}
			if !reflect.DeepEqual(got.Plan.Phases, []string{"P1", "P2", "P3"}) || !reflect.DeepEqual(got.Plan.Completed, originalCompleted) {
				t.Fatalf("plan migration changed completed work: %#v", got.Plan)
			}
			if !reflect.DeepEqual(got.PhaseHistory, originalHistory) {
				t.Fatalf("phase history changed during resume preparation: %#v", got.PhaseHistory)
			}
			if got.CurrentPhase != originalPhase || got.CurrentSubphase != originalSubphase {
				t.Fatalf("resume cursor changed from %s/%s to %s/%s", originalPhase, originalSubphase, got.CurrentPhase, got.CurrentSubphase)
			}
			if got.PhaseHistory[0].Outcome.DevelopmentBaseCommit != "e88cf0e" || got.PhaseHistory[1].Outcome.DevelopmentBaseCommit != "05f0691" {
				t.Fatalf("preserved commit history = %#v", got.PhaseHistory)
			}
			if contract, err := pipeline.SnapshotVerification(got.PipelineConfig); err != nil || len(contract.Steps) != 1 {
				t.Fatalf("persisted migrated snapshot = %#v err=%v", contract, err)
			}
		})
	}
}

func TestPrepareDoesNotReplacePlanWhenReplanArtifactHasNoPhaseList(t *testing.T) {
	root := t.TempDir()
	writePlan(t, root, "---\ngg_verification_steps: [{\"name\":\"tests\",\"command\":\"go\",\"args\":[\"test\",\"./...\"],\"adapter\":\"go-test\"}]\ngg_repair_mode: false\n---\n")
	persistence := &persistence{project: legacyProject(t, root, "see-many-md-files-not")}
	want := append([]string(nil), persistence.project.Plan.Phases...)
	got, err := resume.Prepare(context.Background(), persistence.project, persistence)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Plan.Phases, want) || !reflect.DeepEqual(got.Plan.Completed, []string{"P1"}) {
		t.Fatalf("missing replan phase list changed plan: %#v", got.Plan)
	}
}

func TestPreparePausesLegacyResumeWhenPlanLacksMigrationContract(t *testing.T) {
	root := t.TempDir()
	writePlan(t, root, "---\ngg_plan_phases: [\"P1\"]\n---\n")
	persistence := &persistence{project: legacyProject(t, root, "see-many-md-files-not")}
	_, err := resume.Prepare(context.Background(), persistence.project, persistence)
	if err == nil || !strings.Contains(err.Error(), "requires migration from .gg/plan.md") {
		t.Fatalf("err=%v, want concrete migration action", err)
	}
}

func TestPreparePreservesVerificationHistoryDuringLegacyMigration(t *testing.T) {
	root := t.TempDir()
	writePlan(t, root, "---\ngg_verification_steps: [{\"name\":\"new-tests\",\"command\":\"go\",\"args\":[\"test\",\"./...\"],\"adapter\":\"go-test\"}]\ngg_repair_mode: true\n---\n")
	persistence := &persistence{project: legacyProject(t, root, "see-many-md-files-not")}
	persistence.project.Verification = &state.VerificationState{
		ParentBaseline:        []state.VerificationFinding{{CheckName: "old-tests", Identity: "pkg/Test", Reason: "panic"}},
		ParentResults:         []state.VerificationCommandResult{{CheckName: "old-tests", Status: "failed"}},
		CurrentFindings:       []state.VerificationFinding{{CheckName: "old-tests", Identity: "pkg/Test", Reason: "panic"}},
		Warnings:              []state.VerificationFinding{{CheckName: "old-tests", Identity: "pkg/Other", Reason: "flaky"}},
		PromotedRequiredGreen: []string{"pkg/Test"},
		BoundaryCursor:        "Phase 7",
		RemediationAttempts:   2,
		NextAction:            "resume with fresh budget",
	}
	want := *persistence.project.Verification
	got, err := resume.Prepare(context.Background(), persistence.project, persistence)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Verification.ParentBaseline, want.ParentBaseline) ||
		!reflect.DeepEqual(got.Verification.ParentResults, want.ParentResults) ||
		!reflect.DeepEqual(got.Verification.CurrentFindings, want.CurrentFindings) ||
		!reflect.DeepEqual(got.Verification.Warnings, want.Warnings) ||
		!reflect.DeepEqual(got.Verification.PromotedRequiredGreen, want.PromotedRequiredGreen) ||
		got.Verification.BoundaryCursor != want.BoundaryCursor || got.Verification.RemediationAttempts != want.RemediationAttempts || got.Verification.NextAction != want.NextAction {
		t.Fatalf("legacy migration dropped verification history: got=%#v want=%#v", got.Verification, &want)
	}
}

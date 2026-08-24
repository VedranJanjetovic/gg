package state

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/proof"
)

func validProjectState() ProjectState {
	now := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	return ProjectState{
		SchemaVersion:      CurrentSchemaVersion,
		Name:               "Example Project",
		Slug:               "example-project",
		OriginalGoal:       "Build the project",
		AcceptanceCriteria: []string{"It works"},
		PipelineConfig:     PipelineConfigSnapshot{SchemaVersion: 1, Data: json.RawMessage([]byte{123, 34, 112, 104, 97, 115, 101, 115, 34, 58, 91, 34, 98, 117, 105, 108, 100, 34, 93, 125})},
		CurrentPhase:       "build",
		Status:             StatusPending,
		WorktreePath:       "/tmp/example-project",
		BranchName:         "agent/example-project",
		CreatedAt:          now,
		UpdatedAt:          now,
		StatusChangedAt:    now,
	}
}

func TestNewProjectStateValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ProjectState)
		wantErr bool
	}{
		{name: "valid", mutate: func(*ProjectState) {}},
		{name: "schema version defaults", mutate: func(state *ProjectState) { state.SchemaVersion = 0 }},
		{name: "missing goal", mutate: func(state *ProjectState) { state.OriginalGoal = " " }, wantErr: true},
		{name: "invalid slug", mutate: func(state *ProjectState) { state.Slug = "Example Project" }, wantErr: true},
		{name: "missing criteria", mutate: func(state *ProjectState) { state.AcceptanceCriteria = nil }, wantErr: true},
		{name: "invalid pipeline snapshot", mutate: func(state *ProjectState) { state.PipelineConfig.Data = json.RawMessage(`not-json`) }, wantErr: true},
		{name: "missing current phase", mutate: func(state *ProjectState) { state.CurrentPhase = "" }, wantErr: true},
		{name: "missing worktree", mutate: func(state *ProjectState) { state.WorktreePath = "" }, wantErr: true},
		{name: "missing branch", mutate: func(state *ProjectState) { state.BranchName = "" }, wantErr: true},
		{name: "missing timestamp", mutate: func(state *ProjectState) { state.UpdatedAt = time.Time{} }, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validProjectState()
			test.mutate(&input)
			got, err := NewProjectState(input)
			if (err != nil) != test.wantErr {
				t.Fatalf("NewProjectState() error = %v, wantErr %v", err, test.wantErr)
			}
			if !test.wantErr && got.SchemaVersion != CurrentSchemaVersion {
				t.Fatalf("schema version = %d, want %d", got.SchemaVersion, CurrentSchemaVersion)
			}
		})
	}
}

func TestLifecycleStatusCoverage(t *testing.T) {
	statuses := []LifecycleStatus{StatusPending, StatusRunning, StatusStopped, StatusFailed, StatusFinished, StatusTerminated}
	for _, status := range statuses {
		if !status.IsValid() {
			t.Errorf("status %q is not valid", status)
		}
	}
	for _, status := range []LifecycleStatus{"", "paused", "PENDING"} {
		if status.IsValid() {
			t.Errorf("status %q is unexpectedly valid", status)
		}
	}
}

func TestProjectStateRejectsInconsistentQALoopCursor(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ProjectState)
	}{
		{
			name: "stage without configured maximum",
			mutate: func(state *ProjectState) {
				state.QALoopStage = "qa"
			},
		},
		{
			name: "fix without completed failed attempt",
			mutate: func(state *ProjectState) {
				state.MaxQAAttempts = 3
				state.QALoopStage = "fix"
			},
		},
		{
			name: "exhausted before maximum",
			mutate: func(state *ProjectState) {
				state.MaxQAAttempts = 3
				state.QACompletedAttempts = 1
				state.QALoopStage = "exhausted"
			},
		},
		{
			name: "completed attempt without stage",
			mutate: func(state *ProjectState) {
				state.MaxQAAttempts = 3
				state.QACompletedAttempts = 1
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project := validProjectState()
			test.mutate(&project)
			if _, err := NewProjectState(project); err == nil {
				t.Fatal("NewProjectState() accepted inconsistent QA loop cursor")
			}
		})
	}
}

func TestProjectStateCarriesNormalizedDeferredChecks(t *testing.T) {
	check := proof.DeferredCheck{
		TestLocation: "internal/aws/handler_test.go", CheckName: "TestRemoteFlow",
		FlowScenario: "exercise the deployed API", ExpectedBehavior: "the API persists the request",
		RemoteOnlyReason: "requires AWS credentials", RepositoryEvidence: "config/aws.go uses AWS_ENDPOINT",
		RunInstructions: "run in CI with AWS secrets",
	}
	project := validProjectState()
	project.DeferredChecks = []proof.DeferredCheck{check}
	project.PhaseHistory = []PhaseRecord{{Phase: "qa", Status: StatusFinished, StartedAt: project.CreatedAt, DeferredChecks: []proof.DeferredCheck{check}, Outcome: &ExecutionOutcome{DeferredChecks: []proof.DeferredCheck{check}}}}
	got, err := NewProjectState(project)
	if err != nil {
		t.Fatal(err)
	}
	got.DeferredChecks[0].CheckName = "mutated"
	got.PhaseHistory[0].DeferredChecks[0].CheckName = "mutated"
	got.PhaseHistory[0].Outcome.DeferredChecks[0].CheckName = "mutated"
	if project.DeferredChecks[0].CheckName != "TestRemoteFlow" || project.PhaseHistory[0].DeferredChecks[0].CheckName != "TestRemoteFlow" || project.PhaseHistory[0].Outcome.DeferredChecks[0].CheckName != "TestRemoteFlow" {
		t.Fatal("NewProjectState did not copy deferred check slices")
	}
	encoded, err := json.Marshal(got)
	if err != nil || !strings.Contains(string(encoded), "deferredChecks") {
		t.Fatalf("encoded project = %s, error = %v", encoded, err)
	}
}
